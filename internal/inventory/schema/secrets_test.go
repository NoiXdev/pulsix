package schema_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

func TestMapSecretsTransformsEverySecretAtItsPath(t *testing.T) {
	fields := specExample().HostFields()
	in := map[string]any{
		"vhosts": []any{
			vhost("a.io", map[string]any{"basic_auth": map[string]any{"user": "u", "password": "one"}}),
			vhost("b.io", map[string]any{"basic_auth": map[string]any{"password": "two"}}),
			"not an entry",
		},
		"orphan": map[string]any{"password": "untouched"},
	}
	var seen []string
	out, err := schema.MapSecrets(fields, in, func(path string, v any) (any, error) {
		seen = append(seen, path+"="+v.(string))
		return "X", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"vhosts[0].basic_auth.password=one", "vhosts[1].basic_auth.password=two"}
	if !slices.Equal(seen, want) {
		t.Fatalf("visited %v, want %v", seen, want)
	}
	first := out["vhosts"].([]any)[0].(map[string]any)["basic_auth"].(map[string]any)
	if first["password"] != "X" || first["user"] != "u" {
		t.Fatalf("transformed entry = %v", first)
	}
	if out["vhosts"].([]any)[2] != "not an entry" {
		t.Fatal("a malformed entry must be copied unchanged")
	}
	if out["orphan"].(map[string]any)["password"] != "untouched" {
		t.Fatal("undeclared keys must be copied unchanged")
	}
	if in["vhosts"].([]any)[0].(map[string]any)["basic_auth"].(map[string]any)["password"] != "one" {
		t.Fatal("MapSecrets must not mutate its input")
	}
	empty, err := schema.MapSecrets(fields, nil, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("nil values = %v, %v", empty, err)
	}
}

func TestMapSecretsPropagatesError(t *testing.T) {
	boom := errors.New("boom")
	fields := []schema.Field{{Name: "o", Type: schema.TypeObject, Fields: []schema.Field{{Name: "p", Type: schema.TypeSecret}}}}
	_, err := schema.MapSecrets(fields, map[string]any{"o": map[string]any{"p": "x"}}, func(string, any) (any, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	rep := []schema.Field{{Name: "r", Type: schema.TypeRepeater, Fields: []schema.Field{{Name: "p", Type: schema.TypeSecret}}}}
	_, err = schema.MapSecrets(rep, map[string]any{"r": []any{map[string]any{"p": "x"}}}, func(string, any) (any, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("repeater err = %v", err)
	}
}

func TestSecretMarkers(t *testing.T) {
	sealed := schema.Sealed([]byte("ciphertext"))
	if b, ok := schema.SealedBytes(sealed); !ok || string(b) != "ciphertext" {
		t.Fatalf("SealedBytes = %q, %v", b, ok)
	}
	for _, v := range []any{
		"plain", map[string]any{"$sealed": "!!not base64!!"},
		map[string]any{"$sealed": "YQ==", "extra": 1}, map[string]any{"$sealed": 1},
	} {
		if _, ok := schema.SealedBytes(v); ok {
			t.Errorf("SealedBytes(%v) accepted", v)
		}
	}
	if from, ok := schema.KeepFrom(map[string]any{"$secret": "keep", "from": "a[0].b"}); !ok || from != "a[0].b" {
		t.Fatalf("KeepFrom = %q, %v", from, ok)
	}
	for _, v := range []any{
		map[string]any{"$secret": "keep"}, map[string]any{"$secret": "set", "from": "a"},
		map[string]any{"$secret": "keep", "from": "a", "x": 1}, "keep",
	} {
		if _, ok := schema.KeepFrom(v); ok {
			t.Errorf("KeepFrom(%v) accepted", v)
		}
	}
	if !reflect.DeepEqual(schema.Mask(), map[string]any{"$secret": "set"}) {
		t.Fatalf("Mask = %v", schema.Mask())
	}
}

func TestMaskVarsReplacesSealedValuesAtAnyDepth(t *testing.T) {
	in := map[string]any{
		"top":    schema.Sealed([]byte{1}),
		"list":   []any{schema.Sealed([]byte{2}), "plain"},
		"nested": map[string]any{"deep": schema.Sealed([]byte{3}), "n": 1.0},
	}
	got := schema.MaskVars(in)
	want := map[string]any{
		"top":    schema.Mask(),
		"list":   []any{schema.Mask(), "plain"},
		"nested": map[string]any{"deep": schema.Mask(), "n": 1.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MaskVars = %v", got)
	}
	if _, ok := schema.SealedBytes(in["top"]); !ok {
		t.Fatal("MaskVars must not mutate its input")
	}
}

func TestLookup(t *testing.T) {
	doc := map[string]any{
		"vhosts":  []any{vhost("a.io", nil), vhost("b.io", map[string]any{"basic_auth": map[string]any{"password": "p"}})},
		"aliases": []any{"x", "y"},
		"grid":    []any{[]any{"r0c0"}},
	}
	cases := []struct {
		path string
		want any
		ok   bool
	}{
		{"vhosts[1].basic_auth.password", "p", true},
		{"vhosts[0].domain", "a.io", true},
		{"aliases[1]", "y", true},
		{"grid[0][0]", "r0c0", true},
		{"vhosts[5].domain", nil, false},
		{"vhosts.domain", nil, false},
		{"aliases[0].x", nil, false},
		{"missing", nil, false},
		{"vhosts[x]", nil, false},
		{"vhosts[-1]", nil, false},
		{"vhosts[0", nil, false},
		{"vhosts[0]x", nil, false},
		{"[0]", nil, false},
		{"", nil, false},
	}
	for _, c := range cases {
		got, ok := schema.Lookup(doc, c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Lookup(%q) = %v, %v; want %v, %v", c.path, got, ok, c.want, c.ok)
		}
	}
}
