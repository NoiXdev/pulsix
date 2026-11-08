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

// TestMaskDeclaredMasksPlainValuesUnderSecretFields is backlog 2.21's
// regression from the v0.2a review: SetSchema changing a field from
// string to secret does not reseal anything already stored — the plain
// value sits there until the next SetVars/Assign. MaskVars alone (which
// only recognizes an actual {"$sealed": …} envelope) would show that
// plain value on every read until then; MaskDeclared must mask it too,
// because the field is secret-typed *now*, whether the stored value has
// caught up or not.
func TestMaskDeclaredMasksPlainValuesUnderSecretFields(t *testing.T) {
	fields := []schema.Field{
		{Name: "db_password", Type: schema.TypeSecret},
		{Name: "env", Type: schema.TypeString},
	}
	in := map[string]any{
		"db_password": "still-plaintext-from-before-the-schema-change",
		"env":         "prod",
	}
	got := schema.MaskDeclared(fields, in)
	want := map[string]any{"db_password": schema.Mask(), "env": "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MaskDeclared = %v, want %v", got, want)
	}
	if in["db_password"] != "still-plaintext-from-before-the-schema-change" {
		t.Fatal("MaskDeclared must not mutate its input")
	}

	// An actually-sealed value under a secret field is masked exactly as
	// before (MaskDeclared is a superset of MaskVars, not a replacement).
	sealed := map[string]any{"db_password": schema.Sealed([]byte{1}), "env": "prod"}
	if got := schema.MaskDeclared(fields, sealed); !reflect.DeepEqual(got, want) {
		t.Fatalf("sealed value: MaskDeclared = %v, want %v", got, want)
	}

	// An empty or absent secret value is not "set" and stays as-is.
	empty := map[string]any{"db_password": "", "env": "prod"}
	if got := schema.MaskDeclared(fields, empty); !reflect.DeepEqual(got, empty) {
		t.Fatalf("empty value: MaskDeclared = %v, want unchanged %v", got, empty)
	}
	absent := map[string]any{"env": "prod"}
	if got := schema.MaskDeclared(fields, absent); !reflect.DeepEqual(got, absent) {
		t.Fatalf("absent value: MaskDeclared = %v, want unchanged %v", got, absent)
	}

	// A sealed value that MapSecrets never visits (undeclared, or a field
	// that changed type away from secret) is still caught, same as
	// MaskVars.
	stray := map[string]any{"env": "prod", "orphan": schema.Sealed([]byte{2})}
	strayWant := map[string]any{"env": "prod", "orphan": schema.Mask()}
	if got := schema.MaskDeclared(fields, stray); !reflect.DeepEqual(got, strayWant) {
		t.Fatalf("stray sealed value: MaskDeclared = %v, want %v", got, strayWant)
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

func TestReplaceSealedReplacesAtAnyDepth(t *testing.T) {
	in := map[string]any{
		"top":    schema.Sealed([]byte{1}),
		"list":   []any{schema.Sealed([]byte{2}), "plain"},
		"nested": map[string]any{"deep": schema.Sealed([]byte{3}), "n": 1.0},
	}
	got := schema.ReplaceSealed(in, func() any { return "***" })
	want := map[string]any{"top": "***", "list": []any{"***", "plain"}, "nested": map[string]any{"deep": "***", "n": 1.0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReplaceSealed = %v", got)
	}
	if _, ok := schema.SealedBytes(in["top"]); !ok {
		t.Fatal("ReplaceSealed must not mutate its input")
	}
}

// TestStraySealed pins which sealed values a snapshot refuses: those at a
// declared path whose field is no longer secret-typed, at any depth and in
// any shape. Secret fields and undeclared (orphaned) keys are not strays.
func TestStraySealed(t *testing.T) {
	sealed := schema.Sealed([]byte("ct"))
	str := schema.Field{Name: "v", Type: schema.TypeString}
	secret := schema.Field{Name: "v", Type: schema.TypeSecret}
	leaf := func(path string) schema.StrayValue { return schema.StrayValue{Path: path} }
	container := func(path string) schema.StrayValue { return schema.StrayValue{Path: path, Container: true} }
	cases := []struct {
		name   string
		fields []schema.Field
		values map[string]any
		want   []schema.StrayValue
	}{
		{"sealed under a secret", []schema.Field{secret}, map[string]any{"v": sealed}, nil},
		{"plain under a secret", []schema.Field{secret}, map[string]any{"v": "plain"}, nil},
		{"orphaned sealed value", []schema.Field{str}, map[string]any{"gone": sealed}, nil},
		{"null value", []schema.Field{str}, map[string]any{"v": nil}, nil},
		{"sealed under a string", []schema.Field{str}, map[string]any{"v": sealed}, []schema.StrayValue{leaf("v")}},
		{"sealed under an int", []schema.Field{{Name: "v", Type: schema.TypeInt}}, map[string]any{"v": sealed}, []schema.StrayValue{leaf("v")}},
		// A container-typed field (Object/Repeater) holding a sealed
		// envelope directly, instead of the structure it declares, is a
		// container mismatch, not "this one field is no longer secret" —
		// there is no longer a well-formed field there to describe that way.
		{"sealed under an object", []schema.Field{{Name: "v", Type: schema.TypeObject, Fields: []schema.Field{str}}}, map[string]any{"v": sealed}, []schema.StrayValue{container("v")}},
		{"sealed inside a mismatched map", []schema.Field{str}, map[string]any{"v": map[string]any{"a": sealed}}, []schema.StrayValue{container("v.a")}},
		{"sealed list items", []schema.Field{{Name: "v", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString}}},
			map[string]any{"v": []any{"a", sealed, sealed}}, []schema.StrayValue{container("v[1]"), container("v[2]")}},
		{"nested field turned string", []schema.Field{{Name: "v", Type: schema.TypeRepeater, Fields: []schema.Field{
			{Name: "domain", Type: schema.TypeString}, {Name: "password", Type: schema.TypeString}, {Name: "key", Type: schema.TypeSecret},
		}}}, map[string]any{"v": []any{
			map[string]any{"domain": "a.io", "password": sealed, "key": sealed, "old": sealed},
			sealed,
		}}, []schema.StrayValue{leaf("v[0].password"), container("v[1]")}},
		{"object sub-field", []schema.Field{{Name: "o", Type: schema.TypeObject, Fields: []schema.Field{str, {Name: "s", Type: schema.TypeSecret}}}},
			map[string]any{"o": map[string]any{"v": sealed, "s": sealed}}, []schema.StrayValue{leaf("o.v")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := schema.StraySealed(c.fields, c.values); !slices.Equal(got, c.want) {
				t.Fatalf("StraySealed = %v, want %v", got, c.want)
			}
		})
	}
}

// TestStrayTopLevel names the top-level variables under which StraySealed
// finds a value, in field order and each once: a run leaves those out
// until a person enters their value again (backlog 7.87).
func TestStrayTopLevel(t *testing.T) {
	sealed := schema.Sealed([]byte("ct"))
	cases := []struct {
		name   string
		fields []schema.Field
		values map[string]any
		want   []string
	}{
		{"a secret turned string", []schema.Field{{Name: "region", Type: schema.TypeString}, {Name: "token", Type: schema.TypeString}},
			map[string]any{"region": "eu", "token": sealed}, []string{"token"}},
		{"a repeater sub-field turned string", []schema.Field{{Name: "vhosts", Type: schema.TypeRepeater, Fields: []schema.Field{
			{Name: "domain", Type: schema.TypeString}, {Name: "password", Type: schema.TypeString},
		}}}, map[string]any{"vhosts": []any{
			map[string]any{"domain": "a.io", "password": sealed},
			map[string]any{"domain": "b.io", "password": sealed},
		}}, []string{"vhosts"}},
		{"in field order", []schema.Field{{Name: "b", Type: schema.TypeString}, {Name: "a", Type: schema.TypeInt}},
			map[string]any{"a": sealed, "b": sealed}, []string{"b", "a"}},
		{"a secret field's own envelope", []schema.Field{{Name: "token", Type: schema.TypeSecret}},
			map[string]any{"token": sealed}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := schema.StrayTopLevel(c.fields, c.values); !slices.Equal(got, c.want) {
				t.Fatalf("StrayTopLevel = %v, want %v", got, c.want)
			}
		})
	}
}

// TestStrayNames pins StrayNames' own contract on hand-built input, apart
// from the scan that produces it (TestStrayTopLevel covers StraySealed): the
// top-level variable name of every stray path, in first-seen order, each
// name once, and an empty but non-nil list when there is nothing stray.
func TestStrayNames(t *testing.T) {
	cases := []struct {
		name  string
		stray []schema.StrayValue
		want  []string
	}{
		{"nothing stray", nil, []string{}},
		{"a plain name", []schema.StrayValue{{Path: "token"}}, []string{"token"}},
		{"repeater entries collapse to the repeater", []schema.StrayValue{
			{Path: "vhosts[0].password"}, {Path: "vhosts[1].password"},
		}, []string{"vhosts"}},
		{"an object sub-field is under its object", []schema.StrayValue{{Path: "db.auth.password"}}, []string{"db"}},
		{"a container value", []schema.StrayValue{{Path: "creds", Container: true}}, []string{"creds"}},
		{"first-seen order, each once", []schema.StrayValue{
			{Path: "b.x"}, {Path: "a"}, {Path: "b[2]"}, {Path: "a.y"},
		}, []string{"b", "a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := schema.StrayNames(c.stray)
			if got == nil || !slices.Equal(got, c.want) {
				t.Fatalf("StrayNames(%v) = %#v, want %#v (non-nil)", c.stray, got, c.want)
			}
		})
	}
}
