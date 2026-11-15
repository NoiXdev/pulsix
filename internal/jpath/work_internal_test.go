package jpath

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// workBudget and the bodies are a quarter of the probe's (2 000 000 and
// 1 MiB): the bound under test is a ratio, so it holds at any scale.
const (
	workBudget = 500_000
	bodySize   = 1 << 18
	// maxOpsPerVisit is the most units of work one charged visit may
	// stand for: a node reached is also an apply or descend call, an @
	// step is a step, a filter node is its own charge.
	maxOpsPerVisit = 3
)

// workBodies are documents of about bodySize bytes, at most 64 deep, in
// the shapes that cost the subset the most.
func workBodies(t *testing.T) map[string]any {
	t.Helper()
	fill := func(prefix, unit, sep, suffix string) string {
		n := (bodySize - len(prefix) - len(suffix)) / (len(unit) + len(sep))
		return prefix + strings.Repeat(unit+sep, n-1) + unit + suffix
	}
	var wide strings.Builder
	for range 63 {
		wide.WriteString("[" + strings.Repeat("0,", 50))
	}
	wide.WriteString(fill("", "1", ",", "") + strings.Repeat("]", 64))
	chain := strings.Repeat("[", 63) + "1" + strings.Repeat("]", 63)
	ochain := strings.Repeat(`{"a":`, 63) + "1" + strings.Repeat("}", 63)
	raw := map[string]string{
		"wide":    wide.String(),
		"chains":  fill("[", chain, ",", "]"),
		"ochains": fill("[", ochain, ",", "]"),
		"flat":    fill("[", "0", ",", "]"),
		"maps":    fill("[", "{}", ",", "]"),
		"bignum":  strings.Repeat("[", 63) + "1" + strings.Repeat("0", bodySize-200) + strings.Repeat("]", 63),
	}
	docs := make(map[string]any, len(raw))
	for name, body := range raw {
		dec := json.NewDecoder(bytes.NewReader([]byte(body)))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		docs[name] = v
	}
	return docs
}

// workPaths are the subset's costliest shapes: every filter node the
// parser allows, the longest @ paths, the most segments after `..`.
func workPaths() map[string]string {
	and8 := "@!=1" + strings.Repeat("&&@!=1", 7)
	deep := "@" + strings.Repeat("[0]", 14)
	terms := make([]string, 8)
	for i := range terms {
		terms[i] = deep + "==2"
	}
	missing := make([]string, 8)
	for i := range missing {
		missing[i] = "@." + strings.Repeat("z", 50)
	}
	return map[string]string{
		"all":        "$..*",
		"compare":    "$..[?@ == 7]",
		"exists":     "$..[?@]",
		"segments":   "$..*" + strings.Repeat(".*", 63),
		"filters":    "$..*" + strings.Repeat("[?@!=1]", 62),
		"and":        "$..*" + strings.Repeat("[?"+and8+"]", 8),
		"deep terms": "$..[?" + strings.Join(terms, "||") + "]",
		"missing":    "$..[?" + strings.Join(missing, "||") + "]",
		"not chain":  "$..[?" + strings.Repeat("!(", 15) + "@" + strings.Repeat(")", 15) + "]",
		"indexes":    "$..[0]" + strings.Repeat("[0]", 62),
	}
}

// TestWorkIsBoundedByTheBudget: whatever the shape and the document, the
// work done is a small constant times the visits charged, and so at most
// that constant times the budget (N1 of the Task 2 review: a `!` chain
// used to work uncharged per candidate).
func TestWorkIsBoundedByTheBudget(t *testing.T) {
	docs := workBodies(t)
	for pname, path := range workPaths() {
		p, err := Parse(path)
		if err != nil {
			t.Fatalf("%s: Parse(%q) = %v", pname, path, err)
		}
		for dname, doc := range docs {
			e := &evaluator{left: workBudget}
			_, err := e.run(p, doc)
			charged := workBudget - max(e.left, -1)
			if e.ops > maxOpsPerVisit*charged+len(p.segs) {
				t.Errorf("%s on %s: %d units of work for %d charged visits (%v)", pname, dname, e.ops, charged, err)
			}
			if charged > workBudget+1 {
				t.Errorf("%s on %s: %d visits charged over a budget of %d", pname, dname, charged, workBudget)
			}
		}
	}
}
