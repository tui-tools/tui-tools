package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tools/internal/catalog"
	"github.com/tui-tools/tui-tools/internal/packages"
)

// mode is the screen the app currently shows. Only one is open at a time,
// which keeps the update loop flat.
type mode int

const (
	// modeList is the dashboard: the cards and the detail pane.
	modeList mode = iota
	// modeConfirm is the preview of a command sequence, which is the only
	// path to a change.
	modeConfirm
	// modeFilter is the filter prompt.
	modeFilter
	// modeHelp is the key list.
	modeHelp
	// modeOutput is the transcript of the sequence that just ran, which is
	// what a failed install has to show rather than swallow.
	modeOutput
)

// The two budgets. Reading is a local database query and a small HTTP fetch;
// an install downloads packages, so its budget is the generous one.
const (
	readTimeout = 60 * time.Second
	runTimeout  = 30 * time.Minute
)

// app is the tui-tools Bubble Tea model.
type app struct {
	backend packages.Backend
	theme   theme.Theme
	// backends is what the package-manager version probe found, rendered in
	// the header.
	backends []compat.Result
	// source says where the catalog is read from.
	source catalogSource

	catalog catalog.Catalog
	// catalogNote is why the live catalog was not used, when it was not.
	catalogNote string
	rows        []catalog.Row
	// visible holds the rows left after the filter, in display order.
	visible []catalog.Row
	// repo is what the repository probe found.
	repo pkgmgr.RepoStatus

	width, height int
	cursor        int
	offset        int
	filter        string

	mode    mode
	confirm ui.Confirm
	input   ui.Input

	// transcript is what the last sequence printed, command by command.
	transcript      []string
	transcriptTitle string
	// transcriptOffset scrolls it.
	transcriptOffset int

	status     string
	statusKind ui.StatusKind
	loading    bool
	// loadFailed reports that the last read failed, so the empty state does
	// not claim the family is empty.
	loadFailed bool
	// busy blocks input while a sequence runs.
	busy bool
}

// loadedMsg carries the result of a read: the catalog, and what this machine
// says about the packages in it.
type loadedMsg struct {
	catalog   catalog.Catalog
	note      string
	installed map[string]string
	available map[string]string
	repo      pkgmgr.RepoStatus
	err       error
}

// ranMsg carries the result of a previewed sequence.
type ranMsg struct {
	title      string
	transcript []string
	err        error
}

// launchedMsg carries the result of handing the terminal to another tool.
type launchedMsg struct {
	name string
	err  error
}

// pending is what a confirm dialog carries: the exact commands that will run
// if the answer is yes, and nothing that could produce different ones.
type pending struct {
	title string
	steps []pkgmgr.Command
	// setup is set when the sequence is the repository setup, whose key
	// import is only allowed to proceed after one step's output has been
	// matched against the pinned fingerprint.
	setup   pkgmgr.Setup
	isSetup bool
}

// newApp builds the model around a backend.
func newApp(backend packages.Backend, th theme.Theme, backends []compat.Result,
	source catalogSource) *app {
	a := &app{
		backend: backend, theme: th, backends: backends, source: source,
		width: 80, height: 24, loading: true,
	}
	if th.Warning != "" {
		a.setStatus(ui.StatusWarn, th.Warning)
	}
	return a
}

// Init starts the first read.
func (a *app) Init() tea.Cmd { return a.load(true) }

// load reads the catalog and the machine in the background.
//
// fetch says whether the catalog is re-read too. After an install it is not:
// what changed is the machine, and re-fetching a document from the network to
// learn that a package is now installed would be asking the wrong authority.
func (a *app) load(fetch bool) tea.Cmd {
	backend := a.backend
	source := a.source
	current := a.catalog
	note := a.catalogNote
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()

		doc := current
		if fetch || len(doc.Tools) == 0 {
			doc, note = source.Load(ctx)
			if len(doc.Tools) == 0 {
				return loadedMsg{note: note, err: fmt.Errorf("no catalog: %s", note)}
			}
		}

		msg := loadedMsg{catalog: doc, note: note}
		names := doc.Names()
		var err error
		if msg.installed, err = backend.Installed(ctx, names); err != nil {
			msg.err = err
			return msg
		}
		// A repository that carries nothing, or none at all, is an answer
		// worth showing rather than a failure: the cards then say what is
		// installed and stay quiet about what is current.
		msg.available, _ = backend.Available(ctx, names)
		msg.repo, _ = backend.RepoStatus()
		return msg
	}
}

// runSequence executes a previewed sequence, step by step, stopping at the
// first failure.
//
// The repository setup is the one sequence with a condition inside it: the
// step that reads the downloaded key's fingerprint has to match the one this
// launcher pins before the steps that import it are allowed to run. A caller
// that skipped that check would have imported whatever the network handed
// over, which is the failure the pinning exists to prevent.
func (a *app) runSequence(answer pending) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		defer cancel()

		transcript := make([]string, 0, len(answer.steps)*2)
		for i, step := range answer.steps {
			transcript = append(transcript, "$ "+backend.Preview(step))
			out, err := backend.Run(ctx, step)
			if trimmed := strings.TrimSpace(out); trimmed != "" {
				transcript = append(transcript, trimmed)
			}
			if err != nil {
				return ranMsg{title: answer.title, transcript: transcript, err: err}
			}
			if answer.isSetup && i == answer.setup.Verify {
				if !answer.setup.Match(out) {
					return ranMsg{
						title:      answer.title,
						transcript: transcript,
						err: fmt.Errorf(
							"the downloaded key is not %s, so it was not imported "+
								"and the setup stopped here",
							answer.setup.Fingerprint),
					}
				}
				transcript = append(transcript,
					"key matches the pinned fingerprint "+answer.setup.Fingerprint)
			}
		}
		return ranMsg{title: answer.title, transcript: transcript}
	}
}

// setStatus records a message for the status line.
func (a *app) setStatus(kind ui.StatusKind, message string) {
	a.status = message
	a.statusKind = kind
}

// setStatusf records a formatted message for the status line.
func (a *app) setStatusf(kind ui.StatusKind, format string, args ...any) {
	a.setStatus(kind, fmt.Sprintf(format, args...))
}

// Update is the main event loop.
func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.clampCursor()
		return a, nil

	case loadedMsg:
		return a.handleLoaded(msg)

	case ranMsg:
		return a.handleRan(msg)

	case launchedMsg:
		a.busy = false
		if msg.err != nil {
			a.setStatusf(ui.StatusError, "%s: %v", msg.name, msg.err)
			return a, nil
		}
		a.setStatusf(ui.StatusInfo, "%s exited", msg.name)
		// Another tool of the family may have changed this machine — the
		// launcher's own promise is that the screen shows the machine, not
		// what it remembers of it.
		a.loading = true
		return a, a.load(false)

	case tea.KeyMsg:
		return a.handleKey(msg)
	}

	// Anything else (cursor blink, …) only concerns an open text input.
	if a.mode == modeFilter {
		cmd, _ := a.input.Update(msg)
		return a, cmd
	}
	return a, nil
}

// handleLoaded folds a finished read into the model.
func (a *app) handleLoaded(msg loadedMsg) (tea.Model, tea.Cmd) {
	a.loading = false
	a.catalogNote = msg.note
	if msg.err != nil {
		a.loadFailed = true
		a.setStatus(ui.StatusError, msg.err.Error())
		return a, nil
	}
	a.loadFailed = false
	a.catalog = msg.catalog
	a.repo = msg.repo
	a.rows = catalog.Rows(msg.catalog, msg.installed, msg.available)
	a.applyFilter()

	switch {
	case msg.note != "":
		a.setStatusf(ui.StatusWarn,
			"showing the catalog snapshot built into this binary: %s", msg.note)
	case !a.repo.Configured:
		a.setStatus(ui.StatusWarn,
			"the tui-tools repository is not configured on this machine — "+
				"press s to see the setup")
	}
	return a, nil
}

// handleRan folds a finished sequence into the model.
func (a *app) handleRan(msg ranMsg) (tea.Model, tea.Cmd) {
	a.busy = false
	a.transcript = msg.transcript
	a.transcriptTitle = msg.title
	a.transcriptOffset = 0
	if msg.err != nil {
		a.mode = modeOutput
		a.setStatusf(ui.StatusError, "%s failed: %v", msg.title, msg.err)
		return a, nil
	}
	a.setStatusf(ui.StatusOK, "%s: done", msg.title)
	// Re-read after every change: the machine is the source of truth, not
	// what the tool assumed would happen.
	a.loading = true
	return a, a.load(false)
}

// handleKey routes a key press to the open screen.
func (a *app) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always quits, even mid-dialog.
	if msg.Type == tea.KeyCtrlC {
		return a, tea.Quit
	}
	if a.busy {
		// A sequence is running: swallow input rather than queueing surprises.
		return a, nil
	}

	switch a.mode {
	case modeConfirm:
		return a.handleConfirm(msg)
	case modeFilter:
		return a.handleFilter(msg)
	case modeHelp:
		a.mode = modeList
		return a, nil
	case modeOutput:
		return a.handleOutputKey(msg)
	default:
		return a.handleListKey(msg)
	}
}

// handleConfirm resolves the confirm dialog. This is the only path to a change.
func (a *app) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.confirm.Update(msg)
	if !a.confirm.Done {
		return a, nil
	}
	a.mode = modeList
	confirmed := a.confirm.Confirmed
	answer, ok := a.confirm.Payload.(pending)
	a.confirm = ui.Confirm{}
	if !confirmed || !ok || len(answer.steps) == 0 {
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}
	a.busy = true
	a.setStatusf(ui.StatusInfo, "%s: running %d command(s)…",
		answer.title, len(answer.steps))
	return a, a.runSequence(answer)
}

// handleFilter resolves the filter prompt.
func (a *app) handleFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, _ := a.input.Update(msg)
	if !a.input.Done {
		// Filter as the user types.
		a.filter = a.input.Value()
		a.applyFilter()
		return a, cmd
	}
	if a.input.Accepted {
		a.filter = a.input.Value()
	} else {
		a.filter = ""
	}
	a.applyFilter()
	a.mode = modeList
	return a, nil
}

// handleOutputKey scrolls the transcript screen.
func (a *app) handleOutputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "enter":
		a.mode = modeList
		a.loading = true
		return a, a.load(false)
	case "j", "down":
		a.transcriptOffset++
	case "k", "up":
		a.transcriptOffset = max(a.transcriptOffset-1, 0)
	}
	return a, nil
}

// handleListKey handles the dashboard.
func (a *app) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return a, tea.Quit
	case "?":
		a.mode = modeHelp
	case "j", "down":
		a.moveCursor(1)
	case "k", "up":
		a.moveCursor(-1)
	case "g", "home":
		a.cursor, a.offset = 0, 0
	case "G", "end":
		a.cursor = max(len(a.visible)-1, 0)
		a.clampCursor()
	case "pgdown", "ctrl+f":
		a.moveCursor(a.listHeight())
	case "pgup", "ctrl+b":
		a.moveCursor(-a.listHeight())
	case "/":
		a.input = ui.NewInput("Filter", "name, tagline or category…", a.filter)
		a.input.Help = "Empty clears the filter."
		a.mode = modeFilter
	case "r", "ctrl+r":
		a.loading = true
		a.setStatus(ui.StatusInfo, "re-reading the catalog and the machine…")
		return a, a.load(true)
	case "i":
		return a, a.confirmPackage(actionInstall)
	case "u":
		return a, a.confirmPackage(actionUpgrade)
	case "x", "d":
		return a, a.confirmPackage(actionRemove)
	case "s":
		return a, a.confirmRepoSetup()
	case "enter":
		return a.launch()
	}
	return a, nil
}

// action is one of the three things the package manager is asked to do.
type action string

// The three package actions, which are also their labels.
const (
	actionInstall action = "Install"
	actionUpgrade action = "Update"
	actionRemove  action = "Remove"
)

// confirmPackage builds an action's command sequence and opens the confirm
// dialog carrying every step of it. Nothing runs until that dialog is
// answered, and what runs is the value the dialog previewed.
func (a *app) confirmPackage(what action) tea.Cmd {
	row, ok := a.selected()
	if !ok {
		a.setStatus(ui.StatusWarn, "nothing selected")
		return nil
	}
	if reason, refuse := refuse(what, row); refuse {
		a.setStatus(ui.StatusWarn, reason)
		return nil
	}

	var (
		steps []pkgmgr.Command
		err   error
	)
	switch what {
	case actionInstall:
		steps, err = a.backend.Install([]string{row.Package})
	case actionUpgrade:
		steps, err = a.backend.Upgrade([]string{row.Package})
	case actionRemove:
		steps, err = a.backend.Remove([]string{row.Package})
	}
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}

	title := string(what) + " " + row.Package
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   title,
		Body:    a.wrapBody(actionBody(what, row)),
		Command: a.previewAll(steps),
		Danger:  what == actionRemove,
		Payload: pending{title: title, steps: steps},
	}
	return nil
}

// refuse says why an action makes no sense for a row, so the answer arrives
// before a confirm dialog rather than as a package manager error after one.
func refuse(what action, row catalog.Row) (string, bool) {
	switch what {
	case actionInstall:
		if row.Installed != "" {
			return row.Package + " is already installed", true
		}
		if !row.Supported {
			return row.Package + " ships no build for this architecture", true
		}
	case actionUpgrade:
		if row.Installed == "" {
			return row.Package + " is not installed", true
		}
	case actionRemove:
		if row.Installed == "" {
			return row.Package + " is not installed", true
		}
	}
	return "", false
}

// actionBody is the sentence above the command preview: what the sequence will
// do, in the user's terms.
func actionBody(what action, row catalog.Row) string {
	switch what {
	case actionInstall:
		return row.Package + " will be installed from the tui-tools repository, " +
			"which your package manager verifies before anything is unpacked."
	case actionUpgrade:
		return fmt.Sprintf(
			"%s will be upgraded from %s to %s.", row.Package,
			blank(row.Installed), blank(row.Available))
	default:
		return row.Package + " will be removed. What it pulled in stays: " +
			"nothing is autoremoved."
	}
}

// blank renders an empty version as a word rather than as nothing.
func blank(version string) string {
	if version == "" {
		return "unknown"
	}
	return version
}

// confirmRepoSetup opens the previewed sequence that adds the family
// repository, with the signing key pinned by fingerprint.
func (a *app) confirmRepoSetup() tea.Cmd {
	setup, err := a.backend.RepoSetup(packages.Fingerprint)
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	title := "Set up the tui-tools repository"
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title: title,
		Body: a.wrapBody("The repository at " + a.catalog.Packages.Repo +
			" will be added to " + string(a.backend.Manager()) +
			". The key is pinned: step " + fmt.Sprint(setup.Verify+1) +
			" reads the downloaded key's fingerprint, and the import only " +
			"happens if it is " + setup.Fingerprint + "."),
		Command: a.previewAll(setup.Steps),
		Payload: pending{
			title: title, steps: setup.Steps, setup: setup, isSetup: true,
		},
	}
	return nil
}

// launch hands the terminal to the selected tool. Bubble Tea suspends the
// program, the tool draws on the real terminal, and the dashboard is restored
// when it exits.
//
// It is not previewed as a mutation because it is not one: this starts another
// tool of the family, and that tool previews and confirms whatever it changes.
func (a *app) launch() (tea.Model, tea.Cmd) {
	row, ok := a.selected()
	if !ok {
		a.setStatus(ui.StatusWarn, "nothing selected")
		return a, nil
	}
	if row.Installed == "" {
		a.setStatusf(ui.StatusWarn, "%s is not installed — press i to install it",
			row.Package)
		return a, nil
	}
	process, err := a.backend.Launch(row.Binary)
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return a, nil
	}
	a.busy = true
	a.setStatusf(ui.StatusInfo, "running %s…", process)
	name := row.Binary
	return a, tea.Exec(process, func(err error) tea.Msg {
		return launchedMsg{name: name, err: err}
	})
}

// wrapBody folds a dialog's explanation to the width the dialog has. The kit
// truncates a line that does not fit rather than wrapping it, and an
// explanation of what is about to change is not a thing to lose the end of.
func (a *app) wrapBody(text string) string {
	return strings.Join(wrap(text, min(max(a.width-8, 20), 72), 8), "\n")
}

// previewAll renders every command of a sequence, one per line, each with the
// prompt the dialog puts in front of the first one.
func (a *app) previewAll(steps []pkgmgr.Command) string {
	previews := make([]string, 0, len(steps))
	for _, step := range steps {
		previews = append(previews, a.backend.Preview(step))
	}
	return strings.Join(previews, "\n$ ")
}

// applyFilter recomputes the visible rows.
func (a *app) applyFilter() {
	if a.filter == "" {
		a.visible = a.rows
		a.clampCursor()
		return
	}
	needle := strings.ToLower(a.filter)
	kept := make([]catalog.Row, 0, len(a.rows))
	for _, row := range a.rows {
		if strings.Contains(strings.ToLower(haystack(row)), needle) {
			kept = append(kept, row)
		}
	}
	a.visible = kept
	a.clampCursor()
}

// haystack is what the filter searches: everything on the card.
func haystack(row catalog.Row) string {
	return strings.Join([]string{
		row.Name, row.Tagline, row.Category, string(row.State),
		strings.Join(row.Backends, " "),
	}, " ")
}

// selected returns the highlighted row.
func (a *app) selected() (catalog.Row, bool) {
	if a.cursor < 0 || a.cursor >= len(a.visible) {
		return catalog.Row{}, false
	}
	return a.visible[a.cursor], true
}

// moveCursor moves the selection and keeps the viewport in sync.
func (a *app) moveCursor(delta int) {
	a.cursor += delta
	a.clampCursor()
}

// clampCursor keeps the cursor and the scroll offset within range.
func (a *app) clampCursor() {
	if len(a.visible) == 0 {
		a.cursor, a.offset = 0, 0
		return
	}
	a.cursor = min(max(a.cursor, 0), len(a.visible)-1)

	height := a.listHeight()
	if a.cursor < a.offset {
		a.offset = a.cursor
	}
	if a.cursor >= a.offset+height {
		a.offset = a.cursor - height + 1
	}
	a.offset = max(min(a.offset, max(len(a.visible)-height, 0)), 0)
}
