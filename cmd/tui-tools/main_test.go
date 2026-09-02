package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/manifest"
	"github.com/tui-tools/tui-kit/pkgmgr"
	tuitools "github.com/tui-tools/tui-tools"
	"github.com/tui-tools/tui-tools/internal/catalog"
	"github.com/tui-tools/tui-tools/internal/packages"
)

func TestParseFlags(t *testing.T) {
	opts, err := parseFlags([]string{"--demo", "--check", "--offline",
		"--catalog", "https://example.test/catalog.json", "--sudo", ""}, nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !opts.demo || !opts.check || !opts.offline {
		t.Errorf("flags = %+v", opts)
	}
	if opts.catalogURL != "https://example.test/catalog.json" {
		t.Errorf("catalog = %q", opts.catalogURL)
	}
	// An explicitly empty -sudo disables escalation, which is different from
	// not passing it at all.
	if !opts.sudoSet {
		t.Error("an explicitly empty -sudo read as not given")
	}
}

// Only the keys declared here are read from the environment, so an unrelated
// TUI_* variable can never leak into the configuration.
func TestDefaultsDeclareEveryKey(t *testing.T) {
	got := defaults()
	for _, key := range []string{keyCatalog, keyOffline, config.KeySudo, config.KeyTheme} {
		if _, ok := got[key]; !ok {
			t.Errorf("the %q key is not declared", key)
		}
	}
	if got[keyCatalog] != catalog.URL {
		t.Errorf("the default catalog is %q", got[keyCatalog])
	}
}

func TestApplyOverrides(t *testing.T) {
	cfg, err := config.Load(config.Options{Tool: toolName, Defaults: defaults()})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	applyOverrides(&cfg, options{offline: true, offlineSet: true,
		catalogURL: "https://example.test/c.json", sudo: "", sudoSet: true})
	if !cfg.Bool(keyOffline, false) {
		t.Error("--offline did not reach the configuration")
	}
	if cfg.String(keyCatalog, "") != "https://example.test/c.json" {
		t.Error("--catalog did not reach the configuration")
	}
	if len(cfg.SudoPrefix()) != 0 {
		t.Errorf("an empty --sudo left %v as the prefix", cfg.SudoPrefix())
	}
}

// --demo has to reach every key, which means the sample machine needs a tool
// to install, one to update and one to remove.
func TestDemoMachineShowsEveryState(t *testing.T) {
	backend := demoBackend(catalogSource{url: catalog.URL, offline: true})
	doc, _ := catalog.Embedded()

	installedPkgs, err := backend.Installed(context.Background(), doc.Names())
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	availablePkgs, err := backend.Available(context.Background(), doc.Names())
	if err != nil {
		t.Fatalf("Available: %v", err)
	}

	states := map[catalog.State]int{}
	for _, row := range catalog.Rows(doc, installedPkgs, availablePkgs) {
		states[row.State]++
	}
	for _, want := range []catalog.State{
		catalog.StateNotInstalled, catalog.StateUpToDate, catalog.StateOutdated,
	} {
		if states[want] == 0 {
			t.Errorf("no tool in the demo is %q", want)
		}
	}
}

// The demo has to carry one of each companion situation, because they are the
// two the screen behaves differently for: a mirror installed from somewhere
// else, which is what the origin check and the switch exist for, and a
// component that is not installed at all.
func TestDemoMachineShowsBothCompanions(t *testing.T) {
	source := catalogSource{url: catalog.URL, offline: true, demo: true}
	backend := demoBackend(source)
	doc, _ := source.Load(context.Background())

	installed, available, origins := readCompanions(context.Background(),
		backend, doc)
	rows := catalog.CompanionRows(doc, installed, available, origins)
	if len(rows) < 2 {
		t.Fatalf("the demo shows %d companions", len(rows))
	}

	kinds := map[catalog.Kind]bool{}
	switchable := 0
	for _, row := range rows {
		kinds[row.Kind] = true
		if row.Switchable() {
			switchable++
			if row.Origin.Repo != "extra" {
				t.Errorf("%s came from %q, want extra so the switch is worth "+
					"showing", row.Name, row.Origin.Repo)
			}
		}
	}
	if !kinds[catalog.KindMirror] || !kinds[catalog.KindComponent] {
		t.Errorf("the demo shows the kinds %v", kinds)
	}
	if switchable == 0 {
		t.Error("nothing in the demo can be switched to the family build")
	}
}

// --check is the non-interactive read path, and a script reading it has to be
// able to answer the provenance question without opening a UI.
func TestCheckReportsTheCompanions(t *testing.T) {
	source := catalogSource{url: catalog.URL, offline: true, demo: true}
	var out strings.Builder
	if err := runCheck(context.Background(), demoBackend(source), source, nil,
		&out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}

	var report struct {
		Companions []companionReport `json:"companions"`
	}
	if err := json.Unmarshal([]byte(out.String()), &report); err != nil {
		t.Fatalf("the report is not JSON: %v", err)
	}
	if len(report.Companions) < 2 {
		t.Fatalf("the report carries %d companions", len(report.Companions))
	}
	found := false
	for _, companion := range report.Companions {
		if !companion.Switchable {
			continue
		}
		found = true
		if companion.Origin.Repo == "" || companion.Origin.Family {
			t.Errorf("%s: origin = %+v", companion.Name, companion.Origin)
		}
		if !companion.Origin.Offered || companion.Origin.Version == "" {
			t.Errorf("%s: the family repository offers %+v",
				companion.Name, companion.Origin)
		}
		if companion.Kind != string(catalog.KindMirror) ||
			companion.Upstream == "" {
			t.Errorf("%s: kind %q, upstream %q",
				companion.Name, companion.Kind, companion.Upstream)
		}
	}
	if !found {
		t.Error("no companion in the report says it came from another repository")
	}
}

// A machine whose catalog carries no companion prints none, rather than an
// empty array a script has to special-case.
func TestCheckOmitsTheCompanionsWhenThereAreNone(t *testing.T) {
	// A document from before the companions array existed, which is what an
	// older site still serves.
	const older = `{"schema": 1, "packages": {"repo": "https://pkgs.tui.tools"},
	  "tools": [{"name": "tui-secure", "package": "tui-secure",
	    "binary": "tui-secure", "tagline": "posture", "version": "0.1.2",
	    "platforms": ["linux/amd64", "linux/arm64"]}]}`
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(older))
		}))
	defer server.Close()

	source := catalogSource{url: server.URL}
	doc, note := source.Load(context.Background())
	if note != "" || len(doc.Companions) != 0 {
		t.Fatalf("the served document did not arrive as expected: %q, %+v",
			note, doc.Companions)
	}

	var out strings.Builder
	if err := runCheck(context.Background(), demoBackend(source), source, nil,
		&out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if strings.Contains(out.String(), `"companions"`) {
		t.Error("a catalog with no companions still printed the field")
	}
}

func TestBehindStepsOnePatchBack(t *testing.T) {
	for from, want := range map[string]string{
		"0.1.2": "0.1.1",
		"0.2.2": "0.2.1",
		// Nothing sensible to step back from, so it is left alone rather than
		// invented.
		"0.1.0": "0.1.0",
		"1.0":   "1.0",
	} {
		if got := behind(from); got != want {
			t.Errorf("behind(%q) = %q, want %q", from, got, want)
		}
	}
}

// The embedded manifest is what the header reads and what the release
// packages copy their description from.
func TestEmbeddedManifestDeclaresTheThreeManagers(t *testing.T) {
	m, err := manifest.Load(tuitools.ManifestJSON)
	if err != nil {
		t.Fatalf("the embedded tool.json does not parse: %v", err)
	}
	if m.Name != toolName {
		t.Errorf("manifest name = %q, want %q", m.Name, toolName)
	}
	for _, want := range []string{"apt", "dnf", "pacman"} {
		backend, ok := m.Backend(want)
		if !ok {
			t.Errorf("no %s backend in the manifest", want)
			continue
		}
		if len(backend.VersionCommand) == 0 {
			t.Errorf("%s declares no version command", want)
		}
	}
}

func TestProbeCompatSkipsDemo(t *testing.T) {
	if got := probeCompat(context.Background(), true); got != nil {
		t.Errorf("demo probe = %+v, want nothing", got)
	}
}

// The probe runs against whatever this machine has. It must produce a result
// per declared manager either way — that is the promise: a compatibility probe
// never fails a tool.
func TestProbeCompatOnThisMachine(t *testing.T) {
	got := probeCompat(context.Background(), false)
	if len(got) != 3 {
		t.Fatalf("%d results, want one per declared manager", len(got))
	}
	for _, result := range got {
		t.Logf("this machine: %s %s (%s)", result.Backend, result.Version, result.Status)
	}
	if len(installed(got)) > 1 {
		t.Errorf("more than one package manager answered: %+v", installed(got))
	}
}

// The pinned fingerprint is a full OpenPGP fingerprint, upper case, and it is
// written in exactly one place.
func TestFingerprintIsWellFormed(t *testing.T) {
	got, err := pkgmgr.CheckFingerprint(packages.Fingerprint)
	if err != nil {
		t.Fatalf("the pinned fingerprint is not one: %v", err)
	}
	if got != packages.Fingerprint {
		t.Errorf("the constant is not in the form gpg prints: %q", packages.Fingerprint)
	}
	if strings.ToUpper(packages.Fingerprint) != packages.Fingerprint {
		t.Error("the constant is not upper case")
	}
}
