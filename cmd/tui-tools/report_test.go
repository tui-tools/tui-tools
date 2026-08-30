package main

import (
	"context"
	"os"
	"os/user"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-tools/internal/catalog"
)

// baseConfig is the configuration a report is rendered against: the defaults,
// with nothing read from disk or from the environment.
func baseConfig() config.Config {
	return config.Config{Tool: toolName, Values: defaults()}
}

// offlineSource is the catalog every test here reads, because a unit test that
// fetched the live one would be a test of the network.
func offlineSource() catalogSource {
	return catalogSource{url: catalog.URL, offline: true}
}

// TestRunReportDemo checks the half of the block this tool owns. The kit's own
// tests cover the machine facts and the scrubbing; what has to be right here is
// that --demo says demo, that the imitated package manager is named, and that
// nothing was installed, removed or fetched to produce any of it.
func TestRunReportDemo(t *testing.T) {
	var out strings.Builder
	opts := options{demo: true, report: true}
	err := runReport(context.Background(), baseConfig(), opts, offlineSource(),
		nil, &out)
	if err != nil {
		t.Fatalf("runReport: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"backend: demo\n",
		"mode: demo (sample data, the system was not read)\n",
		"demo backend: ",
		"catalog: snapshot",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\nmanagers: ") {
		t.Errorf("a demo probed no manager and must not list any:\n%s", got)
	}
	if !strings.HasPrefix(got, toolName+" ") {
		t.Errorf("report should start with the tool name:\n%s", got)
	}
}

// TestRunReportLive checks the lines that carry this tool's own answers: the
// manager that drives the machine, whether the family repository is
// configured, where the tool list came from, and what the probe saw of the
// managers it did not pick.
func TestRunReportLive(t *testing.T) {
	managers := []compat.Result{
		{Backend: "apt"},
		{Backend: "dnf", Version: "5.2.18"},
	}

	var out strings.Builder
	err := runReport(context.Background(), baseConfig(), options{report: true},
		offlineSource(), managers, &out)
	if err != nil {
		t.Fatalf("runReport: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"mode: live\n",
		"catalog: snapshot",
		"managers: apt absent, dnf 5.2.18\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	// The repo line is there whether or not this machine has the repository:
	// "not configured" is the answer that explains most of the reports.
	if !strings.Contains(got, "\nrepo: ") && !strings.Contains(got, "backend error: ") {
		t.Errorf("report should say whether the repository is configured:\n%s", got)
	}
}

// TestRunReportKeepsItsPrivacyPromise is the assertion the bug form depends on.
// The block is pasted into a public issue, so the user name, the home path and
// the host name appearing in it would be a disclosure, not a cosmetic slip.
func TestRunReportKeepsItsPrivacyPromise(t *testing.T) {
	var out strings.Builder
	err := runReport(context.Background(), baseConfig(), options{report: true},
		offlineSource(), nil, &out)
	if err != nil {
		t.Fatalf("runReport: %v", err)
	}
	got := out.String()

	if strings.Contains(got, "/home/") {
		t.Errorf("report carries a home path:\n%s", got)
	}
	if host, err := os.Hostname(); err == nil {
		assertAbsent(t, got, host, "host name")
	}
	if u, err := user.Current(); err == nil {
		assertAbsent(t, got, u.Username, "user name")
	}
}

// assertAbsent fails when name appears in a value of the block. The keys are
// fixed by the kit and carry nothing about the machine, so only values are
// looked at; the three values a name can legitimately collide with — the
// distribution, the kernel and the terminal, none of which this tool supplies
// — are skipped, because a machine called "fedora" running Fedora is not a
// leak and failing on it would be a test of the machine rather than the code.
func assertAbsent(t *testing.T, block, name, what string) {
	t.Helper()
	if name == "" {
		return
	}
	for _, line := range strings.Split(block, "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			// The headline, which carries only the tool and the versions.
			key, value = "", line
		}
		if key == "distro" || key == "kernel" || key == "term" {
			continue
		}
		if strings.Contains(value, name) {
			t.Errorf("report carries the %s %q on %q", what, name, line)
		}
	}
}

// TestDescribeManagers renders the probe's verdict for every package manager,
// which is what tells "dnf drives this machine" from "apt is not here".
func TestDescribeManagers(t *testing.T) {
	tests := []struct {
		name    string
		results []compat.Result
		want    string
	}{
		{
			name: "one answered, two did not",
			results: []compat.Result{
				{Backend: "pacman"},
				{Backend: "apt"},
				{Backend: "dnf", Version: "5.2.18"},
			},
			want: "pacman absent, apt absent, dnf 5.2.18",
		},
		{
			name: "a machine with two of them installed",
			results: []compat.Result{
				{Backend: "apt", Version: "2.9.30"},
				{Backend: "dnf", Version: "5.2.18"},
			},
			want: "apt 2.9.30, dnf 5.2.18",
		},
		{
			name:    "nothing was probed at all",
			results: nil,
			want:    "none probed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeManagers(tc.results); got != tc.want {
				t.Errorf("describeManagers = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestScrubURL covers the one value this tool passes into the block that could
// carry something somebody configured: the catalog URL a fetch failure quotes.
func TestScrubURL(t *testing.T) {
	tests := []struct {
		name string
		note string
		url  string
		want string
	}{
		{
			name: "the family's own catalog is named",
			note: "Get \"" + catalog.URL + "\": timeout",
			url:  catalog.URL,
			want: "Get \"" + catalog.URL + "\": timeout",
		},
		{
			name: "a configured one is not",
			note: "Get \"https://staging.example/catalog.json\": timeout",
			url:  "https://staging.example/catalog.json",
			want: "Get \"the configured catalog\": timeout",
		},
		{
			name: "no url to scrub",
			note: "the snapshot could not be parsed",
			url:  "",
			want: "the snapshot could not be parsed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrubURL(tc.note, tc.url); got != tc.want {
				t.Errorf("scrubURL = %q, want %q", got, tc.want)
			}
		})
	}
}
