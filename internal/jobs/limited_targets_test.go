package jobs_test

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
)

// TestLimitedRunTargetsResolvesUnderTheTemplateLimit: the scope is resolved
// under the template's own limit (RunTargets resolves without one), every
// zone's hosts together; a scope the limit empties is an empty list, not an
// error; any other failure surfaces.
func TestLimitedRunTargetsResolvesUnderTheTemplateLimit(t *testing.T) {
	org, templateID, broken := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	h1, h2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tmpl := &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
		templateID: {ID: templateID, OrgID: org, Limit: "web-*", Scope: json.RawMessage(`{"group_id":null,"host_ids":[]}`)},
		broken:     {ID: broken, OrgID: org, Scope: json.RawMessage(`{`)},
	}}
	res := &fakeResolver{targets: []jobs.ZoneTargets{
		{ZoneID: uuid.Must(uuid.NewV7()), HostIDs: []uuid.UUID{h1}},
		{ZoneID: uuid.Must(uuid.NewV7()), HostIDs: []uuid.UUID{h2}},
	}}
	svc := newService(newFakeStore(), &fakeQueue{}, tmpl, res, &fakeHosts{})

	got, err := svc.LimitedRunTargets(ctx, org, templateID)
	if err != nil || !slices.Equal(got, []uuid.UUID{h1, h2}) {
		t.Fatalf("LimitedRunTargets = %v, %v; want both zones' hosts", got, err)
	}
	if len(res.limits) != 1 || res.limits[0] != "web-*" {
		t.Fatalf("resolved under limits %q, want the template's own", res.limits)
	}

	res.targets = nil
	got, err = svc.LimitedRunTargets(ctx, org, templateID)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("an emptied scope = %#v, %v; want an empty list and no error", got, err)
	}

	if _, err := svc.LimitedRunTargets(ctx, org, broken); !errors.Is(err, jobs.ErrInvalidInput) {
		t.Fatalf("broken scope = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.LimitedRunTargets(ctx, org, uuid.Must(uuid.NewV7())); err == nil {
		t.Fatal("an unknown template resolved")
	}
	res.err = errors.New("inventory down")
	if _, err := svc.LimitedRunTargets(ctx, org, templateID); err == nil {
		t.Fatal("a resolver failure must surface")
	}
}

// TestLimitedRunTargetsAgainstTheRealResolver: with the real scope resolver
// a template limited to db1 targets only db1's zone, while RunTargets (the
// maintenance guard's question) still names every host of the scope; a
// limit matching nothing is an empty list.
func TestLimitedRunTargetsAgainstTheRealResolver(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-limited-targets")
	box := crypto.NewBox(testBoxKey("jobs-limited-targets-key-0123456"))
	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	credsSvc := credentials.NewService(credentials.NewSQLStore(d), box, time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(d), credsSvc, hostsSvc)
	invSvc := inventory.NewService(inventory.NewSQLStore(d), hostsSvc, sshSvc, box)

	scoped := db.WithOrgScope(ctx, org)
	zoneA, err := hostsSvc.CreateZone(scoped, org, "lt-a", "Limited A")
	if err != nil {
		t.Fatal(err)
	}
	zoneB, err := hostsSvc.CreateZone(scoped, org, "lt-b", "Limited B")
	if err != nil {
		t.Fatal(err)
	}
	web1, err := hostsSvc.CreateHost(scoped, org, hosts.HostInput{Name: "web1", Address: "10.3.0.1", ZoneID: zoneA.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	db1, err := hostsSvc.CreateHost(scoped, org, hosts.HostInput{Name: "db1", Address: "10.4.0.1", ZoneID: zoneB.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	playbookID := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(scoped, `INSERT INTO playbooks (id, org_id, inline, source) VALUES ($1, $2, $3, 'editor')`,
		playbookID, org, "- hosts: all\n  tasks: []"); err != nil {
		t.Fatal(err)
	}
	insertTemplate := func(t *testing.T, limit string) uuid.UUID {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		if _, err := d.Exec(scoped, `INSERT INTO job_templates (id, org_id, name, playbook_id, scope, "limit") VALUES ($1, $2, $3, $4, '{"group_id":null}', $5)`,
			id, org, "limited-"+id.String(), playbookID, limit); err != nil {
			t.Fatal(err)
		}
		return id
	}
	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d), box)
	svc := jobs.NewService(jobs.NewSQLStore(d), &fakeQueue{}, lookup,
		jobs.NewScopeResolver(invSvc, invSvc, hostsSvc), hostsSvc, hostsSvc, box, time.Now, rand.Reader)

	limited := insertTemplate(t, "db1")
	got, err := svc.LimitedRunTargets(scoped, org, limited)
	if err != nil || !slices.Equal(got, []uuid.UUID{db1.ID}) {
		t.Fatalf("LimitedRunTargets = %v, %v; want db1 only", got, err)
	}
	all, err := svc.RunTargets(scoped, org, limited)
	if err != nil || len(all) != 2 || !slices.Contains(all, web1.ID) || !slices.Contains(all, db1.ID) {
		t.Fatalf("RunTargets = %v, %v; want both hosts (no limit)", all, err)
	}
	got, err = svc.LimitedRunTargets(scoped, org, insertTemplate(t, "nomatch*"))
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("a limit matching nothing = %#v, %v; want an empty list", got, err)
	}
}
