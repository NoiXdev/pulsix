// The run request vars adapter reads what the claim reads, secrets in
// the clear, for the run request resolver's keyed vars digest.
package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
)

func TestRunRequestVarsReadWhatTheClaimReads(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	org, err := gen.New(d.Pool()).CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "app-run-request-vars", Name: "vars"})
	if err != nil {
		t.Fatal(err)
	}
	scoped := db.WithOrgScope(ctx, org.ID)
	box := crypto.NewBox([32]byte{7, 7, 7})

	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	if err := hostsSvc.EnsureDefaultZone(scoped, org.ID); err != nil {
		t.Fatal(err)
	}
	zones, err := hostsSvc.ListZones(scoped, org.ID)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones = %v %v", zones, err)
	}
	host, err := hostsSvc.CreateHost(scoped, org.ID, hosts.HostInput{Name: "web-1", Address: "10.0.0.1", ZoneID: zones[0].ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	credsSvc := credentials.NewService(credentials.NewSQLStore(d), box, time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(d), credsSvc, hostsSvc)
	invStore := inventory.NewSQLStore(d)
	invSvc := inventory.NewService(invStore, hostsSvc, sshSvc, box)
	g, err := invSvc.CreateGroup(scoped, org.ID, inventory.GroupInput{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if g, _, err = invSvc.SetSchema(scoped, org.ID, g.ID, g.Revision, schema.Schema{Fields: []schema.Field{
		{Name: "db_pass", Type: schema.TypeSecret, Scope: schema.ScopeGroup},
		{Name: "host_pass", Type: schema.TypeSecret, Scope: schema.ScopeHost},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err = invSvc.SetVars(scoped, org.ID, g.ID, g.Revision, map[string]any{"db_pass": "group-pass-3e1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := invSvc.Assign(scoped, org.ID, g.ID, host.ID, map[string]any{"host_pass": "member-pass-7a2"}, nil); err != nil {
		t.Fatal(err)
	}

	inline := "- hosts: all\n  tasks: []"
	pb, err := playbooks.NewSQLStore(d).Create(scoped, playbooks.Playbook{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Inline: &inline, Source: playbooks.SourceEditor})
	if err != nil {
		t.Fatal(err)
	}
	tmplID := uuid.Must(uuid.NewV7())
	seal := func(id uuid.UUID, plain string) map[string]any {
		t.Helper()
		ct, err := box.SealAAD([]byte(plain), templates.ExtraVarsAAD(org.ID, id))
		if err != nil {
			t.Fatal(err)
		}
		return schema.Sealed(ct)
	}
	off := false
	store := templates.NewSQLStore(d)
	if _, err := store.Create(scoped, templates.Template{
		ID: tmplID, OrgID: org.ID, PlaybookID: pb.ID, Name: "deploy",
		ExtraVars: map[string]any{
			"api_key": seal(tmplID, "extra-key-5c9"), "old_key": seal(tmplID, "inactive-key"), "token": seal(tmplID, "stray-token"),
			"undeclared": "x", "region": "eu",
		},
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{
			{Name: "api_key", Type: schema.TypeSecret},
			{Name: "old_key", Type: schema.TypeSecret, Active: &off},
			{Name: "token", Type: schema.TypeString},
			{Name: "region", Type: schema.TypeString},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	vars := NewRunRequestVars(store, inventory.NewRenderer(invStore, hostsSvc, sshSvc, credsSvc, box), box)
	extra, err := vars.ExtraVars(scoped, org.ID, tmplID)
	if err != nil {
		t.Fatal(err)
	}
	row, err := NewTemplateLookup(store, playbooks.NewSQLStore(d), box).TemplateLookup(scoped, org.ID, tmplID)
	if err != nil {
		t.Fatal(err)
	}
	var claimed map[string]any
	if err := json.Unmarshal(row.ExtraVars, &claimed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(extra, claimed) {
		t.Fatalf("extra vars = %v, want what the claim sends: %v", extra, claimed)
	}
	// The fixture really exercises the filter: the inactive, undeclared
	// and stray values are gone and the secret is opened.
	if want := map[string]any{"api_key": "extra-key-5c9", "region": "eu"}; !reflect.DeepEqual(extra, want) {
		t.Fatalf("extra vars = %v, want %v", extra, want)
	}
	if _, err := vars.ExtraVars(scoped, org.ID, uuid.Must(uuid.NewV7())); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("unknown template = %v, want templates.ErrNotFound", err)
	}

	inv, err := vars.InventoryVars(scoped, org.ID, []uuid.UUID{host.ID})
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := inv.Connections["web-1"]; !ok || c.HostID != host.ID {
		t.Fatalf("connections = %v, want web-1's", inv.Connections)
	}
	raw, err := json.Marshal(inv.Doc)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"group-pass-3e1", "member-pass-7a2"} {
		if !strings.Contains(text, secret) {
			t.Fatalf("inventory vars = %s, want %q revealed", text, secret)
		}
	}
}
