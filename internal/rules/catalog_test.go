package rules_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/rules"
)

// TestCatalogueMatchesShippedPlugins pins the catalogue against the exact
// metrics v0.3b-1's four plugins emit (internal/agent/checks/{cpu,memory,
// disk,service}.go), cross-checked against the brief: an entry missing
// here is a metric no rule could ever be written against; an entry that
// doesn't match the plugin's own reality is a silently wrong type check.
func TestCatalogueMatchesShippedPlugins(t *testing.T) {
	want := map[string]rules.CatalogueEntry{
		"cpu.used_pct":         {CheckKey: "cpu", Kind: rules.MetricNumeric},
		"mem.used_pct":         {CheckKey: "memory", Kind: rules.MetricNumeric},
		"mem.available_bytes":  {CheckKey: "memory", Kind: rules.MetricNumeric},
		"swap.used_pct":        {CheckKey: "memory", Kind: rules.MetricNumeric},
		"disk.used_pct":        {CheckKey: "disk", Kind: rules.MetricNumeric},
		"disk.free_bytes":      {CheckKey: "disk", Kind: rules.MetricNumeric},
		"disk.inodes_used_pct": {CheckKey: "disk", Kind: rules.MetricNumeric},
		"service.state":        {CheckKey: "service", Kind: rules.MetricDiscrete},
	}
	got := rules.Catalogue()
	if len(got) != len(want) {
		t.Fatalf("Catalogue() has %d entries, want %d: got=%+v", len(got), len(want), got)
	}
	for name, wantEntry := range want {
		gotEntry, ok := got[name]
		if !ok {
			t.Fatalf("Catalogue() is missing %q", name)
		}
		if gotEntry != wantEntry {
			t.Fatalf("Catalogue()[%q] = %+v, want %+v", name, gotEntry, wantEntry)
		}
	}
}

// TestCatalogueIsFreshEveryCall guards the "no package-level mutable
// state" constraint: the returned map must be safe for a caller to mutate
// without corrupting a later call's result.
func TestCatalogueIsFreshEveryCall(t *testing.T) {
	a := rules.Catalogue()
	a["made.up.metric"] = rules.CatalogueEntry{CheckKey: "x", Kind: rules.MetricNumeric}
	b := rules.Catalogue()
	if _, ok := b["made.up.metric"]; ok {
		t.Fatal("mutating one Catalogue() result leaked into a later call — the catalogue must not be shared mutable state")
	}
}

func TestMinMaxWindowBounds(t *testing.T) {
	if rules.MinWindow != time.Minute {
		t.Fatalf("MinWindow = %s, want %s", rules.MinWindow, time.Minute)
	}
	if rules.MaxWindow != 24*time.Hour {
		t.Fatalf("MaxWindow = %s, want %s", rules.MaxWindow, 24*time.Hour)
	}
}

// TestTypeCheckValid confirms well-formed trees across every node kind are
// accepted — the negative tests below would be meaningless if TypeCheck
// rejected everything.
func TestTypeCheckValid(t *testing.T) {
	tests := []struct {
		name string
		node rules.Node
	}{
		{"compare of two numbers", compare(">", number(90), number(0))},
		{"compare of a metric", compare(">", metric("cpu.used_pct"), number(90))},
		{"compare of a window within bounds", compare(">", window("cpu.used_pct", "avg", 600), number(90))},
		{"compare of a fact", compare(">", fact("cpu_count"), number(0))},
		{"arith nested inside compare", compare("==", arith("+", number(1), arith("*", number(2), number(3))), number(7))},
		{"label == on a discrete metric", label("service.state", "==", "active")},
		{"label in on a discrete metric", label("service.state", "in", "active", "activating")},
		{"and of two booleans", and(compare(">", number(1), number(0)), label("service.state", "==", "active"))},
		{"or of two booleans", or(compare(">", number(1), number(0)), label("service.state", "==", "active"))},
		{"not of a boolean", not(compare(">", number(1), number(0)))},
		{"window at exactly MinWindow", compare(">", window("cpu.used_pct", "avg", 60), number(0))},
		{"window at exactly MaxWindow", compare(">", window("cpu.used_pct", "avg", 86400), number(0))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := rules.TypeCheck(tc.node); err != nil {
				t.Fatalf("TypeCheck(%+v) = %v, want nil", tc.node, err)
			}
		})
	}
}

// TestTypeCheckRejects is the brief's TypeCheck table: unknown metric,
// numeric operator on a discrete metric, window below MinWindow, window
// above MaxWindow, and a non-boolean root — each asserting the reported
// Pos, so the parser (a later task) can point straight at the offending
// source position.
func TestTypeCheckRejects(t *testing.T) {
	tests := []struct {
		name    string
		node    rules.Node
		wantPos int
	}{
		{
			"unknown metric",
			compare(">", rules.Node{Kind: rules.KindMetric, Name: "no.such.metric", Pos: 5}, number(0)),
			5,
		},
		{
			"unknown metric in a window",
			compare(">", rules.Node{Kind: rules.KindWindow, Name: "no.such.metric", Agg: "avg", Seconds: 600, Pos: 7}, number(0)),
			7,
		},
		{
			"unknown metric in a label",
			rules.Node{Kind: rules.KindLabel, Name: "no.such.metric", Op: "==", Str: "x", Pos: 9},
			9,
		},
		{
			"numeric operator on a discrete metric",
			compare(">", rules.Node{Kind: rules.KindMetric, Name: "service.state", Pos: 11}, number(0)),
			11,
		},
		{
			"label on a numeric metric",
			rules.Node{Kind: rules.KindLabel, Name: "cpu.used_pct", Op: "==", Str: "x", Pos: 13},
			13,
		},
		{
			"window below MinWindow",
			compare(">", rules.Node{Kind: rules.KindWindow, Name: "cpu.used_pct", Agg: "avg", Seconds: 30, Pos: 17}, number(0)),
			17,
		},
		{
			"window above MaxWindow",
			compare(">", rules.Node{Kind: rules.KindWindow, Name: "cpu.used_pct", Agg: "avg", Seconds: 86401, Pos: 19}, number(0)),
			19,
		},
		{
			"non-boolean root",
			rules.Node{Kind: rules.KindNumber, Num: 1, Pos: 23},
			23,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := rules.TypeCheck(tc.node)
			if err == nil {
				t.Fatalf("TypeCheck(%+v) = nil, want an error", tc.node)
			}
			if !errors.Is(err, rules.ErrInvalidRule) {
				t.Fatalf("TypeCheck error %v does not wrap ErrInvalidRule", err)
			}
			var tcErr *rules.TypeCheckError
			if !errors.As(err, &tcErr) {
				t.Fatalf("TypeCheck error %v is not a *rules.TypeCheckError", err)
			}
			if tcErr.Pos != tc.wantPos {
				t.Fatalf("TypeCheck error Pos = %d, want %d (err=%v)", tcErr.Pos, tc.wantPos, err)
			}
		})
	}
}

// TestTypeCheckErrorMessageCarriesPosAndMsg pins TypeCheckError.Error's
// rendering, since nothing above calls it directly (they only inspect the
// struct's fields).
func TestTypeCheckErrorMessageCarriesPosAndMsg(t *testing.T) {
	err := &rules.TypeCheckError{Pos: 42, Msg: "something went wrong"}
	got := err.Error()
	if !strings.Contains(got, "42") || !strings.Contains(got, "something went wrong") {
		t.Fatalf("Error() = %q, want it to mention the position 42 and the message", got)
	}
}

// TestTypeCheckRejectsMalformedNodes exercises the defensive branches
// TypeCheck carries for shapes a correct parser (Task 2) never produces
// but a hand-built or corrupted tree could: unknown operators and wrong
// argument counts at every operator node, and an empty and/or (which
// would otherwise report "the rule is invalid" for the wrong reason). All
// of these are ErrInvalidRule, same as the semantic failures above.
func TestTypeCheckRejectsMalformedNodes(t *testing.T) {
	tests := []struct {
		name string
		node rules.Node
	}{
		{"compare unknown operator", compare("~", number(1), number(1))},
		{"compare wrong arg count", rules.Node{Kind: rules.KindCompare, Op: ">", Args: []rules.Node{number(1)}}},
		{"label unknown operator", rules.Node{Kind: rules.KindLabel, Name: "service.state", Op: "~"}},
		{"and with no operands", rules.Node{Kind: rules.KindAnd}},
		{"or with no operands", rules.Node{Kind: rules.KindOr}},
		{"not wrong arg count", rules.Node{Kind: rules.KindNot, Args: []rules.Node{number(1), number(2)}}},
		{"arith unknown operator", compare("==", arith("%", number(1), number(1)), number(0))},
		{"arith wrong arg count", compare("==", rules.Node{Kind: rules.KindArith, Op: "+", Args: []rules.Node{number(1)}}, number(0))},
		{"fact with no name", compare(">", fact(""), number(0))},
		{"window unknown aggregate", compare(">", rules.Node{Kind: rules.KindWindow, Name: "cpu.used_pct", Agg: "median", Seconds: 600}, number(0))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := rules.TypeCheck(tc.node); err == nil || !errors.Is(err, rules.ErrInvalidRule) {
				t.Fatalf("TypeCheck(%+v) = %v, want an ErrInvalidRule", tc.node, err)
			}
		})
	}
}
