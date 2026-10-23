package main

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestManagerEmbedsTheZoneDatabase is backlog 2.88. Fourteen call sites
// across six packages resolve a named IANA zone — a schedule's, a
// maintenance window's, an alert route's, an on-call rotation's, a
// digest's — and time.LoadLocation reads the host's zoneinfo files to do
// it. On a host that ships none, every one of those fails at once, and
// the failures read as the operator's fault: internal/schedules'
// decide.go stops the schedule and tells them "the schedule's timetable
// is invalid; edit and save it", which no edit can fix.
//
// Blank-importing time/tzdata makes the program carry its own copy. The
// embedded database is a FALLBACK, consulted only after the host's own
// files are not found, so a host with a maintained zoneinfo keeps getting
// its updates. What it buys is the guarantee that a LoadLocation failure
// means an unknown zone NAME — which is what every one of those call
// sites already tells the operator.
//
// This is a build-configuration invariant, so the test reads the import
// rather than any run-time behaviour: with the host's own zoneinfo
// present, as it is wherever this test runs, the embedded copy is never
// reached and nothing observable changes.
func TestManagerEmbedsTheZoneDatabase(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		// Shipped sources only: an import in a _test.go file would
		// satisfy the letter of this test and embed nothing in the
		// binary.
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil || path != "time/tzdata" {
				continue
			}
			if imp.Name == nil || imp.Name.Name != "_" {
				t.Fatalf("%s imports time/tzdata under a name; it registers its data through its init and is only ever blank-imported", name)
			}
			return
		}
	}
	t.Fatal("no shipped file in cmd/manager blank-imports time/tzdata: the binary resolves named time zones for schedules, maintenance windows, alert routes, on-call rotations and digests, and without the embedded database a host with no zoneinfo files fails all of them as if the operator had saved a bad zone")
}
