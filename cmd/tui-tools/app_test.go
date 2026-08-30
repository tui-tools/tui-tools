package main

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-tools/internal/catalog"
	"github.com/tui-tools/tui-tools/internal/packages"
)

// offline is the source every test reads: the snapshot in the binary, so a
// test never depends on tui.tools answering.
var offline = catalogSource{url: catalog.URL, offline: true}

// newTestApp builds a dashboard around the demo machine and drives its first
// read to completion, so the tests start where a user starts: with the cards
// on screen.
func newTestApp(t *testing.T) (*app, *packages.Fake) {
	t.Helper()

	doc, note := offline.Load(context.Background())
	if note != "" {
		t.Fatalf("the embedded snapshot did not load: %s", note)
	}
	installedPkgs := map[string]string{}
	availablePkgs := map[string]string{}
	for _, tool := range doc.Tools {
		availablePkgs[tool.Package] = "0.1.2"
	}
	// One installed and current, one a version behind, the rest missing.
	installedPkgs[doc.Tools[0].Package] = "0.1.2"
	installedPkgs[doc.Tools[1].Package] = "0.1.1"

	backend := packages.NewFake(doc.Names(), installedPkgs, availablePkgs)
	a := newApp(backend, theme.New(), nil, offline)
	a.width, a.height = 120, 40

	msg := a.Init()()
	if _, cmd := a.Update(msg); cmd != nil {
		t.Fatalf("the first read asked for more work: %T", cmd)
	}
	if len(a.rows) == 0 {
		t.Fatal("the dashboard is empty after a successful read")
	}
	return a, backend
}

// press sends a key and returns whatever the model asked to be done next.
func press(t *testing.T, a *app, key string) tea.Cmd {
	t.Helper()
	var msg tea.Msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	if key == "enter" {
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	}
	_, cmd := a.Update(msg)
	return cmd
}

// selectRow puts the cursor on the tool with a package name.
func selectRow(t *testing.T, a *app, pkg string) {
	t.Helper()
	for i, row := range a.visible {
		if row.Package == pkg {
			a.cursor = i
			return
		}
	}
	t.Fatalf("%s is not on the dashboard", pkg)
}

// The assertion the whole family rests on: the command that runs is the
// command the preview showed, character for character.
func TestInstallRunsExactlyTheCommandsThePreviewShowed(t *testing.T) {
	a, backend := newTestApp(t)

	var missing string
	for _, row := range a.visible {
		if row.State == catalog.StateNotInstalled {
			missing = row.Package
			break
		}
	}
	if missing == "" {
		t.Fatal("the demo machine has everything installed, so there is nothing to install")
	}
	selectRow(t, a, missing)

	press(t, a, "i")
	if a.mode != modeConfirm {
		t.Fatalf("i did not open the confirm dialog, mode = %v", a.mode)
	}
	previewed := a.confirm.Command
	if !strings.Contains(previewed, missing) {
		t.Fatalf("the preview does not name %s: %q", missing, previewed)
	}

	cmd := press(t, a, "y")
	if cmd == nil {
		t.Fatal("confirming ran nothing")
	}
	msg, ok := cmd().(ranMsg)
	if !ok {
		t.Fatalf("confirming produced %T, want a ranMsg", msg)
	}
	if msg.err != nil {
		t.Fatalf("the sequence failed: %v", msg.err)
	}

	ran := strings.Join(backend.Previews(), "\n$ ")
	if ran != previewed {
		t.Errorf("what ran is not what was previewed:\n previewed: %q\n ran:       %q",
			previewed, ran)
	}
}

// And the other half of it: nothing runs that was not confirmed.
func TestCancellingRunsNothing(t *testing.T) {
	a, backend := newTestApp(t)
	selectRow(t, a, a.visible[0].Package)

	press(t, a, "i")
	press(t, a, "u") // an update key while a dialog is open is not an action
	press(t, a, "n")

	if len(backend.Ran) != 0 {
		t.Errorf("cancelling ran %v", backend.Previews())
	}
	if a.mode != modeList {
		t.Errorf("the dialog did not close, mode = %v", a.mode)
	}
}

// Removing is the destructive one, and it has to say so: the dialog is painted
// in the danger colour and the command is a removal of exactly one package.
func TestRemoveIsMarkedDestructive(t *testing.T) {
	a, _ := newTestApp(t)
	var installedPkg string
	for _, row := range a.visible {
		if row.Installed != "" {
			installedPkg = row.Package
			break
		}
	}
	selectRow(t, a, installedPkg)

	press(t, a, "x")
	if a.mode != modeConfirm {
		t.Fatal("x did not open the confirm dialog")
	}
	if !a.confirm.Danger {
		t.Error("a removal was not painted as destructive")
	}
	answer := a.confirm.Payload.(pending)
	for _, step := range answer.steps {
		if !step.Destructive() {
			t.Errorf("step %q is not marked destructive", step.String())
		}
	}
}

// An action that makes no sense is refused before a dialog opens, so nobody
// confirms a command the package manager was going to reject anyway.
func TestActionsThatMakeNoSenseAreRefusedBeforeTheDialog(t *testing.T) {
	a, backend := newTestApp(t)

	for _, row := range a.visible {
		if row.Installed != "" {
			selectRow(t, a, row.Package)
			break
		}
	}
	press(t, a, "i")
	if a.mode == modeConfirm {
		t.Error("installing something already installed opened a dialog")
	}

	for _, row := range a.visible {
		if row.Installed == "" {
			selectRow(t, a, row.Package)
			break
		}
	}
	press(t, a, "x")
	if a.mode == modeConfirm {
		t.Error("removing something that is not installed opened a dialog")
	}
	if len(backend.Ran) != 0 {
		t.Errorf("a refused action still ran %v", backend.Previews())
	}
}

// The repository setup is the one sequence with a condition inside it. It has
// to reach the end when the downloaded key is the pinned one.
func TestRepoSetupImportsAKeyThatMatchesTheFingerprint(t *testing.T) {
	a, backend := newTestApp(t)

	press(t, a, "s")
	if a.mode != modeConfirm {
		t.Fatal("s did not open the confirm dialog")
	}
	if !strings.Contains(a.confirm.Body, packages.Fingerprint) {
		t.Errorf("the dialog does not name the pinned fingerprint: %q", a.confirm.Body)
	}

	cmd := press(t, a, "y")
	msg, ok := cmd().(ranMsg)
	if !ok {
		t.Fatalf("confirming produced %T, want a ranMsg", msg)
	}
	if msg.err != nil {
		t.Fatalf("the setup stopped: %v", msg.err)
	}
	if len(backend.Ran) == 0 {
		t.Fatal("the setup ran nothing")
	}
	if !strings.Contains(strings.Join(msg.transcript, "\n"), packages.Fingerprint) {
		t.Error("the transcript does not record the fingerprint comparison")
	}
}

// wrongKey is a demo machine whose key server hands over a different key. It
// exists to prove the one thing the pinning is for.
type wrongKey struct {
	*packages.Fake
}

// Run answers the fingerprint probe with somebody else's key.
func (w wrongKey) Run(ctx context.Context, cmd pkgmgr.Command) (string, error) {
	if len(cmd.Argv) > 0 && cmd.Argv[0] == "gpg" {
		w.Ran = append(w.Ran, cmd)
		return "fpr:::::::::0000000000000000000000000000000000000000:\n", nil
	}
	return w.Fake.Run(ctx, cmd)
}

func TestRepoSetupStopsWhenTheKeyIsNotTheOnePinned(t *testing.T) {
	a, backend := newTestApp(t)
	a.backend = wrongKey{Fake: backend}

	press(t, a, "s")
	cmd := press(t, a, "y")
	msg, ok := cmd().(ranMsg)
	if !ok {
		t.Fatalf("confirming produced %T, want a ranMsg", msg)
	}
	if msg.err == nil {
		t.Fatal("a key that is not the pinned one was accepted")
	}
	if !strings.Contains(msg.err.Error(), packages.Fingerprint) {
		t.Errorf("the refusal does not say what was expected: %v", msg.err)
	}
	// And the steps after the check never ran: the last thing recorded is the
	// probe itself.
	last := backend.Ran[len(backend.Ran)-1]
	if last.Argv[0] != "gpg" {
		t.Errorf("the sequence continued past the check, last command was %q",
			last.String())
	}
}

// enter hands the terminal over, and only for a tool that is actually here.
func TestLaunchOnlyReachesAnInstalledTool(t *testing.T) {
	a, backend := newTestApp(t)

	for _, row := range a.visible {
		if row.Installed == "" {
			selectRow(t, a, row.Package)
			break
		}
	}
	press(t, a, "enter")
	if len(backend.Launched) != 0 {
		t.Errorf("enter tried to start %v, which is not installed", backend.Launched)
	}

	var here string
	for _, row := range a.visible {
		if row.Installed != "" {
			here = row.Binary
			selectRow(t, a, row.Package)
			break
		}
	}
	if cmd := press(t, a, "enter"); cmd == nil {
		t.Fatal("enter on an installed tool did nothing")
	}
	if len(backend.Launched) != 1 || backend.Launched[0] != here {
		t.Errorf("launched %v, want [%s]", backend.Launched, here)
	}
	// Launching is a handover, not a change: no command was run for it.
	if len(backend.Ran) != 0 {
		t.Errorf("launching ran %v", backend.Previews())
	}
}

// The filter is what makes a fourteen-tool list usable, and it searches
// everything on the card rather than the name alone.
func TestFilterSearchesTheWholeCard(t *testing.T) {
	a, _ := newTestApp(t)
	a.filter = a.rows[0].Category
	a.applyFilter()
	if len(a.visible) == 0 {
		t.Errorf("filtering by the category %q matched nothing", a.rows[0].Category)
	}
	a.filter = "definitely-not-a-tool"
	a.applyFilter()
	if len(a.visible) != 0 {
		t.Errorf("a nonsense filter matched %d tools", len(a.visible))
	}
}

// Every screen has to render at every width the family supports, because a
// panel that wraps desynchronises Bubble Tea's line accounting and every frame
// after it is drawn in the wrong place.
func TestEveryScreenRendersAtEveryWidth(t *testing.T) {
	a, _ := newTestApp(t)
	for _, size := range [][2]int{{40, 12}, {80, 24}, {120, 40}, {200, 60}} {
		a.width, a.height = size[0], size[1]
		a.clampCursor()
		for _, m := range []mode{modeList, modeHelp, modeOutput} {
			a.mode = m
			if a.View() == "" {
				t.Errorf("mode %v rendered nothing at %dx%d", m, size[0], size[1])
			}
		}
		a.mode = modeList
	}
}
