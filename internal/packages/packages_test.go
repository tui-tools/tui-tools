package packages

import (
	"context"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/pkgmgr"
)

// The demo machine is stocked from the names it is given, and nothing else
// reaches its catalogue.
func TestNewFakeIsStockedFromTheCatalogue(t *testing.T) {
	fake := NewFake(
		[]string{"tui-secure", "tui-cert"},
		map[string]string{"tui-secure": "0.1.1", "tui-elsewhere": "9.9.9"},
		map[string]string{"tui-secure": "0.1.2", "tui-cert": "0.1.2"})

	if _, ok := fake.InstalledPkgs["tui-elsewhere"]; ok {
		t.Error("a name that was not in the catalogue was installed anyway")
	}
	if fake.InstalledPkgs["tui-secure"] != "0.1.1" {
		t.Errorf("installed = %v", fake.InstalledPkgs)
	}
	if fake.AvailablePkgs["tui-cert"] != "0.1.2" {
		t.Errorf("available = %v", fake.AvailablePkgs)
	}
}

// The handover is refused for anything that is not a tui-tools binary, which
// is the only check standing between a catalog entry and a process.
func TestLaunchRefusesANameThatIsNotOurs(t *testing.T) {
	fake := NewFake([]string{"tui-secure"},
		map[string]string{"tui-secure": "0.1.2"}, nil)

	for _, name := range []string{"", "rm", "tui-secure; rm -rf /", "../../bin/sh",
		"TUI-SECURE", "tui_secure"} {
		if _, err := fake.Launch(name); err == nil {
			t.Errorf("Launch(%q) was allowed", name)
		}
	}
	if len(fake.Launched) != 0 {
		t.Errorf("a refused name still reached the handover: %v", fake.Launched)
	}
}

func TestLaunchRefusesAToolThatIsNotInstalled(t *testing.T) {
	fake := NewFake([]string{"tui-secure"}, nil,
		map[string]string{"tui-secure": "0.1.2"})
	if _, err := fake.Launch("tui-secure"); err == nil {
		t.Error("a tool that is not installed was launched")
	}
}

// The demo handover starts nothing, and says what it would have started.
func TestDemoLaunchStartsNothing(t *testing.T) {
	fake := NewFake([]string{"tui-secure"},
		map[string]string{"tui-secure": "0.1.2"}, nil)

	process, err := fake.Launch("tui-secure")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if process.String() != "tui-secure" {
		t.Errorf("String() = %q", process.String())
	}
	var out strings.Builder
	process.SetStdout(&out)
	if err := process.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "tui-secure") {
		t.Errorf("the demo handover printed %q", out.String())
	}
}

// The demo answers the fingerprint probe with the family's own key, so the
// comparison the setup makes actually runs in --demo instead of being skipped.
func TestDemoAnswersTheKeyProbeWithThePinnedFingerprint(t *testing.T) {
	fake := NewFake([]string{"tui-secure"}, nil, nil)
	setup, err := fake.RepoSetup(Fingerprint)
	if err != nil {
		t.Fatalf("RepoSetup: %v", err)
	}
	out, err := fake.Run(context.Background(), setup.Steps[setup.Verify])
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !setup.Match(out) {
		t.Errorf("the demo key probe answered %q, which does not match the pin", out)
	}
	// Everything else still goes to the kit's fake.
	if _, err := fake.Run(context.Background(),
		pkgmgr.Command{Argv: []string{"pacman", "-Sy"}}); err != nil {
		t.Fatalf("an ordinary step failed: %v", err)
	}
}

// The pinned fingerprint has to be one gpg could print, or the setup can never
// match it.
func TestFingerprintIsAFullFingerprint(t *testing.T) {
	if _, err := pkgmgr.CheckFingerprint(Fingerprint); err != nil {
		t.Errorf("the pinned fingerprint is not one: %v", err)
	}
}
