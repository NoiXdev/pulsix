package triggers

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParsePathSubset(t *testing.T) {
	for _, ok := range []string{"$", "$.ref", "$.commits[*].id", "$.commits[0].id", "$['a b'].c", "$.hosts[-1]", "$[*]"} {
		if _, err := parsePath(ok); err != nil {
			t.Errorf("parsePath(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ref", "$..id", "$.commits[?@.id=='a']", "$.commits[0:2]", "$['a','b']", "$.a.length()", strings.Repeat("a", 300)} {
		if _, err := parsePath(bad); err == nil {
			t.Errorf("parsePath(%q) accepted", bad)
		}
	}
}

func TestApplyMapping(t *testing.T) {
	c, err := compileMapping(Mapping{
		ExtraVars: map[string]string{"release": "$.ref", "ids": "$.commits[*].id", "missing": "$.nope", "first": "$.commits[0]"},
		Limit:     "$.hosts[*]",
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	_ = json.Unmarshal([]byte(`{"ref":"refs/heads/main","commits":[{"id":"a"},{"id":"b"}],"hosts":["web1","web2"]}`), &doc)
	got, field, err := c.apply(doc)
	if err != nil {
		t.Fatalf("apply: %v (%s)", err, field)
	}
	if got.ExtraVars["release"] != "refs/heads/main" || !reflect.DeepEqual(got.ExtraVars["ids"], []any{"a", "b"}) {
		t.Fatalf("vars = %#v", got.ExtraVars)
	}
	if _, set := got.ExtraVars["missing"]; set {
		t.Fatal("a missing path must leave the var unset")
	}
	if !reflect.DeepEqual(got.ExtraVars["first"], map[string]any{"id": "a"}) {
		t.Fatalf("first = %#v", got.ExtraVars["first"])
	}
	if got.Limit == nil || *got.Limit != "web1,web2" {
		t.Fatalf("limit = %v", got.Limit)
	}
}

func TestApplyMappingLimitMustBeStrings(t *testing.T) {
	c, _ := compileMapping(Mapping{Limit: "$.n"})
	var doc any
	_ = json.Unmarshal([]byte(`{"n":5}`), &doc)
	if _, field, err := c.apply(doc); err == nil || field != "limit" {
		t.Fatalf("apply = %v, field %q", err, field)
	}
	empty, _ := compileMapping(Mapping{})
	if got, _, err := empty.apply(doc); err != nil || !got.Empty() {
		t.Fatalf("empty mapping = %+v, %v", got, err)
	}
}

// TestWildcardOverAnObjectIsDeterministic is backlog 2.62. A [*]
// selector over a JSON OBJECT walked the decoded map, and Go randomises
// map iteration — so the same payload produced a differently ordered
// list on every delivery. For a mapped extra var that is a list whose
// order a playbook may well act on; for the limit it is worse, because
// the parts are joined with "," and the run therefore records a
// different host pattern each time, with no way to reproduce what ran.
//
// Nodes now come back ordered by their normalized path: object keys
// lexically, array indices numerically — so an array's own order,
// which was never random, is untouched.
func TestWildcardOverAnObjectIsDeterministic(t *testing.T) {
	c, err := compileMapping(Mapping{
		ExtraVars: map[string]string{"hosts": "$.by_name[*]"},
		Limit:     "$.by_name[*]",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Enough keys that map iteration order varies between ranges with
	// near-certainty; deliberately not in lexical order in the source.
	doc := map[string]any{"by_name": map[string]any{
		"delta": "web-04", "alpha": "web-01", "echo": "web-05",
		"charlie": "web-03", "bravo": "web-02", "foxtrot": "web-06",
		"golf": "web-07", "hotel": "web-08",
	}}
	wantLimit := "web-01,web-02,web-03,web-04,web-05,web-06,web-07,web-08"

	for i := 0; i < 50; i++ {
		got, field, err := c.apply(doc)
		if err != nil {
			t.Fatalf("apply: %s %v", field, err)
		}
		if got.Limit == nil || *got.Limit != wantLimit {
			t.Fatalf("run %d: limit = %q, want %q — the same payload must always map to the same pattern", i, deref(got.Limit), wantLimit)
		}
		list, ok := got.ExtraVars["hosts"].([]any)
		if !ok {
			t.Fatalf("hosts = %T, want a list", got.ExtraVars["hosts"])
		}
		for j, want := range []string{"web-01", "web-02", "web-03", "web-04", "web-05", "web-06", "web-07", "web-08"} {
			if list[j] != want {
				t.Fatalf("run %d: hosts[%d] = %v, want %s", i, j, list[j], want)
			}
		}
	}
}

// TestWildcardOverAnArrayKeepsItsOwnOrder: an array's order is the
// payload's own and was never random. Sorting by normalized path must
// not reorder it — index 10 after index 2, not before it.
func TestWildcardOverAnArrayKeepsItsOwnOrder(t *testing.T) {
	c, err := compileMapping(Mapping{Limit: "$.hosts[*]"})
	if err != nil {
		t.Fatal(err)
	}
	hosts := make([]any, 12)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("web-%d", i)
	}
	got, field, err := c.apply(map[string]any{"hosts": hosts})
	if err != nil {
		t.Fatalf("apply: %s %v", field, err)
	}
	want := "web-0,web-1,web-2,web-3,web-4,web-5,web-6,web-7,web-8,web-9,web-10,web-11"
	if got.Limit == nil || *got.Limit != want {
		t.Fatalf("limit = %q, want %q", deref(got.Limit), want)
	}
}

// deref renders a mapped limit for a failure message; "" when unset.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
