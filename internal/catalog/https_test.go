package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCheckURLRequiresHTTPS: the catalog decides what the dashboard offers to
// install, so only HTTPS is read, with plain HTTP allowed for loopback alone.
func TestCheckURLRequiresHTTPS(t *testing.T) {
	for _, good := range []string{
		"https://tui.tools/catalog.json",
		"HTTPS://tui.tools/catalog.json",
		"https://example.org:8443/c.json",
		"http://127.0.0.1:8080/catalog.json",
		"http://127.1.2.3/catalog.json",
		"http://[::1]:9000/catalog.json",
		"http://localhost:3000/catalog.json",
	} {
		if err := CheckURL(good); err != nil {
			t.Errorf("%s was refused: %v", good, err)
		}
	}
	for _, bad := range []string{
		"http://tui.tools/catalog.json",
		"http://10.0.0.1/catalog.json",
		"http://localhost.example.org/catalog.json",
		"http://127.0.0.1.nip.io/catalog.json",
		"ftp://tui.tools/catalog.json",
		"file:///etc/passwd",
		"https:///catalog.json",
		"tui.tools/catalog.json",
		"",
	} {
		if err := CheckURL(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// TestCheckURLHidesCredentials: an address carrying user info is refused
// without repeating the password.
func TestCheckURLHidesCredentials(t *testing.T) {
	err := CheckURL("http://user:hunter2@mirror.example.org/catalog.json")
	if err == nil {
		t.Fatal("a plain-HTTP remote address was accepted")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the error repeats the password: %v", err)
	}
}

// TestFetchRefusesPlainHTTPBeforeConnecting: nothing is requested from a
// plain-HTTP remote address, and Load falls back to the embedded snapshot
// with the reason.
func TestFetchRefusesPlainHTTPBeforeConnecting(t *testing.T) {
	if _, err := Fetch(context.Background(), "http://tui.tools.invalid/catalog.json"); err == nil ||
		!strings.Contains(err.Error(), "https://") {
		t.Fatalf("Fetch over plain HTTP: err = %v", err)
	}
	doc, note := Load(context.Background(), "http://tui.tools.invalid/catalog.json", false)
	if doc.Source != SourceSnapshot {
		t.Errorf("source = %q, want the snapshot", doc.Source)
	}
	if !strings.Contains(note, "HTTPS") {
		t.Errorf("note = %q, want the HTTPS refusal", note)
	}
}

// TestFetchRefusesARedirectToPlainHTTP: the rule holds for every hop, so a
// catalog address cannot hand the request on to a plain-HTTP host.
func TestFetchRefusesARedirectToPlainHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://tui.tools.invalid/catalog.json",
				http.StatusFound)
		}))
	defer server.Close()

	if _, err := Fetch(context.Background(), server.URL); err == nil ||
		!strings.Contains(err.Error(), "https://") {
		t.Errorf("a redirect to plain HTTP was followed: err = %v", err)
	}
}

// TestFetchFollowsALoopbackRedirect: a redirect that stays within the rule is
// still followed.
func TestFetchFollowsALoopbackRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/catalog.json", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/catalog.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sample))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if _, err := Fetch(context.Background(), server.URL+"/old"); err != nil {
		t.Errorf("Fetch: %v", err)
	}
}
