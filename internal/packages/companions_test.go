package packages

import (
	"context"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-tools/internal/catalog"
)

// The names a companion command may be built from. A mirror carries the
// upstream project's own name and a component is a tui-tools-<something>
// package, so the tools' ^tui-[a-z]+$ is not the rule here — but everything
// that could turn a name into a second command still is.
func TestCompanionNamesThatMayReachACommandLine(t *testing.T) {
	for _, name := range []string{"headscale", "tui-tools-example", "caddy2",
		"step-ca"} {
		if !ValidCompanionName(name) {
			t.Errorf("%q was refused", name)
		}
	}
	for _, name := range []string{"", "-headscale", "headscale-", "Headscale",
		"head scale", "headscale; rm -rf /", "$(id)", "../../bin/sh",
		"head--scale", "head_scale", strings.Repeat("a", 65)} {
		if ValidCompanionName(name) {
			t.Errorf("%q was allowed", name)
		}
	}
	if err := CheckCompanionNames(nil); err == nil {
		t.Error("a command with no package named was allowed")
	}
}

// Every builder refuses a name before it builds anything, so an injected name
// never reaches a preview, let alone an argv.
func TestCompanionBuildersRefuseANameThatIsNotOne(t *testing.T) {
	bad := []string{"headscale; rm -rf /"}
	for _, manager := range []pkgmgr.Manager{
		pkgmgr.ManagerAPT, pkgmgr.ManagerDNF, pkgmgr.ManagerPacman} {
		if _, err := BuildCompanionInstall(manager, bad); err == nil {
			t.Errorf("%s: install accepted %q", manager, bad[0])
		}
		if _, err := BuildCompanionRemove(manager, bad); err == nil {
			t.Errorf("%s: remove accepted %q", manager, bad[0])
		}
		if _, err := BuildCompanionUpgrade(manager, bad); err == nil {
			t.Errorf("%s: upgrade accepted %q", manager, bad[0])
		}
		if _, err := BuildCompanionInstalled(manager, bad); err == nil {
			t.Errorf("%s: the installed query accepted %q", manager, bad[0])
		}
		if _, err := BuildOriginProbes(manager, bad); err == nil {
			t.Errorf("%s: the origin probe accepted %q", manager, bad[0])
		}
		if _, err := BuildCompanionSwitch(manager, bad[0],
			catalog.Origin{Offered: true, Version: "1.0"}); err == nil {
			t.Errorf("%s: the switch accepted %q", manager, bad[0])
		}
	}
}

// The origin probes read the machine and must never escalate: a screen that
// has to raise a sudo prompt to say where a package came from is a screen
// nobody will wait for.
func TestOriginProbesAreUnprivilegedReads(t *testing.T) {
	for _, manager := range []pkgmgr.Manager{
		pkgmgr.ManagerAPT, pkgmgr.ManagerDNF, pkgmgr.ManagerPacman} {
		probes, err := BuildOriginProbes(manager, []string{"headscale"})
		if err != nil {
			t.Fatalf("%s: %v", manager, err)
		}
		if len(probes) == 0 {
			t.Fatalf("%s: no probe was built", manager)
		}
		for _, probe := range probes {
			if probe.Privileged {
				t.Errorf("%s: %q is privileged", manager, probe.String())
			}
			if probe.Destructive() {
				t.Errorf("%s: %q is destructive", manager, probe.String())
			}
		}
	}
}

// pacman -Sl prints one line per package in a repository, with a bare
// [installed] when the machine has exactly the version the repository offers.
const pacmanList = `tui-tools headscale 0.26.1-1 [installed: 0.25.1-1]
tui-tools tui-firewall 0.2.2-1 [installed]
tui-tools step-ca 0.28.0-1
`

// pacman -Qi is the installed package, in blocks of `Field : value`.
const pacmanInfo = `Name            : headscale
Version         : 0.25.1-1
Description     : An open source implementation of the Tailscale control server
Depends On      : glibc  libcap
Optional Deps   : None
Install Date    : Mon 01 Sep 2026 09:12:44 AM -03
`

// pacman -Si is the same shape, one block per repository that offers it.
const pacmanSync = `Repository      : extra
Name            : headscale
Version         : 0.25.1-1
Description     : An open source implementation of the Tailscale control server

Repository      : tui-tools
Name            : headscale
Version         : 0.26.1-1
Description     : Self-hosted Tailscale control server, rebuilt from source
`

func TestPacmanOriginIsInferredFromTheVersions(t *testing.T) {
	origins := ParseOrigins(pkgmgr.ManagerPacman, []string{"headscale"},
		[]string{pacmanList, pacmanInfo, pacmanSync})

	origin := origins["headscale"]
	if origin.Repo != "extra" {
		t.Errorf("repo = %q, want extra: %s", origin.Repo, origin.Detail)
	}
	if origin.Family {
		t.Error("a package installed from extra was called the family's")
	}
	if !origin.Offered || origin.Version != "0.26.1-1" {
		t.Errorf("what the family offers = %q (offered %v)",
			origin.Version, origin.Offered)
	}
	if origin.Detail == "" {
		t.Error("the answer does not say how it was reached")
	}
}

// The bare [installed] marker is pacman saying the machine has exactly what
// this repository offers, which is the only claim on the family's copy that
// pacman can make: it records no from-repo field at all.
func TestPacmanOriginReadsTheInstalledMarker(t *testing.T) {
	list := "tui-tools headscale 0.26.1-1 [installed]\n"
	info := "Name            : headscale\nVersion         : 0.26.1-1\n"
	origins := ParseOrigins(pkgmgr.ManagerPacman, []string{"headscale"},
		[]string{list, info, pacmanSync})

	origin := origins["headscale"]
	if !origin.Family || origin.Repo != RepoName {
		t.Errorf("origin = %+v, want the family's", origin)
	}
}

// A version no configured repository offers is an honest "cannot say" rather
// than a guess.
func TestPacmanOriginSaysWhenItCannotSay(t *testing.T) {
	info := "Name            : headscale\nVersion         : 9.9.9-1\n"
	origins := ParseOrigins(pkgmgr.ManagerPacman, []string{"headscale"},
		[]string{pacmanList, info, pacmanSync})

	origin := origins["headscale"]
	if origin.Repo != "" || origin.Family {
		t.Errorf("origin = %+v, want no repository named", origin)
	}
	if !strings.Contains(origin.Detail, "cannot") {
		t.Errorf("detail = %q", origin.Detail)
	}
}

// apt-cache policy prints a version table per package: the installed version
// carries ***, and the lines under a version are where it can be fetched from.
const aptPolicy = `headscale:
  Installed: 0.25.1
  Candidate: 0.26.1
  Version table:
     0.26.1 500
        500 https://pkgs.tui.tools/deb stable/main amd64 Packages
 *** 0.25.1 500
        500 http://deb.debian.org/debian trixie/main amd64 Packages
        100 /var/lib/dpkg/status
tui-tools-example:
  Installed: (none)
  Candidate: 0.1.0
  Version table:
     0.1.0 500
        500 https://pkgs.tui.tools/deb stable/main amd64 Packages
`

func TestAPTOriginIsReadOffTheVersionTable(t *testing.T) {
	origins := ParseOrigins(pkgmgr.ManagerAPT,
		[]string{"headscale", "tui-tools-example"}, []string{aptPolicy})

	installed := origins["headscale"]
	if installed.Repo != "deb.debian.org" {
		t.Errorf("repo = %q: %s", installed.Repo, installed.Detail)
	}
	if installed.Family {
		t.Error("a package installed from Debian was called the family's")
	}
	if !installed.Offered || installed.Version != "0.26.1" {
		t.Errorf("what the family offers = %q (offered %v)",
			installed.Version, installed.Offered)
	}

	missing := origins["tui-tools-example"]
	if !missing.Offered || missing.Version != "0.1.0" {
		t.Errorf("the family repository's own package was not seen: %+v", missing)
	}
	if missing.Repo != "" {
		t.Errorf("a package that is not installed came from %q", missing.Repo)
	}
}

// A package installed from the family repository is recognised as such, and
// the dpkg status file is not mistaken for a repository.
func TestAPTOriginRecognisesTheFamilyRepository(t *testing.T) {
	policy := `headscale:
  Installed: 0.26.1
  Candidate: 0.26.1
  Version table:
 *** 0.26.1 500
        500 https://pkgs.tui.tools/deb stable/main amd64 Packages
        100 /var/lib/dpkg/status
`
	origin := ParseOrigins(pkgmgr.ManagerAPT, []string{"headscale"},
		[]string{policy})["headscale"]
	if !origin.Family || origin.Repo != RepoName {
		t.Errorf("origin = %+v, want the family's", origin)
	}
}

// A package installed from a file has no repository behind it, and apt says so
// by leaving the dpkg status file as the only source.
func TestAPTOriginSaysWhenNoRepositoryOffersIt(t *testing.T) {
	policy := `headscale:
  Installed: 0.25.1
  Candidate: (none)
  Version table:
 *** 0.25.1 100
        100 /var/lib/dpkg/status
`
	origin := ParseOrigins(pkgmgr.ManagerAPT, []string{"headscale"},
		[]string{policy})["headscale"]
	if origin.Repo != "" || origin.Family || origin.Offered {
		t.Errorf("origin = %+v, want nothing claimed", origin)
	}
	if !strings.Contains(origin.Detail, "cannot be said") {
		t.Errorf("detail = %q", origin.Detail)
	}
}

func TestDNFOriginIsReadFromTheRecordedRepository(t *testing.T) {
	installed := "headscale|updates\ntui-tools-example|tui-tools\n"
	offered := "headscale|0.26.1-1.fc42\ntui-tools-example|0.1.0-1.fc42\n"
	origins := ParseOrigins(pkgmgr.ManagerDNF,
		[]string{"headscale", "tui-tools-example"},
		[]string{installed, offered})

	if got := origins["headscale"]; got.Repo != "updates" || got.Family {
		t.Errorf("headscale = %+v, want updates", got)
	}
	if got := origins["headscale"]; !got.Offered || got.Version != "0.26.1-1.fc42" {
		t.Errorf("what the family offers = %+v", got)
	}
	component := origins["tui-tools-example"]
	if !component.Family || component.Repo != RepoName {
		t.Errorf("tui-tools-example = %+v, want the family's", component)
	}
}

// dnf answers @System for a package installed from a file, which is not a
// repository and must not be shown as one.
func TestDNFOriginSaysWhenThereIsNoRepository(t *testing.T) {
	origin := ParseOrigins(pkgmgr.ManagerDNF, []string{"headscale"},
		[]string{"headscale|@System\n", ""})["headscale"]
	if origin.Repo != "" || origin.Family || origin.Offered {
		t.Errorf("origin = %+v, want nothing claimed", origin)
	}
	if !strings.Contains(origin.Detail, "from a file") {
		t.Errorf("detail = %q", origin.Detail)
	}
}

// The switch is what makes the provenance answer actionable, and each manager
// is told to take the package from the family repository in its own way.
func TestSwitchNamesTheFamilyRepository(t *testing.T) {
	origin := catalog.Origin{Repo: "extra", Offered: true, Version: "0.26.1"}
	for manager, want := range map[pkgmgr.Manager]string{
		pkgmgr.ManagerPacman: "pacman -S --noconfirm tui-tools/headscale",
		pkgmgr.ManagerDNF:    "dnf install --repo tui-tools -y headscale",
		pkgmgr.ManagerAPT:    "apt-get install -y --allow-downgrades headscale=0.26.1",
	} {
		steps, err := BuildCompanionSwitch(manager, "headscale", origin)
		if err != nil {
			t.Fatalf("%s: %v", manager, err)
		}
		last := steps[len(steps)-1]
		if last.String() != want {
			t.Errorf("%s: %q, want %q", manager, last.String(), want)
		}
		if !last.Privileged {
			t.Errorf("%s: the switch is not marked privileged", manager)
		}
	}
}

// A switch is only ever built against a repository that carries the package,
// and apt additionally needs a version it can put on the command line.
func TestSwitchRefusesWhatItCannotName(t *testing.T) {
	if _, err := BuildCompanionSwitch(pkgmgr.ManagerPacman, "headscale",
		catalog.Origin{Repo: "extra"}); err == nil {
		t.Error("a switch to a repository that does not carry it was built")
	}
	for _, version := range []string{"", "0.26.1; rm -rf /", "$(id)"} {
		if _, err := BuildCompanionSwitch(pkgmgr.ManagerAPT, "headscale",
			catalog.Origin{Offered: true, Version: version}); err == nil {
			t.Errorf("apt was handed the version %q", version)
		}
	}
}

// Several repositories can offer the same companion, and `pacman -S` takes the
// first one pacman.conf lists. The available version has to be that one, or a
// row claims an update the machine would not get.
func TestPacmanAvailableIsTheVersionAnInstallWouldFetch(t *testing.T) {
	got := parseVersions(pkgmgr.ManagerPacman, pacmanSync, true)
	if got["headscale"] != "0.25.1-1" {
		t.Errorf("available = %q, want the first repository's %q",
			got["headscale"], "0.25.1-1")
	}
}

// demoMachine is the companion machine --demo drives: a mirror installed from
// somewhere else, and a component that is not installed.
func demoMachine() *Fake {
	machine := NewFake(nil, nil, nil)
	machine.Companions["headscale"] = FakeCompanion{
		Installed: "0.25.1-1", Offered: "0.26.1-1", From: "extra",
		OtherVersion: "0.25.1-1",
	}
	machine.Companions["tui-tools-example"] = FakeCompanion{Offered: "0.1.0-1"}
	return machine
}

// Demo parity: the demo answers in pacman's own output formats, so what --demo
// shows went through the same probes and the same parsers a real machine does.
func TestDemoCompanionsGoThroughTheRealParsers(t *testing.T) {
	machine := demoMachine()
	ctx := context.Background()
	names := []string{"headscale", "tui-tools-example"}

	installed, available := CompanionVersions(ctx, machine, names)
	if installed["headscale"] != "0.25.1-1" {
		t.Errorf("installed = %v", installed)
	}
	if installed["tui-tools-example"] != "" {
		t.Errorf("a component nobody installed is installed: %v", installed)
	}
	// The demo machine has the family repository last in pacman.conf, as a real
	// one does, so what a bare install would fetch is the distribution's build
	// and that is what "available" says. What the family offers is a separate
	// answer, and it is in the origin.
	if available["headscale"] != "0.25.1-1" ||
		available["tui-tools-example"] != "0.1.0-1" {
		t.Errorf("available = %v", available)
	}

	origin := CompanionOrigins(ctx, machine, []string{"headscale"})["headscale"]
	if origin.Repo != "extra" || origin.Family {
		t.Errorf("origin = %+v, want extra", origin)
	}
	if !origin.Offered || origin.Version != "0.26.1-1" {
		t.Errorf("the demo family repository offers %+v", origin)
	}
	// Reading is not running: a demo that recorded its probes would make the
	// "what ran is what was previewed" assertion meaningless.
	if len(machine.Ran) != 0 {
		t.Errorf("the read probes were recorded as commands: %v", machine.Previews())
	}
}

// And the other half of parity: the switch the dialog previews is a switch the
// demo machine applies, so the row afterwards says what a real one would.
func TestDemoAppliesTheSwitch(t *testing.T) {
	machine := demoMachine()
	ctx := context.Background()

	origin := CompanionOrigins(ctx, machine, []string{"headscale"})["headscale"]
	steps, err := BuildCompanionSwitch(machine.Manager(), "headscale", origin)
	if err != nil {
		t.Fatalf("BuildCompanionSwitch: %v", err)
	}
	for _, step := range steps {
		if _, err := machine.Run(ctx, step); err != nil {
			t.Fatalf("%s: %v", step.String(), err)
		}
	}

	installed, _ := CompanionVersions(ctx, machine, []string{"headscale"})
	if installed["headscale"] != "0.26.1-1" {
		t.Errorf("after the switch the machine has %v", installed)
	}
	after := CompanionOrigins(ctx, machine, []string{"headscale"})["headscale"]
	if !after.Family || after.Repo != RepoName {
		t.Errorf("after the switch the origin is %+v", after)
	}
	if len(machine.Ran) != len(steps) {
		t.Errorf("the demo ran %v, want the %d previewed steps",
			machine.Previews(), len(steps))
	}
}

// Installing and removing a companion moves the demo machine the way the
// commands would move a real one.
func TestDemoInstallsAndRemovesACompanion(t *testing.T) {
	machine := demoMachine()
	ctx := context.Background()

	steps, err := BuildCompanionInstall(machine.Manager(),
		[]string{"tui-tools-example"})
	if err != nil {
		t.Fatalf("BuildCompanionInstall: %v", err)
	}
	for _, step := range steps {
		if _, err := machine.Run(ctx, step); err != nil {
			t.Fatalf("%s: %v", step.String(), err)
		}
	}
	installed, _ := CompanionVersions(ctx, machine, []string{"tui-tools-example"})
	if installed["tui-tools-example"] != "0.1.0-1" {
		t.Fatalf("after the install the machine has %v", installed)
	}

	remove, err := BuildCompanionRemove(machine.Manager(),
		[]string{"tui-tools-example"})
	if err != nil {
		t.Fatalf("BuildCompanionRemove: %v", err)
	}
	for _, step := range remove {
		if _, err := machine.Run(ctx, step); err != nil {
			t.Fatalf("%s: %v", step.String(), err)
		}
	}
	installed, _ = CompanionVersions(ctx, machine, []string{"tui-tools-example"})
	if installed["tui-tools-example"] != "" {
		t.Errorf("after the removal the machine still has %v", installed)
	}
}
