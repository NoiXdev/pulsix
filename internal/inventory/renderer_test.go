package inventory_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/sshconf"
)

func rendererFixture(t *testing.T) (fixture, *inventory.Renderer, inventory.GroupView) {
	t.Helper()
	f := newFixture()
	g := webGroup(t, f)
	g, err := f.svc.SetVars(ctx, f.org, g.ID, g.Revision, map[string]any{"env": "prod", "db_password": "group-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Assign(ctx, f.org, g.ID, f.web01, map[string]any{
		"vhosts": []any{map[string]any{"domain": "a.io", "password": "member-secret"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	key := uuid.Must(uuid.NewV7())
	ssh := fakeSSH{
		hostSSH:  []sshconf.HostSSH{{HostID: f.web01, OrgID: f.org, Port: 22, CredentialID: &key, BastionMode: sshconf.ModeInherit}},
		bastions: []sshconf.Bastion{{ID: f.bastionID, OrgID: f.org, Name: "bastion", Address: "b.example", Port: 22, Username: "ops"}},
		zones:    map[uuid.UUID]uuid.UUID{},
	}
	creds := fakeCreds{{ID: key, OrgID: f.org, Name: "key", Kind: credentials.KindSSHKey, Username: "ansible"}}
	r := inventory.NewRenderer(f.store, f.store.hosts, ssh, creds, testBox())
	return f, r, g
}

func TestPreviewMasksAndSnapshotReveals(t *testing.T) {
	f, r, g := rendererFixture(t)
	preview, err := r.Preview(ctx, f.org, render.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	web := preview.Doc["webserver"].(map[string]any)
	if web["vars"].(map[string]any)["db_password"] != render.MaskedSecret {
		t.Fatalf("preview group secret = %v", web["vars"])
	}
	hv := preview.Doc["_meta"].(map[string]any)["hostvars"].(map[string]any)["web-01"].(map[string]any)
	if hv["vhosts"].([]any)[0].(map[string]any)["password"] != render.MaskedSecret || hv["ansible_user"] != "ansible" {
		t.Fatalf("preview hostvars = %v", hv)
	}

	snap, err := r.Snapshot(ctx, f.org, render.Scope{GroupID: &g.ID})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Doc["webserver"].(map[string]any)["vars"].(map[string]any)["db_password"] != "group-secret" {
		t.Fatal("snapshot must reveal the group secret")
	}
	shv := snap.Doc["_meta"].(map[string]any)["hostvars"].(map[string]any)["web-01"].(map[string]any)
	if shv["vhosts"].([]any)[0].(map[string]any)["password"] != "member-secret" {
		t.Fatal("snapshot must reveal the membership secret")
	}

	// A ciphertext moved into another row does not open.
	other := f.group(t, "other", nil)
	stored := f.store.groups[other.ID]
	stored.Schema = schema.Schema{Fields: []schema.Field{{Name: "db_password", Type: schema.TypeSecret}}}
	stored.Vars = map[string]any{"db_password": f.store.groups[g.ID].Vars["db_password"]}
	f.store.groups[other.ID] = stored
	if _, err := r.Snapshot(ctx, f.org, render.Scope{}); err == nil || !strings.Contains(err.Error(), "open sealed") {
		t.Fatalf("a moved ciphertext must fail to open: %v", err)
	}
	if _, err := r.Preview(ctx, f.org, render.Scope{}); err != nil {
		t.Fatalf("preview never opens ciphertext: %v", err)
	}

	if _, err := r.Preview(ctx, f.org, render.Scope{GroupID: ptrUUID(uuid.Must(uuid.NewV7()))}); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("unknown scope group: %v", err)
	}
}

func TestHostPreview(t *testing.T) {
	f, r, _ := rendererFixture(t)
	hp, err := r.HostPreview(ctx, f.org, f.web01)
	if err != nil {
		t.Fatal(err)
	}
	if hp.Connection == nil || hp.Connection.BastionSource != render.SourceNone || hp.Vars["ansible_host"] != "web-01" {
		t.Fatalf("HostPreview = %+v", hp)
	}
	unmanaged, err := r.HostPreview(ctx, f.org, f.switch01)
	if err != nil || unmanaged.Connection != nil || len(unmanaged.Vars) != 0 {
		t.Fatalf("an unmanaged host has no preview: %+v, %v", unmanaged, err)
	}
	if _, err := r.HostPreview(ctx, f.org, uuid.Must(uuid.NewV7())); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("unknown host: %v", err)
	}
}

func ptrUUID(id uuid.UUID) *uuid.UUID { return &id }
