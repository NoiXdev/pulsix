package jobs_test

import (
	"crypto/rand"
	"encoding/json"
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
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// TestQueueClaimShipsRealPayload is the end-to-end claim: a template with
// an inline playbook, a scope naming a managed host with an SSH credential
// behind an explicit jump host, enqueued through the real resolver and
// claimed through the real queue with the real renderer and credential
// service. The payload must carry the rendered inventory, the connection
// chain and every referenced credential's opened secret.
func TestQueueClaimShipsRealPayload(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-claim-payload-org")
	box := crypto.NewBox(testBoxKey("jobs-e2e-payload-key-0123456789ab"))

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
	hostCred, err := credsSvc.Create(scoped, org, credentials.CreateInput{
		Name: "host-login", Kind: credentials.KindSSHKey, Username: "ansible",
		Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	bastionCred, err := credsSvc.Create(scoped, org, credentials.CreateInput{
		Name: "jump-login", Kind: credentials.KindSSHPassword, Username: "jump",
		Material: credentials.Material{Password: "hunter2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := hostsSvc.CreateHost(scoped, org, hosts.HostInput{
		Name: "web1", Address: "10.0.0.1", ZoneID: zone.ID, Managed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	bastion, err := sshSvc.CreateBastion(scoped, org, sshconf.BastionInput{
		Name: "jump", Address: "10.0.0.9", Username: "jump", CredentialID: &bastionCred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	explicit := sshconf.ModeExplicit
	if _, err := sshSvc.SetHostSSH(scoped, org, host.ID, sshconf.HostSSHInput{
		CredentialID: &hostCred.ID, BastionMode: explicit, BastionID: &bastion.ID,
	}); err != nil {
		t.Fatal(err)
	}
	grp, err := invSvc.CreateGroup(scoped, org, inventory.GroupInput{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invSvc.Assign(scoped, org, grp.ID, host.ID, nil, nil); err != nil {
		t.Fatal(err)
	}

	// The template and its playbook: rows inserted directly — their
	// services arrive with Task 7.
	playbookID, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := d.Exec(scoped,
		`INSERT INTO playbooks (id, org_id, inline, source) VALUES ($1, $2, $3, 'editor')`,
		playbookID, org, "- hosts: all\n  tasks: []"); err != nil {
		t.Fatal(err)
	}
	scopeJSON, _ := json.Marshal(map[string]any{"group_id": grp.ID.String()})
	if _, err := d.Exec(scoped,
		`INSERT INTO job_templates (id, org_id, name, playbook_id, scope, "limit", extra_vars, credential_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		templateID, org, "smoke", playbookID, scopeJSON, "web1", []byte(`{"run":"smoke"}`), hostCred.ID); err != nil {
		t.Fatal(err)
	}

	templates := jobs.NewSQLTemplates(d)
	resolver := jobs.NewScopeResolver(invSvc, invSvc, hostsSvc)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, templates, sshSvc, time.Now, rand.Reader)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, templates, resolver, hostsSvc, box, time.Now, rand.Reader)

	created, err := svc.EnqueueRun(scoped, org, templateID, nil)
	if err != nil {
		t.Fatalf("EnqueueRun: %v", err)
	}
	if len(created) != 1 || created[0].ZoneID != zone.ID || len(created[0].HostIDs) != 1 || created[0].HostIDs[0] != host.ID {
		t.Fatalf("enqueued = %+v", created)
	}

	claim, err := queue.Claim(ctx, []uuid.UUID{zone.ID}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	j := &claim.Job
	if j.GetKind() != "run" || j.GetOrgId() != org.String() || j.GetZoneId() != zone.ID.String() {
		t.Fatalf("identity = %+v", j)
	}
	if j.GetTemplateId() != templateID.String() || j.GetPlaybook() != "- hosts: all\n  tasks: []" || j.GetLimit() != "web1" {
		t.Fatalf("template fields = %+v", j)
	}
	if j.GetSshUsername() != "ansible" || j.GetExtraVars()["run"] != `"smoke"` {
		t.Fatalf("user/extra vars = %q, %v", j.GetSshUsername(), j.GetExtraVars())
	}
	if !strings.Contains(j.GetInventoryDoc(), "web1") || !strings.Contains(j.GetInventoryDoc(), "10.0.0.1") {
		t.Fatalf("inventory doc = %q", j.GetInventoryDoc())
	}
	var conn struct {
		HostID       uuid.UUID   `json:"host_id"`
		CredentialID *uuid.UUID  `json:"credential_id"`
		Bastions     []uuid.UUID `json:"bastions"`
	}
	if err := json.Unmarshal([]byte(j.GetConnections()["web1"]), &conn); err != nil {
		t.Fatalf("connection: %v", err)
	}
	if conn.CredentialID == nil || *conn.CredentialID != hostCred.ID || len(conn.Bastions) != 1 || conn.Bastions[0] != bastion.ID {
		t.Fatalf("connection = %+v", conn)
	}
	// Both the host's and the bastion's credentials must ship, opened.
	if len(j.GetCredentials()) != 2 {
		t.Fatalf("credentials = %v", j.GetCredentials())
	}
	var hostSecret, bastionSecret credentials.Secret
	if err := json.Unmarshal([]byte(j.GetCredentials()[hostCred.ID.String()]), &hostSecret); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(j.GetCredentials()[bastionCred.ID.String()]), &bastionSecret); err != nil {
		t.Fatal(err)
	}
	if hostSecret.PrivateKey == "" || bastionSecret.Password != "hunter2" {
		t.Fatalf("secrets = %+v / %+v", hostSecret, bastionSecret)
	}
	if len(j.GetHostKeys()) != 0 {
		t.Fatalf("host keys must be empty until Task 10")
	}

	// The queue is drained.
	if second, err := queue.Claim(ctx, []uuid.UUID{zone.ID}, "v1"); err != nil || second != nil {
		t.Fatalf("second claim = %v, %v", second, err)
	}
}

// TestQueueClaimFailsUnbuildableJobs covers the poison-job path: a run job
// whose template scope is broken fails with the reason instead of
// blocking the queue, and the next claim reports empty.
func TestQueueClaimFailsUnbuildableJobs(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-claim-broken-org")
	box := crypto.NewBox(testBoxKey("jobs-broken-claim-key-0123456789ab"))
	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	credsSvc := credentials.NewService(credentials.NewSQLStore(d), box, time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(d), credsSvc, hostsSvc)
	invStore := inventory.NewSQLStore(d)
	renderer := inventory.NewRenderer(invStore, hostsSvc, sshSvc, credsSvc, box)

	scoped := db.WithOrgScope(ctx, org)
	playbookID, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := d.Exec(scoped,
		`INSERT INTO playbooks (id, org_id, inline, source) VALUES ($1, $2, $3, 'editor')`,
		playbookID, org, "- hosts: all"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(scoped,
		`INSERT INTO job_templates (id, org_id, name, playbook_id, scope) VALUES ($1, $2, $3, $4, $5)`,
		templateID, org, "broken", playbookID, []byte(`{"group_id":"not-a-uuid"}`)); err != nil {
		t.Fatal(err)
	}

	templates := jobs.NewSQLTemplates(d)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, templates, sshSvc, time.Now, rand.Reader)
	zone := uuid.Must(uuid.NewV7())
	j := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	j.TemplateID = &templateID
	if err := jobs.NewSQLStore(d).CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}

	claim, err := queue.Claim(ctx, []uuid.UUID{zone}, "v1")
	if err != nil || claim != nil {
		t.Fatalf("Claim = %v, %v, want an empty queue", claim, err)
	}
	got, err := jobs.NewSQLStore(d).GetJob(scoped, org, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != jobs.StatusError || got.FinishedAt == nil || got.Error == "" {
		t.Fatalf("unbuildable job = %+v", got)
	}
}
