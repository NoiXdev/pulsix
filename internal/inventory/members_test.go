package inventory_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

func webGroup(t *testing.T, f fixture) inventory.GroupView {
	t.Helper()
	g := f.group(t, "webserver", nil)
	v, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, webSchema())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAssignValidatesAndSeals(t *testing.T) {
	f := newFixture()
	g := webGroup(t, f)

	if _, err := f.svc.Assign(ctx, f.org, g.ID, f.switch01, map[string]any{}, nil); !errors.Is(err, inventory.ErrNotManaged) {
		t.Fatalf("unmanaged host: %v", err)
	}
	if _, err := f.svc.Assign(ctx, f.org, g.ID, uuid.Must(uuid.NewV7()), map[string]any{}, nil); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("unknown host: %v", err)
	}
	if _, err := f.svc.Assign(ctx, f.org, uuid.Must(uuid.NewV7()), f.web01, map[string]any{}, nil); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
	_, err := f.svc.Assign(ctx, f.org, g.ID, f.web01, map[string]any{"vhosts": []any{map[string]any{}}}, nil)
	problemAt(t, err, "vhosts[0].domain", "is required")
	_, err = f.svc.Assign(ctx, f.org, g.ID, f.web01, map[string]any{"vhosts": []any{map[string]any{"domain": "a.io"}}, "env": "prod"}, nil)
	problemAt(t, err, "env", "unknown field") // env is group-scoped and not overridable

	m, err := f.svc.Assign(ctx, f.org, g.ID, f.web01, map[string]any{
		"php_default": "8.4",
		"vhosts": []any{
			map[string]any{"domain": "a.io", "password": "first"},
			map[string]any{"domain": "b.io", "password": "second"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if !m.Complete || m.HostName != "web-01" || m.GroupName != "webserver" || m.Revision != 1 {
		t.Fatalf("member = %+v", m)
	}
	masked := m.Vars["vhosts"].([]any)[0].(map[string]any)["password"]
	if masked.(map[string]any)["$secret"] != "set" {
		t.Fatalf("returned secret must be masked: %v", masked)
	}
	stored := f.store.members[memberKey{g.ID, f.web01}].Vars["vhosts"].([]any)
	first, _ := schema.SealedBytes(stored[0].(map[string]any)["password"])
	plain, err := testBox().OpenAAD(first, inventory.MemberAAD(f.org, g.ID, f.web01))
	if err != nil || string(plain) != "first" {
		t.Fatalf("sealed = %q, %v", plain, err)
	}

	// Reorder the entries: the moved secret follows its keep marker.
	swapped, err := f.svc.Assign(ctx, f.org, g.ID, f.web01, map[string]any{
		"vhosts": []any{
			map[string]any{"domain": "b.io", "password": map[string]any{"$secret": "keep", "from": "vhosts[1].password"}},
			map[string]any{"domain": "a.io", "password": map[string]any{"$secret": "keep", "from": "vhosts[0].password"}},
		},
	}, &m.Revision)
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	moved := f.store.members[memberKey{g.ID, f.web01}].Vars["vhosts"].([]any)
	if ct, _ := schema.SealedBytes(moved[1].(map[string]any)["password"]); string(ct) != string(first) {
		t.Fatal("the moved entry must keep its original ciphertext")
	}
	if swapped.Revision != m.Revision+1 {
		t.Fatalf("revision = %d", swapped.Revision)
	}
	if _, err := f.svc.Assign(ctx, f.org, g.ID, f.web01, map[string]any{"vhosts": []any{map[string]any{"domain": "a.io"}}}, &m.Revision); !errors.Is(err, inventory.ErrRevisionMismatch) {
		t.Fatalf("stale revision: %v", err)
	}
	other := f.group(t, "other", nil)
	one := int64(1)
	if _, err := f.svc.Assign(ctx, f.org, other.ID, f.web01, map[string]any{}, &one); !errors.Is(err, inventory.ErrRevisionMismatch) {
		t.Fatalf("revision for a membership that does not exist: %v", err)
	}
}

func TestMembersReportCompleteness(t *testing.T) {
	f := newFixture()
	g := f.group(t, "webserver", nil)
	if _, err := f.svc.Assign(ctx, f.org, g.ID, f.web01, map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	// A required host field added later makes the existing member incomplete.
	view, incomplete, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, webSchema())
	if err != nil || incomplete != 1 {
		t.Fatalf("incomplete = %d, %v", incomplete, err)
	}
	members, err := f.svc.Members(ctx, f.org, view.ID)
	if err != nil || len(members) != 1 {
		t.Fatalf("Members = %v, %v", members, err)
	}
	if members[0].Complete || len(members[0].Problems) != 1 || members[0].Problems[0].Path != "vhosts" || members[0].HostName != "web-01" {
		t.Fatalf("member = %+v", members[0])
	}
	groups, err := f.svc.HostGroups(ctx, f.org, f.web01)
	if err != nil || len(groups) != 1 || groups[0].GroupName != "webserver" || groups[0].Complete {
		t.Fatalf("HostGroups = %+v, %v", groups, err)
	}
	if _, err := f.svc.Members(ctx, f.org, uuid.Must(uuid.NewV7())); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
	if _, err := f.svc.HostGroups(ctx, f.org, uuid.Must(uuid.NewV7())); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("unknown host: %v", err)
	}
	if err := f.svc.Unassign(ctx, f.org, g.ID, f.web01); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Unassign(ctx, f.org, g.ID, f.web01); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("second Unassign: %v", err)
	}
}

// TestMemberReadMasksLeftoverPlainValueAfterSchemaChangeToSecret is
// backlog 2.21's string→secret case, for a membership instead of a group:
// a host-scoped field's value set while it was still a plain string sits
// unsealed after the field becomes secret-typed, until the member is
// saved again. Members and HostGroups must mask it anyway.
func TestMemberReadMasksLeftoverPlainValueAfterSchemaChangeToSecret(t *testing.T) {
	f := newFixture()
	g := f.group(t, "webserver", nil)
	stringSchema := schema.Schema{Fields: []schema.Field{{Name: "tag", Type: schema.TypeString, Scope: schema.ScopeHost}}}
	v, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, stringSchema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Assign(ctx, f.org, g.ID, f.web01, map[string]any{"tag": "still-plaintext"}, nil); err != nil {
		t.Fatal(err)
	}

	secretSchema := schema.Schema{Fields: []schema.Field{{Name: "tag", Type: schema.TypeSecret, Scope: schema.ScopeHost}}}
	if _, _, err := f.svc.SetSchema(ctx, f.org, g.ID, v.Revision, secretSchema); err != nil {
		t.Fatal(err)
	}

	members, err := f.svc.Members(ctx, f.org, g.ID)
	if err != nil || len(members) != 1 {
		t.Fatalf("Members = %v, %v", members, err)
	}
	if m, ok := members[0].Vars["tag"].(map[string]any); !ok || m["$secret"] != "set" {
		t.Fatalf("Members must mask the leftover plain value now that the field is secret, got %#v", members[0].Vars["tag"])
	}

	groups, err := f.svc.HostGroups(ctx, f.org, f.web01)
	if err != nil || len(groups) != 1 {
		t.Fatalf("HostGroups = %v, %v", groups, err)
	}
	if m, ok := groups[0].Vars["tag"].(map[string]any); !ok || m["$secret"] != "set" {
		t.Fatalf("HostGroups must mask it too, got %#v", groups[0].Vars["tag"])
	}
}
