package inventory_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

func testBox() *crypto.Box {
	var key [32]byte
	copy(key[:], "inventory-test-key-0123456789abc")
	return crypto.NewBox(key)
}

type fixture struct {
	svc       *inventory.Service
	store     *fakeStore
	org       uuid.UUID
	web01     uuid.UUID // managed
	switch01  uuid.UUID // not managed
	bastionID uuid.UUID
}

func newFixture() fixture {
	f := fixture{org: uuid.Must(uuid.NewV7()), web01: uuid.Must(uuid.NewV7()), switch01: uuid.Must(uuid.NewV7()), bastionID: uuid.Must(uuid.NewV7())}
	hs := fakeHosts{
		f.web01:    {ID: f.web01, OrgID: f.org, Name: "web-01", Managed: true},
		f.switch01: {ID: f.switch01, OrgID: f.org, Name: "switch-01", Monitored: true},
	}
	f.store = newFakeStore(hs)
	bastions := fakeBastions{f.bastionID: {ID: f.bastionID, OrgID: f.org, Name: "bastion"}}
	f.svc = inventory.NewService(f.store, hs, bastions, testBox())
	return f
}

func (f fixture) group(t *testing.T, name string, parent *uuid.UUID) inventory.GroupView {
	t.Helper()
	g, err := f.svc.CreateGroup(ctx, f.org, inventory.GroupInput{Name: name, ParentID: parent})
	if err != nil {
		t.Fatalf("CreateGroup(%s): %v", name, err)
	}
	return g
}

func problemAt(t *testing.T, err error, path, substr string) {
	t.Helper()
	var ve *schema.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *schema.ValidationError", err)
	}
	for _, p := range ve.Problems {
		if p.Path == path && strings.Contains(p.Message, substr) {
			return
		}
	}
	t.Fatalf("problems %+v lack %q at %q", ve.Problems, substr, path)
}

func TestCreateGroup(t *testing.T) {
	f := newFixture()
	prod := f.group(t, "production", nil)
	if prod.Revision != 1 || len(prod.Schema.Fields) != 0 || len(prod.Vars) != 0 {
		t.Fatalf("new group = %+v", prod)
	}
	web := f.group(t, "webserver", &prod.ID)
	if web.ParentID == nil || *web.ParentID != prod.ID {
		t.Fatalf("parent = %v", web.ParentID)
	}
	unknown := uuid.Must(uuid.NewV7())
	cases := []struct {
		name string
		in   inventory.GroupInput
		want error
		msg  string
	}{
		{"reserved all", inventory.GroupInput{Name: "all"}, inventory.ErrInvalidInput, "reserved"},
		{"reserved ungrouped", inventory.GroupInput{Name: "ungrouped"}, inventory.ErrInvalidInput, "reserved"},
		{"reserved _meta", inventory.GroupInput{Name: "_meta"}, inventory.ErrInvalidInput, "reserved"},
		{"dash", inventory.GroupInput{Name: "web-servers"}, inventory.ErrInvalidInput, "letters, digits and underscores"},
		{"uppercase", inventory.GroupInput{Name: "Web"}, inventory.ErrInvalidInput, "letters, digits and underscores"},
		{"long description", inventory.GroupInput{Name: "x", Description: strings.Repeat("d", 1025)}, inventory.ErrInvalidInput, "description"},
		{"unknown parent", inventory.GroupInput{Name: "x", ParentID: &unknown}, inventory.ErrInvalidInput, "parent"},
		{"duplicate", inventory.GroupInput{Name: "webserver"}, inventory.ErrNameTaken, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.svc.CreateGroup(ctx, f.org, c.in)
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("err = %v, want %v mentioning %q", err, c.want, c.msg)
			}
		})
	}
}

func TestCreateGroupDepthLimit(t *testing.T) {
	f := newFixture()
	var parent *uuid.UUID
	for i := 0; i < inventory.MaxGroupDepth; i++ {
		g := f.group(t, fmt.Sprintf("level_%d", i), parent)
		id := g.ID
		parent = &id
	}
	_, err := f.svc.CreateGroup(ctx, f.org, inventory.GroupInput{Name: "too_deep", ParentID: parent})
	if !errors.Is(err, inventory.ErrInvalidInput) || !strings.Contains(err.Error(), "levels") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateGroup(t *testing.T) {
	f := newFixture()
	prod := f.group(t, "production", nil)
	web := f.group(t, "webserver", &prod.ID)
	php := f.group(t, "php", &web.ID)
	staging := f.group(t, "staging", nil)
	unknown := uuid.Must(uuid.NewV7())

	cases := []struct {
		name  string
		id    uuid.UUID
		rev   int64
		patch inventory.GroupPatch
		want  error
		msg   string
	}{
		{"below itself", web.ID, web.Revision, inventory.GroupPatch{Name: "webserver", ParentID: &web.ID}, inventory.ErrInvalidInput, "below itself"},
		{"below own descendant", prod.ID, prod.Revision, inventory.GroupPatch{Name: "production", ParentID: &php.ID}, inventory.ErrInvalidInput, "below itself"},
		{"unknown parent", web.ID, web.Revision, inventory.GroupPatch{Name: "webserver", ParentID: &unknown}, inventory.ErrInvalidInput, "parent"},
		{"unknown bastion", web.ID, web.Revision, inventory.GroupPatch{Name: "webserver", ParentID: &prod.ID, BastionID: &unknown}, inventory.ErrInvalidInput, "jump host"},
		{"stale revision", web.ID, web.Revision + 5, inventory.GroupPatch{Name: "webserver", ParentID: &prod.ID}, inventory.ErrRevisionMismatch, ""},
		{"unknown group", unknown, 1, inventory.GroupPatch{Name: "x"}, inventory.ErrNotFound, ""},
		// Not-found must win even when the patch also names a bastion that
		// does not exist: the group's own existence is checked first, so
		// this must not come back as ErrInvalidInput instead.
		{"unknown group with unknown bastion", unknown, 1, inventory.GroupPatch{Name: "x", BastionID: &unknown}, inventory.ErrNotFound, ""},
		{"bad name", web.ID, web.Revision, inventory.GroupPatch{Name: "all"}, inventory.ErrInvalidInput, "reserved"},
		{"rename to _meta", web.ID, web.Revision, inventory.GroupPatch{Name: "_meta"}, inventory.ErrInvalidInput, "reserved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.svc.UpdateGroup(ctx, f.org, c.id, c.rev, c.patch)
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("err = %v, want %v mentioning %q", err, c.want, c.msg)
			}
		})
	}

	moved, err := f.svc.UpdateGroup(ctx, f.org, web.ID, web.Revision, inventory.GroupPatch{
		Name: "webserver", Description: "Web tier", ParentID: &staging.ID, BastionID: &f.bastionID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if *moved.ParentID != staging.ID || moved.Revision != web.Revision+1 || *moved.BastionID != f.bastionID || moved.Description != "Web tier" {
		t.Fatalf("moved = %+v", moved)
	}
	root, err := f.svc.UpdateGroup(ctx, f.org, web.ID, moved.Revision, inventory.GroupPatch{Name: "webserver"})
	if err != nil || root.ParentID != nil || root.BastionID != nil {
		t.Fatalf("to root = %+v, %v", root, err)
	}
}

func TestUpdateGroupDepthCountsTheSubtree(t *testing.T) {
	f := newFixture()
	// A chain of MaxGroupDepth-1 levels, and a separate group with one child.
	var parent *uuid.UUID
	for i := 0; i < inventory.MaxGroupDepth-1; i++ {
		g := f.group(t, fmt.Sprintf("level_%d", i), parent)
		id := g.ID
		parent = &id
	}
	sub := f.group(t, "sub", nil)
	f.group(t, "sub_child", &sub.ID)
	_, err := f.svc.UpdateGroup(ctx, f.org, sub.ID, sub.Revision, inventory.GroupPatch{Name: "sub", ParentID: parent})
	if !errors.Is(err, inventory.ErrInvalidInput) || !strings.Contains(err.Error(), "levels") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteGroup(t *testing.T) {
	f := newFixture()
	prod := f.group(t, "production", nil)
	web := f.group(t, "webserver", &prod.ID)
	if err := f.svc.DeleteGroup(ctx, f.org, prod.ID); !errors.Is(err, inventory.ErrHasChildren) {
		t.Fatalf("delete parent: %v", err)
	}
	if err := f.svc.DeleteGroup(ctx, f.org, web.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetGroup(ctx, f.org, web.ID); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func webSchema() schema.Schema {
	return schema.Schema{Fields: []schema.Field{
		{Name: "env", Type: schema.TypeEnum, Options: []string{"prod", "staging"}, Required: true},
		{Name: "db_password", Type: schema.TypeSecret},
		{Name: "php_default", Type: schema.TypeEnum, Options: []string{"8.3", "8.4"}, Default: "8.3", Overridable: true},
		{Name: "vhosts", Type: schema.TypeRepeater, Scope: schema.ScopeHost, Required: true, Fields: []schema.Field{
			{Name: "domain", Type: schema.TypeString, Required: true},
			{Name: "password", Type: schema.TypeSecret},
		}},
	}}
}

func TestSetSchema(t *testing.T) {
	f := newFixture()
	g := f.group(t, "webserver", nil)
	bad := schema.Schema{Fields: []schema.Field{{Name: "ansible_host", Type: schema.TypeString}}}
	_, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, bad)
	problemAt(t, err, "fields[0].name", "ansible_")

	view, incomplete, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, webSchema())
	if err != nil {
		t.Fatal(err)
	}
	if incomplete != 0 || view.Revision != g.Revision+1 || len(view.Schema.Fields) != 4 {
		t.Fatalf("view = %+v, incomplete = %d", view, incomplete)
	}
	if len(view.Problems) != 1 || view.Problems[0].Path != "env" {
		t.Fatalf("a new required group field must show as a problem: %+v", view.Problems)
	}
	if _, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, webSchema()); !errors.Is(err, inventory.ErrRevisionMismatch) {
		t.Fatalf("stale revision: %v", err)
	}
	empty, _, err := f.svc.SetSchema(ctx, f.org, g.ID, view.Revision, schema.Schema{})
	if err != nil || empty.Schema.Fields == nil {
		t.Fatalf("an empty schema is stored with an empty, non-nil field list: %+v, %v", empty.Schema, err)
	}
}

// TestSetSchemaRefusesSourceFields: a group only ever stores manual fields
// (spec goal 2); a field taken from a playbook belongs to job templates.
func TestSetSchemaRefusesSourceFields(t *testing.T) {
	f := newFixture()
	g := f.group(t, "webserver", nil)
	sc := schema.Schema{Fields: []schema.Field{{
		Name: "nginx_port", Type: schema.TypeInt, Origin: schema.OriginSource,
		Source: &schema.FieldSource{Kind: schema.SourceArgumentSpec, Role: "nginx", File: "roles/nginx/meta/argument_specs.yml"},
	}}}
	_, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, sc)
	problemAt(t, err, "fields[0].origin", "only job templates take fields from a playbook")

	sc.Fields[0].Origin, sc.Fields[0].Source, sc.Fields[0].Active = "", nil, new(bool)
	view, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, sc)
	if err != nil {
		t.Fatalf("an inactive manual field: %v", err)
	}
	if view.Schema.Fields[0].IsActive() {
		t.Fatal("the stored field must stay inactive")
	}
}

// TestGroupReadMasksLeftoverPlainValueAfterSchemaChangeToSecret is
// backlog 2.21's string→secret case: SetSchema never rewrites stored
// values, so a value set while a field was still a plain string sits
// there, unsealed, after the field becomes secret-typed — until the next
// SetVars reseals it. Every read (GetGroup here) must mask it anyway,
// because the field is secret *now*.
func TestGroupReadMasksLeftoverPlainValueAfterSchemaChangeToSecret(t *testing.T) {
	f := newFixture()
	g := f.group(t, "webserver", nil)
	v, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, schema.Schema{Fields: []schema.Field{{Name: "note", Type: schema.TypeString}}})
	if err != nil {
		t.Fatal(err)
	}
	v, err = f.svc.SetVars(ctx, f.org, g.ID, v.Revision, map[string]any{"note": "still-plaintext"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Vars["note"] != "still-plaintext" {
		t.Fatalf("a plain string field must read as plain: %v", v.Vars)
	}

	v, _, err = f.svc.SetSchema(ctx, f.org, g.ID, v.Revision, schema.Schema{Fields: []schema.Field{{Name: "note", Type: schema.TypeSecret}}})
	if err != nil {
		t.Fatal(err)
	}
	if f.store.groups[g.ID].Vars["note"] != "still-plaintext" {
		t.Fatalf("SetSchema must not touch stored values: %v", f.store.groups[g.ID].Vars)
	}

	got, err := f.svc.GetGroup(ctx, f.org, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := got.Vars["note"].(map[string]any); !ok || m["$secret"] != "set" {
		t.Fatalf("a read must mask the leftover plain value now that the field is secret, got %#v", got.Vars["note"])
	}
}

func TestSetVarsSealsKeepsAndMasks(t *testing.T) {
	f := newFixture()
	g := f.group(t, "webserver", nil)
	v, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, webSchema())
	if err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.SetVars(ctx, f.org, g.ID, v.Revision, map[string]any{"env": "prod", "db_password": "hunter2"})
	if err != nil {
		t.Fatalf("SetVars: %v", err)
	}
	if view.Vars["db_password"].(map[string]any)["$secret"] != "set" || view.Vars["env"] != "prod" || len(view.Problems) != 0 {
		t.Fatalf("view = %+v", view)
	}
	stored := f.store.groups[g.ID].Vars
	raw, _ := json.Marshal(stored)
	if strings.Contains(string(raw), "hunter2") {
		t.Fatal("plaintext secret reached the store")
	}
	ct, ok := schema.SealedBytes(stored["db_password"])
	if !ok {
		t.Fatalf("stored secret = %v", stored["db_password"])
	}
	plain, err := testBox().OpenAAD(ct, inventory.GroupAAD(f.org, g.ID))
	if err != nil || string(plain) != "hunter2" {
		t.Fatalf("sealed value = %q, %v", plain, err)
	}

	kept, err := f.svc.SetVars(ctx, f.org, g.ID, view.Revision, map[string]any{"env": "staging", "db_password": map[string]any{"$secret": "keep", "from": "db_password"}})
	if err != nil {
		t.Fatalf("keep: %v", err)
	}
	if ct2, _ := schema.SealedBytes(f.store.groups[g.ID].Vars["db_password"]); string(ct2) != string(ct) {
		t.Fatal("a keep marker must keep the stored ciphertext unchanged")
	}

	cases := []struct {
		name string
		vars map[string]any
		path string
		msg  string
	}{
		{"dangling keep", map[string]any{"env": "prod", "db_password": map[string]any{"$secret": "keep", "from": "nope"}}, "db_password", "enter it again"},
		{"client-sealed value", map[string]any{"env": "prod", "db_password": schema.Sealed([]byte("forged"))}, "db_password", "cannot be written"},
		{"unknown field", map[string]any{"env": "prod", "extra": 1.0}, "extra", "unknown field"},
		{"host field on the group", map[string]any{"env": "prod", "vhosts": []any{}}, "vhosts", "unknown field"},
		{"required missing", map[string]any{}, "env", "is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.svc.SetVars(ctx, f.org, g.ID, kept.Revision, c.vars)
			problemAt(t, err, c.path, c.msg)
		})
	}
	if _, err := f.svc.SetVars(ctx, f.org, uuid.Must(uuid.NewV7()), 1, nil); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
}

func TestSchemaChangeKeepsValuesAndReportsOrphans(t *testing.T) {
	f := newFixture()
	g := f.group(t, "webserver", nil)
	v, _, _ := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, webSchema())
	v, err := f.svc.SetVars(ctx, f.org, g.ID, v.Revision, map[string]any{"env": "prod", "db_password": "x"})
	if err != nil {
		t.Fatal(err)
	}
	reduced := webSchema()
	reduced.Fields = reduced.Fields[:1] // keep env only
	view, _, err := f.svc.SetSchema(ctx, f.org, g.ID, v.Revision, reduced)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Orphaned) != 1 || view.Orphaned[0] != "db_password" {
		t.Fatalf("Orphaned = %v", view.Orphaned)
	}
	if _, ok := schema.SealedBytes(f.store.groups[g.ID].Vars["db_password"]); !ok {
		t.Fatal("a schema change must not rewrite stored values")
	}
	if view.Vars["db_password"].(map[string]any)["$secret"] != "set" {
		t.Fatal("an orphaned secret must still be masked on read")
	}
}

func TestListGroupsCountsMembers(t *testing.T) {
	f := newFixture()
	g := f.group(t, "webserver", nil)
	f.group(t, "empty", nil)
	if _, err := f.svc.Assign(ctx, f.org, g.ID, f.web01, map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	list, err := f.svc.ListGroups(ctx, f.org)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListGroups = %v, %v", list, err)
	}
	counts := map[string]int{}
	for _, s := range list {
		counts[s.Name] = s.Members
	}
	if counts["webserver"] != 1 || counts["empty"] != 0 {
		t.Fatalf("counts = %v", counts)
	}
}
