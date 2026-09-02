// Command tui-tools is the launcher of the tui-tools family: every tool of
// the family on one screen, with what this machine has, what the repository
// offers, and what would be run to change that.
//
// It installs, updates and removes through the distribution's own package
// manager, previewing the exact command line first, and hands the terminal
// over to an installed tool on enter. The catalog it reads — the list of tools
// and their versions — only informs; the package manager verifies the signed
// repository and the signed package, and that is what decides whether a byte
// reaches the machine.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-tools/internal/catalog"
	"github.com/tui-tools/tui-tools/internal/packages"
)

// toolName is the binary name, which is also the configuration directory:
// /etc/tui-tools/config.toml and ~/.config/tui-tools/config.toml.
const toolName = "tui-tools"

// The tool's own configuration keys.
const (
	// keyCatalog is where the family catalog is fetched from. It is
	// configurable so a staging copy of the site can be pointed at; whatever
	// it answers is validated exactly like the production one, and nothing
	// from it reaches a command line but a package name that matched
	// ^tui-[a-z]+$.
	keyCatalog = "catalog"
	// keyOffline skips the fetch entirely and uses the snapshot embedded in
	// this binary.
	keyOffline = "offline"
)

// version is stamped by the release build (-ldflags "-X main.version=…").
var version = "dev"

// defaults declares the configuration keys tui-tools understands. Only these
// are read from the environment (TUI_TOOLS_CATALOG, …), so an unrelated
// variable can never leak in.
func defaults() map[string]string {
	return map[string]string{
		keyCatalog:      catalog.URL,
		keyOffline:      "false",
		config.KeySudo:  "sudo -n",
		config.KeyTheme: "",
	}
}

// options holds the parsed command line.
type options struct {
	demo        bool
	check       bool
	report      bool
	offline     bool
	offlineSet  bool
	catalogURL  string
	themePath   string
	sudo        string
	showVersion bool
	// sudoSet records whether -sudo was passed, so `--sudo ""` can disable
	// escalation instead of reading as "not given".
	sudoSet bool
}

// parseFlags defines and reads the command line.
func parseFlags(args []string, out *os.File) (options, error) {
	var opts options
	fs := flag.NewFlagSet(toolName, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.BoolVar(&opts.demo, "demo", false,
		"run against a sample machine and the embedded catalog, without "+
			"touching anything")
	fs.BoolVar(&opts.check, "check", false,
		"read the machine and print the family's state as JSON, then exit "+
			"(no UI, nothing installed, nothing removed)")
	fs.BoolVar(&opts.report, "report", false, reportUsage)
	fs.BoolVar(&opts.offline, "offline", false,
		"use the catalog snapshot embedded in this binary instead of fetching it")
	fs.StringVar(&opts.catalogURL, "catalog", "",
		"where to fetch the family catalog (overrides the config file)")
	fs.StringVar(&opts.themePath, "theme", "",
		"path to an Omarchy-style colors.toml (overrides the config file)")
	fs.StringVar(&opts.sudo, "sudo", "",
		"privilege escalation prefix, e.g. \"sudo -n\" or \"\" to disable")
	fs.BoolVar(&opts.showVersion, "version", false, "print the version and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(out, "tui-tools — every tool of the family, "+
			"installed through your package manager\n\n"+
			"Usage:\n  tui-tools [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(out, "\nConfiguration is read from %s, then %s, "+
			"then TUI_TOOLS_* in the environment.\n",
			config.SystemPathFor(toolName), config.UserPathFor(toolName))
	}
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "sudo":
			opts.sudoSet = true
		case "offline":
			opts.offlineSet = true
		}
	})
	return opts, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, toolName+":", err)
		os.Exit(1)
	}
}

// run wires the configuration, the backend and the Bubble Tea program.
func run(args []string) error {
	opts, err := parseFlags(args, os.Stdout)
	if err != nil {
		// flag already printed the reason and the usage.
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if opts.showVersion {
		fmt.Println(toolName, version)
		return nil
	}

	cfg, err := config.Load(config.Options{Tool: toolName, Defaults: defaults()})
	if err != nil {
		return err
	}
	applyOverrides(&cfg, opts)

	// The package manager is probed once, before the first read: which
	// version of apt, dnf or pacman this machine runs is a fact the header
	// shows and the compatibility block is judged against.
	backends := probeCompat(context.Background(), opts.demo)

	source := catalogSource{
		url: cfg.String(keyCatalog, catalog.URL),
		// A demo never reaches the network: what it shows has to be the same
		// document every time, on every machine.
		offline: opts.demo || cfg.Bool(keyOffline, false),
		demo:    opts.demo,
	}

	// The configured theme is handed to the kit through the same variable the
	// user could set by hand, so precedence stays in one place. It is set
	// before the backend is built so --report can name the theme the UI would
	// have used even on a machine whose package manager the launcher cannot
	// drive.
	if path := cfg.Theme(); path != "" {
		if err := os.Setenv("TUI_THEME", path); err != nil {
			return err
		}
	}

	// --report is the non-interactive path that must work everywhere. It
	// installs nothing, needs no privileges, and it survives a machine with a
	// package manager the launcher does not drive, because "there is nothing
	// here to drive" is one of the things a bug report has to be able to say.
	// So it comes before the backend is required.
	if opts.report {
		return runReport(context.Background(), cfg, opts, source, backends,
			os.Stdout)
	}

	backend, err := pickBackend(cfg, opts, source)
	if err != nil {
		return err
	}

	// --check is the non-interactive path: it reads and prints, and never
	// starts a terminal program.
	if opts.check {
		return runCheck(context.Background(), backend, source, backends, os.Stdout)
	}

	program := tea.NewProgram(newApp(backend, theme.New(), backends, source),
		tea.WithAltScreen())
	_, err = program.Run()
	return err
}

// catalogSource is where the catalog is read from, and whether the network is
// allowed to answer.
type catalogSource struct {
	url     string
	offline bool
	// demo says the document is being read for --demo, which is the only
	// caller allowed to add a sample entry to it.
	demo bool
}

// Load reads the catalog, live or embedded, and returns the reason the live
// one was not used when there is one.
//
// The demo is the one caller that gets a document the family did not publish
// exactly: it needs a component companion on screen, because a component and a
// mirror behave differently there, and the snapshot a given build carries may
// have none. Nothing else ever adds an entry.
func (s catalogSource) Load(ctx context.Context) (catalog.Catalog, string) {
	doc, note := catalog.Load(ctx, s.url, s.offline)
	if s.demo && len(doc.Tools) > 0 {
		doc = catalog.WithExampleComponent(doc)
	}
	return doc, note
}

// applyOverrides folds the command line into the configuration, which is the
// last and highest-precedence layer.
func applyOverrides(cfg *config.Config, opts options) {
	if opts.catalogURL != "" {
		cfg.Set(keyCatalog, opts.catalogURL)
	}
	if opts.offlineSet {
		cfg.Set(keyOffline, "true")
	}
	if opts.themePath != "" {
		cfg.Set(config.KeyTheme, opts.themePath)
	}
	// An explicitly empty -sudo disables escalation, so the flag is applied
	// whenever it was passed, empty value included.
	if opts.sudoSet {
		cfg.Set(config.KeySudo, opts.sudo)
	}
}

// pickBackend returns the demo machine or the real package manager.
func pickBackend(cfg config.Config, opts options,
	source catalogSource) (packages.Backend, error) {
	if opts.demo {
		return demoBackend(source), nil
	}
	return packages.New(cfg.SudoPrefix())
}

// demoBackend builds the sample machine --demo drives: the family's own
// catalog, with a few tools installed, one of them a version behind, and the
// rest waiting in the repository. It is stocked from the embedded snapshot, so
// the demo shows the real family rather than invented names.
func demoBackend(source catalogSource) packages.Backend {
	doc, _ := source.Load(context.Background())
	names := doc.Names()

	installedPkgs := map[string]string{}
	availablePkgs := map[string]string{}
	for i, tool := range doc.Tools {
		available := tool.Version
		if available == "" {
			available = "0.1.2"
		}
		availablePkgs[tool.Package] = available
		switch i % 3 {
		case 0:
			// Installed and current.
			installedPkgs[tool.Package] = available
		case 1:
			// Installed and a version behind, which is what the dashboard
			// exists to make visible.
			installedPkgs[tool.Package] = behind(available)
		}
	}
	machine := packages.NewFake(names, installedPkgs, availablePkgs)
	stockCompanions(machine, doc)
	return machine
}

// stockCompanions gives the demo machine one of each companion situation worth
// looking at.
//
// A mirror is installed, and installed from somewhere else — which is the case
// the origin check exists for, and the only one where `o` has anything to do. A
// component is not installed at all, which is the case `i` is for. Everything
// the demo then shows is produced by the same probes, parsers and builders a
// real machine goes through.
func stockCompanions(machine *packages.Fake, doc catalog.Catalog) {
	for _, companion := range doc.Companions {
		offered := companion.Version
		if offered == "" {
			offered = "0.1.0"
		}
		state := packages.FakeCompanion{Offered: offered}
		if companion.Kind == catalog.KindMirror {
			state.Installed = behind(offered)
			state.From = "extra"
			state.OtherVersion = state.Installed
		}
		for _, pkg := range companion.Packages {
			machine.Companions[pkg] = state
		}
	}
}

// behind returns the version one patch release older, for the demo's
// out-of-date tools.
func behind(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) != 3 || parts[2] == "" || parts[2][0] <= '0' {
		return version
	}
	parts[2] = string(parts[2][0] - 1)
	return strings.Join(parts, ".")
}
