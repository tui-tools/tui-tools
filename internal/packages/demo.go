package packages

import (
	"sort"
	"strings"

	"github.com/tui-tools/tui-kit/pkgmgr"
)

// This file is the demo machine's companion half.
//
// tui-kit's fake answers the family's own packages, and it validates every name
// the way the real path does — which means it refuses a companion name, exactly
// as the real builders would if they were asked to. So the demo machine keeps
// its own companion catalogue here and answers the companion commands itself,
// in the output format the manager it pretends to be would print.
//
// It prints pacman's formats, because that is the manager the demo machine
// runs. The point of answering in a real format rather than in a convenient one
// is that --demo then goes through the same parsers a real machine does: a bug
// in parsePacmanOrigins is a bug you can see in the demo.

// FakeCompanion is one companion package on the demo machine.
type FakeCompanion struct {
	// Installed is the version on the machine, empty when it is not here.
	Installed string
	// Offered is what the tui-tools repository carries, empty when it carries
	// nothing.
	Offered string
	// From is the repository the installed copy came from, which is what makes
	// the switch worth showing: a package that is here but not the family's.
	From string
	// OtherVersion is what From offers, when that is not what is installed. It
	// is empty for the ordinary case, where the repository a package came from
	// still offers the version the machine has.
	OtherVersion string
}

// version is what a repository offers for this package.
func (c FakeCompanion) version(repo string) string {
	if repo == RepoName {
		return c.Offered
	}
	if c.OtherVersion != "" {
		return c.OtherVersion
	}
	return c.Installed
}

// companionAnswer answers a companion command against the demo catalogue.
//
// It reports whether the command was one, and whether it changed the machine.
// The second answer is what keeps Ran meaning what it has always meant: the
// commands a confirmed sequence ran. The kit's fake answers the tools' reads
// without going through Run at all, and the companion reads are the same sort
// of question, so they are not recorded either.
//
// Anything this does not recognise is left to the kit's fake.
func (f *Fake) companionAnswer(cmd pkgmgr.Command) (out string, mutation, handled bool) {
	if len(f.Companions) == 0 || f.Manager() != pkgmgr.ManagerPacman {
		return "", false, false
	}
	if len(cmd.Argv) < 2 || cmd.Argv[0] != "pacman" {
		return "", false, false
	}
	names := f.companionArgs(cmd.Argv[1:])
	switch cmd.Argv[1] {
	case "-Sl":
		if len(cmd.Argv) < 3 || cmd.Argv[2] != RepoName {
			return "", false, false
		}
		return f.listRepo(), false, true
	case "-Q":
		if len(names) == 0 {
			return "", false, false
		}
		return f.query(names), false, true
	case "-Qi":
		if len(names) == 0 {
			return "", false, false
		}
		return f.info(names, ""), false, true
	case "-Si":
		if len(names) == 0 {
			return "", false, false
		}
		return f.sync(names), false, true
	case "-S":
		repo, name, ok := f.switchTarget(cmd.Argv[1:])
		if !ok {
			return "", false, false
		}
		f.install(name, repo)
		return "ok", true, true
	case "-Syu":
		if len(names) == 0 {
			return "", false, false
		}
		for _, name := range names {
			f.install(name, RepoName)
		}
		return "ok", true, true
	case "-R":
		if len(names) == 0 {
			return "", false, false
		}
		for _, name := range names {
			companion := f.Companions[name]
			companion.Installed, companion.From = "", ""
			f.Companions[name] = companion
		}
		return "ok", true, true
	}
	return "", false, false
}

// companionArgs picks the companion names out of an argv, in the order they
// appear. A command that names none is not a companion command.
func (f *Fake) companionArgs(args []string) []string {
	var names []string
	for _, arg := range args {
		if _, ok := f.Companions[arg]; ok {
			names = append(names, arg)
		}
	}
	return names
}

// switchTarget reads a `<repo>/<name>` argument, which is how pacman is told
// which repository to install from.
func (f *Fake) switchTarget(args []string) (repo, name string, ok bool) {
	for _, arg := range args {
		repo, name, ok = strings.Cut(arg, "/")
		if !ok {
			continue
		}
		if _, known := f.Companions[name]; known {
			return repo, name, true
		}
	}
	return "", "", false
}

// install moves a companion to the version a repository offers, and records
// that repository as where the copy on the machine came from.
func (f *Fake) install(name, repo string) {
	companion := f.Companions[name]
	version := companion.version(repo)
	if version == "" {
		return
	}
	companion.Installed = version
	companion.From = repo
	f.Companions[name] = companion
}

// listRepo prints what `pacman -Sl tui-tools` prints: the repository, the
// package, the version it offers, and the installed marker — bare when the
// machine has that exact version, and carrying the machine's version when it
// has a different one.
func (f *Fake) listRepo() string {
	var lines []string
	for _, name := range f.companionNames() {
		companion := f.Companions[name]
		if companion.Offered == "" {
			continue
		}
		line := RepoName + " " + name + " " + companion.Offered
		switch companion.Installed {
		case "":
			// Not on the machine, so pacman prints no marker at all.
		case companion.Offered:
			line += " [installed]"
		default:
			line += " [installed: " + companion.Installed + "]"
		}
		lines = append(lines, line)
	}
	return join(lines)
}

// query prints what `pacman -Q` prints.
func (f *Fake) query(names []string) string {
	var lines []string
	for _, name := range names {
		if version := f.Companions[name].Installed; version != "" {
			lines = append(lines, name+" "+version)
		}
	}
	return join(lines)
}

// info prints what `pacman -Qi` prints, cut down to the fields the parser
// reads.
func (f *Fake) info(names []string, repo string) string {
	var blocks []string
	for _, name := range names {
		companion := f.Companions[name]
		if companion.Installed == "" {
			continue
		}
		block := "Name            : " + name + "\n" +
			"Version         : " + companion.Installed + "\n" +
			"Description     : a demo companion package\n"
		if repo != "" {
			block = "Repository      : " + repo + "\n" + block
		}
		blocks = append(blocks, block)
	}
	return join(blocks)
}

// sync prints what `pacman -Si` prints: one block per repository that offers
// the package.
func (f *Fake) sync(names []string) string {
	var blocks []string
	for _, name := range names {
		companion := f.Companions[name]
		for _, repo := range f.reposOffering(companion) {
			blocks = append(blocks, "Repository      : "+repo+"\n"+
				"Name            : "+name+"\n"+
				"Version         : "+companion.version(repo)+"\n")
		}
	}
	return join(blocks)
}

// reposOffering lists the repositories that carry a companion on the demo
// machine, sorted so the output does not change between runs.
func (f *Fake) reposOffering(companion FakeCompanion) []string {
	var repos []string
	if companion.Offered != "" {
		repos = append(repos, RepoName)
	}
	if companion.From != "" && companion.From != RepoName {
		repos = append(repos, companion.From)
	}
	sort.Strings(repos)
	return repos
}

// companionNames lists the demo catalogue, sorted.
func (f *Fake) companionNames() []string {
	names := make([]string, 0, len(f.Companions))
	for name := range f.Companions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// join renders blocks the way pacman separates them: a blank line between two
// of them, and a trailing newline.
func join(blocks []string) string {
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n") + "\n"
}
