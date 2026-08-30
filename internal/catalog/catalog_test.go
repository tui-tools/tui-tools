package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// sample is a catalog document with one entry of every kind the parser has to
// deal with: a normal tool, an unreleased one, the launcher itself, a name
// that is not the family's, and a package name that is not.
const sample = `{
  "schema": 1,
  "generated": "2026-08-30T14:41:20.278Z",
  "packages": {"repo": "https://pkgs.tui.tools", "live": true},
  "signing": {"pubkey": "https://pkgs.tui.tools/pubkey.asc"},
  "tools": [
    {"name": "tui-secure", "package": "tui-secure", "binary": "tui-secure",
     "version": "0.1.2", "platforms": ["linux/amd64", "linux/arm64"],
     "tagline": "posture", "category": "security"},
    {"name": "tui-cert", "package": "tui-cert", "binary": "tui-cert",
     "version": "0.1.2", "platforms": ["linux/amd64", "linux/arm64"]},
    {"name": "tui-template", "package": "tui-template", "binary": "tui-template",
     "unreleased": true},
    {"name": "tui-tools", "package": "tui-tools", "binary": "tui-tools"},
    {"name": "rm", "package": "rm", "binary": "rm"},
    {"name": "tui-evil", "package": "tui-evil; rm -rf /", "binary": "tui-evil"}
  ]
}`

func TestParseKeepsOnlyWhatMayBeActedOn(t *testing.T) {
	doc, err := Parse([]byte(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := strings.Join(doc.Names(), " ")
	if want := "tui-cert tui-secure"; got != want {
		t.Errorf("names = %q, want %q", got, want)
	}
	if doc.Generated.IsZero() {
		t.Error("the generated time was not read")
	}
	if doc.Packages.Repo != "https://pkgs.tui.tools" {
		t.Errorf("packages.repo = %q", doc.Packages.Repo)
	}
}

// The rule the whole trust boundary rests on: a name that is not tui-<word>
// never becomes something a command line could carry.
func TestParseRefusesNamesThatCouldReachACommandLine(t *testing.T) {
	doc, err := Parse([]byte(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, pkg := range doc.Names() {
		if !name.MatchString(pkg) {
			t.Errorf("kept %q, which is not a tui-tools package name", pkg)
		}
	}
	if _, ok := doc.Find("tui-evil; rm -rf /"); ok {
		t.Error("an injected package name survived the parse")
	}
}

func TestParseRefusesAnUnknownSchema(t *testing.T) {
	if _, err := Parse([]byte(`{"schema": 2, "tools": []}`)); err == nil {
		t.Fatal("a schema this build does not read was accepted")
	}
}

func TestParseRefusesAnEmptyCatalog(t *testing.T) {
	if _, err := Parse([]byte(`{"schema": 1, "tools": []}`)); err == nil {
		t.Fatal("a catalog with nothing to act on was accepted")
	}
}

// TestSnapshot is what `make catalog` runs after downloading a new snapshot:
// the document embedded in the binary has to be one this launcher can read,
// or --demo and every machine with no network show nothing.
func TestSnapshot(t *testing.T) {
	doc, err := Embedded()
	if err != nil {
		t.Fatalf("the embedded snapshot does not parse: %v", err)
	}
	if doc.Source != SourceSnapshot {
		t.Errorf("source = %q, want %q", doc.Source, SourceSnapshot)
	}
	if len(doc.Tools) < 5 {
		t.Errorf("the snapshot carries %d tools, which is too few to be the family",
			len(doc.Tools))
	}
	if doc.Packages.Repo == "" {
		t.Error("the snapshot names no package repository")
	}
	for _, tool := range doc.Tools {
		if tool.Package == "" || tool.Binary == "" || tool.Tagline == "" {
			t.Errorf("%s: incomplete entry %+v", tool.Name, tool)
		}
		if tool.Name == Self {
			t.Error("the launcher is in its own install list")
		}
	}
	t.Logf("snapshot: %d tools, generated %s", len(doc.Tools), doc.Generated)
}

func TestRowsClassifyTheMachine(t *testing.T) {
	doc, err := Parse([]byte(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rows := Rows(doc,
		map[string]string{"tui-secure": "0.1.1"},
		map[string]string{"tui-secure": "0.1.2", "tui-cert": "0.1.2"})

	states := map[string]State{}
	for _, row := range rows {
		states[row.Name] = row.State
	}
	if got := states["tui-secure"]; got != StateOutdated {
		t.Errorf("tui-secure = %q, want %q", got, StateOutdated)
	}
	if got := states["tui-cert"]; got != StateNotInstalled {
		t.Errorf("tui-cert = %q, want %q", got, StateNotInstalled)
	}
}

// A freshly installed package must not report an update to itself just
// because the manager printed a package release the repository did not.
func TestRowsIgnoreThePackageRelease(t *testing.T) {
	doc, _ := Parse([]byte(sample))
	rows := Rows(doc,
		map[string]string{"tui-secure": "0.1.2-1.fc42"},
		map[string]string{"tui-secure": "0.1.2-1"})
	for _, row := range rows {
		if row.Name == "tui-secure" && row.State != StateUpToDate {
			t.Errorf("state = %q, want %q", row.State, StateUpToDate)
		}
	}
}

// An unreadable repository is an answer, not a failure: what is installed is
// still shown, and nothing is claimed about what is current.
func TestRowsSurviveAnUnreadableRepository(t *testing.T) {
	doc, _ := Parse([]byte(sample))
	rows := Rows(doc, map[string]string{"tui-secure": "0.1.2"}, nil)
	for _, row := range rows {
		if row.Name == "tui-secure" && row.State != StateUnknown {
			t.Errorf("state = %q, want %q", row.State, StateUnknown)
		}
	}
}

func TestRowsMarkAnUnsupportedArchitecture(t *testing.T) {
	doc, err := Parse([]byte(`{"schema": 1, "tools": [
	  {"name": "tui-secure", "package": "tui-secure", "binary": "tui-secure",
	   "platforms": ["linux/riscv64"]}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rows := Rows(doc, nil, nil)
	if rows[0].Supported {
		t.Errorf("a tool with no %s/%s build was called supported",
			runtime.GOOS, runtime.GOARCH)
	}
	if !strings.Contains(rows[0].Compat, "no ") {
		t.Errorf("compat = %q, want it to say there is no build", rows[0].Compat)
	}
}

func TestFetchReadsAServedCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(sample))
		}))
	defer server.Close()

	doc, err := Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Source != SourceLive {
		t.Errorf("source = %q, want %q", doc.Source, SourceLive)
	}
}

// The fallback is the promise that a machine with no network still has a
// dashboard: the snapshot is used, and the reason the live one failed comes
// back so the screen can say which document it is showing.
func TestLoadFallsBackToTheSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
	defer server.Close()

	doc, note := Load(context.Background(), server.URL, false)
	if doc.Source != SourceSnapshot {
		t.Errorf("source = %q, want %q", doc.Source, SourceSnapshot)
	}
	if note == "" {
		t.Error("the fallback reported no reason")
	}
}

func TestLoadOfflineNeverReachesTheNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			t.Error("--offline reached the network")
		}))
	defer server.Close()

	doc, note := Load(context.Background(), server.URL, true)
	if doc.Source != SourceSnapshot {
		t.Errorf("source = %q, want %q", doc.Source, SourceSnapshot)
	}
	if note != "" {
		t.Errorf("note = %q, want none", note)
	}
}
