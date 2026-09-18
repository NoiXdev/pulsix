package triggers

import (
	"encoding/json"
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
