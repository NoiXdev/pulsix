package alerts

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestEventKindsMatchTheSchema pins EventKinds() to the schema itself,
// by reading the migrations and parsing their CHECK lists. An earlier
// version of this test compared EventKinds() against a third hand-written
// copy of the same seven constants a few lines below the first two, which
// proved only that the file agreed with itself: a migration adding an
// eighth kind passed it unchanged, which is exactly the drift the type's
// own doc comment cites fleetStateRank to justify preventing.
//
// Two tables carry the enum, and both are checked here even though only
// the first belongs to this package:
//
//   - alert_events.kind (00049) — this package's own table, the authority
//     the EventKind type describes.
//   - alert_route_events.kind (00052) — internal/alertroutes' table,
//     whose rows are alerts.EventKind values written by
//     alertroutes.Validate through ParseEventKind. The Go list it must
//     agree with is this package's, so the pin lives beside that list
//     rather than beside the table; splitting it would mean a second copy
//     of the parser below and a second place to remember.
func TestEventKindsMatchTheSchema(t *testing.T) {
	for _, tc := range []struct{ migration, table string }{
		{"../db/migrations/00049_alerts.sql", "alert_events"},
		{"../db/migrations/00052_alert_routes.sql", "alert_route_events"},
	} {
		want := checkListIn(t, tc.migration, tc.table, "kind")
		got := EventKinds()
		if len(got) != len(want) {
			t.Fatalf("%s.kind lists %d kinds (%v), EventKinds() has %d (%v)", tc.table, len(want), want, len(got), got)
		}
		for i := range want {
			if string(got[i]) != want[i] {
				t.Errorf("%s.kind[%d] = %q, EventKinds()[%d] = %q", tc.table, i, want[i], i, got[i])
			}
		}
		for _, k := range want {
			if parsed, ok := ParseEventKind(k); !ok || string(parsed) != k {
				t.Errorf("ParseEventKind(%q) = %q, %v; want %q, true — %s accepts it", k, parsed, ok, k, tc.table)
			}
		}
	}
	if _, ok := ParseEventKind("resolved"); ok {
		t.Error(`ParseEventKind("resolved") accepted a kind no CHECK list has`)
	}
}

// quotedLiteral matches one single-quoted SQL string literal.
var quotedLiteral = regexp.MustCompile(`'([^']*)'`)

// checkListIn returns, in the schema's own order, the literals of the
// `CHECK (<column> IN (…))` constraint declared on <column> inside
// `CREATE TABLE <table>` in the named migration. It is a deliberately
// small parser rather than a query against a live database: the point is
// to fail at `go test ./internal/alerts` the moment a migration file
// changes, with no container and no migration run in between.
func checkListIn(t *testing.T, migration, table, column string) []string {
	t.Helper()
	b, err := os.ReadFile(migration)
	if err != nil {
		t.Fatalf("read %s: %v", migration, err)
	}
	body := createTableBody(t, string(b), table, migration)
	head := "CHECK (" + column + " IN ("
	i := strings.Index(body, head)
	if i < 0 {
		t.Fatalf("%s: no %q constraint on %s.%s", migration, head, table, column)
	}
	rest := body[i+len(head):]
	end := strings.Index(rest, ")")
	if end < 0 {
		t.Fatalf("%s: unterminated CHECK list on %s.%s", migration, table, column)
	}
	var out []string
	for _, m := range quotedLiteral.FindAllStringSubmatch(rest[:end], -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("%s: the CHECK list on %s.%s is empty", migration, table, column)
	}
	return out
}

// createTableBody is the text between the parentheses of
// `CREATE TABLE <table> (…)` in sql, found by counting parentheses so
// that a nested CHECK list cannot end the body early. Scoping to the
// table matters: 00052 declares three columns named by a CHECK list and
// only one of them is the event kind.
func createTableBody(t *testing.T, sql, table, migration string) string {
	t.Helper()
	head := "CREATE TABLE " + table + " ("
	i := strings.Index(sql, head)
	if i < 0 {
		t.Fatalf("%s: no CREATE TABLE %s", migration, table)
	}
	rest := sql[i+len(head)-1:] // keep the opening parenthesis
	depth := 0
	for j, r := range rest {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return rest[1:j]
			}
		}
	}
	t.Fatalf("%s: unterminated CREATE TABLE %s", migration, table)
	return ""
}
