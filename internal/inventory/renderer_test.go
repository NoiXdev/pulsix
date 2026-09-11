package inventory_test

import (
	"encoding/json"
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
	// svc.Assign rejects an unmanaged host (inventory.ErrNotManaged, see
	// TestAssignValidatesAndSeals in members_test.go), so switch-01's
	// membership is written straight into the fake store — it mirrors a
	// host that lost its Managed capability after being assigned, which is
	// exactly the case the unmanaged-member warning exists for.
	f.store.members[memberKey{g.ID, f.switch01}] = inventory.Membership{OrgID: f.org, GroupID: g.ID, HostID: f.switch01, Revision: 1}
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
	// switch-01 is a member of the webserver group (see rendererFixture)
	// but not managed, so HostPreview's Scope{HostIDs: [switch-01]} puts it
	// squarely in scope: it must carry the unmanaged-member warning, same
	// as any other scoped render that would otherwise include it.
	unmanaged, err := r.HostPreview(ctx, f.org, f.switch01)
	if err != nil || unmanaged.Connection != nil || len(unmanaged.Vars) != 0 {
		t.Fatalf("an unmanaged host has no preview: %+v, %v", unmanaged, err)
	}
	if len(unmanaged.Warnings) != 1 || unmanaged.Warnings[0].Host != "switch-01" || !strings.Contains(unmanaged.Warnings[0].Message, "not managed") {
		t.Fatalf("an unmanaged host in scope must carry the warning: %+v", unmanaged.Warnings)
	}
	if _, err := r.HostPreview(ctx, f.org, uuid.Must(uuid.NewV7())); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("unknown host: %v", err)
	}
}

func ptrUUID(id uuid.UUID) *uuid.UUID { return &id }

// TestRenderFailureIsUnrenderable pins the sentinel the API maps to 409: a
// jump host chain that became cyclic behind the service's back makes every
// render fail with inventory.ErrUnrenderable naming the host.
func TestRenderFailureIsUnrenderable(t *testing.T) {
	f, _, _ := rendererFixture(t)
	a, b := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	ssh := fakeSSH{
		hostSSH: []sshconf.HostSSH{{HostID: f.web01, OrgID: f.org, Port: 22, BastionMode: sshconf.ModeExplicit, BastionID: &a}},
		bastions: []sshconf.Bastion{
			{ID: a, OrgID: f.org, Name: "a", Address: "a.example", Port: 22, Username: "ops", ViaBastionID: &b},
			{ID: b, OrgID: f.org, Name: "b", Address: "b.example", Port: 22, Username: "ops", ViaBastionID: &a},
		},
	}
	r := inventory.NewRenderer(f.store, f.store.hosts, ssh, fakeCreds{}, testBox())
	if _, err := r.Preview(ctx, f.org, render.Scope{}); !errors.Is(err, inventory.ErrUnrenderable) || !strings.Contains(err.Error(), "web-01") {
		t.Fatalf("Preview: %v", err)
	}
	if _, err := r.HostPreview(ctx, f.org, f.web01); !errors.Is(err, inventory.ErrUnrenderable) {
		t.Fatalf("HostPreview: %v", err)
	}
}

// retypeSecrets turns the webserver group's two secret fields into strings,
// as an operator editing the schema would.
func retypeSecrets(t *testing.T, f fixture, groupID uuid.UUID, group, member bool) {
	t.Helper()
	sc := webSchema()
	if group {
		sc.Fields[1].Type = schema.TypeString
	}
	if member {
		sc.Fields[3].Fields[1].Type = schema.TypeString
	}
	cur := f.store.groups[groupID]
	if _, _, err := f.svc.SetSchema(ctx, f.org, groupID, cur.Revision, sc); err != nil {
		t.Fatal(err)
	}
}

// TestSecretFieldTurnedStringNeverLeaksTheEnvelope pins the fix for sealed
// values surviving a secret field's type change: the preview masks them and
// a snapshot refuses to render rather than hand Ansible the envelope or
// silently lose the value.
func TestSecretFieldTurnedStringNeverLeaksTheEnvelope(t *testing.T) {
	f, r, g := rendererFixture(t)
	retypeSecrets(t, f, g.ID, false, true)

	preview, err := r.Preview(ctx, f.org, render.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	hv := preview.Doc["_meta"].(map[string]any)["hostvars"].(map[string]any)["web-01"].(map[string]any)
	if got := hv["vhosts"].([]any)[0].(map[string]any)["password"]; got != render.MaskedSecret {
		t.Fatalf("preview member value after secret→string = %#v", got)
	}
	_, err = r.Snapshot(ctx, f.org, render.Scope{})
	if !errors.Is(err, inventory.ErrUnrenderable) {
		t.Fatalf("snapshot with a sealed member value under a string field: %v", err)
	}
	for _, want := range []string{"webserver", "web-01", "vhosts[0].password"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("snapshot error %q does not name %s", err, want)
		}
	}

	retypeSecrets(t, f, g.ID, true, true)
	preview, err = r.Preview(ctx, f.org, render.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	if got := preview.Doc["webserver"].(map[string]any)["vars"].(map[string]any)["db_password"]; got != render.MaskedSecret {
		t.Fatalf("preview group value after secret→string = %#v", got)
	}
	raw, err := json.Marshal(preview.Doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "$sealed") {
		t.Fatalf("the preview carries a ciphertext envelope: %s", raw)
	}
	_, err = r.Snapshot(ctx, f.org, render.Scope{})
	if !errors.Is(err, inventory.ErrUnrenderable) || !strings.Contains(err.Error(), "webserver") || !strings.Contains(err.Error(), "db_password") {
		t.Fatalf("snapshot with a sealed group value under a string field: %v", err)
	}
}

// TestStringFieldTurnedSecretIsMaskedInThePreview pins the reverse change: a
// value stored in plain text before its field became a secret is masked in
// the preview like any other secret.
func TestStringFieldTurnedSecretIsMaskedInThePreview(t *testing.T) {
	f, r, g := rendererFixture(t)
	sc := webSchema()
	sc.Fields = append(sc.Fields, schema.Field{Name: "api_token", Type: schema.TypeString})
	v, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, sc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetVars(ctx, f.org, g.ID, v.Revision, map[string]any{
		"env": "prod", "db_password": map[string]any{"$secret": "keep", "from": "db_password"}, "api_token": "plain-token",
	}); err != nil {
		t.Fatal(err)
	}
	sc.Fields[len(sc.Fields)-1].Type = schema.TypeSecret
	if _, _, err := f.svc.SetSchema(ctx, f.org, g.ID, f.store.groups[g.ID].Revision, sc); err != nil {
		t.Fatal(err)
	}
	preview, err := r.Preview(ctx, f.org, render.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	if got := preview.Doc["webserver"].(map[string]any)["vars"].(map[string]any)["api_token"]; got != render.MaskedSecret {
		t.Fatalf("preview of a plain value under a secret field = %#v", got)
	}
	snap, err := r.Snapshot(ctx, f.org, render.Scope{})
	if err != nil || snap.Doc["webserver"].(map[string]any)["vars"].(map[string]any)["api_token"] != "plain-token" {
		t.Fatalf("snapshot of a plain value under a secret field: %v", err)
	}
}
