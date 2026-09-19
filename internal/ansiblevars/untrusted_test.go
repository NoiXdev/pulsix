package ansiblevars_test

import (
	"encoding/json"
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
