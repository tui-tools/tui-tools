package main

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tools/internal/catalog"
)

// Layout constants: the rows the card list cannot use.
const (
	headerLines = 2
	footerLines = 2
	// detailLines is the height of the detail pane, including its rule.
	detailLines = 8
	// detailMinHeight is the terminal height below which the detail pane is
	// dropped: on a short terminal the cards themselves are what matter.
	detailMinHeight = 22
	// minListHeight keeps at least one visible card on a very short terminal.
	minListHeight = 1
)

// showsDetail reports whether the detail pane fits.
func (a *app) showsDetail() bool { return a.height >= detailMinHeight }

// listHeight is the number of cards that fit on screen.
func (a *app) listHeight() int {
	// header + table header + detail pane + help bar + status line.
	used := headerLines + 1 + footerLines
	if a.showsDetail() {
		used += detailLines
	}
	return max(a.height-used, minListHeight)
}

// View renders the whole screen.
func (a *app) View() string {
	switch a.mode {
	case modeConfirm:
		return a.confirm.View(a.theme, a.width, a.height)
	case modeFilter:
		return a.input.View(a.theme, a.width, a.height)
	case modeHelp:
		return lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center,
			ui.HelpScreen(a.theme, "tui-tools — keys", helpKeys(), a.width))
	case modeOutput:
		return a.outputView()
	default:
		return a.listView()
	}
}

// listView renders the dashboard: header, cards, detail pane, help bar,
// status line.
func (a *app) listView() string {
	var body string
	switch {
	case a.loading && len(a.visible) == 0:
		body = ui.EmptyState(a.theme, "reading the catalog and the machine…",
			a.width, a.listHeight()+1)
	case len(a.visible) == 0 && a.loadFailed:
		body = ui.EmptyState(a.theme, "could not read — see the message below",
			a.width, a.listHeight()+1)
	case len(a.visible) == 0 && a.filter != "":
		body = ui.EmptyState(a.theme, "no tool matches "+strconv.Quote(a.filter),
			a.width, a.listHeight()+1)
	case len(a.visible) == 0:
		body = ui.EmptyState(a.theme, "the catalog carries no tool this "+
			"launcher may act on", a.width, a.listHeight()+1)
	default:
		body = a.cards()
	}

	bands := []string{a.header(), body}
	if a.showsDetail() {
		bands = append(bands, a.detail())
	}
	bands = append(bands,
		ui.HelpBar(a.theme, shortHelpKeys(), a.width),
		ui.StatusLine(a.theme, a.statusKind, a.status, a.defaultStatus(), a.width))
	return strings.Join(bands, "\n")
}

// header renders the facts at the top: the machine, the repository and where
// the catalog on screen came from.
func (a *app) header() string {
	installedCount := 0
	for _, row := range a.rows {
		if row.Installed != "" {
			installedCount++
		}
	}

	facts := []ui.Fact{{
		Label: "family",
		Value: strconv.Itoa(installedCount) + " of " +
			strconv.Itoa(len(a.rows)) + " installed",
	}}

	repoStyle := a.theme.Warn
	repoValue := "not configured — press s"
	if a.repo.Configured {
		repoStyle = a.theme.OK
		repoValue = "configured"
	}
	facts = append(facts, ui.Fact{
		Label: "repository", Value: repoValue, Style: &repoStyle})

	facts = append(facts, ui.Fact{Label: "catalog", Value: a.catalogFact()})

	// The package manager's version, for the one manager this machine has.
	for _, result := range installed(a.backends) {
		facts = append(facts, ui.CompatFact(a.theme, result))
	}

	subtitle := a.backend.Describe()
	if a.filter != "" {
		subtitle += "  ·  filter: " + a.filter
	}
	return ui.Header{Title: "tui-tools", Subtitle: subtitle, Facts: facts}.
		Render(a.theme, a.width)
}

// catalogFact says which catalog is on screen and when it was generated. Live
// and embedded are different claims about the world, and the difference
// belongs on screen rather than in a log.
func (a *app) catalogFact() string {
	source := string(a.catalog.Source)
	if source == "" {
		source = "unknown"
	}
	if a.catalog.Generated.IsZero() {
		return source
	}
	return source + " · " + a.catalog.Generated.UTC().Format("2006-01-02 15:04Z")
}

// defaultStatus is the hint shown when there is no message to report.
func (a *app) defaultStatus() string {
	count := strconv.Itoa(len(a.visible))
	if len(a.visible) != len(a.rows) {
		return count + " of " + strconv.Itoa(len(a.rows)) +
			" tools  ·  enter to launch  ·  ? for help"
	}
	return count + " tools  ·  enter to launch  ·  ? for help"
}

// cards renders the tool list, dropping columns on narrow terminals.
func (a *app) cards() string {
	columns := []ui.Column{
		{Title: "TOOL", Width: 20},
		{Title: "STATE", Width: 17},
		{Title: "VERSION", Width: 17},
	}
	showTagline := a.width >= 78
	if showTagline {
		columns = append(columns, ui.Column{Title: "WHAT IT DOES", Width: 24, Flex: true})
	}
	showCategory := a.width >= 104
	if showCategory {
		columns = append(columns, ui.Column{Title: "CATEGORY", Width: 14})
	}

	rows := make([][]string, 0, len(a.visible))
	styles := make([]*lipgloss.Style, 0, len(a.visible))
	for _, row := range a.visible {
		cells := []string{
			glyph(row) + " " + row.Name,
			string(row.State),
			versionCell(row),
		}
		if showTagline {
			cells = append(cells, row.Tagline)
		}
		if showCategory {
			cells = append(cells, row.Category)
		}
		rows = append(rows, cells)
		styles = append(styles, a.rowStyle(row))
	}

	return ui.Table{
		Columns: columns, Rows: rows, Styles: styles,
		Selected: a.cursor, Offset: a.offset, Height: a.listHeight(),
	}.Render(a.theme, a.width)
}

// glyph is the one character that says what the machine has. The catalog's
// icon is an SVG a terminal cannot draw, so the marker here is the state,
// which is what the eye is looking for on this screen anyway.
func glyph(row catalog.Row) string {
	switch {
	case !row.Supported:
		return "!"
	case row.State == catalog.StateOutdated:
		return "↑"
	case row.Installed != "":
		return "●"
	default:
		return "○"
	}
}

// versionCell renders the versions the way the state reads them.
func versionCell(row catalog.Row) string {
	switch row.State {
	case catalog.StateOutdated:
		return row.Installed + " → " + row.Available
	case catalog.StateNotInstalled:
		if row.Available != "" {
			return row.Available
		}
		return "-"
	default:
		return row.Installed
	}
}

// rowStyle colours a card, so the eye finds what matters without reading.
func (a *app) rowStyle(row catalog.Row) *lipgloss.Style {
	var style lipgloss.Style
	switch {
	case !row.Supported:
		style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
	case row.State == catalog.StateOutdated:
		style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
	case row.State == catalog.StateNotInstalled:
		style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	default:
		style = a.theme.Row
	}
	return &style
}

// detail renders the pane under the cards: what the selected tool is, what it
// drives, where to read about it and how to check a release of it.
func (a *app) detail() string {
	t := a.theme
	rule := t.Muted.Render(strings.Repeat("─", max(a.width, 1)))

	row, ok := a.selected()
	if !ok {
		return rule
	}

	width := max(a.width, 20)
	lines := []string{rule}

	title := t.Title.Render(row.Name) + t.Muted.Render("  ·  "+row.Category) +
		t.Muted.Render("  ·  "+row.Compat)
	lines = append(lines, ui.Truncate(title, width))
	lines = append(lines, ui.Truncate(t.Subtitle.Render(row.Tagline), width))

	summary := wrap(firstParagraph(row.Description), width, 2)
	for len(summary) < 2 {
		summary = append(summary, "")
	}
	for _, line := range summary {
		lines = append(lines, ui.Truncate(t.Base.Render(line), width))
	}

	backends := "none — it shells out to nothing"
	if len(row.Backends) > 0 {
		backends = strings.Join(row.Backends, ", ")
	}
	lines = append(lines, ui.Truncate(
		t.Muted.Render("drives ")+t.Base.Render(backends)+
			t.Muted.Render("   platforms ")+t.Base.Render(strings.Join(row.Platforms, ", ")),
		width))
	lines = append(lines, ui.Truncate(
		t.Muted.Render("repo ")+t.Base.Render(row.Repo)+
			t.Muted.Render("   page ")+t.Base.Render(row.Page)+
			t.Muted.Render("   changelog ")+t.Base.Render(blankLink(row.Changelog)),
		width))
	lines = append(lines, ui.Truncate(
		t.Muted.Render("verify ")+t.Command.Render(verifyHint(row)), width))

	return strings.Join(lines[:detailLines], "\n")
}

// blankLink renders a missing link as a word rather than as nothing.
func blankLink(url string) string {
	if url == "" {
		return "none yet"
	}
	return url
}

// verifyHint is the one command that answers "was this file built by that
// repository's workflow". Every release of the family carries the provenance
// it checks, so the answer is the same shape for every tool.
func verifyHint(row catalog.Row) string {
	slug := strings.TrimPrefix(row.Repo, "https://github.com/")
	if slug == "" || slug == row.Repo {
		slug = "tui-tools/" + row.Name
	}
	return "gh attestation verify " + row.Name + "_" + blank(row.Available) +
		"_linux_amd64.tar.gz -R " + slug
}

// firstParagraph keeps the opening paragraph of a description, which is the
// part written to stand on its own.
func firstParagraph(description string) string {
	if i := strings.Index(description, "\n\n"); i > 0 {
		return description[:i]
	}
	return description
}

// wrap breaks text into at most limit lines of width cells, on word
// boundaries, ending the last one with an ellipsis when there was more.
func wrap(text string, width, limit int) []string {
	words := strings.Fields(text)
	lines := make([]string, 0, limit)
	current := ""
	for i, word := range words {
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if lipgloss.Width(candidate) <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
		if len(lines) == limit {
			// There was more text than fits: say so on the last line.
			return append(lines[:limit-1], ui.Truncate(lines[limit-1]+" "+
				strings.Join(words[i:], " "), width))
		}
	}
	if current != "" && len(lines) < limit {
		lines = append(lines, current)
	}
	return lines
}

// outputView renders the transcript of the sequence that just ran: every
// command as it was previewed, and what it printed.
func (a *app) outputView() string {
	t := a.theme
	height := max(a.height-headerLines-footerLines-1, minListHeight)
	offset := min(a.transcriptOffset, max(len(a.transcript)-height, 0))
	a.transcriptOffset = offset

	body := make([]string, 0, height)
	for _, line := range a.transcript[offset:min(offset+height, len(a.transcript))] {
		style := t.Base
		if strings.HasPrefix(line, "$ ") {
			style = t.Command
		}
		body = append(body, ui.Truncate(style.Render(line), max(a.width, 1)))
	}
	for len(body) < height {
		body = append(body, "")
	}

	header := ui.Header{
		Title:    "tui-tools",
		Subtitle: a.transcriptTitle,
		Facts:    []ui.Fact{{Label: "commands", Value: strconv.Itoa(len(a.transcript))}},
	}.Render(t, a.width)

	help := ui.HelpBar(t, []ui.KeyHint{
		{Key: "↑/k, ↓/j", Desc: "scroll"},
		{Key: "esc", Desc: "back to the dashboard"},
	}, a.width)
	status := ui.StatusLine(t, a.statusKind, a.status, "esc returns to the dashboard", a.width)
	return strings.Join([]string{header, strings.Join(body, "\n"), help, status}, "\n")
}

// shortHelpKeys is the single-line hint bar.
func shortHelpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "enter", Desc: "launch"},
		{Key: "i", Desc: "install"},
		{Key: "u", Desc: "update"},
		{Key: "x", Desc: "remove"},
		{Key: "s", Desc: "repository"},
		{Key: "r", Desc: "refresh"},
		{Key: "/", Desc: "filter"},
		{Key: "?", Desc: "help"},
		{Key: "q", Desc: "quit"},
	}
}

// helpKeys is the full key list.
func helpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "↑/k, ↓/j", Desc: "move the selection"},
		{Key: "g / G", Desc: "first / last tool"},
		{Key: "pgup/pgdn", Desc: "scroll a page"},
		{Key: "/", Desc: "filter by name, tagline, category or state"},
		{Key: "", Desc: ""},
		{Key: "enter", Desc: "launch the selected tool, and come back when it exits"},
		{Key: "i", Desc: "install it, through this machine's package manager"},
		{Key: "u", Desc: "update it"},
		{Key: "x / d", Desc: "remove it; nothing it pulled in is autoremoved"},
		{Key: "s", Desc: "set the tui-tools repository up, with the key pinned by fingerprint"},
		{Key: "r", Desc: "re-read the catalog and the machine"},
		{Key: "", Desc: ""},
		{Key: "?", Desc: "this help"},
		{Key: "q", Desc: "quit"},
		{Key: "", Desc: ""},
		{Key: "note", Desc: "every install, update, removal and repository change is " +
			"previewed as the exact command line and confirmed first"},
		{Key: "note", Desc: "the catalog only says which tools exist; your package " +
			"manager verifies the signed repository and the signed package"},
	}
}
