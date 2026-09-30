package rules_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/noixdev/pulsix/internal/rules"
)

// assertSameForm fails the test unless got and want are deeply equal. Form
// carries a slice field (Labels), so it cannot use plain "==" — reflect.
// DeepEqual is the same tool ast_test.go uses for that reason.
func assertSameForm(t *testing.T, got, want rules.Form) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("form mismatch:\n got  = %+v\n want = %+v", got, want)
	}
}

// isZeroForm and isZeroNode report whether a Form or Node is the exact zero
// value. Both types carry slice fields, so neither supports "==" — this is
// the same reflect.DeepEqual-against-a-zero-value idiom used throughout
// this package's tests.
func isZeroForm(f rules.Form) bool { return reflect.DeepEqual(f, rules.Form{}) }
func isZeroNode(n rules.Node) bool { return reflect.DeepEqual(n, rules.Node{}) }

// TestFormCompileAsFormRoundTrip is the brief's round-trip table: every
// numeric operator with and without a clear condition, and every discrete
// operator (which never carries a clear condition — that combination is
// the rejection table's own case, not a round-trip one). Each case compiles
// f, recovers a Form from the resulting trees, and asserts the recovered
// Form equals f exactly.
func TestFormCompileAsFormRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		form rules.Form
	}{
		{"numeric >", rules.Form{Metric: "cpu.used_pct", Op: ">", Value: 90}},
		{"numeric > with clear", rules.Form{Metric: "cpu.used_pct", Op: ">", Value: 90, ClearOp: "<", ClearValue: 70}},
		{"numeric >=", rules.Form{Metric: "cpu.used_pct", Op: ">=", Value: 90}},
		{"numeric >= with clear", rules.Form{Metric: "cpu.used_pct", Op: ">=", Value: 90, ClearOp: "<=", ClearValue: 70}},
		{"numeric <", rules.Form{Metric: "disk.free_bytes", Op: "<", Value: 1000}},
		{"numeric < with clear", rules.Form{Metric: "disk.free_bytes", Op: "<", Value: 1000, ClearOp: ">", ClearValue: 5000}},
		{"numeric <=", rules.Form{Metric: "disk.free_bytes", Op: "<=", Value: 1000}},
		{"numeric <= with clear", rules.Form{Metric: "disk.free_bytes", Op: "<=", Value: 1000, ClearOp: ">=", ClearValue: 5000}},
		{"numeric ==", rules.Form{Metric: "cpu.load1", Op: "==", Value: 0}},
		{"numeric == with clear", rules.Form{Metric: "cpu.load1", Op: "==", Value: 0, ClearOp: "!=", ClearValue: 0}},
		{"numeric !=", rules.Form{Metric: "cpu.load1", Op: "!=", Value: 0}},
		{"numeric != with clear", rules.Form{Metric: "cpu.load1", Op: "!=", Value: 0, ClearOp: "==", ClearValue: 0}},
		{"discrete ==", rules.Form{Metric: "service.state", Op: "==", Labels: []string{"failed"}}},
		{"discrete !=", rules.Form{Metric: "service.state", Op: "!=", Labels: []string{"active"}}},
		{"discrete in", rules.Form{Metric: "service.state", Op: "in", Labels: []string{"failed", "inactive"}}},
		{"discrete in single value", rules.Form{Metric: "service.state", Op: "in", Labels: []string{"failed"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			trigger, clear, err := tc.form.Compile()
			if err != nil {
				t.Fatalf("Compile(%+v) = %v, want nil error", tc.form, err)
			}
			got, ok := rules.AsForm(trigger, clear)
			if !ok {
				t.Fatalf("AsForm(%+v, %+v) = false, want true", trigger, clear)
			}
			assertSameForm(t, got, tc.form)
		})
	}
}

// TestFormCompileRejects is the brief's rejection table: an operator that
// does not fit the metric's kind (both directions — a numeric-only operator
// on a discrete metric and vice versa), "in" with an empty list, a clear
// condition on a discrete metric, and a metric outside the catalogue. Every
// case must report an error wrapping ErrInvalidRule.
func TestFormCompileRejects(t *testing.T) {
	tests := []struct {
		name string
		form rules.Form
	}{
		{"unknown metric", rules.Form{Metric: "no.such.metric", Op: ">", Value: 0}},
		{"numeric operator on a discrete metric", rules.Form{Metric: "service.state", Op: ">", Value: 0}},
		{"discrete operator on a numeric metric", rules.Form{Metric: "cpu.used_pct", Op: "in", Value: 0}},
		{"in with an empty list", rules.Form{Metric: "service.state", Op: "in", Labels: nil}},
		{"== with no label", rules.Form{Metric: "service.state", Op: "==", Labels: nil}},
		{"clear condition on a discrete metric", rules.Form{Metric: "service.state", Op: "==", Labels: []string{"active"}, ClearOp: "!=", ClearValue: 0}},
		{"clear operator that does not fit the metric's kind", rules.Form{Metric: "cpu.used_pct", Op: ">", Value: 90, ClearOp: "in", ClearValue: 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			trigger, clear, err := tc.form.Compile()
			if err == nil {
				t.Fatalf("Compile(%+v) = nil error, want an error", tc.form)
			}
			if !errors.Is(err, rules.ErrInvalidRule) {
				t.Fatalf("Compile(%+v) error %v does not wrap ErrInvalidRule", tc.form, err)
			}
			if !isZeroNode(trigger) || !isZeroNode(clear) {
				t.Fatalf("Compile(%+v) returned non-zero nodes alongside an error: trigger=%+v clear=%+v", tc.form, trigger, clear)
			}
		})
	}
}

// TestAsFormRejectsOutsideTheFormSubset is the brief's recognition table:
// trees whose shape a Form could never produce must report ok = false
// rather than a wrong or partial Form. The one-way switch to expression
// mode in the GUI depends on this false being reliable.
func TestAsFormRejectsOutsideTheFormSubset(t *testing.T) {
	validClear := rules.Node{} // no clear condition, for cases that only probe the trigger
	tests := []struct {
		name    string
		trigger rules.Node
		clear   rules.Node
	}{
		{"or at the root", or(compare(">", metric("cpu.used_pct"), number(1)), compare("<", metric("cpu.used_pct"), number(0))), validClear},
		{"window node", compare(">", window("cpu.used_pct", "avg", 600), number(90)), validClear},
		{"fact node", compare(">", fact("cpu_count"), number(0)), validClear},
		{"arithmetic node", compare("==", arith("+", number(1), number(2)), number(3)), validClear},
		{"comparison of two metrics", compare(">", metric("cpu.used_pct"), metric("mem.used_pct")), validClear},
		{"compare with wrong arg count", rules.Node{Kind: rules.KindCompare, Op: ">", Args: []rules.Node{number(1)}}, validClear},
		{"compare with unknown operator", rules.Node{Kind: rules.KindCompare, Op: "~", Args: []rules.Node{metric("cpu.used_pct"), number(1)}}, validClear},
		{"unknown metric in a compare", compare(">", metric("no.such.metric"), number(0)), validClear},
		{"label on a numeric metric", label("cpu.used_pct", "=="), validClear},
		{"label with unknown operator", rules.Node{Kind: rules.KindLabel, Name: "service.state", Op: "~", Str: "active"}, validClear},
		{"label in with no values", rules.Node{Kind: rules.KindLabel, Name: "service.state", Op: "in"}, validClear},
		{
			"clear on a different metric than the trigger",
			compare(">", metric("cpu.used_pct"), number(90)),
			compare("<", metric("mem.used_pct"), number(10)),
		},
		{
			"clear itself outside the form subset",
			compare(">", metric("cpu.used_pct"), number(90)),
			or(compare("<", metric("cpu.used_pct"), number(10)), compare("<", metric("cpu.used_pct"), number(5))),
		},
		{
			"clear is a label condition",
			compare(">", metric("cpu.used_pct"), number(90)),
			label("service.state", "==", "active"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := rules.AsForm(tc.trigger, tc.clear)
			if ok {
				t.Fatalf("AsForm(%+v, %+v) = %+v, true; want ok = false", tc.trigger, tc.clear, got)
			}
			if !isZeroForm(got) {
				t.Fatalf("AsForm(%+v, %+v) returned a non-zero Form alongside ok = false: %+v", tc.trigger, tc.clear, got)
			}
		})
	}
}

// TestFormCompileProducesFormatableTypeCheckedTrees answers the review
// question raised on Task 2's Format: Format's numeric branch has a
// "default: return \"\"" that is only unreachable for trees Parse builds.
// Every tree Form.Compile can produce must also be one Format renders fully
// (not empty, not a partial expression) and TypeCheck accepts — checked
// here by feeding Format's own output back through Parse, which fails on
// anything empty or partial.
func TestFormCompileProducesFormatableTypeCheckedTrees(t *testing.T) {
	tests := []rules.Form{
		{Metric: "cpu.used_pct", Op: ">", Value: 90},
		{Metric: "cpu.used_pct", Op: ">", Value: 90, ClearOp: "<", ClearValue: 70},
		{Metric: "mem.used_pct", Op: "<=", Value: 5},
		{Metric: "service.state", Op: "==", Labels: []string{"failed"}},
		{Metric: "service.state", Op: "!=", Labels: []string{"active"}},
		{Metric: "service.state", Op: "in", Labels: []string{"failed", "inactive"}},
	}
	for _, f := range tests {
		t.Run(f.Metric+" "+f.Op, func(t *testing.T) {
			trigger, clear, err := f.Compile()
			if err != nil {
				t.Fatalf("Compile(%+v) = %v, want nil", f, err)
			}
			checkFormatsFully(t, "trigger", trigger)
			if clear.Kind != "" {
				checkFormatsFully(t, "clear", clear)
			}
		})
	}
}

// checkFormatsFully asserts that Format(n) is non-empty, that TypeCheck
// accepts n directly, and that re-parsing Format(n)'s own output succeeds
// and also type-checks — the strongest evidence that Format did not emit
// an empty string or a partial expression for n.
func checkFormatsFully(t *testing.T, label string, n rules.Node) {
	t.Helper()
	if err := rules.TypeCheck(n); err != nil {
		t.Fatalf("%s: TypeCheck(%+v) = %v, want nil", label, n, err)
	}
	src := rules.Format(n)
	if src == "" {
		t.Fatalf("%s: Format(%+v) = \"\", want non-empty source", label, n)
	}
	if _, err := rules.Parse(src); err != nil {
		t.Fatalf("%s: Format(%+v) = %q, which does not re-parse: %v", label, n, src, err)
	}
}
