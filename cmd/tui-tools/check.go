package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-tools/internal/catalog"
	"github.com/tui-tools/tui-tools/internal/packages"
)

// checkTimeout bounds the whole read: one HTTP fetch and two local database
// queries. A machine whose package manager is wedged must not hang a
// non-interactive check forever.
const checkTimeout = 90 * time.Second

// checkReport is what --check prints: the machine, the repository, the
// catalog and one entry per tool.
//
// It is a report of the read path only. --check never builds and never runs an
// install, an upgrade, a removal or a repository setup: the whole point is
// that it is safe to run anywhere, including in CI against a machine somebody
// depends on.
type checkReport struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	// Manager is the package manager that drives this machine, and Describe
	// is the backend's own one-line summary — which is where the demo backend
	// says it is a demo.
	Manager  string `json:"manager"`
	Describe string `json:"describe"`
	// Distro and DistroID say whose machine this is.
	Distro   string `json:"distro"`
	DistroID string `json:"distroId,omitempty"`
	// Repo is what the repository probe found.
	Repo repoReport `json:"repo"`
	// Catalog says which document the tool list came from.
	Catalog catalogReport `json:"catalog"`
	// Summary is the headline a script reads without walking the list.
	Summary summaryReport `json:"summary"`
	// Tools is one entry per tool of the family, in catalog order.
	Tools []toolReport `json:"tools"`
	// Compat is what the package-manager version probes found, one entry per
	// backend the manifest declares. It is reported rather than asserted: an
	// untested version is a fact about the machine, not a failure of the read
	// path.
	Compat []compat.Result `json:"compat"`
}

// repoReport is the family repository's state on this machine, plus the
// fingerprint a setup would pin.
type repoReport struct {
	Configured bool   `json:"configured"`
	Path       string `json:"path,omitempty"`
	Detail     string `json:"detail,omitempty"`
	// Fingerprint is the signing key this launcher pins. It is printed so a
	// machine's configuration can be audited against it from the outside.
	Fingerprint string `json:"fingerprint"`
}

// catalogReport says which catalog produced the tool list, and why, when it
// was not the live one.
type catalogReport struct {
	Source    string `json:"source"`
	URL       string `json:"url"`
	Generated string `json:"generated,omitempty"`
	// PackagesRepo and PackagesLive are what the catalog says about
	// pkgs.tui.tools: where it is, and whether it answered when the catalog
	// was built.
	PackagesRepo string `json:"packagesRepo,omitempty"`
	PackagesLive bool   `json:"packagesLive"`
	// Note is why the live catalog was not used, when it was not.
	Note string `json:"note,omitempty"`
}

// summaryReport counts the states, so a script can act on the answer without
// reading every entry.
type summaryReport struct {
	Total     int `json:"total"`
	Installed int `json:"installed"`
	Outdated  int `json:"outdated"`
	Missing   int `json:"missing"`
}

// toolReport is one tool: what the catalog says, and what this machine says.
type toolReport struct {
	Name      string `json:"name"`
	Package   string `json:"package"`
	Binary    string `json:"binary"`
	Category  string `json:"category"`
	Tagline   string `json:"tagline"`
	Installed string `json:"installed,omitempty"`
	Available string `json:"available,omitempty"`
	// Current is the version the catalog says is the latest release, which is
	// a claim from the website rather than from a repository.
	Current string `json:"current,omitempty"`
	State   string `json:"state"`
	// Supported reports that a release exists for this architecture.
	Supported bool     `json:"supported"`
	Compat    string   `json:"compat"`
	Backends  []string `json:"backends"`
	Repo      string   `json:"repo"`
	Page      string   `json:"page"`
	Changelog string   `json:"changelog,omitempty"`
}

// runCheck reads the catalog and the machine and prints the result as JSON.
//
// It returns an error only when the tool itself could not work — a machine
// with nothing installed and no repository configured is a *successful* run of
// tui-tools, and the news is in `summary` where a script can read it without
// confusing "this machine has no tools" with "this launcher is broken".
func runCheck(ctx context.Context, backend packages.Backend, source catalogSource,
	backends []compat.Result, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	doc, note := source.Load(ctx)
	if len(doc.Tools) == 0 {
		return fmt.Errorf("no catalog to report on: %s", note)
	}

	names := doc.Names()
	installedPkgs, err := backend.Installed(ctx, names)
	if err != nil {
		return fmt.Errorf("reading the installed packages failed: %w", err)
	}
	// A repository that carries nothing, or none at all, is an answer rather
	// than a failure: the report then says what is installed and claims
	// nothing about what is current.
	availablePkgs, _ := backend.Available(ctx, names)
	status, _ := backend.RepoStatus()

	rows := catalog.Rows(doc, installedPkgs, availablePkgs)
	report := checkReport{
		Tool:     toolName,
		Version:  version,
		Manager:  string(backend.Manager()),
		Describe: backend.Describe(),
		Distro:   backend.Distro().String(),
		DistroID: backend.Distro().ID,
		Repo: repoReport{
			Configured:  status.Configured,
			Path:        status.Path,
			Detail:      status.Detail,
			Fingerprint: packages.Fingerprint,
		},
		Catalog: catalogReport{
			Source:       string(doc.Source),
			URL:          source.url,
			PackagesRepo: doc.Packages.Repo,
			PackagesLive: doc.Packages.Live,
			Note:         note,
		},
		Tools:  make([]toolReport, 0, len(rows)),
		Compat: backends,
	}
	if !doc.Generated.IsZero() {
		report.Catalog.Generated = doc.Generated.UTC().Format(time.RFC3339)
	}
	if doc.Source == catalog.SourceSnapshot {
		report.Catalog.URL = "embedded snapshot"
	}

	for _, row := range rows {
		report.Summary.Total++
		switch row.State {
		case catalog.StateNotInstalled:
			report.Summary.Missing++
		case catalog.StateOutdated:
			report.Summary.Installed++
			report.Summary.Outdated++
		default:
			report.Summary.Installed++
		}
		report.Tools = append(report.Tools, toolReport{
			Name:      row.Name,
			Package:   row.Package,
			Binary:    row.Binary,
			Category:  row.Category,
			Tagline:   row.Tagline,
			Installed: row.Installed,
			Available: row.Available,
			Current:   row.Version,
			State:     string(row.State),
			Supported: row.Supported,
			Compat:    row.Compat,
			Backends:  row.Backends,
			Repo:      row.Repo,
			Page:      row.Page,
			Changelog: row.Changelog,
		})
	}

	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
