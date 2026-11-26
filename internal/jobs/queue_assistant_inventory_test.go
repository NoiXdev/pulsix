package jobs_test

import (
	"crypto/rand"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
)

// TestClaimShipsAssistantInventoryValuesInert is decision D6 at the claim
// (spec 2026-11-25 assistant API gaps): a group variable and a membership
// variable an assistant wrote — here a lookup that would run a command on
// the worker — reach the worker's inventory document in the inert form, so
// Ansible keeps them as literal text. A person resending them unchanged
// leaves them so; once a person accepts them the next claim ships them
// live.
func TestClaimShipsAssistantInventoryValuesInert(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-claim-assistant-inventory")
	box := crypto.NewBox(testBoxKey("jobs-assistant-inventory-key-012"))

	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	credsSvc := credentials.NewService(credentials.NewSQLStore(d), box, time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(d), credsSvc, hostsSvc)
	invStore := inventory.NewSQLStore(d)
	invSvc := inventory.NewService(invStore, hostsSvc, sshSvc, box)
	renderer := inventory.NewRenderer(invStore, hostsSvc, sshSvc, credsSvc, box)

	scoped := db.WithOrgScope(ctx, org)
	zone, err := hostsSvc.CreateZone(scoped, org, "z1", "Zone 1")
	if err != nil {
		t.Fatal(err)
	}
	host, err := hostsSvc.CreateHost(scoped, org, hosts.HostInput{Name: "web1", Address: "10.0.0.1", ZoneID: zone.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	grp, err := invSvc.CreateGroup(scoped, org, inventory.GroupInput{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if grp, _, err = invSvc.SetSchema(scoped, org, grp.ID, grp.Revision, schema.Schema{Fields: []schema.Field{
		{Name: "banner", Type: schema.TypeString},
		{Name: "note", Type: schema.TypeString, Scope: schema.ScopeHost},
	}}); err != nil {
		t.Fatal(err)
	}
	const planted = "{{ lookup('pipe','id') }}"
	assistant := assistantchange.Actor{UserID: uuid.Must(uuid.NewV7()), Assistant: true}
	if grp, err = invSvc.SetVarsBy(scoped, org, grp.ID, grp.Revision, map[string]any{"banner": planted}, assistant); err != nil {
		t.Fatal(err)
	}
	member, err := invSvc.AssignBy(scoped, org, grp.ID, host.ID, map[string]any{"note": planted}, nil, assistant)
	if err != nil {
		t.Fatal(err)
	}

	playbookID, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := d.Exec(scoped, `INSERT INTO playbooks (id, org_id, inline, source) VALUES ($1, $2, $3, 'editor')`,
		playbookID, org, "- hosts: all\n  tasks: []"); err != nil {
		t.Fatal(err)
	}
	scopeJSON, _ := json.Marshal(map[string]any{"group_id": grp.ID.String()})
	if _, err := d.Exec(scoped, `INSERT INTO job_templates (id, org_id, name, playbook_id, scope) VALUES ($1, $2, $3, $4, $5)`,
		templateID, org, "banner", playbookID, scopeJSON); err != nil {
		t.Fatal(err)
	}
	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d), box)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
	queue.SetKeyPins(hostsSvc)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, lookup, jobs.NewScopeResolver(invSvc, invSvc, hostsSvc), hostsSvc, hostsSvc, box, time.Now, rand.Reader)

	// claimDoc enqueues a run of the template, claims it and returns the
	// group's vars and the host's vars from the shipped inventory document.
	claimDoc := func() (map[string]any, map[string]any) {
		t.Helper()
		if _, err := svc.EnqueueRun(scoped, org, templateID, nil); err != nil {
			t.Fatalf("EnqueueRun: %v", err)
		}
		claim, err := queue.Claim(ctx, org, []uuid.UUID{zone.ID}, "v1")
		if err != nil || claim == nil {
			t.Fatalf("Claim = %v, %v", claim, err)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(claim.Job.GetInventoryDoc()), &doc); err != nil {
			t.Fatalf("inventory doc: %v", err)
		}
		vars, _ := doc["web"].(map[string]any)["vars"].(map[string]any)
		hv, _ := doc["_meta"].(map[string]any)["hostvars"].(map[string]any)["web1"].(map[string]any)
		return vars, hv
	}

	inert := map[string]any{"__ansible_unsafe": planted}
	vars, hv := claimDoc()
	if !reflect.DeepEqual(vars["banner"], inert) || !reflect.DeepEqual(hv["note"], inert) {
		t.Fatalf("assistant-written values shipped as group vars %#v, host vars %#v; want both inert", vars, hv)
	}

	// A person resending the same values leaves them plain text: only a
	// change, or an explicit accept, makes an assistant's value live.
	if grp, err = invSvc.SetVars(scoped, org, grp.ID, grp.Revision, map[string]any{"banner": planted}); err != nil {
		t.Fatal(err)
	}
	vars, hv = claimDoc()
	if !reflect.DeepEqual(vars["banner"], inert) || !reflect.DeepEqual(hv["note"], inert) {
		t.Fatalf("after a person resent the value shipped group vars %#v, host vars %#v; want both still inert", vars, hv)
	}
	// A person accepts both as they are: live from the next claim on.
	if _, err := invSvc.AcceptVars(scoped, org, grp.ID, grp.Revision, []string{"banner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := invSvc.AcceptMemberVars(scoped, org, grp.ID, host.ID, member.Revision, []string{"note"}); err != nil {
		t.Fatal(err)
	}
	vars, hv = claimDoc()
	if vars["banner"] != planted || hv["note"] != planted {
		t.Fatalf("after a person's accept shipped group vars %#v, host vars %#v; want both live", vars, hv)
	}
}

// TestClaimShipsAHostAddressInert (decision D7, controller's call): an
// address that is no IP address or host name is refused at the write, and
// one stored before that rule (a legacy row) reaches the worker as plain
// text in ansible_host, never as a template Ansible would evaluate.
func TestClaimShipsAHostAddressInert(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-claim-host-address")
	box := crypto.NewBox(testBoxKey("jobs-host-address-inert-key-0123"))
	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	credsSvc := credentials.NewService(credentials.NewSQLStore(d), box, time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(d), credsSvc, hostsSvc)
	invStore := inventory.NewSQLStore(d)
	invSvc := inventory.NewService(invStore, hostsSvc, sshSvc, box)
	renderer := inventory.NewRenderer(invStore, hostsSvc, sshSvc, credsSvc, box)
	scoped := db.WithOrgScope(ctx, org)
	zone, err := hostsSvc.CreateZone(scoped, org, "z1", "Zone 1")
	if err != nil {
		t.Fatal(err)
	}
	const planted = "{{ lookup('pipe','id') }}"
	if _, err := hostsSvc.CreateHost(scoped, org, hosts.HostInput{Name: "web1", Address: planted, ZoneID: zone.ID, Managed: true}); err == nil {
		t.Fatal("a templated address was stored")
	}
	host, err := hostsSvc.CreateHost(scoped, org, hosts.HostInput{Name: "web1", Address: "10.0.0.1", ZoneID: zone.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(scoped, `UPDATE hosts SET address = $1 WHERE id = $2`, planted, host.ID); err != nil {
		t.Fatal(err)
	}
	playbookID, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := d.Exec(scoped, `INSERT INTO playbooks (id, org_id, inline, source) VALUES ($1, $2, $3, 'editor')`,
		playbookID, org, "- hosts: all\n  tasks: []"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(scoped, `INSERT INTO job_templates (id, org_id, name, playbook_id, scope) VALUES ($1, $2, $3, $4, '{}')`,
		templateID, org, "ping", playbookID); err != nil {
		t.Fatal(err)
	}
	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d), box)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
	queue.SetKeyPins(hostsSvc)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, lookup, jobs.NewScopeResolver(invSvc, invSvc, hostsSvc), hostsSvc, hostsSvc, box, time.Now, rand.Reader)
	if _, err := svc.EnqueueRun(scoped, org, templateID, nil); err != nil {
		t.Fatalf("EnqueueRun: %v", err)
	}
	claim, err := queue.Claim(ctx, org, []uuid.UUID{zone.ID}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(claim.Job.GetInventoryDoc()), &doc); err != nil {
		t.Fatal(err)
	}
	hv, _ := doc["_meta"].(map[string]any)["hostvars"].(map[string]any)["web1"].(map[string]any)
	if want := map[string]any{"__ansible_unsafe": planted}; !reflect.DeepEqual(hv["ansible_host"], want) {
		t.Fatalf("ansible_host = %#v, want %#v", hv["ansible_host"], want)
	}
}
