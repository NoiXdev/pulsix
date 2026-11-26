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

	// A person's save makes live only what it changed: greeting resent
	// unchanged keeps its mark.
	g, err = f.svc.SetVars(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "greeting": planted, "pw": keepPW})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after a person resent the same values", g.AssistantKeys, "greeting")
	g, err = f.svc.SetVars(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "greeting": "hello", "pw": keepPW})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after a person changed greeting", g.AssistantKeys)
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

	// A person's save that leaves the default alone (a label edit) keeps
	// its mark; one that changes it makes it the person's.
	labelled := slices.Clone(fewer)
	labelled[3].Label = "Object"
	g, _, err = f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, schema.Schema{Fields: labelled})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after a person's label-only save", g.AssistantDefaultKeys, "obj")
	labelled = slices.Clone(labelled) // the fake store keeps the slice it was given
	labelled[3].Fields = []schema.Field{{Name: "x", Type: schema.TypeString, Default: "plain"}}
	g, _, err = f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, schema.Schema{Fields: labelled})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after a person changed the default", g.AssistantDefaultKeys)
}

// TestInactiveFieldKeepsItsMarkThroughAPersonsSave is review finding 1: an
// assistant's value under a field it then made inactive is not shown by
// the values form, which still resends it; that save must not make it
// live, so reactivating the field leaves it plain text.
func TestInactiveFieldKeepsItsMarkThroughAPersonsSave(t *testing.T) {
	f := newFixture()
	g := f.group(t, "web", nil)
	fields := []schema.Field{{Name: "banner", Type: schema.TypeString}, {Name: "owner", Type: schema.TypeString}}
	g, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, schema.Schema{Fields: fields})
	if err != nil {
		t.Fatal(err)
	}
	if g, err = f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"banner": planted}, assistant); err != nil {
		t.Fatal(err)
	}
	off := false
	inactive := slices.Clone(fields)
	inactive[0].Active = &off
	if g, _, err = f.svc.SetSchemaBy(ctx, f.org, g.ID, g.Revision, schema.Schema{Fields: inactive}, assistant); err != nil {
		t.Fatal(err)
	}
	// The person's form shows owner only and resends banner as stored.
	if g, err = f.svc.SetVars(ctx, f.org, g.ID, g.Revision, map[string]any{"banner": planted, "owner": "ops"}); err != nil {
		t.Fatal(err)
	}
	if g, _, err = f.svc.SetSchemaBy(ctx, f.org, g.ID, g.Revision, schema.Schema{Fields: fields}, assistant); err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "banner after the round trip", g.AssistantKeys, "banner")
	r := inventory.NewRenderer(f.store, f.store.hosts, fakeSSH{zones: map[uuid.UUID]uuid.UUID{}}, fakeCreds{}, testBox())
	if err := f.svcAssign(t, g.ID); err != nil {
		t.Fatal(err)
	}
	snap, err := r.Snapshot(ctx, f.org, render.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Doc["web"].(map[string]any)["vars"].(map[string]any)["banner"]; !reflect.DeepEqual(got, map[string]any{"__ansible_unsafe": planted}) {
		t.Fatalf("banner renders %#v, want it inert", got)
	}
}

func (f fixture) svcAssign(t *testing.T, group uuid.UUID) error {
	t.Helper()
	_, err := f.svc.Assign(ctx, f.org, group, f.web01, nil, nil)
	return err
}

// TestAcceptMakesAssistantValuesLive: a person accepts named values (or
// defaults) as live unchanged; a key that holds no assistant value is
// refused, and the revision guards the accept.
func TestAcceptMakesAssistantValuesLive(t *testing.T) {
	f := newFixture()
	g := f.marksGroup(t)
	g, err := f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"greeting": planted, "owner": "x"}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AcceptVars(ctx, f.org, g.ID, g.Revision, []string{"count"}); !errors.Is(err, inventory.ErrInvalidInput) {
		t.Fatalf("accepting an unmarked key = %v", err)
	}
	if _, err := f.svc.AcceptVars(ctx, f.org, g.ID, g.Revision, nil); !errors.Is(err, inventory.ErrInvalidInput) {
		t.Fatalf("accepting nothing = %v", err)
	}
	if _, err := f.svc.AcceptVars(ctx, f.org, g.ID, g.Revision-1, []string{"greeting"}); !errors.Is(err, inventory.ErrRevisionMismatch) {
		t.Fatalf("a stale accept = %v", err)
	}
	if g, err = f.svc.AcceptVars(ctx, f.org, g.ID, g.Revision, []string{"greeting"}); err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "after accepting greeting", g.AssistantKeys, "owner")
	if g.Vars["greeting"] != planted {
		t.Fatalf("accept changed the value: %v", g.Vars["greeting"])
	}

	sc := marksSchema()
	sc.Fields[0].Default = "hi"
	sc.Fields[1].Default = "ops"
	if g, _, err = f.svc.SetSchemaBy(ctx, f.org, g.ID, g.Revision, sc, assistant); err != nil {
		t.Fatal(err)
	}
	if g, err = f.svc.AcceptDefaults(ctx, f.org, g.ID, g.Revision, []string{"owner"}); err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "default marks after accepting owner", g.AssistantDefaultKeys, "greeting")

	m, err := f.svc.AssignBy(ctx, f.org, g.ID, f.web01, map[string]any{"note": planted}, nil, assistant)
	if err != nil {
		t.Fatal(err)
	}
	if m, err = f.svc.AcceptMemberVars(ctx, f.org, g.ID, f.web01, m.Revision, []string{"note"}); err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "member marks after accepting note", m.AssistantKeys)
	if m.HostName != "web-01" || m.GroupName != "web" {
		t.Fatalf("accepted member view = %+v", m.Membership)
	}
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
	// Review finding 5: an assistant may not add a field (or sub-field)
	// under a name Ansible or Pulsix act on, default or not.
	_, _, err = f.svc.SetSchemaBy(ctx, f.org, h.ID, h.Revision, schema.Schema{Fields: []schema.Field{{Name: "hostvars", Type: schema.TypeString}}}, assistant)
	problemAt(t, err, "fields[0].name", "reserved")
	_, _, err = f.svc.SetSchemaBy(ctx, f.org, h.ID, h.Revision, schema.Schema{Fields: []schema.Field{{Name: "cfg", Type: schema.TypeObject,
		Fields: []schema.Field{{Name: "a", Type: schema.TypeString}, {Name: "__ansible_vault", Type: schema.TypeString}}}}}, assistant)
	problemAt(t, err, "fields[0].fields[1].name", "reserved")
	// A person may; the assistant then keeps the person's field.
	h, _, err = f.svc.SetSchema(ctx, f.org, h.ID, h.Revision, schema.Schema{Fields: []schema.Field{{Name: "hostvars", Type: schema.TypeString}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.SetSchemaBy(ctx, f.org, h.ID, h.Revision, schema.Schema{Fields: []schema.Field{
		{Name: "hostvars", Type: schema.TypeString, Label: "x"}, {Name: "plain", Type: schema.TypeString},
	}}, assistant); err != nil {
		t.Fatalf("keeping a person's field: %v", err)
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

	// A person accepts both as they are: the values are live.
	if _, err := f.svc.AcceptVars(ctx, f.org, g.ID, g.Revision, []string{"greeting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AcceptMemberVars(ctx, f.org, g.ID, f.web01, m.Revision, []string{"note"}); err != nil {
		t.Fatal(err)
	}
	if snap, err = r.Snapshot(ctx, f.org, render.Scope{}); err != nil {
		t.Fatal(err)
	}
	vars = snap.Doc["web"].(map[string]any)["vars"].(map[string]any)
	hv = snap.Doc["_meta"].(map[string]any)["hostvars"].(map[string]any)["web-01"].(map[string]any)
	if vars["greeting"] != planted || hv["note"] != planted || snap.AssistantWritten != nil {
		t.Fatalf("after a person's accept: group vars = %#v, web-01 = %#v, written = %+v", vars, hv, snap.AssistantWritten)
	}
	if hp, err = r.HostPreview(ctx, f.org, f.web01); err != nil || len(hp.AssistantWritten) != 0 {
		t.Fatalf("HostPreview after a person's accept = %v, %v", hp.AssistantWritten, err)
	}
}
