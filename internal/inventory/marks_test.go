package inventory_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

const planted = "{{ lookup('pipe','id') }}"

var (
	assistant = assistantchange.Actor{UserID: uuid.MustParse("00000000-0000-7000-8000-000000000001"), Assistant: true}
	person    = assistantchange.Actor{UserID: uuid.MustParse("00000000-0000-7000-8000-000000000002")}
)

func marksSchema() schema.Schema {
	return schema.Schema{Fields: []schema.Field{
		{Name: "greeting", Type: schema.TypeString},
		{Name: "owner", Type: schema.TypeString},
		{Name: "count", Type: schema.TypeInt},
		{Name: "pw", Type: schema.TypeSecret},
		{Name: "omit", Type: schema.TypeString},
		{Name: "cfg", Type: schema.TypeObject, Fields: []schema.Field{
			{Name: "a", Type: schema.TypeString},
			{Name: "__ansible_vault", Type: schema.TypeString},
		}},
		{Name: "note", Type: schema.TypeString, Scope: schema.ScopeHost},
		{Name: "vars", Type: schema.TypeString, Scope: schema.ScopeHost},
	}}
}

func (f fixture) marksGroup(t *testing.T) inventory.GroupView {
	t.Helper()
	g := f.group(t, "web", nil)
	g, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, marksSchema())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func wantKeys(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// TestAssistantGroupVarsAreMarked pins the ruling "where the mark lives"
// (decision D6): an assistant's group-vars write marks the keys it set or
// changed, keeps the marks of keys it left as they were, drops the mark of
// a key it removed; a person's save clears every mark of the group.
func TestAssistantGroupVarsAreMarked(t *testing.T) {
	f := newFixture()
	g := f.marksGroup(t)
	g, err := f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "count": 1.0, "pw": "s3cret"}, person)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after a person's save", g.AssistantKeys)

	keepPW := map[string]any{"$secret": "keep", "from": "pw"}
	g, err = f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "count": 2.0, "greeting": planted, "pw": keepPW}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after the assistant's change", g.AssistantKeys, "count", "greeting")

	g, err = f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "count": 2.0, "greeting": planted, "pw": keepPW}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after the assistant's unchanged save", g.AssistantKeys, "count", "greeting")

	g, err = f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "greeting": planted, "pw": keepPW}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after the assistant removed count", g.AssistantKeys, "greeting")

	// Renaming the group keeps the marks: no value moved.
	g, err = f.svc.UpdateGroup(ctx, f.org, g.ID, g.Revision, inventory.GroupPatch{Name: "web2"})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after a rename", g.AssistantKeys, "greeting")

	g, err = f.svc.SetVars(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "greeting": planted, "pw": keepPW})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after a person saved the same values", g.AssistantKeys)
}

// TestAssistantMembershipVarsAreMarked: the same rule for a membership's
// values, whether the membership is new or replaced.
func TestAssistantMembershipVarsAreMarked(t *testing.T) {
	f := newFixture()
	g := f.marksGroup(t)
	m, err := f.svc.AssignBy(ctx, f.org, g.ID, f.web01, map[string]any{"note": planted}, nil, assistant)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "a new membership's marks", m.AssistantKeys, "note")
	rev := m.Revision
	m, err = f.svc.AssignBy(ctx, f.org, g.ID, f.web01, map[string]any{"note": "by a person"}, &rev, person)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after a person's save", m.AssistantKeys)
	m, err = f.svc.AssignBy(ctx, f.org, g.ID, f.web01, map[string]any{"note": "by a person"}, nil, assistant)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after the assistant re-sent the person's value", m.AssistantKeys)
	views, err := f.svc.HostGroups(ctx, f.org, f.web01)
	if err != nil || len(views) != 1 {
		t.Fatalf("HostGroups = %v, %v", views, err)
	}
	wantKeys(t, "HostGroups marks", views[0].AssistantKeys)
}

// TestAssistantSchemaDefaultsAreMarked: a schema save through an assistant
// marks the fields whose default (at any depth) it set or changed, keeps
// the marks of fields still carrying a default and drops the others; a
// person's schema save clears all.
func TestAssistantSchemaDefaultsAreMarked(t *testing.T) {
	f := newFixture()
	g := f.group(t, "web", nil)
	base := []schema.Field{
		{Name: "motd", Type: schema.TypeString, Default: "a"},
		{Name: "role", Type: schema.TypeString, Scope: schema.ScopeHost, Default: "r"},
		{Name: "plain", Type: schema.TypeString},
	}
	g, _, err := f.svc.SetSchemaBy(ctx, f.org, g.ID, g.Revision, schema.Schema{Fields: base}, person)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after a person's schema", g.AssistantDefaultKeys)

	changed := slices.Clone(base)
	changed[0].Default = planted
	changed = append(changed,
		schema.Field{Name: "extra", Type: schema.TypeString, Default: "e"},
		schema.Field{Name: "obj", Type: schema.TypeObject, Fields: []schema.Field{{Name: "x", Type: schema.TypeString, Default: "{{ y }}"}}},
		schema.Field{Name: "bare", Type: schema.TypeString},
	)
	g, _, err = f.svc.SetSchemaBy(ctx, f.org, g.ID, g.Revision, schema.Schema{Fields: changed}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after the assistant's defaults", g.AssistantDefaultKeys, "extra", "motd", "obj")

	g, _, err = f.svc.SetSchemaBy(ctx, f.org, g.ID, g.Revision, schema.Schema{Fields: changed}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after the assistant's unchanged save", g.AssistantDefaultKeys, "extra", "motd", "obj")

	fewer := slices.Clone(changed[:3])
	fewer[0].Default = nil
	fewer = append(fewer, changed[4])
	g, _, err = f.svc.SetSchemaBy(ctx, f.org, g.ID, g.Revision, schema.Schema{Fields: fewer}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after removing a field and a default", g.AssistantDefaultKeys, "obj")

	g, _, err = f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, schema.Schema{Fields: fewer})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after a person saved the schema", g.AssistantDefaultKeys)
}

// TestAssistantInventoryWritesRefuseExecutionNames pins the write-time
// checks: an assistant may not set or change a value (or a schema
// default) under a name Ansible or Pulsix act on, nor a value holding an
// object key Ansible's JSON loader reads as its own marker. A person may;
// an assistant re-sending a person's value unchanged is not refused.
func TestAssistantInventoryWritesRefuseExecutionNames(t *testing.T) {
	f := newFixture()
	g := f.marksGroup(t)
	var ve *schema.ValidationError

	if _, err := f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"omit": "x"}, assistant); !errors.As(err, &ve) {
		t.Fatalf("assistant omit = %v, want a validation error", err)
	}
	problemAt(t, err0(f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"omit": "x"}, assistant)), "omit", "reserved")
	problemAt(t, err0(f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"cfg": map[string]any{"__ansible_vault": "x"}}, assistant)), "cfg", "__ansible")

	g, err := f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"omit": "x"}, person)
	if err != nil {
		t.Fatalf("a person may set omit: %v", err)
	}
	if _, err := f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"omit": "x", "greeting": "hi"}, assistant); err != nil {
		t.Fatalf("assistant keeping a person's omit unchanged: %v", err)
	}

	_, err = f.svc.AssignBy(ctx, f.org, g.ID, f.web01, map[string]any{"vars": "x"}, nil, assistant)
	problemAt(t, err, "vars", "reserved")

	h := f.group(t, "other", nil)
	_, _, err = f.svc.SetSchemaBy(ctx, f.org, h.ID, h.Revision, schema.Schema{Fields: []schema.Field{{Name: "hostvars", Type: schema.TypeString, Default: "x"}}}, assistant)
	problemAt(t, err, "fields[0].default", "reserved")
	_, _, err = f.svc.SetSchemaBy(ctx, f.org, h.ID, h.Revision, schema.Schema{Fields: []schema.Field{{Name: "cfg", Type: schema.TypeObject,
		Fields: []schema.Field{{Name: "__ansible_unsafe", Type: schema.TypeString, Default: "x"}}}}}, assistant)
	problemAt(t, err, "fields[0].default", "__ansible")
	if _, _, err := f.svc.SetSchemaBy(ctx, f.org, h.ID, h.Revision, schema.Schema{Fields: []schema.Field{{Name: "hostvars", Type: schema.TypeString}}}, assistant); err != nil {
		t.Fatalf("a field without a default carries no value: %v", err)
	}
}

func err0[T any](_ T, err error) error { return err }

// TestRendererEmitsMarkedValuesInert: the marks the service writes reach
// the renderer — a snapshot (what a run receives) and both previews carry
// an assistant's values inert and say which they are, until a person
// saves them, which makes them live.
func TestRendererEmitsMarkedValuesInert(t *testing.T) {
	f := newFixture()
	g := f.marksGroup(t)
	g, err := f.svc.SetVars(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if g, err = f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "greeting": planted}, assistant); err != nil {
		t.Fatal(err)
	}
	m, err := f.svc.AssignBy(ctx, f.org, g.ID, f.web01, map[string]any{"note": planted}, nil, assistant)
	if err != nil {
		t.Fatal(err)
	}
	r := inventory.NewRenderer(f.store, f.store.hosts, fakeSSH{zones: map[uuid.UUID]uuid.UUID{}}, fakeCreds{}, testBox())
	inert := map[string]any{"__ansible_unsafe": planted}

	snap, err := r.Snapshot(ctx, f.org, render.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	vars := snap.Doc["web"].(map[string]any)["vars"].(map[string]any)
	hv := snap.Doc["_meta"].(map[string]any)["hostvars"].(map[string]any)["web-01"].(map[string]any)
	if !reflect.DeepEqual(vars["greeting"], inert) || vars["owner"] != "ops" || !reflect.DeepEqual(hv["note"], inert) {
		t.Fatalf("snapshot group vars = %#v, web-01 = %#v", vars, hv)
	}
	want := []render.AssistantValue{{Group: "web", Key: "greeting"}, {Group: "web", Host: "web-01", Key: "note"}}
	if !reflect.DeepEqual(snap.AssistantWritten, want) {
		t.Fatalf("snapshot AssistantWritten = %+v", snap.AssistantWritten)
	}
	preview, err := r.Preview(ctx, f.org, render.Scope{})
	if err != nil || !reflect.DeepEqual(preview.AssistantWritten, want) {
		t.Fatalf("preview AssistantWritten = %+v, %v", preview.AssistantWritten, err)
	}
	hp, err := r.HostPreview(ctx, f.org, f.web01)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hp.Vars["greeting"], inert) || !slices.Equal(hp.AssistantWritten, []string{"greeting", "note"}) {
		t.Fatalf("HostPreview greeting = %#v, AssistantWritten = %v", hp.Vars["greeting"], hp.AssistantWritten)
	}

	// A person saves both, unchanged: the values are live.
	if _, err := f.svc.SetVars(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "greeting": planted}); err != nil {
		t.Fatal(err)
	}
	rev := m.Revision
	if _, err := f.svc.Assign(ctx, f.org, g.ID, f.web01, map[string]any{"note": planted}, &rev); err != nil {
		t.Fatal(err)
	}
	if snap, err = r.Snapshot(ctx, f.org, render.Scope{}); err != nil {
		t.Fatal(err)
	}
	vars = snap.Doc["web"].(map[string]any)["vars"].(map[string]any)
	hv = snap.Doc["_meta"].(map[string]any)["hostvars"].(map[string]any)["web-01"].(map[string]any)
	if vars["greeting"] != planted || hv["note"] != planted || snap.AssistantWritten != nil {
		t.Fatalf("after a person's save: group vars = %#v, web-01 = %#v, written = %+v", vars, hv, snap.AssistantWritten)
	}
	if hp, err = r.HostPreview(ctx, f.org, f.web01); err != nil || len(hp.AssistantWritten) != 0 {
		t.Fatalf("HostPreview after a person's save = %v, %v", hp.AssistantWritten, err)
	}
}
