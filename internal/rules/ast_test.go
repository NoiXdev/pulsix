package rules_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/noixdev/pulsix/internal/rules"
)

// TestNodeRoundTripsThroughJSON exercises every node kind through
// encoding/json: the tree stored as jsonb (a later task) must decode back
// to the exact tree that was encoded, field for field. Pos carries
// `json:"-"` and is deliberately left zero on both sides — it is the
// parser's bookkeeping, never persisted.
func TestNodeRoundTripsThroughJSON(t *testing.T) {
	tests := []struct {
		name string
		node rules.Node
	}{
		{"number literal", rules.Node{Kind: rules.KindNumber, Num: 42.5}},
		{"string literal", rules.Node{Kind: rules.KindString, Str: "hello"}},
		{"metric", rules.Node{Kind: rules.KindMetric, Name: "cpu.used_pct"}},
		{"window", rules.Node{Kind: rules.KindWindow, Name: "cpu.used_pct", Agg: "avg", Seconds: 600}},
		{"fact", rules.Node{Kind: rules.KindFact, Name: "cpu_count"}},
		{
			"arith",
			rules.Node{Kind: rules.KindArith, Op: "+", Args: []rules.Node{
				{Kind: rules.KindNumber, Num: 1},
				{Kind: rules.KindNumber, Num: 2},
			}},
		},
		{
			"compare",
			rules.Node{Kind: rules.KindCompare, Op: ">", Args: []rules.Node{
				{Kind: rules.KindMetric, Name: "cpu.used_pct"},
				{Kind: rules.KindNumber, Num: 90},
			}},
		},
		{"label equals", rules.Node{Kind: rules.KindLabel, Name: "service.state", Op: "==", Str: "active"}},
		{"label in", rules.Node{Kind: rules.KindLabel, Name: "service.state", Op: "in", Values: []string{"active", "activating"}}},
		{
			"and/or/not nested",
			rules.Node{Kind: rules.KindAnd, Args: []rules.Node{
				{Kind: rules.KindOr, Args: []rules.Node{
					{Kind: rules.KindLabel, Name: "service.state", Op: "==", Str: "failed"},
					{Kind: rules.KindLabel, Name: "service.state", Op: "==", Str: "unknown"},
				}},
				{Kind: rules.KindNot, Args: []rules.Node{
					{Kind: rules.KindCompare, Op: "==", Args: []rules.Node{
						{Kind: rules.KindNumber, Num: 1},
						{Kind: rules.KindNumber, Num: 2},
					}},
				}},
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.node)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got rules.Node
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, tc.node) {
				t.Fatalf("round trip mismatch:\n got  = %+v\n want = %+v\n json = %s", got, tc.node, b)
			}
		})
	}
}

// TestNodePosIsNeverSerialized pins Pos's `json:"-"` tag: the parser's
// source offset must not leak into the jsonb a later task persists.
func TestNodePosIsNeverSerialized(t *testing.T) {
	n := rules.Node{Kind: rules.KindNumber, Num: 1, Pos: 17}
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := raw["Pos"]; ok {
		t.Fatalf("json = %s, want no Pos field at all", b)
	}
	if _, ok := raw["pos"]; ok {
		t.Fatalf("json = %s, want no pos field at all", b)
	}
}
