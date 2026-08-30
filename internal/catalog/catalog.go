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
	return doc, nil
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

// Row is one card: what the catalog says about a tool, and what this machine
// says about it.
type Row struct {
	Tool
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
