package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/report"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-tools/internal/catalog"
	"github.com/tui-tools/tui-tools/internal/packages"
)

// runReport prints the block a bug report needs and exits. Everything generic
// — the kit version, the distribution, the kernel, the terminal, where the
// binary came from — is collected by the kit, so the whole family answers
// --report in the same shape. What this function adds is the part only the
// launcher knows: which package manager drives this machine and what version
// it is, whether the family repository is configured, and which catalog the
// tool list came from.
//
// Those three are most of the launcher's bug reports. "tui-firewall is not
// listed" is a different bug depending on whether the repository is missing,
// the manager is one the tool does not drive, or the catalog came from the
// snapshot embedded a release ago.
//
// It installs nothing, removes nothing and needs no privileges, and it runs
// before the backend is required: a machine whose package manager the launcher
// cannot drive at all still produces a report, with the failure as one of its
// lines. The catalog fetch is the one thing here that touches the network,
// bounded by the catalog's own timeout and falling back to the snapshot, which
// is exactly what the running tool does.
func runReport(ctx context.Context, cfg config.Config, opts options,
	source catalogSource, managers []compat.Result, out io.Writer) error {
	palette, _ := theme.ResolvePalette()

	info := report.Info{
		Tool:    toolName,
		Version: version,
		Demo:    opts.demo,
		Sudo:    cfg.String(config.KeySudo, ""),
		Theme:   palette.Name,
	}

	var backendError string
	backend, err := pickBackend(cfg, opts, source)
	if err != nil {
		backendError = err.Error()
	} else {
		manager := backend.Manager().String()
		info.Backend = manager
		// The same probe --check and the header use. There is one version
		// probe in this tool and this is it; the manager driving the machine
		// is the one entry of it that answered.
		if result, ok := managerCompat(managers, manager); ok {
			info.BackendVersion = result.Version
			info.BackendDetail = result.Detail
		}
		if opts.demo {
			// The fake imitates a real package manager, and which one decides
			// which command builders the session exercised.
			info.Backend = "demo"
			info.Extra = append(info.Extra, report.Field{
				Key: "demo backend", Value: manager,
			})
		}
		info.Extra = append(info.Extra, report.Field{
			Key: "repo", Value: describeRepo(backend),
		})
	}

	info.Extra = append(info.Extra, report.Field{
		Key: "catalog", Value: describeCatalog(ctx, source),
	})
	if !opts.demo {
		info.Extra = append(info.Extra, report.Field{
			Key: "managers", Value: describeManagers(managers),
		})
	}
	if backendError != "" {
		info.Extra = append(info.Extra, report.Field{
			Key: "backend error", Value: backendError,
		})
	}

	_, err = io.WriteString(out, report.Render(info))
	return err
}

// managerCompat finds the probe result for the manager that drives this
// machine. Three managers are declared and one of them answers, so the other
// two are of no interest on the backend line — they are on the `managers` line
// below, where a machine that has two of them installed is visible.
func managerCompat(results []compat.Result, manager string) (compat.Result, bool) {
	for _, result := range results {
		if result.Backend == manager {
			return result, true
		}
	}
	return compat.Result{}, false
}

// describeRepo says whether tui-* packages would come from the family's own
// repository, and from which file. The path is under /etc on every
// distribution the launcher drives, so it names the machine's configuration
// and never its user.
//
// "not configured" is not a failure and is not reported as one: it is the
// state of a machine that has not been set up yet, and it explains every
// version the launcher could not offer.
func describeRepo(backend packages.Backend) string {
	status, err := backend.RepoStatus()
	switch {
	case err != nil:
		return "unknown: " + err.Error()
	case status.Configured && strings.HasPrefix(status.Path, "/"):
		return "configured (" + status.Path + ")"
	case status.Configured:
		// The fake answers with a placeholder rather than a path. The mode
		// line above already says the machine was not read.
		return "configured"
	case status.Detail != "":
		return "not configured: " + status.Detail
	}
	return "not configured"
}

// describeManagers renders the version probe of every package manager the
// launcher knows as one line. A report that says only "dnf" leaves the reader
// guessing whether apt was absent or merely older than the minimum, and that
// difference is most of the manager selection bugs.
func describeManagers(results []compat.Result) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		if result.Version != "" {
			parts = append(parts, result.Backend+" "+result.Version)
			continue
		}
		parts = append(parts, result.Backend+" absent")
	}
	if len(parts) == 0 {
		return "none probed"
	}
	return strings.Join(parts, ", ")
}

// describeCatalog says which document the tool list came from, and why it was
// not the live one when it was not. A stale snapshot and a blocked network
// look identical from the outside and produce the same "my tool is missing"
// report, so the reason is on the line.
//
// The configured URL is named only when it is the family's own. A URL somebody
// pointed at a staging copy is theirs, may carry a host or a token, and is not
// this block's to publish; that it is not the default is the only fact a
// maintainer needs.
func describeCatalog(ctx context.Context, source catalogSource) string {
	doc, note := source.Load(ctx)
	where := "the family catalog"
	if source.url != catalog.URL {
		where = "a configured catalog"
	}
	line := string(doc.Source)
	if line == "" {
		line = "none"
	}
	if doc.Source == catalog.SourceLive {
		line += " (" + where + ")"
	}
	if !doc.Generated.IsZero() {
		line += ", generated " + doc.Generated.UTC().Format("2006-01-02")
	}
	if note != "" {
		line += " (" + scrubURL(note, source.url) + ")"
	}
	return line
}

// scrubURL removes a configured catalog URL from a message that quotes it. A
// fetch failure names what it could not reach, and what somebody configured is
// not this block's to publish.
func scrubURL(note, url string) string {
	if url == "" || url == catalog.URL {
		return note
	}
	return strings.ReplaceAll(note, url, "the configured catalog")
}

// reportUsage is the flag's one-line help, kept here next to what it prints.
var reportUsage = fmt.Sprintf(
	"print the versions and machine facts a bug report needs, then exit "+
		"(no UI, no privileges, nothing about you: paste it into a %s issue)",
	toolName)
