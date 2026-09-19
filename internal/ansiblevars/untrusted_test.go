package ansiblevars_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/noixdev/pulsix/internal/ansiblevars"
)

func TestInertWrapsEveryStringRecursively(t *testing.T) {
	in := map[string]any{
		"branch":   "{{ lookup('pipe', 'id') }}",
		"replicas": float64(3),
		"enabled":  true,
		"none":     nil,
		"list":     []any{"{% if 1 %}x{% endif %}", float64(1), []any{"{# c #}"}},
		"nested":   map[string]any{"ref": "refs/heads/main", "deep": map[string]any{"x": "{{ y }}"}},
	}
	got := ansiblevars.Inert(in)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"branch":{"__ansible_unsafe":"{{ lookup('pipe', 'id') }}"},"enabled":true,` +
		`"list":[{"__ansible_unsafe":"{% if 1 %}x{% endif %}"},1,[{"__ansible_unsafe":"{# c #}"}]],` +
		`"nested":{"deep":{"x":{"__ansible_unsafe":"{{ y }}"}},"ref":{"__ansible_unsafe":"refs/heads/main"}},` +
		`"none":null,"replicas":3}`
	if string(b) != want {
		t.Fatalf("Inert =\n %s\nwant\n %s", b, want)
	}
	// The input is not modified: the caller's map still holds plain strings.
	if in["branch"] != "{{ lookup('pipe', 'id') }}" {
		t.Fatalf("Inert modified its input: %v", in["branch"])
	}
	if nested := in["nested"].(map[string]any); nested["ref"] != "refs/heads/main" {
		t.Fatalf("Inert modified a nested input map: %v", nested)
	}
}

func TestInertLeavesScalarsAlone(t *testing.T) {
	for _, v := range []any{nil, true, float64(2.5), json.Number("7")} {
		if got := ansiblevars.Inert(v); got != v {
			t.Fatalf("Inert(%v) = %v", v, got)
		}
	}
	if got := ansiblevars.Inert("x"); got.(map[string]any)["__ansible_unsafe"] != "x" {
		t.Fatalf("Inert(string) = %v", got)
	}
}

func TestCheckUntrustedLimit(t *testing.T) {
	cases := []struct {
		limit string
		ok    bool
	}{
		{"", true},
		{"web", true},
		{"web-01.example.com", true},
		{"webservers:&staging:!db_01", true},
		{"web*,db*", true},
		{"all", true},
		{"@/etc/passwd", false},
		{"web,@/root/.ssh/id_ed25519", false},
		{"~web[0-9]+", false},
		{"webservers[0:2]", false},
		{"web {{ x }}", false},
		{"web servers", false},
		{"web\n@/etc/passwd", false},
		{"$HOME", false},
		{"web;id", false},
	}
	for _, c := range cases {
		err := ansiblevars.CheckUntrustedLimit(c.limit)
		if (err == nil) != c.ok {
			t.Errorf("CheckUntrustedLimit(%q) = %v, want ok=%v", c.limit, err, c.ok)
		}
	}
}

// TestCheckUntrustedKeys is final re-review New Breakage 1: a sender-chosen
// key such as "__ansible_unsafe" or "__ansible_vault" makes ansible-core's
// JSON load of the extra-vars file fail; it then re-reads the file as
// YAML, every unsafe marker becomes a plain dict and its strings are
// templated. Untrusted values may not carry any "__ansible" key, at any
// depth.
func TestCheckUntrustedKeys(t *testing.T) {
	cases := []struct {
		name string
		v    any
		ok   bool
	}{
		{"scalar", "{{ x }}", true},
		{"nil", nil, true},
		{"plain object", map[string]any{"ref": "main", "labels": map[string]any{"env": "prod"}}, true},
		{"prefix only inside a value", map[string]any{"k": "__ansible_unsafe"}, true},
		{"similar but not reserved", map[string]any{"_ansible_unsafe": "x", "ansible_unsafe": "x"}, true},
		{"top-level unsafe key", map[string]any{"__ansible_unsafe": "x"}, false},
		{"vault key", map[string]any{"__ansible_vault": "x"}, false},
		{"type key", map[string]any{"__ansible_type": "x"}, false},
		{"any __ansible prefix", map[string]any{"__ansible_future": float64(1)}, false},
		{"nested object", map[string]any{"a": map[string]any{"b": map[string]any{"__ansible_unsafe": float64(1)}}}, false},
		{"inside a list", []any{"x", []any{map[string]any{"__ansible_vault": nil}}}, false},
		{"object in list in object", map[string]any{"items": []any{map[string]any{"ok": "x"}, map[string]any{"__ansible_unsafe": []any{}}}}, false},
	}
	for _, c := range cases {
		err := ansiblevars.CheckUntrustedKeys(c.v)
		if (err == nil) != c.ok {
			t.Errorf("%s: CheckUntrustedKeys = %v, want ok=%v", c.name, err, c.ok)
		}
		if err != nil && !errors.Is(err, ansiblevars.ErrReservedKey) {
			t.Errorf("%s: error %v does not wrap ErrReservedKey", c.name, err)
		}
	}
}

// TestCheckMarkers: the extra-vars document an untrusted run sends must
// load as JSON in Ansible, or the markers on the untrusted values are lost.
// An object with an "__ansible" key is accepted only as a well-formed
// marker — {"__ansible_unsafe": "<s>"} or {"__ansible_vault": "<s>"} —
// which is what Inert produces and what an operator may write on purpose.
func TestCheckMarkers(t *testing.T) {
	cases := []struct {
		name string
		v    any
		ok   bool
	}{
		{"plain", map[string]any{"a": []any{"x", float64(1), nil, map[string]any{"b": true}}}, true},
		{"inert output", ansiblevars.Inert(map[string]any{"a": []any{"{{ x }}", map[string]any{"b": "y"}}}), true},
		{"vault marker", map[string]any{"pw": map[string]any{"__ansible_vault": "$ANSIBLE_VAULT;1.1;AES256\n30"}}, true},
		{"unsafe marker around an object", map[string]any{"a": map[string]any{"__ansible_unsafe": map[string]any{"__ansible_unsafe": "x"}}}, false},
		{"unsafe marker around a number", map[string]any{"a": map[string]any{"__ansible_unsafe": float64(1)}}, false},
		{"vault marker around null", []any{map[string]any{"__ansible_vault": nil}}, false},
		{"marker with a sibling key", map[string]any{"__ansible_unsafe": "x", "extra": "y"}, false},
		{"type key", map[string]any{"__ansible_type": "x"}, false},
		{"unknown reserved key", map[string]any{"a": map[string]any{"__ansible_other": "x"}}, false},
	}
	for _, c := range cases {
		err := ansiblevars.CheckMarkers(c.v)
		if (err == nil) != c.ok {
			t.Errorf("%s: CheckMarkers = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}
