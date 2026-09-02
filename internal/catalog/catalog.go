// Package catalog reads the family catalog — https://tui.tools/catalog.json —
// and turns it into the rows the launcher shows.
//
// The catalog informs; it never decides. Everything in it arrives over the
// network as a claim about which tools exist and which version each one is at.
// Nothing from it is ever placed on a command line except a package name that
// matched ^tui-[a-z]+$ here and is validated a second time by tui-kit/pkgmgr
// before it can reach an argv. What actually reaches the machine is decided by
// apt, dnf or pacman verifying the signed repository index and the signed
// package, which is a check this tool cannot weaken and does not repeat.
//
// A snapshot of the document is embedded in the binary, so the dashboard works
// on a machine with no network and inside --demo. `make catalog` refreshes it
// and the tests validate what it refreshed.
package catalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// URL is where the family publishes the catalog.
const URL = "https://tui.tools/catalog.json"

// Self is this tool's own name. It is dropped from the list: the launcher is
// already running, so offering to install it is an answer to nobody's
// question, and offering to remove it is worse.
const Self = "tui-tools"

// schemaVersion is the document version this code was written against. The
// endpoint promises that new fields are added without bumping it and that a
// bump means a field changed meaning, so anything else is refused rather than
// half-read.
const schemaVersion = 1

// fetchTimeout bounds the whole HTTP exchange. A launcher that hangs on a
// slow network is a launcher nobody can use offline.
const fetchTimeout = 8 * time.Second

// maxBody caps what is read from the network. The document is around 60 kB;
// a megabyte is generous and still bounded.
const maxBody = 1 << 20

// name is the only shape a tool name may have. Every name in the document is
// held to it before it is kept, and a package name is held to it again by
// tui-kit/pkgmgr before it can reach a command line.
var name = regexp.MustCompile(`^tui-[a-z]+$`)

// companionName is the only shape a companion name may have.
//
// A companion is not a terminal UI, so it is not called tui-<word>: a mirror
// carries the upstream project's own name ("headscale") and a component is a
// "tui-tools-<something>" package. The set is still narrow — it starts with a
// lower-case letter and carries lower-case letters, digits and single dashes —
// because these names reach an argv exactly like a tool's package name does,
// and internal/packages holds every one of them to the same pattern again
// before it builds a command from it.
var companionName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// maxNameLength bounds a companion name. A package name longer than this is
// not one any of the three managers would carry, and a bound is cheaper than
// wondering what a document could put on a command line.
const maxNameLength = 64

//go:embed snapshot.json
var snapshot []byte

// Source says where a catalog came from, which the header shows: a live
// document and a snapshot from build time are different claims about the world
// and the user is entitled to know which one is on screen.
type Source string

// The two sources a catalog can have.
const (
	// SourceLive is the document fetched from tui.tools.
	SourceLive Source = "live"
	// SourceSnapshot is the copy embedded in this binary.
	SourceSnapshot Source = "snapshot"
)

// Tool is one entry of the catalog: what the family says about a tool.
type Tool struct {
	Name string `json:"name"`
	// Package is what the tool is called to a package manager. It is the only
	// field of this struct that ever reaches a command line.
	Package     string   `json:"package"`
	Binary      string   `json:"binary"`
	Tagline     string   `json:"tagline"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Icon        string   `json:"icon"`
	Screenshot  string   `json:"screenshot"`
	Version     string   `json:"version"`
	Released    string   `json:"released"`
	Unreleased  bool     `json:"unreleased"`
	Platforms   []string `json:"platforms"`
	License     string   `json:"license"`
	Backends    []string `json:"backends"`
	Repo        string   `json:"repo"`
	Page        string   `json:"page"`
	Changelog   string   `json:"changelog"`
}

// Kind says what a row is: one of the family's terminal UIs, or one of the two
// sorts of companion package.
type Kind string

// The three kinds a row can have.
const (
	// KindTool is a tui-<word> terminal UI, which the launcher can also start.
	KindTool Kind = "tool"
	// KindMirror is an upstream project rebuilt from its own source tag under
	// the family's signing and provenance gate.
	KindMirror Kind = "mirror"
	// KindComponent is a family package that is not a terminal UI.
	KindComponent Kind = "component"
)

// Companion is one entry of the catalog's `companions` array: a package the
// family ships that is not a terminal UI, so there is nothing to launch and
// the launcher only installs, updates, removes and reports on it.
type Companion struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Summary is the one line the card shows, where a tool has a tagline.
	Summary string `json:"summary"`
	// Upstream and UpstreamVersion are the mirrored project and the source tag
	// this package was rebuilt from. Both are absent for a component.
	Upstream        string `json:"upstream"`
	UpstreamVersion string `json:"upstreamVersion"`
	// Version is the family's own release, empty while it is unreleased.
	Version  string `json:"version"`
	Released string `json:"released"`
	// Packages are what the entry is called to a package manager. They are the
	// only fields of this struct that ever reach a command line, and each one
	// is held to companionName here and again by internal/packages. An entry
	// that names none is read as naming itself.
	Packages []string `json:"packages"`
	Repo     string   `json:"repo"`
	Page     string   `json:"page"`
}

// Package is the package a companion's row is computed from: the first one it
// ships, which is the one that carries its name.
func (c Companion) Package() string {
	if len(c.Packages) == 0 {
		return c.Name
	}
	return c.Packages[0]
}

// Origin says where an installed companion package came from, and whether the
// family repository offers it. It is what makes the provenance question
// answerable on screen: a mirror is only rebuilt under the family's signing and
// provenance gate if the copy on the machine is the family's copy.
type Origin struct {
	// Repo names the repository the installed version came from, as the
	// package manager reports or implies it ("tui-tools", "extra",
	// "deb.debian.org"). Empty when nothing on the machine could say.
	Repo string `json:"repo,omitempty"`
	// Family reports that Repo is the tui-tools repository.
	Family bool `json:"family"`
	// Offered reports that the tui-tools repository carries the package.
	Offered bool `json:"offered"`
	// Version is what the tui-tools repository offers, when it does.
	Version string `json:"version,omitempty"`
	// Detail is one line saying how the answer was reached, for a screen that
	// has to explain itself rather than show a badge.
	Detail string `json:"detail,omitempty"`
}

// Packages is what the catalog says about the family's package repository.
type Packages struct {
	Repo          string `json:"repo"`
	InstallScript string `json:"install_script"`
	// Live reports whether the repository answered when the catalog was
	// built. It is a fact worth showing rather than assuming: a setup offered
	// against a repository that is down wastes the user's time.
	Live bool   `json:"live"`
	Deb  string `json:"deb"`
	RPM  string `json:"rpm"`
	Arch string `json:"arch"`
}

// Signing is what the catalog says about the repository's key. It publishes
// the key's URL and no fingerprint: the fingerprint a machine pins is the one
// this tool carries, not one the network told it to expect.
type Signing struct {
	Pubkey string `json:"pubkey"`
}

// Catalog is the document, after validation.
type Catalog struct {
	Schema    int       `json:"schema"`
	Generated time.Time `json:"generated"`
	Packages  Packages  `json:"packages"`
	Signing   Signing   `json:"signing"`
	Tools     []Tool    `json:"tools"`
	// Companions are the family packages that are not terminal UIs. The field
	// arrived after the first release of this launcher and does not bump the
	// schema, so a document without it is read exactly as before.
	Companions []Companion `json:"companions"`

	// Source says whether this came off the network or out of the binary.
	Source Source `json:"-"`
}

// Names returns the package names of every tool, which is what the package
// manager is asked about. They are validated names, so the set is safe to hand
// to pkgmgr — which validates them again anyway.
func (c Catalog) Names() []string {
	names := make([]string, 0, len(c.Tools))
	for _, tool := range c.Tools {
		names = append(names, tool.Package)
	}
	return names
}

// CompanionNames returns every package name the companions ship, which is what
// the package manager is asked about. They are validated names, so the set is
// safe to hand to internal/packages, which validates them again anyway.
func (c Catalog) CompanionNames() []string {
	names := make([]string, 0, len(c.Companions))
	for _, companion := range c.Companions {
		names = append(names, companion.Packages...)
	}
	return names
}

// Find returns the tool with a package name, when the catalog has one.
func (c Catalog) Find(pkg string) (Tool, bool) {
	for _, tool := range c.Tools {
		if tool.Package == pkg {
			return tool, true
		}
	}
	return Tool{}, false
}

// Parse reads a catalog document and keeps only what a launcher may act on.
//
// Three things are dropped rather than shown: an entry whose name or package
// name is not `tui-<word>`, because it is not a name this family will pass to
// anything; an entry marked unreleased, because there is nothing to install;
// and the launcher itself, which is already running.
func Parse(data []byte) (Catalog, error) {
	var doc Catalog
	if err := json.Unmarshal(data, &doc); err != nil {
		return Catalog{}, fmt.Errorf("catalog: %w", err)
	}
	if doc.Schema != schemaVersion {
		return Catalog{}, fmt.Errorf(
			"catalog: schema %d, this build reads %d", doc.Schema, schemaVersion)
	}

	kept := make([]Tool, 0, len(doc.Tools))
	for _, tool := range doc.Tools {
		switch {
		case !name.MatchString(tool.Name):
			continue
		case tool.Package == "" || !name.MatchString(tool.Package):
			continue
		case tool.Unreleased:
			continue
		case tool.Name == Self:
			continue
		}
		kept = append(kept, tool)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Name < kept[j].Name })
	doc.Tools = kept

	if len(doc.Tools) == 0 {
		return Catalog{}, errors.New("catalog: no tool in it is one this launcher may act on")
	}
	doc.Companions = keepCompanions(doc.Companions)
	return doc, nil
}

// keepCompanions drops every companion entry the launcher may not act on.
//
// A document with no companions at all is the normal case for a build that
// meets an older catalog, so an empty result is an answer rather than an error:
// the dashboard then shows the tools alone.
func keepCompanions(companions []Companion) []Companion {
	kept := make([]Companion, 0, len(companions))
	for _, companion := range companions {
		if !validName(companion.Name) {
			continue
		}
		switch companion.Kind {
		case KindMirror, KindComponent:
		default:
			// A kind this build has no row for is not a kind it may act on.
			continue
		}
		if len(companion.Packages) == 0 {
			// The documented default: an entry that names no package ships one
			// package, called after itself.
			companion.Packages = []string{companion.Name}
		}
		names := make([]string, 0, len(companion.Packages))
		for _, pkg := range companion.Packages {
			if validName(pkg) {
				names = append(names, pkg)
			}
		}
		if len(names) == 0 {
			continue
		}
		companion.Packages = names
		kept = append(kept, companion)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Name < kept[j].Name })
	return kept
}

// validName reports whether a companion name or package name is one this
// launcher will let near a command line.
func validName(name string) bool {
	return len(name) <= maxNameLength && companionName.MatchString(name)
}

// Embedded returns the snapshot compiled into this binary.
func Embedded() (Catalog, error) {
	doc, err := Parse(snapshot)
	if err != nil {
		return Catalog{}, err
	}
	doc.Source = SourceSnapshot
	return doc, nil
}

// ExampleComponentName is the component --demo shows.
const ExampleComponentName = "tui-tools-example"

// WithExampleComponent adds one component companion to a catalog that carries
// none, and returns the rest untouched.
//
// It exists for --demo alone, and it is called from there rather than from
// Parse: the dashboard must never invent an entry the family did not publish.
// The demo has to show both sorts of companion, because a mirror and a
// component behave differently on the same screen, and the family's own
// components may not be in the document a given build snapshotted. The name is
// the family's reserved example, so nobody can mistake it for a package to
// install.
func WithExampleComponent(doc Catalog) Catalog {
	for _, companion := range doc.Companions {
		if companion.Kind == KindComponent {
			return doc
		}
	}
	example := Companion{
		Name:     ExampleComponentName,
		Kind:     KindComponent,
		Summary:  "A family package that is not a terminal UI, shown so --demo has one",
		Version:  "0.1.0",
		Packages: []string{ExampleComponentName},
		Repo:     "https://github.com/tui-tools/tui-tools",
		Page:     "https://tui.tools/#companions",
	}
	doc.Companions = keepCompanions(append(doc.Companions, example))
	return doc
}

// Fetch reads the live catalog over HTTPS.
func Fetch(ctx context.Context, url string) (Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Catalog{}, err
	}
	request.Header.Set("Accept", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Catalog{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Catalog{}, fmt.Errorf("catalog: %s answered %s", url, response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody))
	if err != nil {
		return Catalog{}, err
	}
	doc, err := Parse(body)
	if err != nil {
		return Catalog{}, err
	}
	doc.Source = SourceLive
	return doc, nil
}

// Load returns the live catalog, falling back to the embedded snapshot.
//
// The fallback is silent in the sense that it is not an error — a machine with
// no network is a machine this tool still has to work on — but it is never
// silent on screen: the returned catalog says which source it came from and
// the reason the live one failed comes back beside it.
func Load(ctx context.Context, url string, offline bool) (Catalog, string) {
	if offline {
		doc, err := Embedded()
		if err != nil {
			return Catalog{}, err.Error()
		}
		return doc, ""
	}
	doc, err := Fetch(ctx, url)
	if err == nil {
		return doc, ""
	}
	reason := err.Error()
	fallback, embedErr := Embedded()
	if embedErr != nil {
		return Catalog{}, reason
	}
	return fallback, reason
}

// State is what the machine has to say about one tool.
type State string

// The four states a card can be in.
const (
	// StateNotInstalled: the machine does not have it.
	StateNotInstalled State = "not installed"
	// StateUpToDate: installed, and nothing newer is offered.
	StateUpToDate State = "up to date"
	// StateOutdated: installed, and the repository offers a newer version.
	StateOutdated State = "update available"
	// StateUnknown: installed, but the repository was not readable, so
	// whether it is current cannot be claimed.
	StateUnknown State = "installed"
)

// Row is one card: what the catalog says about a tool or a companion, and what
// this machine says about it.
//
// Both groups share the type, and the columns on screen, because the questions
// are the same: is it here, is it current, and what would be run to change
// that. Kind is what separates them, and it is the only thing the dashboard has
// to branch on.
type Row struct {
	Tool
	Kind Kind `json:"kind"`
	// Upstream and UpstreamVersion are the mirrored project and the source tag
	// a mirror was rebuilt from. Both are empty for a tool and a component.
	Upstream        string `json:"upstream,omitempty"`
	UpstreamVersion string `json:"upstreamVersion,omitempty"`
	// Packages are every package a companion ships, the first of which is the
	// one this row is computed from.
	Packages []string `json:"packages,omitempty"`
	// Origin says where the installed package came from. It is only read for a
	// companion: a tool's provenance is not in question, because a tui-<word>
	// package exists in no repository but the family's.
	Origin Origin `json:"origin,omitzero"`
	// Installed is the version on this machine, empty when there is none.
	Installed string `json:"installed"`
	// Available is the version the repositories would install, empty when no
	// repository here carries it.
	Available string `json:"available"`
	State     State  `json:"state"`
	// Supported reports that a release of this tool exists for this
	// machine's architecture.
	Supported bool `json:"supported"`
	// Compat is one line about this machine: the architecture, and whether
	// the configured repositories carry the package.
	Compat string `json:"compat"`
}

// Rows joins the catalog to what the package manager reported.
//
// Neither map is required to hold every name: a package that is not installed
// is simply absent from the first, and one no repository carries is absent
// from the second. That is the answer rather than an error, so an unreachable
// repository degrades the screen to "installed, cannot say" instead of
// emptying it.
func Rows(doc Catalog, installed, available map[string]string) []Row {
	rows := make([]Row, 0, len(doc.Tools))
	for _, tool := range doc.Tools {
		row := Row{
			Tool:      tool,
			Kind:      KindTool,
			Installed: installed[tool.Package],
			Available: available[tool.Package],
			Supported: supports(tool.Platforms),
		}
		row.State = state(row)
		row.Compat = compatLine(row)
		rows = append(rows, row)
	}
	return rows
}

// CompanionRows joins the companions to what the package manager reported, the
// same way Rows does for the tools.
//
// The one difference is the architecture question. A tool declares the
// platforms it ships a release for, and a machine the release skips is told so
// rather than offered an install that fails. A companion declares none: it is a
// package in the family repository, and whether that repository carries a build
// for this machine is exactly what `available` answers. So the row is marked
// supported and the compatibility line is left to say the rest.
func CompanionRows(doc Catalog, installed, available map[string]string,
	origins map[string]Origin) []Row {
	rows := make([]Row, 0, len(doc.Companions))
	for _, companion := range doc.Companions {
		pkg := companion.Package()
		row := Row{
			Tool: Tool{
				Name:    companion.Name,
				Package: pkg,
				Tagline: companion.Summary,
				// The category cell is where the kind badge is read, so the
				// same column that shelves a tool names a companion's sort.
				Category: string(companion.Kind),
				Version:  companion.Version,
				Released: companion.Released,
				Repo:     companion.Repo,
				Page:     companion.Page,
			},
			Kind:            companion.Kind,
			Upstream:        companion.Upstream,
			UpstreamVersion: companion.UpstreamVersion,
			Packages:        companion.Packages,
			Origin:          origins[pkg],
			Installed:       installed[pkg],
			Available:       available[pkg],
			Supported:       true,
		}
		row.State = state(row)
		row.Compat = compatLine(row)
		rows = append(rows, row)
	}
	return rows
}

// IsCompanion reports whether a row is a companion rather than a tool, which is
// what decides that enter has nothing to launch and that the origin of the
// installed package is a question worth asking.
func (r Row) IsCompanion() bool {
	return r.Kind == KindMirror || r.Kind == KindComponent
}

// Switchable reports whether the installed package could be replaced by the
// family's own build: it is here, it came from somewhere else, and the
// tui-tools repository carries it.
func (r Row) Switchable() bool {
	return r.IsCompanion() && r.Installed != "" &&
		r.Origin.Offered && !r.Origin.Family
}

// state classifies one row. A version string is compared for equality rather
// than ordered: the repository is the authority on what an upgrade would
// fetch, and "different from what is installed" is exactly what it answers.
func state(row Row) State {
	switch {
	case row.Installed == "":
		return StateNotInstalled
	case row.Available == "":
		return StateUnknown
	case sameVersion(row.Installed, row.Available):
		return StateUpToDate
	default:
		return StateOutdated
	}
}

// sameVersion compares two versions the way the three managers print them,
// which is not quite the same way: pacman appends a package release ("0.1.2-1")
// and rpm appends a release and sometimes a distribution tag ("0.1.2-1.fc42"),
// while the upstream version is what both start with. Comparing the part
// before the first dash is what stops a freshly installed package from
// reporting an update to itself.
func sameVersion(installed, available string) bool {
	return upstream(installed) == upstream(available)
}

// upstream keeps the part of a package version before the package release.
func upstream(version string) string {
	if i := strings.IndexByte(version, '-'); i > 0 {
		return version[:i]
	}
	return version
}

// supports reports whether a release exists for the architecture this binary
// runs on. A tool that ships no linux/arm64 build is not installable on an
// arm64 machine, and saying so on the card is better than an install that
// fails.
func supports(platforms []string) bool {
	want := runtime.GOOS + "/" + runtime.GOARCH
	for _, platform := range platforms {
		if platform == want {
			return true
		}
	}
	return false
}

// compatLine says, in a few words, what this machine can do with the tool.
func compatLine(row Row) string {
	if !row.Supported {
		return "no " + runtime.GOOS + "/" + runtime.GOARCH + " build"
	}
	if row.Available == "" {
		return runtime.GOARCH + " · not in the configured repositories"
	}
	return runtime.GOARCH + " · in the repository"
}
