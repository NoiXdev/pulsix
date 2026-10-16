package alerts

import (
	"os"
	"regexp"
	"slices"
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
// Two tables carry the enum, and they are deliberately NOT compared the
// same way:
//
//   - alert_events.kind (00056, not 00049) — this package's own table, the
//     authority EventKind describes. 00049 declared the original
//     seven-value CHECK; 00056 (v0.4d) DROPs and ADDs it to widen the list
//     to eight for 'escalated' — pointing this test at 00049 alone is
//     exactly how it slept through that widening the first time: it kept
//     comparing EventKinds() against a CHECK list the schema had already
//     moved past. EventKinds() must match this one exactly, in order.
//   - alert_route_events.kind (00052) — internal/alertroutes' table of a
//     ROUTE's own Events selection, which drives the forward pass's
//     matching (match.go). It is one short of EventKinds() ON PURPOSE:
//     'escalated' is not — and must never become — something a route can
//     select as a trigger. escalatePass (follow_escalate.go) already
//     delivers that event directly, through its own claim-then-enqueue
//     path; a route that could also select it would give the same
//     alert_events row two uncoordinated delivery paths racing over it.
//     So this table is checked against EventKinds() MINUS EventEscalated,
//     and a widening of its own CHECK list to include 'escalated' fails
//     this test loudly rather than quietly enabling the double delivery.
func TestEventKindsMatchTheSchema(t *testing.T) {
	got := EventKinds()

	want := checkListIn(t, "../db/migrations/00056_escalation_chains.sql", "alert_events", "kind")
	assertKindsMatch(t, "alert_events", want, got)

	// EventKinds() always ends in EventEscalated (its own const block
	// comment says so) — dropping the last element is what "minus
	// escalated" means in code, not a slice of the first seven by number.
	routeWant := checkListIn(t, "../db/migrations/00052_alert_routes.sql", "alert_route_events", "kind")
	assertKindsMatch(t, "alert_route_events", routeWant, got[:len(got)-1])
	if slices.Contains(routeWant, string(EventEscalated)) {
		t.Fatal("alert_route_events.kind now permits 'escalated' — a route could select it as a trigger " +
			"and race escalatePass's own delivery over the same event; this test's own doc comment " +
			"explains why that must not happen, update it and this assertion together if it is now intended")
	}

	if _, ok := ParseEventKind("resolved"); ok {
		t.Error(`ParseEventKind("resolved") accepted a kind no CHECK list has`)
	}
}

// assertKindsMatch checks that want — one table's own CHECK list, in
// schema order — equals gotKinds exactly, and that ParseEventKind accepts
// every literal in it. Factored out of TestEventKindsMatchTheSchema so
// that function's own two tables can be held to the same equality check
// against two DIFFERENT slices (the full EventKinds() for alert_events,
// EventKinds() minus EventEscalated for alert_route_events) without
// duplicating the comparison itself.
func assertKindsMatch(t *testing.T, table string, want []string, gotKinds []EventKind) {
	t.Helper()
	if len(gotKinds) != len(want) {
		t.Fatalf("%s.kind lists %d kinds (%v), want %d (%v)", table, len(want), want, len(gotKinds), gotKinds)
	}
	for i := range want {
		if string(gotKinds[i]) != want[i] {
			t.Errorf("%s.kind[%d] = %q, want %q", table, i, want[i], gotKinds[i])
		}
	}
	for _, k := range want {
		if parsed, ok := ParseEventKind(k); !ok || string(parsed) != k {
			t.Errorf("ParseEventKind(%q) = %q, %v; want %q, true — %s accepts it", k, parsed, ok, k, table)
		}
	}
}

// quotedLiteral matches one single-quoted SQL string literal.
var quotedLiteral = regexp.MustCompile(`'([^']*)'`)

// checkListIn returns, in the schema's own order, the literals of the
// `CHECK (<column> IN (…))` constraint declared on <column>, either inside
// `CREATE TABLE <table>` or — migration 00056's own shape for
// alert_events, which widens a constraint an earlier migration declared
// rather than redeclaring the table — in a bare `ALTER TABLE <table> ADD
// CONSTRAINT ... CHECK (...)` with no CREATE TABLE of its own to scope
// into. It is a deliberately small parser rather than a query against a
// live database: the point is to fail at `go test ./internal/alerts` the
// moment a migration file changes, with no container and no migration run
// in between.
func checkListIn(t *testing.T, migration, table, column string) []string {
	t.Helper()
	b, err := os.ReadFile(migration)
	if err != nil {
		t.Fatalf("read %s: %v", migration, err)
	}
	body := string(b)
	if strings.Contains(body, "CREATE TABLE "+table+" (") {
		body = createTableBody(t, body, table, migration)
	}
	// Else: no CREATE TABLE for table in this migration at all — it only
	// widens a CHECK an earlier migration declared (00056's own ALTER
	// TABLE ... ADD CONSTRAINT) — so the search runs over the whole file
	// rather than a table body there is none of here.
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
