// Package packages is the launcher's backend: the one place in this tool that
// reaches the machine.
//
// It is a thin wrapper over tui-kit/pkgmgr, which detects the distribution's
// package manager, reports what is installed and what the repositories offer,
// and builds the install, remove, upgrade and repository-setup commands as
// values. The kit executes them through tui-kit/runner, the family's single
// exec site, so the command line in the confirm dialog is the command line
// that runs. What this package adds is the family's own two facts — the
// signing key this launcher pins, and how to hand the terminal over to an
// installed tool — and both of them live here because this directory is where
// the exec boundary allows a process to be started.
package packages

import (
	"context"

	"github.com/tui-tools/tui-kit/pkgmgr"
)

// Fingerprint is the OpenPGP fingerprint of the key that signs
// pkgs.tui.tools, and the only key this launcher will let a repository setup
// import. It is the same key Omarchy Server's `tui-tools` addon pins and the
// same one pkgs.tui.tools/install.sh imports.
//
// It is written here, once, and nowhere else: tui-kit deliberately carries no
// fingerprint, because a key compiled into a library cannot be rotated without
// a release of the library.
//
// Check it against the published key before trusting this constant:
//
//	curl -fsSL https://pkgs.tui.tools/pubkey.asc | gpg --show-keys
//
// The setup sequence makes the same comparison on the machine: the step that
// downloads the key is followed by a `gpg --show-keys` whose output has to
// carry this fingerprint before anything is allowed to import it.
const Fingerprint = "767CFB337B01F32FFC073F3F389120B277E4FB44"

// Backend is what the UI holds: everything tui-kit/pkgmgr promises, plus the
// handover to an installed tool. Both the real machine and the demo satisfy
// it, so every key works in --demo and nothing reaches the system.
type Backend interface {
	pkgmgr.Interface

	// Launch prepares the handover to an installed tool. The returned
	// process is started by Bubble Tea, which suspends the UI, gives the
	// terminal to the child and restores the screen when it exits.
	Launch(binary string) (Process, error)
}

// Real drives this machine's package manager.
type Real struct {
	*pkgmgr.Real
}

// New detects the package manager and validates the escalation prefix.
func New(sudoPrefix []string) (*Real, error) {
	pm, err := pkgmgr.New(pkgmgr.Options{SudoPrefix: sudoPrefix})
	if err != nil {
		return nil, err
	}
	return &Real{Real: pm}, nil
}

// Fake is the demo machine: a package manager that touches nothing and a
// handover that starts no process.
type Fake struct {
	*pkgmgr.Fake
	// Launched records every binary a demo handover was asked for, so a test
	// can assert that enter reached the right tool without starting one.
	Launched []string
}

// NewFake returns the demo machine, stocked from the tools in the catalogue
// the caller passes in: some installed, one of them a version behind, the rest
// waiting in the repository, which is the picture the dashboard exists to
// show.
func NewFake(names []string, installed, available map[string]string) *Fake {
	machine := pkgmgr.NewFake()
	machine.InstalledPkgs = map[string]string{}
	machine.AvailablePkgs = map[string]string{}
	for _, name := range names {
		if version, ok := available[name]; ok {
			machine.AvailablePkgs[name] = version
		}
		if version, ok := installed[name]; ok {
			machine.InstalledPkgs[name] = version
		}
	}
	return &Fake{Fake: machine}
}

// Run answers the demo machine's commands.
//
// One of them is answered here rather than by the kit's fake: the step of the
// repository setup that reads the downloaded key's fingerprint. The kit's fake
// answers "ok" to everything, and the launcher — correctly — refuses to import
// a key whose fingerprint it did not recognise, so a demo would stop there and
// the one code path worth showing would never run. Instead the demo hands back
// what gpg prints for the family's own key, so the comparison happens, passes,
// and the sequence continues exactly as it does on a real machine.
//
// This is the only place a demo answer is invented, and it is invented to make
// a check run rather than to skip one.
func (f *Fake) Run(ctx context.Context, cmd pkgmgr.Command) (string, error) {
	if isKeyProbe(cmd) {
		f.Ran = append(f.Ran, cmd)
		return "fpr:::::::::" + Fingerprint + ":\n", nil
	}
	return f.Fake.Run(ctx, cmd)
}

// isKeyProbe reports whether a step is the `gpg --show-keys` the setup uses to
// read the fingerprint of the key it just downloaded.
func isKeyProbe(cmd pkgmgr.Command) bool {
	if len(cmd.Argv) < 2 || cmd.Argv[0] != "gpg" {
		return false
	}
	for _, arg := range cmd.Argv[1:] {
		if arg == "--show-keys" {
			return true
		}
	}
	return false
}
