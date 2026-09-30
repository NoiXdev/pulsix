package rules_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/rules"
)

// number builds a KindNumber leaf — the table below's tests read easier
// with a one-word constructor for the most common leaf than with the
// struct literal repeated at every use.
func number(v float64) rules.Node { return rules.Node{Kind: rules.KindNumber, Num: v} }

func metric(name string) rules.Node { return rules.Node{Kind: rules.KindMetric, Name: name} }

func window(name, agg string, seconds int) rules.Node {
	return rules.Node{Kind: rules.KindWindow, Name: name, Agg: agg, Seconds: seconds}
}

func fact(name string) rules.Node { return rules.Node{Kind: rules.KindFact, Name: name} }

func arith(op string, l, r rules.Node) rules.Node {
	return rules.Node{Kind: rules.KindArith, Op: op, Args: []rules.Node{l, r}}
}

func compare(op string, l, r rules.Node) rules.Node {
	return rules.Node{Kind: rules.KindCompare, Op: op, Args: []rules.Node{l, r}}
}

func label(name, op string, values ...string) rules.Node {
	n := rules.Node{Kind: rules.KindLabel, Name: name, Op: op}
	if op == "in" {
		n.Values = values
	} else if len(values) > 0 {
		n.Str = values[0]
	}
	return n
}

func and(args ...rules.Node) rules.Node { return rules.Node{Kind: rules.KindAnd, Args: args} }
func or(args ...rules.Node) rules.Node  { return rules.Node{Kind: rules.KindOr, Args: args} }
func not(arg rules.Node) rules.Node     { return rules.Node{Kind: rules.KindNot, Args: []rules.Node{arg}} }

// TestMatchEveryNodeKind is the first table the brief asks for: every
// comparison operator, and/or/not, arithmetic precedence, and label
// ==/!=/in, each over an Input built to make the node match or not.
func TestMatchEveryNodeKind(t *testing.T) {
	in := rules.Input{
		Metrics: map[string]float64{"cpu.used_pct": 95, "disk.free_bytes": 100},
		Labels:  map[string]string{"service.state": "active"},
		Windows: map[rules.WindowKey]float64{
			{Metric: "cpu.used_pct", Agg: "avg", Seconds: 600}: 80,
		},
		Facts: map[string]string{"cpu_count": "4"},
	}

	tests := []struct {
		name string
		node rules.Node
		want bool
	}{
		{"compare >", compare(">", metric("cpu.used_pct"), number(90)), true},
		{"compare > false", compare(">", metric("cpu.used_pct"), number(99)), false},
		{"compare >=", compare(">=", number(95), number(95)), true},
		{"compare <", compare("<", number(1), number(2)), true},
		{"compare <=", compare("<=", number(2), number(2)), true},
		{"compare ==", compare("==", number(2), number(2)), true},
		{"compare != true", compare("!=", number(1), number(2)), true},
		{"compare != false", compare("!=", number(2), number(2)), false},

		{"metric leaf via window", compare(">", window("cpu.used_pct", "avg", 600), number(50)), true},
		{"fact leaf", compare("==", fact("cpu_count"), number(4)), true},

		{"arith precedence: 2 + 3*4 = 14", compare("==", arith("+", number(2), arith("*", number(3), number(4))), number(14)), true},
		{"arith precedence: (2+3)*4 = 20", compare("==", arith("*", arith("+", number(2), number(3)), number(4)), number(20)), true},
		{"arith subtraction", compare("==", arith("-", number(5), number(3)), number(2)), true},
		{"arith division", compare("==", arith("/", number(10), number(2)), number(5)), true},

		{"label == match", label("service.state", "==", "active"), true},
		{"label == no match", label("service.state", "==", "failed"), false},
		{"label != match", label("service.state", "!=", "failed"), true},
		{"label != no match", label("service.state", "!=", "active"), false},
		{"label in match", label("service.state", "in", "failed", "active"), true},
		{"label in no match", label("service.state", "in", "failed", "unknown"), false},

		{"and both true", and(compare(">", number(2), number(1)), label("service.state", "==", "active")), true},
		{"and one false", and(compare(">", number(2), number(1)), label("service.state", "==", "failed")), false},
		{"or one true", or(label("service.state", "==", "failed"), compare(">", number(2), number(1))), true},
		{"or both false", or(label("service.state", "==", "failed"), compare("<", number(2), number(1))), false},
		{"not true->false", not(compare(">", number(1), number(2))), true},
		{"not false->true", not(compare(">", number(2), number(1))), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rules.Match(tc.node, in); got != tc.want {
				t.Fatalf("Match(%+v) = %v, want %v", tc.node, got, tc.want)
			}
		})
	}
}

// TestMatchShortCircuits proves and/or genuinely stop at the first
// deciding argument rather than evaluating every argument and only then
// combining them: the second argument here is a metric absent from Input,
// which would flip the answer to false (missing metric = non-match) if it
// were ever consulted after the first argument already decided the result.
func TestMatchShortCircuits(t *testing.T) {
	in := rules.Input{Metrics: map[string]float64{}}
	unresolvable := compare(">", metric("does.not.exist"), number(0))

	t.Run("and short-circuits on the first false", func(t *testing.T) {
		node := and(compare("==", number(1), number(2)), unresolvable)
		if got := rules.Match(node, in); got != false {
			t.Fatalf("Match = %v, want false", got)
		}
	})
	t.Run("or short-circuits on the first true", func(t *testing.T) {
		node := or(compare("==", number(1), number(1)), unresolvable)
		if got := rules.Match(node, in); got != true {
			t.Fatalf("Match = %v, want true", got)
		}
	})
}

// TestMatchNeverEscalatesWhenUnevaluable is the second table the brief
// asks for: every path that makes a tree impossible to evaluate must
// report "does not match", never true, and Match never panics or errs.
// Review focus 3 (a window with exactly one sample, rate == 0) is
// deliberately not in this table — TestMatchRateOverOneSampleIsAZeroNotAFault
// below pins the opposite: that case is a genuine match, not a non-match.
func TestMatchNeverEscalatesWhenUnevaluable(t *testing.T) {
	base := rules.Input{
		Metrics: map[string]float64{"cpu.used_pct": 95},
		Labels:  map[string]string{"service.state": "active"},
		Windows: map[rules.WindowKey]float64{
			{Metric: "disk.free_bytes", Agg: "avg", Seconds: 60}: 100,
		},
		Facts: map[string]string{"cpu_count": "4", "not_a_number": "banana"},
	}

	tests := []struct {
		name string
		node rules.Node
		in   rules.Input
	}{
		{
			"metric absent from Input",
			compare(">", metric("mem.used_pct"), number(0)),
			base,
		},
		{
			"fact absent",
			compare(">", fact("no_such_fact"), number(0)),
			base,
		},
		{
			"fact present but not numeric",
			compare(">", fact("not_a_number"), number(0)),
			base,
		},
		{
			"window key absent",
			compare(">", window("cpu.used_pct", "avg", 300), number(0)),
			base,
		},
		{
			"division by zero",
			compare(">", arith("/", number(1), number(0)), number(0)),
			base,
		},
		{
			// A numeric operator applied to a discrete metric: the
			// current value of a discrete metric lives in Labels, never
			// in Metrics, so a compare/arith reading it through a
			// KindMetric leaf never finds it there and cannot match.
			"numeric operator applied to a discrete metric",
			compare(">", metric("service.state"), number(0)),
			base,
		},
		{
			"label metric absent from Labels",
			label("no.such.metric", "==", "active"),
			base,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rules.Match(tc.node, tc.in); got != false {
				t.Fatalf("Match(%+v) = %v, want false (a tree that cannot be evaluated must never match)", tc.node, got)
			}
		})
	}
}

// TestMatchRateOverOneSampleIsAZeroNotAFault is review focus 3, restated at
// the node-evaluation level (the design doc's "window containing no sample,
// or exactly one"): the store computes rate as (last-first)/seconds and
// hands Match a single number per WindowKey, so a rate of exactly 0 (one
// sample: first == last) must compare equal to zero, not be treated as an
// absent window or a division fault — those are two different failure
// modes and this test tells them apart.
func TestMatchRateOverOneSampleIsAZeroNotAFault(t *testing.T) {
	in := rules.Input{
		Windows: map[rules.WindowKey]float64{
			{Metric: "disk.free_bytes", Agg: "rate", Seconds: 600}: 0,
		},
	}
	node := compare("==", window("disk.free_bytes", "rate", 600), number(0))
	if got := rules.Match(node, in); got != true {
		t.Fatalf("Match = %v, want true — a present, computed rate of exactly zero is a real value, not a non-match", got)
	}
}

// TestNumberOkFlag pins Number's own contract (used directly by arith and
// compare, and exposed for a later task's clear/candidate evaluation):
// ok is false exactly when some leaf under n could not be resolved.
func TestNumberOkFlag(t *testing.T) {
	in := rules.Input{
		Metrics: map[string]float64{"cpu.used_pct": 50},
		Facts:   map[string]string{"cpu_count": "4"},
	}
	tests := []struct {
		name   string
		node   rules.Node
		wantV  float64
		wantOK bool
	}{
		{"number literal", number(3), 3, true},
		{"metric present", metric("cpu.used_pct"), 50, true},
		{"metric absent", metric("mem.used_pct"), 0, false},
		{"fact numeric", fact("cpu_count"), 4, true},
		{"fact absent", fact("nope"), 0, false},
		{"arith ok", arith("+", number(1), number(2)), 3, true},
		{"arith not ok (right side missing)", arith("+", number(1), metric("missing")), 0, false},
		{"division by zero", arith("/", number(1), number(0)), 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := rules.Number(tc.node, in)
			if ok != tc.wantOK {
				t.Fatalf("Number(%+v) ok = %v, want %v", tc.node, ok, tc.wantOK)
			}
			if ok && v != tc.wantV {
				t.Fatalf("Number(%+v) = %v, want %v", tc.node, v, tc.wantV)
			}
		})
	}
}

// TestWindowKeysDeduplicates: a tree that asks for the same window
// aggregate twice (e.g. once in the trigger and once reused in an
// arithmetic expression) must ask the store to resolve it once, and a tree
// with no window node at all must return an empty, non-nil-shaped result
// with no window mentioned.
func TestWindowKeysDeduplicates(t *testing.T) {
	w1 := window("cpu.used_pct", "avg", 600)
	w2 := window("disk.free_bytes", "rate", 3600)
	tree := and(
		compare(">", w1, number(90)),
		or(compare("<", w1, number(10)), compare(">", w2, number(0))),
	)
	keys := rules.WindowKeys(tree)
	want := map[rules.WindowKey]bool{
		{Metric: "cpu.used_pct", Agg: "avg", Seconds: 600}:      true,
		{Metric: "disk.free_bytes", Agg: "rate", Seconds: 3600}: true,
	}
	if len(keys) != len(want) {
		t.Fatalf("WindowKeys = %+v, want exactly %d distinct keys", keys, len(want))
	}
	for _, k := range keys {
		if !want[k] {
			t.Fatalf("WindowKeys returned unexpected key %+v", k)
		}
		delete(want, k)
	}
	if len(want) != 0 {
		t.Fatalf("WindowKeys missing keys: %+v", want)
	}
}

func TestWindowKeysNoWindows(t *testing.T) {
	tree := compare(">", metric("cpu.used_pct"), number(90))
	if keys := rules.WindowKeys(tree); len(keys) != 0 {
		t.Fatalf("WindowKeys = %+v, want none", keys)
	}
}

// TestMatchAndNumberOnMalformedNodesNeverEscalate: TypeCheck (a later
// task's parser and API always call it before a tree is stored) refuses a
// malformed node before Match ever meets one, but Match/Number's own
// contract — "a tree that cannot be evaluated does not match" — must hold
// even so, defensively: a wrong argument count or an unrecognized
// operator must fall through to false/not-ok, never panic and never
// match.
func TestMatchAndNumberOnMalformedNodesNeverEscalate(t *testing.T) {
	in := rules.Input{Metrics: map[string]float64{"cpu.used_pct": 50}}

	matchCases := []struct {
		name string
		node rules.Node
	}{
		{"compare with one operand", rules.Node{Kind: rules.KindCompare, Op: ">", Args: []rules.Node{number(1)}}},
		{"compare with unknown operator", compare("~", number(1), number(1))},
		{"label with unknown operator", rules.Node{Kind: rules.KindLabel, Name: "service.state", Op: "~"}},
		{"not with zero operands", rules.Node{Kind: rules.KindNot, Args: nil}},
		{"not with two operands", rules.Node{Kind: rules.KindNot, Args: []rules.Node{number(1), number(2)}}},
		{"a bare literal at the root", number(1)},
	}
	for _, tc := range matchCases {
		t.Run("Match/"+tc.name, func(t *testing.T) {
			if got := rules.Match(tc.node, in); got != false {
				t.Fatalf("Match(%+v) = %v, want false", tc.node, got)
			}
		})
	}

	numberCases := []struct {
		name string
		node rules.Node
	}{
		{"arith with one operand", rules.Node{Kind: rules.KindArith, Op: "+", Args: []rules.Node{number(1)}}},
		{"arith with unknown operator", arith("%", number(1), number(1))},
		{"a boolean node in numeric position", rules.Node{Kind: rules.KindAnd}},
	}
	for _, tc := range numberCases {
		t.Run("Number/"+tc.name, func(t *testing.T) {
			if _, ok := rules.Number(tc.node, in); ok {
				t.Fatalf("Number(%+v) ok = true, want false", tc.node)
			}
		})
	}
}

// TestMatchLabelInWithNoValuesNeverMatches: an "in" label with an empty
// Values list has nothing to match against — the empty set contains
// nothing, not "anything".
func TestMatchLabelInWithNoValuesNeverMatches(t *testing.T) {
	in := rules.Input{Labels: map[string]string{"service.state": "active"}}
	node := rules.Node{Kind: rules.KindLabel, Name: "service.state", Op: "in", Values: nil}
	if got := rules.Match(node, in); got != false {
		t.Fatalf("Match = %v, want false", got)
	}
}
