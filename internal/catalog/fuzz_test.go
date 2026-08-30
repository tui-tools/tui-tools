package catalog

import "testing"

// The catalog is the one thing this launcher reads that it did not write: a
// JSON document fetched over the network, describing tools whose package names
// end up on a command line. Parse is what stands between that document and the
// rest of the program, so what it returns has to hold for any bytes at all —
// see tui-kit/templates/FUZZING.md for the family rule, and
// tui-kit/pkgmgr/fuzz_test.go for the worked example.
//
// `go test` runs the seeds below on every commit; exploring past them is a
// thing you run when you touch the parser:
//
//	go test -run=^$ -fuzz=FuzzParse -fuzztime=5m ./internal/catalog/

// FuzzParse asserts the promise the package documentation makes: everything
// Parse keeps is something the launcher may act on. A name that is not
// `tui-<word>`, the launcher itself and an unreleased entry are dropped, and
// nothing else can come back — which is what keeps a document written by
// somebody else from choosing the argument of an install command.
func FuzzParse(f *testing.F) {
	// snapshot.json is the real document, embedded in the binary, and the
	// test sample carries an entry of every kind the parser has to refuse.
	f.Add(string(snapshot))
	f.Add(sample)
	// The shapes a real document never has.
	f.Add("")
	f.Add("{}")
	f.Add("null")
	f.Add(`{"schema":1}`)
	f.Add(`{"schema":1,"tools":[]}`)
	f.Add(`{"schema":2,"tools":[{"name":"tui-cert","package":"tui-cert"}]}`)
	f.Add(`{"schema":1,"tools":[{"name":"tui-cert","package":"tui-cert; rm -rf /"}]}`)
	f.Add(`{"schema":1,"tools":[{"name":"tui-tools","package":"tui-tools"}]}`)

	f.Fuzz(func(t *testing.T, data string) {
		doc, err := Parse([]byte(data))
		if err != nil {
			if len(doc.Tools) != 0 || doc.Schema != 0 {
				t.Fatalf("failed and still returned a document: %+v", doc)
			}
			return
		}
		if doc.Schema != schemaVersion {
			t.Fatalf("kept schema %d, this build reads %d", doc.Schema, schemaVersion)
		}
		if len(doc.Tools) == 0 {
			t.Fatal("succeeded with nothing to show")
		}
		previous := ""
		for _, tool := range doc.Tools {
			if !name.MatchString(tool.Name) {
				t.Fatalf("kept a name this family would not use: %q", tool.Name)
			}
			// The package name is the only field that reaches an argv.
			if !name.MatchString(tool.Package) {
				t.Fatalf("kept a package name that could reach a command line: %q", tool.Package)
			}
			if tool.Unreleased {
				t.Fatalf("kept %q, which has nothing to install", tool.Name)
			}
			if tool.Name == Self {
				t.Fatal("kept the launcher itself")
			}
			if tool.Name < previous {
				t.Fatalf("tools are out of order: %q before %q", previous, tool.Name)
			}
			previous = tool.Name
			if _, ok := doc.Find(tool.Package); !ok {
				t.Fatalf("Find cannot recover %q", tool.Package)
			}
		}
		if len(doc.Names()) != len(doc.Tools) {
			t.Fatalf("Names() returned %d of %d tools", len(doc.Names()), len(doc.Tools))
		}
		// Rows is what the dashboard draws, over whatever the package manager
		// happened to report. Every card has to say something about itself.
		for _, row := range Rows(doc, nil, map[string]string{"tui-cert": "9.9.9-1"}) {
			if row.State == "" || row.Compat == "" {
				t.Fatalf("card without a state or a compatibility line: %+v", row)
			}
		}
	})
}
