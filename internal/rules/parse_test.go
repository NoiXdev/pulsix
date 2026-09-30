package rules_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/rules"
)

// treeJSON renders n through encoding/json, the same encoding the tree is
// stored under: Pos carries `json:"-"` (see ast.go), so this is a
// structural-equality check that ignores parser bookkeeping and only
// compares the shape a later task would actually persist and evaluate.
func treeJSON(t *testing.T, n rules.Node) string {
	t.Helper()
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(b)
}

// assertSameTree fails the test unless got and want encode identically,
// ignoring Pos.
func assertSameTree(t *testing.T, got, want rules.Node) {
	t.Helper()
	gotJSON, wantJSON := treeJSON(t, got), treeJSON(t, want)
	if gotJSON != wantJSON {
		t.Fatalf("tree mismatch:\n got  = %s\n want = %s", gotJSON, wantJSON)
	}
}

// TestParseSuccessTable is the brief's own examples plus operator
// precedence, parenthesised overrides and unary minus. Every case also
// round-trips through Format(Parse(src)) back to an equal tree — the
// property-flavoured half of the brief's test paragraph — so a bug that
// only shows up on re-parsing the formatted text fails the same
// subtest that pins the tree, right next to the fixture that provoked it.
func TestParseSuccessTable(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want rules.Node
	}{
		{
			"flagship: window compared to a fact times a constant",
			`avg(cpu.used_pct, 10m) > fact("cpu_count") * 0.8`,
			compare(">", window("cpu.used_pct", "avg", 600), arith("*", fact("cpu_count"), number(0.8))),
		},
		{
			"or of two plain comparisons",
			`disk.used_pct >= 90 or disk.inodes_used_pct >= 90`,
			or(compare(">=", metric("disk.used_pct"), number(90)), compare(">=", metric("disk.inodes_used_pct"), number(90))),
		},
		{
			"label in with two values",
			`service.state in ("failed", "inactive")`,
			label("service.state", "in", "failed", "inactive"),
		},
		{
			"not wrapping a parenthesised comparison",
			`not (mem.used_pct < 50)`,
			not(compare("<", metric("mem.used_pct"), number(50))),
		},
		{
			"precedence: and binds tighter than or",
			`cpu.used_pct > 1 or mem.used_pct > 2 and disk.used_pct > 3`,
			or(
				compare(">", metric("cpu.used_pct"), number(1)),
				and(compare(">", metric("mem.used_pct"), number(2)), compare(">", metric("disk.used_pct"), number(3))),
			),
		},
		{
			"parenthesised override beats precedence",
			`(cpu.used_pct > 1 or mem.used_pct > 2) and disk.used_pct > 3`,
			and(
				or(compare(">", metric("cpu.used_pct"), number(1)), compare(">", metric("mem.used_pct"), number(2))),
				compare(">", metric("disk.used_pct"), number(3)),
			),
		},
		{
			"unary minus on a literal",
			`cpu.used_pct > -1`,
			compare(">", metric("cpu.used_pct"), arith("-", number(0), number(1))),
		},
		{
			"arithmetic precedence inside a comparison",
			`cpu.used_pct == 2 + 3 * 4`,
			compare("==", metric("cpu.used_pct"), arith("+", number(2), arith("*", number(3), number(4)))),
		},
		{
			"label not-equals",
			`service.state != "failed"`,
			label("service.state", "!=", "failed"),
		},
		{
			"a duration in hours and one in seconds",
			`avg(cpu.load5, 1h) > min(cpu.load1, 90s)`,
			compare(">", window("cpu.load5", "avg", 3600), window("cpu.load1", "min", 90)),
		},
		{
			"less-than-or-equal comparison",
			`disk.free_bytes <= 1000`,
			compare("<=", metric("disk.free_bytes"), number(1000)),
		},
		{
			"binary minus and division alongside plus and times",
			`cpu.used_pct == 10 - 4 / 2 + 1 * 3`,
			compare("==", metric("cpu.used_pct"),
				arith("+", arith("-", number(10), arith("/", number(4), number(2))), arith("*", number(1), number(3)))),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rules.Parse(tc.src)
			if err != nil {
				t.Fatalf("Parse(%q) = %v, want a tree", tc.src, err)
			}
			assertSameTree(t, got, tc.want)

			formatted := rules.Format(got)
			reparsed, err := rules.Parse(formatted)
			if err != nil {
				t.Fatalf("Parse(Format(Parse(%q))) = %v (formatted = %q)", tc.src, err, formatted)
			}
			assertSameTree(t, reparsed, got)
		})
	}
}

// parseErrorCase is one row shared by TestParseErrorTable and
// TestParseAdditionalErrorPaths: a source string, the byte offset an
// editor's caret belongs at, and the exact message.
type parseErrorCase struct {
	name    string
	src     string
	wantPos int
	wantMsg string
}

// runParseErrorCases asserts every case in tests fails Parse with a
// *rules.ParseError wrapping ErrInvalidRule, at exactly the given Pos and
// Msg.
func runParseErrorCases(t *testing.T, tests []parseErrorCase) {
	t.Helper()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rules.Parse(tc.src)
			if err == nil {
				t.Fatalf("Parse(%q) = nil error, want one", tc.src)
			}
			if !errors.Is(err, rules.ErrInvalidRule) {
				t.Fatalf("Parse(%q) error %v does not wrap ErrInvalidRule", tc.src, err)
			}
			var perr *rules.ParseError
			if !errors.As(err, &perr) {
				t.Fatalf("Parse(%q) error %v is not a *rules.ParseError", tc.src, err)
			}
			if perr.Pos != tc.wantPos {
				t.Fatalf("Parse(%q) error Pos = %d, want %d (err=%v)", tc.src, perr.Pos, tc.wantPos, err)
			}
			if perr.Msg != tc.wantMsg {
				t.Fatalf("Parse(%q) error Msg = %q, want %q", tc.src, perr.Msg, tc.wantMsg)
			}
		})
	}
}

// TestParseErrorTable is the brief's error table: every case asserts both
// the message and the byte offset an editor would put the caret at. Where
// the offending text has an unambiguous, unique substring in src, wantPos
// is computed from it with strings.Index rather than hand-counted, so the
// test documents *why* that position is correct instead of just asserting
// a number.
func TestParseErrorTable(t *testing.T) {
	tests := []parseErrorCase{
		{
			"unterminated string",
			`service.state == "failed`,
			strings.Index(`service.state == "failed`, `"`),
			"unterminated string",
		},
		{
			"unknown function",
			`foo(cpu.used_pct, 10m)`,
			strings.Index(`foo(cpu.used_pct, 10m)`, "foo"),
			`unknown function "foo"`,
		},
		{
			"missing comma in a call",
			`avg(cpu.used_pct 10m)`,
			strings.Index(`avg(cpu.used_pct 10m)`, "10m"),
			"expected ',', got duration",
		},
		{
			"trailing operator",
			`cpu.used_pct >`,
			len(`cpu.used_pct >`),
			"expected expression, got end of input",
		},
		{
			"empty input",
			``,
			0,
			"expected expression, got end of input",
		},
		{
			"duration without a unit",
			`avg(cpu.used_pct, 10)`,
			strings.Index(`avg(cpu.used_pct, 10)`, "10"),
			"duration: missing unit",
		},
		{
			"duration with an unknown unit",
			`avg(cpu.used_pct, 10x)`,
			strings.Index(`avg(cpu.used_pct, 10x)`, "10x"),
			`duration: unknown unit "x"`,
		},
		{
			"unbalanced parentheses",
			`(cpu.used_pct > 90`,
			len(`(cpu.used_pct > 90`),
			"expected ')', got end of input",
		},
		{
			"unknown metric name, surfacing TypeCheck's position through Parse",
			`no.such.metric > 1`,
			strings.Index(`no.such.metric > 1`, "no.such.metric"),
			`metric: unknown metric "no.such.metric"`,
		},
	}
	runParseErrorCases(t, tests)
}

// TestParseAdditionalErrorPaths covers error paths beyond the brief's own
// table: a stray token of each broad kind left over after an otherwise
// complete rule (proving Parse rejects trailing garbage rather than
// silently ignoring it), the two single-character operators that only
// have a meaning paired with '=', and the error paths inside "in" and a
// window/fact call that the brief's table does not separately name
// (missing '(', a non-string value, a missing closing ')').
func TestParseAdditionalErrorPaths(t *testing.T) {
	tests := []parseErrorCase{
		{
			"stray number after a complete rule",
			`cpu.used_pct > 1 2`,
			strings.LastIndex(`cpu.used_pct > 1 2`, "2"),
			"unexpected number",
		},
		{
			"stray string after a complete rule",
			`cpu.used_pct > 1 "extra"`,
			strings.Index(`cpu.used_pct > 1 "extra"`, `"extra"`),
			"unexpected string",
		},
		{
			"stray opening paren after a complete rule",
			`cpu.used_pct > 1 (`,
			strings.LastIndex(`cpu.used_pct > 1 (`, "("),
			"unexpected '('",
		},
		{
			"stray closing paren after a complete rule",
			`cpu.used_pct > 90)`,
			strings.LastIndex(`cpu.used_pct > 90)`, ")"),
			"unexpected ')'",
		},
		{
			"stray comma after a complete rule",
			`cpu.used_pct > 1,`,
			strings.LastIndex(`cpu.used_pct > 1,`, ","),
			"unexpected ','",
		},
		{
			"stray plus after a parenthesised, already-complete rule",
			`(cpu.used_pct > 1) +`,
			strings.LastIndex(`(cpu.used_pct > 1) +`, "+"),
			"unexpected '+'",
		},
		{
			"a bare '=' is not an operator on its own",
			`cpu.used_pct = 1`,
			strings.Index(`cpu.used_pct = 1`, "="),
			`unexpected character "="`,
		},
		{
			"a bare '!' is not an operator on its own",
			`cpu.used_pct ! 1`,
			strings.Index(`cpu.used_pct ! 1`, "!"),
			`unexpected character "!"`,
		},
		{
			"label in missing its opening paren",
			`service.state in "failed"`,
			strings.Index(`service.state in "failed"`, `"failed"`),
			`expected '(', got string`,
		},
		{
			"label in with a non-string value",
			`service.state in (5)`,
			strings.Index(`service.state in (5)`, "5"),
			"expected string, got number",
		},
		{
			"label in missing its closing paren",
			`service.state in ("failed"`,
			len(`service.state in ("failed"`),
			"expected ')', got end of input",
		},
		{
			"window call with a non-identifier metric argument",
			`avg(5, 10m) > 0`,
			strings.Index(`avg(5, 10m) > 0`, "5"),
			"expected identifier, got number",
		},
		{
			"window call missing its closing paren",
			`avg(cpu.used_pct, 10m > 0`,
			strings.Index(`avg(cpu.used_pct, 10m > 0`, ">"),
			"expected ')', got '>'",
		},
		{
			"fact call with a non-string argument",
			`fact(5) > 0`,
			strings.Index(`fact(5) > 0`, "5"),
			"expected string, got number",
		},
		{
			"fact call missing its closing paren",
			`fact("cpu_count" > 0`,
			strings.Index(`fact("cpu_count" > 0`, ">"),
			"expected ')', got '>'",
		},
	}
	runParseErrorCases(t, tests)
}

// TestParseErrorMessageCarriesPosAndMsg pins ParseError.Error's rendering,
// mirroring TestTypeCheckErrorMessageCarriesPosAndMsg in catalog_test.go
// for the parser's own error type.
func TestParseErrorMessageCarriesPosAndMsg(t *testing.T) {
	err := &rules.ParseError{Pos: 7, Msg: "something went wrong"}
	got := err.Error()
	if !strings.Contains(got, "7") || !strings.Contains(got, "something went wrong") {
		t.Fatalf("Error() = %q, want it to mention the position 7 and the message", got)
	}
	if !errors.Is(err, rules.ErrInvalidRule) {
		t.Fatal("ParseError must unwrap to ErrInvalidRule")
	}
}

// TestParseSurfacesTypeCheckAsParseError is the controller's ruling made
// concrete: a caller of Parse never has to type-switch between
// *rules.ParseError and *rules.TypeCheckError to find the caret position —
// every failure Parse returns, syntax or semantic, is a *rules.ParseError
// with the offending Pos carried over unchanged.
func TestParseSurfacesTypeCheckAsParseError(t *testing.T) {
	// A window below MinWindow is syntactically fine (a plain duration) but
	// semantically rejected by TypeCheck, not by the parser itself.
	src := `avg(cpu.used_pct, 30s) > 90`
	_, err := rules.Parse(src)
	if err == nil {
		t.Fatalf("Parse(%q) = nil, want a TypeCheck failure (30s is below MinWindow)", src)
	}
	var perr *rules.ParseError
	if !errors.As(err, &perr) {
		t.Fatalf("Parse(%q) error %v is not a *rules.ParseError", src, err)
	}
	var tcErr *rules.TypeCheckError
	if errors.As(err, &tcErr) {
		t.Fatalf("Parse(%q) error %v exposes a *rules.TypeCheckError directly; the controller's ruling asks for it converted", src, err)
	}
	wantPos := strings.Index(src, "avg")
	if perr.Pos != wantPos {
		t.Fatalf("Parse(%q) error Pos = %d, want %d (the window node's own position)", src, perr.Pos, wantPos)
	}
}

// TestParseRejectsBareArithAsPredicate pins a stricter reading of the
// grammar than "let TypeCheck catch it": comparison := arith (op) arith
// requires the operator, so a bare arithmetic expression with no
// comparison operator at all is a syntax error at parse time, not a
// deferred "not a boolean expression" from TypeCheck.
func TestParseRejectsBareArithAsPredicate(t *testing.T) {
	_, err := rules.Parse(`cpu.used_pct`)
	if err == nil {
		t.Fatal("Parse(\"cpu.used_pct\") = nil, want an error: a bare metric is not a predicate")
	}
	var perr *rules.ParseError
	if !errors.As(err, &perr) {
		t.Fatalf("error %v is not a *rules.ParseError", err)
	}
	if perr.Pos != len(`cpu.used_pct`) {
		t.Fatalf("Pos = %d, want %d (end of input, where a comparison operator was expected)", perr.Pos, len(`cpu.used_pct`))
	}
}

// TestFormat pins Format's exact rendering for the node shapes simple
// enough to predict by hand; the success table above already covers the
// deeply nested cases through round-tripping rather than string equality,
// since minimal parenthesisation is not part of Format's contract — only
// that the rendering re-parses to an equal tree.
func TestFormat(t *testing.T) {
	tests := []struct {
		name string
		node rules.Node
		want string
	}{
		{"number", number(42.5), "42.5"},
		{"whole number", number(90), "90"},
		{"metric", metric("cpu.used_pct"), "cpu.used_pct"},
		{"window in minutes", window("cpu.used_pct", "avg", 600), "avg(cpu.used_pct, 10m)"},
		{"window in hours", window("cpu.load5", "rate", 3600), "rate(cpu.load5, 1h)"},
		{"window in seconds", window("cpu.load1", "min", 45), "min(cpu.load1, 45s)"},
		{"fact", fact("cpu_count"), `fact("cpu_count")`},
		{"unary minus of an atom", arith("-", number(0), metric("cpu.used_pct")), "-cpu.used_pct"},
		{"simple compare", compare(">", metric("cpu.used_pct"), number(90)), "cpu.used_pct > 90"},
		{"simple arith", arith("+", number(1), number(2)), "1 + 2"},
		{"label equals", label("service.state", "==", "active"), `service.state == "active"`},
		{"label in", label("service.state", "in", "failed", "inactive"), `service.state in ("failed", "inactive")`},
		{
			"and of two comparisons",
			and(compare(">", metric("cpu.used_pct"), number(1)), compare(">", metric("mem.used_pct"), number(2))),
			"cpu.used_pct > 1 and mem.used_pct > 2",
		},
		{
			"or of two comparisons",
			or(compare(">", metric("cpu.used_pct"), number(1)), compare(">", metric("mem.used_pct"), number(2))),
			"cpu.used_pct > 1 or mem.used_pct > 2",
		},
		{
			"not",
			not(compare("<", metric("mem.used_pct"), number(50))),
			"not mem.used_pct < 50",
		},
		{
			"an or nested inside an and needs parens to survive re-parsing",
			and(or(compare(">", metric("cpu.used_pct"), number(1)), compare(">", metric("mem.used_pct"), number(2))), compare(">", metric("disk.used_pct"), number(3))),
			"(cpu.used_pct > 1 or mem.used_pct > 2) and disk.used_pct > 3",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rules.Format(tc.node); got != tc.want {
				t.Fatalf("Format(%+v) = %q, want %q", tc.node, got, tc.want)
			}
		})
	}
}

// TestFormatRoundTripsExactString reruns a handful of Format's outputs
// back through Parse, confirming they are not merely well-formed text but
// text that reproduces the exact node Format was given — the same
// property TestParseSuccessTable checks starting from source text instead
// of from a hand-built tree.
func TestFormatRoundTripsExactString(t *testing.T) {
	node := and(
		or(compare(">", metric("cpu.used_pct"), number(1)), compare(">", metric("mem.used_pct"), number(2))),
		not(label("service.state", "==", "failed")),
	)
	formatted := rules.Format(node)
	reparsed, err := rules.Parse(formatted)
	if err != nil {
		t.Fatalf("Parse(Format(node)) = %v (formatted = %q)", err, formatted)
	}
	assertSameTree(t, reparsed, node)
}
