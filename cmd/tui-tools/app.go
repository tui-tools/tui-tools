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
	// modeDetail is the full status of one entry. It is what enter opens on a
	// companion, which has no terminal UI to hand the screen over to.
	modeDetail
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
	// visible holds the rows left after the filter, in display order: the
	// tools first, then the companions.
	visible []catalog.Row
	// lines is what the list actually draws — the visible rows with the group
	// headings between them. The cursor indexes visible; the scroll offset
	// indexes this, because a heading takes a line of the screen like anything
	// else.
	lines []line
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
	// The same two answers for the companion packages, plus where the
	// installed ones came from.
	companionInstalled map[string]string
	companionAvailable map[string]string
	origins            map[string]catalog.Origin
	repo               pkgmgr.RepoStatus
	err                error
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
		msg.companionInstalled, msg.companionAvailable, msg.origins =
			readCompanions(ctx, backend, doc)
		msg.repo, _ = backend.RepoStatus()
		return msg
	}
}

// readCompanions asks the machine about the companion packages: what is here,
// what the repositories offer, and — for the ones that are here — which
// repository the copy on the disk came from.
//
// The origin is only asked about installed packages, because an origin is a
// fact about a copy on the disk and there is no copy of something that is not
// installed. All of it is read-only, and a manager that answers nothing leaves
// the rows saying "cannot say" rather than emptying the screen.
func readCompanions(ctx context.Context, backend packages.Backend,
	doc catalog.Catalog) (installed, available map[string]string,
	origins map[string]catalog.Origin) {
	names := doc.CompanionNames()
	if len(names) == 0 {
		return nil, nil, nil
	}
	installed, available = packages.CompanionVersions(ctx, backend, names)

	here := make([]string, 0, len(names))
	for _, name := range names {
		if installed[name] != "" {
			here = append(here, name)
		}
	}
	return installed, available, packages.CompanionOrigins(ctx, backend, here)
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
	// The companions come after the tools, and stay after them: the order the
	// rows are built in is the order the group headings are drawn from.
	a.rows = append(
		catalog.Rows(msg.catalog, msg.installed, msg.available),
		catalog.CompanionRows(msg.catalog, msg.companionInstalled,
			msg.companionAvailable, msg.origins)...)
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
	case modeHelp, modeDetail:
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
	case "o":
		return a, a.confirmSwitch()
	case "enter":
		return a.open()
	}
	return a, nil
}

// open answers enter: a tool is launched, and a companion — which is not a
// terminal UI and has nothing to hand the screen over to — opens its status
// instead of doing nothing.
func (a *app) open() (tea.Model, tea.Cmd) {
	row, ok := a.selected()
	if !ok {
		a.setStatus(ui.StatusWarn, "nothing selected")
		return a, nil
	}
	if row.IsCompanion() {
		a.mode = modeDetail
		return a, nil
	}
	return a.launch()
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

	steps, err := a.steps(what, row)
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

// steps builds an action's command sequence for a row.
//
// A tool goes through tui-kit, which validates its name against ^tui-[a-z]+$
// on the way. A companion cannot: its name is the upstream project's own, so
// the same argv shapes are built by internal/packages, which holds it to the
// companion pattern instead. Both end up as values the dialog previews and the
// kit runner executes.
func (a *app) steps(what action, row catalog.Row) ([]pkgmgr.Command, error) {
	names := []string{row.Package}
	if row.IsCompanion() {
		switch what {
		case actionInstall:
			return packages.BuildCompanionInstall(a.backend.Manager(), names)
		case actionUpgrade:
			return packages.BuildCompanionUpgrade(a.backend.Manager(), names)
		default:
			return packages.BuildCompanionRemove(a.backend.Manager(), names)
		}
	}
	switch what {
	case actionInstall:
		return a.backend.Install(names)
	case actionUpgrade:
		return a.backend.Upgrade(names)
	default:
		return a.backend.Remove(names)
	}
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
		if row.IsCompanion() && row.Available == "" {
			return "no repository configured on this machine carries " +
				row.Package + "; press s to set the family repository up", true
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
		body := row.Package + " will be installed from the tui-tools repository, " +
			"which your package manager verifies before anything is unpacked."
		if row.Kind == catalog.KindMirror {
			body += " It is " + packages.ProvenanceLine + "."
		}
		return body
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

// confirmSwitch opens the previewed sequence that replaces an installed
// companion package with the family's own build of it.
//
// It is offered only when the machine's copy came from somewhere else and the
// family repository carries it, because that is the only case where the
// question — is this the build that went through the family's signing and
// provenance gate — has a different answer afterwards.
func (a *app) confirmSwitch() tea.Cmd {
	row, ok := a.selected()
	if !ok {
		a.setStatus(ui.StatusWarn, "nothing selected")
		return nil
	}
	if reason, refuse := refuseSwitch(row); refuse {
		a.setStatus(ui.StatusWarn, reason)
		return nil
	}

	steps, err := packages.BuildCompanionSwitch(a.backend.Manager(),
		row.Package, row.Origin)
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}

	title := "Switch " + row.Package + " to the " + packages.RepoName + " build"
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title: title,
		Body: a.wrapBody(row.Package + " is installed from " +
			originName(row.Origin) + ". The " + packages.RepoName +
			" build is " + packages.ProvenanceLine + ", and this replaces the " +
			"copy on this machine with it."),
		Command: a.previewAll(steps),
		Payload: pending{title: title, steps: steps},
	}
	return nil
}

// refuseSwitch says why a switch makes no sense for a row.
func refuseSwitch(row catalog.Row) (string, bool) {
	switch {
	case !row.IsCompanion():
		return "only a companion package can be switched: a tui-tools tool " +
			"exists in no repository but the family's", true
	case row.Installed == "":
		return row.Package + " is not installed; press i to install it " +
			"from the " + packages.RepoName + " repository", true
	case !row.Origin.Offered:
		return "the " + packages.RepoName + " repository does not carry " +
			row.Package + " on this machine", true
	case row.Origin.Family:
		return row.Package + " already comes from the " + packages.RepoName +
			" repository", true
	}
	return "", false
}

// originName renders an origin the way a sentence reads it, so an unknown one
// is a phrase rather than an empty gap.
func originName(origin catalog.Origin) string {
	if origin.Repo == "" {
		return "a repository this machine cannot name"
	}
	return "the " + origin.Repo + " repository"
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

// applyFilter recomputes the visible rows and the lines that draw them.
func (a *app) applyFilter() {
	if a.filter == "" {
		a.visible = a.rows
		a.buildLines()
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
	a.buildLines()
	a.clampCursor()
}

// haystack is what the filter searches: everything on the card, which for a
// companion includes its kind and the project it mirrors.
func haystack(row catalog.Row) string {
	return strings.Join([]string{
		row.Name, row.Tagline, row.Category, string(row.State),
		string(row.Kind), row.Upstream, row.Origin.Repo,
		strings.Join(row.Backends, " "),
	}, " ")
}

// line is one drawn line of the list: a group heading, or one of the visible
// rows. The cursor only ever lands on a row; a heading is scenery.
type line struct {
	// heading is the group's name, and empty on a row.
	heading string
	// row is the index into visible, and -1 on a heading.
	row int
}

// The heading that separates the two groups, and the line that says what the
// second one is. The tools need no heading: the table's own header already says
// what the first group is, and a screen that labels the obvious is a screen with
// one less row of tools on it.
const (
	companionsHeading = "COMPANIONS"
	companionsNote    = "family packages that are not terminal UIs"
)

// buildLines lays the visible rows out with a heading in front of the first
// companion, which is what makes the second group a group rather than more of
// the first.
func (a *app) buildLines() {
	lines := make([]line, 0, len(a.visible)+1)
	headed := false
	for i, row := range a.visible {
		if row.IsCompanion() && !headed {
			headed = true
			lines = append(lines, line{heading: companionsHeading, row: -1})
		}
		lines = append(lines, line{row: i})
	}
	a.lines = lines
}

// lineOf returns the line a visible row is drawn on.
func (a *app) lineOf(row int) int {
	for i, l := range a.lines {
		if l.row == row {
			return i
		}
	}
	return 0
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
//
// The cursor counts rows and the offset counts lines, because a group heading
// occupies a line of the screen without ever being selected. The heading above
// the selection is pulled into view with it, so scrolling into the companions
// never shows a group without its name.
func (a *app) clampCursor() {
	if len(a.visible) == 0 {
		a.cursor, a.offset = 0, 0
		return
	}
	a.cursor = min(max(a.cursor, 0), len(a.visible)-1)

	height := a.listHeight()
	at := a.lineOf(a.cursor)
	if at < a.offset {
		a.offset = at
		if at > 0 && a.lines[at-1].heading != "" {
			a.offset = at - 1
		}
	}
	if at >= a.offset+height {
		a.offset = at - height + 1
	}
	a.offset = max(min(a.offset, max(len(a.lines)-height, 0)), 0)
}
