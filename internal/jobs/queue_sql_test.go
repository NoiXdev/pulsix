package jobs_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/shared"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
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

	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d), box)
	resolver := jobs.NewScopeResolver(invSvc, invSvc, hostsSvc)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
	queue.SetKeyPins(hostsSvc)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, lookup, resolver, hostsSvc, hostsSvc, box, time.Now, rand.Reader)

	created, err := svc.EnqueueRun(scoped, org, templateID, nil)
	if err != nil {
		t.Fatalf("EnqueueRun: %v", err)
	}
	if len(created) != 1 || created[0].ZoneID != zone.ID || len(created[0].HostIDs) != 1 || created[0].HostIDs[0] != host.ID {
		t.Fatalf("enqueued = %+v", created)
	}

	claim, err := queue.Claim(ctx, org, []uuid.UUID{zone.ID}, "v1")
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
	if second, err := queue.Claim(ctx, org, []uuid.UUID{zone.ID}, "v1"); err != nil || second != nil {
		t.Fatalf("second claim = %v, %v", second, err)
	}
}

// TestEnqueueRunGatesZonesByTemplateLimit runs the per-zone limit gate
// (backlog 2.45) through the real stack: a template whose limit matches
// only one zone's hosts gets a job for that zone only, the job keeps the
// zone's full host list (the limit is NOT narrowed at enqueue — it ships on
// the claim for ansible to apply), a limit matching nothing anywhere fails
// the enqueue with ErrEmptyScope, and an undecidable pattern keeps both
// zones' jobs.
func TestEnqueueRunGatesZonesByTemplateLimit(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-limit-gate-org")
	box := crypto.NewBox(testBoxKey("jobs-e2e-limit-key-0123456789012"))

	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	credsSvc := credentials.NewService(credentials.NewSQLStore(d), box, time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(d), credsSvc, hostsSvc)
	invSvc := inventory.NewService(inventory.NewSQLStore(d), hostsSvc, sshSvc, box)

	scoped := db.WithOrgScope(ctx, org)
	zoneA, err := hostsSvc.CreateZone(scoped, org, "lim-a", "Limit zone A")
	if err != nil {
		t.Fatal(err)
	}
	zoneB, err := hostsSvc.CreateZone(scoped, org, "lim-b", "Limit zone B")
	if err != nil {
		t.Fatal(err)
	}
	web1, err := hostsSvc.CreateHost(scoped, org, hosts.HostInput{Name: "web1", Address: "10.1.0.1", ZoneID: zoneA.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	db1, err := hostsSvc.CreateHost(scoped, org, hosts.HostInput{Name: "db1", Address: "10.2.0.1", ZoneID: zoneB.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}

	playbookID := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(scoped,
		`INSERT INTO playbooks (id, org_id, inline, source) VALUES ($1, $2, $3, 'editor')`,
		playbookID, org, "- hosts: all\n  tasks: []"); err != nil {
		t.Fatal(err)
	}
	insertTemplate := func(t *testing.T, limit string) uuid.UUID {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		scopeJSON, _ := json.Marshal(map[string]any{"group_id": nil})
		if _, err := d.Exec(scoped,
			`INSERT INTO job_templates (id, org_id, name, playbook_id, scope, "limit")
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			id, org, "gated-"+id.String(), playbookID, scopeJSON, limit); err != nil {
			t.Fatal(err)
		}
		return id
	}

	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d), box)
	svc := jobs.NewService(
		jobs.NewSQLStore(d), &fakeQueue{}, lookup,
		jobs.NewScopeResolver(invSvc, invSvc, hostsSvc), hostsSvc, hostsSvc, box, time.Now, rand.Reader,
	)

	t.Run("zone without matches gets no job", func(t *testing.T) {
		created, err := svc.EnqueueRun(scoped, org, insertTemplate(t, "db1"), nil)
		if err != nil {
			t.Fatalf("EnqueueRun: %v", err)
		}
		if len(created) != 1 || created[0].ZoneID != zoneB.ID {
			t.Fatalf("jobs = %+v, want one job for zone B", created)
		}
		// The gate never narrows the host list.
		if len(created[0].HostIDs) != 1 || created[0].HostIDs[0] != db1.ID {
			t.Fatalf("hosts = %v, want the zone's full set", created[0].HostIDs)
		}
	})

	t.Run("limit matching nothing is ErrEmptyScope", func(t *testing.T) {
		if _, err := svc.EnqueueRun(scoped, org, insertTemplate(t, "nomatch*"), nil); !errors.Is(err, jobs.ErrEmptyScope) {
			t.Fatalf("err = %v, want ErrEmptyScope", err)
		}
	})

	t.Run("undecidable pattern keeps every zone", func(t *testing.T) {
		created, err := svc.EnqueueRun(scoped, org, insertTemplate(t, "~web.*"), nil)
		if err != nil {
			t.Fatalf("EnqueueRun: %v", err)
		}
		if len(created) != 2 {
			t.Fatalf("jobs = %d, want one per zone", len(created))
		}
		for _, j := range created {
			if j.ZoneID != zoneA.ID && j.ZoneID != zoneB.ID {
				t.Fatalf("job zone = %v", j.ZoneID)
			}
			if j.ZoneID == zoneA.ID && (len(j.HostIDs) != 1 || j.HostIDs[0] != web1.ID) {
				t.Fatalf("zone A hosts = %v", j.HostIDs)
			}
		}
	})
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

	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d), box)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
	queue.SetKeyPins(hostsSvc)
	zone := seedZone(t, d, org)
	j := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	j.TemplateID = &templateID
	if err := jobs.NewSQLStore(d).CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}

	claim, err := queue.Claim(ctx, org, []uuid.UUID{zone}, "v1")
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

// sinkCall is one recorded SyncSink observation.
type sinkCall struct {
	orgID, repoID uuid.UUID
	at            time.Time
	errText       string
	// hostKey/hostPublicKey are the observed git server key the sink call
	// carries (backlog 2.41's TOFU write-back); empty when the sync
	// reported none.
	hostKey       string
	hostPublicKey string
}

// recordingSink is a SyncSink that records every call.
type recordingSink struct {
	mu    sync.Mutex
	calls []sinkCall
	err   error
}

func (s *recordingSink) RecordSyncResult(_ context.Context, orgID, repoID uuid.UUID, at time.Time, errText, hostKey, hostPublicKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, sinkCall{orgID, repoID, at, errText, hostKey, hostPublicKey})
	return nil
}

func (s *recordingSink) all() []sinkCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sinkCall(nil), s.calls...)
}

// TestQueueClaimSyncRepoCarriesPlainPayload verifies the claim path for the
// one non-host kind: the plain {"repo_id": ...} payload passes through
// unsealed, no playbook or inventory snapshot ships, and no credential is
// opened — a git sync touches no managed host.
func TestQueueClaimSyncRepoCarriesPlainPayload(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-sync-claim-org")
	box := crypto.NewBox(testBoxKey("jobs-sync-claim-key-0123456789ab"))

	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	credsSvc := credentials.NewService(credentials.NewSQLStore(d), box, time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(d), credsSvc, hostsSvc)
	invStore := inventory.NewSQLStore(d)
	invSvc := inventory.NewService(invStore, hostsSvc, sshSvc, box)
	renderer := inventory.NewRenderer(invStore, hostsSvc, sshSvc, credsSvc, box)

	scoped := db.WithOrgScope(ctx, org)
	if _, err := hostsSvc.CreateZone(scoped, org, hosts.DefaultZoneSlug, hosts.DefaultZoneName); err != nil {
		t.Fatal(err)
	}
	repoID := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(scoped,
		`INSERT INTO git_repos (id, org_id, name, url) VALUES ($1, $2, 'infra', 'https://git.example.com/acme/infra.git')`,
		repoID, org); err != nil {
		t.Fatal(err)
	}

	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d), box)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
	queue.SetKeyPins(hostsSvc)
	resolver := jobs.NewScopeResolver(invSvc, invSvc, hostsSvc)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, lookup, resolver, hostsSvc, hostsSvc, box, time.Now, rand.Reader)
	// The claim-time enrichment resolves the repository through the same
	// registry the API serves.
	reposSvc := repos.NewService(repos.NewSQLStore(d), credsSvc, svc)
	queue.SetRepoSource(repoRefAdapter{reposSvc: reposSvc})

	job, err := svc.EnqueueSync(scoped, org, repoID, nil)
	if err != nil {
		t.Fatalf("EnqueueSync: %v", err)
	}
	claim, err := queue.Claim(ctx, org, []uuid.UUID{job.ZoneID}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	j := &claim.Job
	if j.GetKind() != "sync_repo" || j.GetOrgId() != org.String() {
		t.Fatalf("identity = %+v", j)
	}
	var claimedPayload struct {
		Repo struct {
			ID      string `json:"id"`
			URL     string `json:"url"`
			Branch  string `json:"branch"`
			Subpath string `json:"subpath"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(j.GetKindPayload()), &claimedPayload); err != nil ||
		claimedPayload.Repo.ID != repoID.String() {
		t.Fatalf("kind payload = %q, want the enriched repo payload", j.GetKindPayload())
	}
	if claimedPayload.Repo.URL != "https://git.example.com/acme/infra.git" || claimedPayload.Repo.Branch != "main" {
		t.Fatalf("repo payload = %+v", claimedPayload.Repo)
	}
	if j.GetPlaybook() != "" || j.GetTemplateId() != "" || j.GetLimit() != "" || j.GetSshUsername() != "" {
		t.Fatalf("host-bound fields must stay empty: %+v", j)
	}
	if doc := j.GetInventoryDoc(); doc != "{}" {
		t.Fatalf("inventory doc = %q, want the empty document", doc)
	}
	if len(j.GetConnections()) != 0 || len(j.GetHostKeys()) != 0 {
		t.Fatalf("no host material may ship: %v", j.GetConnections())
	}
}

// TestQueueClaimSyncRepoCarriesTheGitServerPin proves the claim-time
// enrichment ships a pinned repository's git server key (backlog 2.41):
// the claimed kind payload's repo object carries the pin's fingerprint and
// full key, so the fetch demands the pinned key; an unpinned repository's
// payload carries neither field (trust on first use stays the default).
func TestQueueClaimSyncRepoCarriesTheGitServerPin(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-sync-pin-claim-org")
	box := crypto.NewBox(testBoxKey("jobs-sync-pin-claim-key-01234567"))

	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	credsSvc := credentials.NewService(credentials.NewSQLStore(d), box, time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(d), credsSvc, hostsSvc)
	invStore := inventory.NewSQLStore(d)
	invSvc := inventory.NewService(invStore, hostsSvc, sshSvc, box)
	renderer := inventory.NewRenderer(invStore, hostsSvc, sshSvc, credsSvc, box)

	scoped := db.WithOrgScope(ctx, org)
	if _, err := hostsSvc.CreateZone(scoped, org, hosts.DefaultZoneSlug, hosts.DefaultZoneName); err != nil {
		t.Fatal(err)
	}
	repoID := uuid.Must(uuid.NewV7())
	fp, pub := "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", "ssh-ed25519 AAAA pinned"
	if _, err := d.Exec(scoped,
		`INSERT INTO git_repos (id, org_id, name, url, ssh_host_key, ssh_host_public_key)
		 VALUES ($1, $2, 'infra', 'ssh://git.example.com/acme/infra.git', $3, $4)`,
		repoID, org, fp, pub); err != nil {
		t.Fatal(err)
	}

	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d), box)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
	queue.SetKeyPins(hostsSvc)
	resolver := jobs.NewScopeResolver(invSvc, invSvc, hostsSvc)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, lookup, resolver, hostsSvc, hostsSvc, box, time.Now, rand.Reader)
	reposSvc := repos.NewService(repos.NewSQLStore(d), credsSvc, svc)
	queue.SetRepoSource(repoRefAdapter{reposSvc: reposSvc})

	job, err := svc.EnqueueSync(scoped, org, repoID, nil)
	if err != nil {
		t.Fatalf("EnqueueSync: %v", err)
	}
	claim, err := queue.Claim(ctx, org, []uuid.UUID{job.ZoneID}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	var claimedPayload struct {
		Repo struct {
			ID            string `json:"id"`
			HostKey       string `json:"host_key"`
			HostPublicKey string `json:"host_public_key"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(claim.Job.GetKindPayload()), &claimedPayload); err != nil {
		t.Fatalf("kind payload = %q: %v", claim.Job.GetKindPayload(), err)
	}
	if claimedPayload.Repo.HostKey != fp || claimedPayload.Repo.HostPublicKey != pub {
		t.Fatalf("pinned repo payload = %+v", claimedPayload.Repo)
	}

	// An unpinned repository (https, no host key) carries no pin fields.
	unpinnedID := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(scoped,
		`INSERT INTO git_repos (id, org_id, name, url) VALUES ($1, $2, 'plain', 'https://git.example.com/acme/plain.git')`,
		unpinnedID, org); err != nil {
		t.Fatal(err)
	}
	job, err = svc.EnqueueSync(scoped, org, unpinnedID, nil)
	if err != nil {
		t.Fatalf("EnqueueSync (unpinned): %v", err)
	}
	claim, err = queue.Claim(ctx, org, []uuid.UUID{job.ZoneID}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim (unpinned) = %v, %v", claim, err)
	}
	var unpinnedPayload struct {
		Repo struct {
			HostKey       string `json:"host_key"`
			HostPublicKey string `json:"host_public_key"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(claim.Job.GetKindPayload()), &unpinnedPayload); err != nil {
		t.Fatal(err)
	}
	if unpinnedPayload.Repo.HostKey != "" || unpinnedPayload.Repo.HostPublicKey != "" {
		t.Fatalf("unpinned repo payload = %+v", unpinnedPayload.Repo)
	}
}

// repoRefAdapter adapts repos.Service to the queue's RepoSource port, the
// way internal/app wires it.
type repoRefAdapter struct{ reposSvc *repos.Service }

func (a repoRefAdapter) Get(ctx context.Context, orgID, repoID uuid.UUID) (jobs.RepoRef, error) {
	r, err := a.reposSvc.Get(ctx, orgID, repoID)
	if err != nil {
		return jobs.RepoRef{}, err
	}
	return jobs.RepoRef{ID: r.ID, URL: r.URL, Branch: r.Branch, Subpath: r.Subpath, CredentialID: r.CredentialID,
		VaultIdentities: toJobsVault(r.VaultIdentities),
		SSHHostKey:      r.SSHHostKey, SSHHostPublicKey: r.SSHHostPublicKey}, nil
}

// vaultResolverAdapter mirrors internal/app's unexported vaultResolver
// (jobs.VaultResolver wired to repos.ResolveVaultIdentities): app's own
// adapters are deliberately unexported, so an integration test that needs
// the real repos.Service and the real merge algorithm rebuilds the same
// thin wiring here, the same reason repoRefAdapter above is a local copy
// rather than an app import.
type vaultResolverAdapter struct{}

func (vaultResolverAdapter) ResolveVaultIdentities(repo, template []jobs.VaultIdentity) []jobs.VaultIdentity {
	resolved := repos.ResolveVaultIdentities(toRepoVault(repo), toRepoVault(template))
	return toJobsVault(resolved)
}

func toJobsVault(in []repos.VaultIdentity) []jobs.VaultIdentity {
	out := make([]jobs.VaultIdentity, len(in))
	for i, id := range in {
		out[i] = jobs.VaultIdentity{CredentialID: id.CredentialID, Label: id.Label, FromTemplate: id.FromTemplate}
	}
	return out
}

func toRepoVault(in []jobs.VaultIdentity) []repos.VaultIdentity {
	out := make([]repos.VaultIdentity, len(in))
	for i, id := range in {
		out[i] = repos.VaultIdentity{CredentialID: id.CredentialID, Label: id.Label, FromTemplate: id.FromTemplate}
	}
	return out
}

// TestQueueFinishRecordsSyncResults pins the finish-path sync recording:
// the queue detects a finished sync_repo job and hands (org, repo, at,
// error) to the injected SyncSink — never for other kinds, never for a
// cancelled sync, never twice, and never failing the finish itself.
func TestQueueFinishRecordsSyncResults(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-sync-finish-org")
	store := jobs.NewSQLStore(d)
	queue := jobs.NewSQLQueue(d, crypto.NewBox(testBoxKey("jobs-sync-finish-key")), emptySources{}, emptySources{}, emptySources{}, emptySources{}, time.Now, rand.Reader)
	sink := &recordingSink{}
	queue.SetSyncSink(sink)

	scoped := db.WithOrgScope(ctx, org)
	zone := seedZone(t, d, org)
	repoID := uuid.Must(uuid.NewV7())
	syncJob := func(status jobs.Status) jobs.Job {
		j := newJob(org, zone, jobs.KindSyncRepo, jobs.StatusQueued)
		j.Payload = json.RawMessage(`{"repo_id":"` + repoID.String() + `"}`)
		if err := store.CreateJob(scoped, j, j.Payload); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `UPDATE jobs SET status = $2 WHERE id = $1`, j.ID, string(status)); err != nil {
			t.Fatal(err)
		}
		return j
	}

	t.Run("success reaches the sink", func(t *testing.T) {
		j := syncJob(jobs.StatusRunning)
		if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, map[string]any{}, ""); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		calls := sink.all()
		if len(calls) != 1 || calls[0].orgID != org || calls[0].repoID != repoID || calls[0].errText != "" || calls[0].at.IsZero() {
			t.Fatalf("sink calls = %+v", calls)
		}
		if calls[0].hostKey != "" || calls[0].hostPublicKey != "" {
			t.Fatalf("a sync without a result event carries no host key: %+v", calls[0])
		}
	})
	t.Run("the observed git server key rides the sink call", func(t *testing.T) {
		// Its own sink so the later count assertions stay untouched.
		keySink := &recordingSink{}
		queue.SetSyncSink(keySink)
		t.Cleanup(func() { queue.SetSyncSink(sink) })
		j := syncJob(jobs.StatusRunning)
		if err := queue.AppendEvent(ctx, j.ID, jobs.Event{
			Seq: 1, Task: "sync_repo", State: "ok",
			Payload: json.RawMessage(`{"repo_id":"` + repoID.String() + `","requirements_installed":false,` +
				`"host_key":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","host_public_key":"ssh-ed25519 AAAA observed"}`),
			At: time.Now(),
		}); err != nil {
			t.Fatalf("append sync event: %v", err)
		}
		if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, map[string]any{}, ""); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		calls := keySink.all()
		if len(calls) != 1 {
			t.Fatalf("sink calls = %+v", calls)
		}
		if calls[0].hostKey != "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" || calls[0].hostPublicKey != "ssh-ed25519 AAAA observed" {
			t.Fatalf("the observed key must ride the sink call: %+v", calls[0])
		}
	})
	t.Run("failure carries the reason", func(t *testing.T) {
		j := syncJob(jobs.StatusRunning)
		if err := queue.Finish(ctx, j.ID, jobs.StatusFailed, nil, "git pull: not a repository"); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		calls := sink.all()
		if len(calls) != 2 || calls[1].errText != "git pull: not a repository" {
			t.Fatalf("sink calls = %+v", calls)
		}
	})
	t.Run("cancelled sync records nothing", func(t *testing.T) {
		j := syncJob(jobs.StatusRunning)
		if err := queue.Finish(ctx, j.ID, jobs.StatusCancelled, nil, ""); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if calls := sink.all(); len(calls) != 2 {
			t.Fatalf("a cancelled sync must not record a sync result: %+v", calls)
		}
	})
	t.Run("other kinds stay out", func(t *testing.T) {
		j := newJob(org, zone, jobs.KindConnectionTest, jobs.StatusQueued)
		if err := store.CreateJob(scoped, j, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `UPDATE jobs SET status = 'running' WHERE id = $1`, j.ID); err != nil {
			t.Fatal(err)
		}
		if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, map[string]any{}, ""); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if calls := sink.all(); len(calls) != 2 {
			t.Fatalf("non-sync finish must not hit the sink: %+v", calls)
		}
	})
	t.Run("finish is idempotent for the sink", func(t *testing.T) {
		j := syncJob(jobs.StatusRunning)
		if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatal(err)
		}
		if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatal(err)
		}
		if calls := sink.all(); len(calls) != 3 {
			t.Fatalf("a retried finish must not re-record: %+v", calls)
		}
	})
	t.Run("nil sink is safe", func(t *testing.T) {
		j := syncJob(jobs.StatusRunning)
		queue.SetSyncSink(nil)
		if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatalf("Finish with nil sink: %v", err)
		}
	})
	t.Run("sink failure fails the finish and keeps the job running", func(t *testing.T) {
		failing := &recordingSink{err: errors.New("boom")}
		queue.SetSyncSink(failing)
		j := syncJob(jobs.StatusRunning)
		if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err == nil {
			t.Fatal("Finish = nil, want the sink failure (the finish is atomic)")
		}
		if row, err := store.GetJob(scoped, org, j.ID); err != nil || row.Status != jobs.StatusRunning {
			t.Fatalf("job after the failed finish = %+v, %v; want still running", row, err)
		}
		// The retry, with the sink healthy again, completes the finish.
		healthy := &recordingSink{}
		queue.SetSyncSink(healthy)
		if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatalf("retried Finish: %v", err)
		}
		if row, err := store.GetJob(scoped, org, j.ID); err != nil || row.Status != jobs.StatusSucceeded || len(healthy.all()) != 1 {
			t.Fatalf("job after the retry = %+v, %v, sink calls %d; want succeeded and recorded once", row, err, len(healthy.all()))
		}
	})
	t.Run("a repository deleted meanwhile does not wedge the finish", func(t *testing.T) {
		queue.SetSyncSink(&recordingSink{err: jobs.ErrNotFound})
		j := syncJob(jobs.StatusRunning)
		if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatalf("Finish = %v, want nil (a vanished repository is logged and skipped)", err)
		}
	})
}

// TestQueueFinishWiresTheRealSyncSink runs the production wiring: the
// queue's sink is the org-scoped jobs service, so finishing a sync writes
// the repository's last_sync/last_sync_error columns directly.
func TestQueueFinishWiresTheRealSyncSink(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-sync-sink-org")
	box := crypto.NewBox(testBoxKey("jobs-sync-sink-key-0123456789ab"))

	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	credsSvc := credentials.NewService(credentials.NewSQLStore(d), box, time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(d), credsSvc, hostsSvc)
	invStore := inventory.NewSQLStore(d)
	invSvc := inventory.NewService(invStore, hostsSvc, sshSvc, box)
	renderer := inventory.NewRenderer(invStore, hostsSvc, sshSvc, credsSvc, box)
	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d), box)
	resolver := jobs.NewScopeResolver(invSvc, invSvc, hostsSvc)

	scoped := db.WithOrgScope(ctx, org)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
	queue.SetKeyPins(hostsSvc)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, lookup, resolver, hostsSvc, hostsSvc, box, time.Now, rand.Reader)
	queue.SetSyncSink(svc)

	if _, err := hostsSvc.CreateZone(scoped, org, hosts.DefaultZoneSlug, hosts.DefaultZoneName); err != nil {
		t.Fatal(err)
	}
	repoID := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(scoped,
		`INSERT INTO git_repos (id, org_id, name, url) VALUES ($1, $2, 'infra', 'https://git.example.com/acme/infra.git')`,
		repoID, org); err != nil {
		t.Fatal(err)
	}

	store := jobs.NewSQLStore(d)
	zone := seedZone(t, d, org)
	syncJob := func() jobs.Job {
		j := newJob(org, zone, jobs.KindSyncRepo, jobs.StatusQueued)
		j.Payload = json.RawMessage(`{"repo_id":"` + repoID.String() + `"}`)
		if err := store.CreateJob(scoped, j, j.Payload); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `UPDATE jobs SET status = 'running' WHERE id = $1`, j.ID); err != nil {
			t.Fatal(err)
		}
		return j
	}

	// Success first: last_sync set, last_sync_error cleared.
	j := syncJob()
	if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	var lastSync *time.Time
	var lastErr *string
	if err := d.Pool().QueryRow(ctx, `SELECT last_sync, last_sync_error FROM git_repos WHERE id = $1`, repoID).Scan(&lastSync, &lastErr); err != nil {
		t.Fatal(err)
	}
	if lastSync == nil || lastSync.IsZero() || lastErr != nil {
		t.Fatalf("success columns = %v / %v", lastSync, lastErr)
	}

	// Then a failure: last_sync_error carries the reason.
	j2 := syncJob()
	if err := queue.Finish(ctx, j2.ID, jobs.StatusFailed, nil, "git pull: not a repository"); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool().QueryRow(ctx, `SELECT last_sync, last_sync_error FROM git_repos WHERE id = $1`, repoID).Scan(&lastSync, &lastErr); err != nil {
		t.Fatal(err)
	}
	if lastErr == nil || *lastErr != "git pull: not a repository" {
		t.Fatalf("failure record = %v / %v", lastSync, lastErr)
	}

	// A repository deleted while its sync ran: the sink records nothing,
	// the finish still succeeds.
	j3 := newJob(org, zone, jobs.KindSyncRepo, jobs.StatusQueued)
	j3.Payload = json.RawMessage(`{"repo_id":"` + uuid.Must(uuid.NewV7()).String() + `"}`)
	if err := store.CreateJob(scoped, j3, j3.Payload); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, `UPDATE jobs SET status = 'running' WHERE id = $1`, j3.ID); err != nil {
		t.Fatal(err)
	}
	if err := queue.Finish(ctx, j3.ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("a vanished repository must not fail the finish: %v", err)
	}
}

// TestQueueFinishCompletesTheRepoKeyPin runs the TOFU completion through
// the production wiring (backlog 2.41): the first successful sync whose ok
// event carries the observed git server key pins the repository row; a
// later sync reporting a different but VALID pair leaves the pin untouched
// (PinRepoHostKey's WHERE ssh_host_key IS NULL — the pin write is
// TOFU-only, the re-trust guard the threat model relies on); a malformed
// pair is dropped in the service; a failed sync records no pin.
func TestQueueFinishCompletesTheRepoKeyPin(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-sync-pin-org")
	box := crypto.NewBox(testBoxKey("jobs-sync-pin-key-0123456789"))

	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	credsSvc := credentials.NewService(credentials.NewSQLStore(d), box, time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(d), credsSvc, hostsSvc)
	invStore := inventory.NewSQLStore(d)
	invSvc := inventory.NewService(invStore, hostsSvc, sshSvc, box)
	renderer := inventory.NewRenderer(invStore, hostsSvc, sshSvc, credsSvc, box)
	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d), box)
	resolver := jobs.NewScopeResolver(invSvc, invSvc, hostsSvc)

	scoped := db.WithOrgScope(ctx, org)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
	queue.SetKeyPins(hostsSvc)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, lookup, resolver, hostsSvc, hostsSvc, box, time.Now, rand.Reader)
	queue.SetSyncSink(svc)

	if _, err := hostsSvc.CreateZone(scoped, org, hosts.DefaultZoneSlug, hosts.DefaultZoneName); err != nil {
		t.Fatal(err)
	}
	repoID := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(scoped,
		`INSERT INTO git_repos (id, org_id, name, url) VALUES ($1, $2, 'infra', 'ssh://git@git.example.com/acme/infra.git')`,
		repoID, org); err != nil {
		t.Fatal(err)
	}
	store := jobs.NewSQLStore(d)
	zone := seedZone(t, d, org)
	fp1, pub1 := hostKeyPair(t)
	fp2, pub2 := hostKeyPair(t)

	syncJob := func() jobs.Job {
		j := newJob(org, zone, jobs.KindSyncRepo, jobs.StatusQueued)
		j.Payload = json.RawMessage(`{"repo_id":"` + repoID.String() + `"}`)
		if err := store.CreateJob(scoped, j, j.Payload); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `UPDATE jobs SET status = 'running' WHERE id = $1`, j.ID); err != nil {
			t.Fatal(err)
		}
		return j
	}
	pinOf := func(t *testing.T) (*string, *string) {
		t.Helper()
		var hostKey, hostPub *string
		if err := d.Pool().QueryRow(ctx, `SELECT ssh_host_key, ssh_host_public_key FROM git_repos WHERE org_id = $1 AND id = $2`, org, repoID).Scan(&hostKey, &hostPub); err != nil {
			t.Fatal(err)
		}
		return hostKey, hostPub
	}

	// The first successful sync's observed key completes the TOFU pin.
	j := syncJob()
	if err := queue.AppendEvent(ctx, j.ID, jobs.Event{
		Seq: 1, Task: "sync_repo", State: "ok",
		Payload: json.RawMessage(`{"repo_id":"` + repoID.String() + `","requirements_installed":false,"host_key":"` + fp1 + `","host_public_key":"` + pub1 + `"}`),
		At:      time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	hostKey, hostPub := pinOf(t)
	if hostKey == nil || *hostKey != fp1 || hostPub == nil || *hostPub != pub1 {
		t.Fatalf("TOFU pin after the first sync = %v / %v", hostKey, hostPub)
	}

	// A later sync reporting a DIFFERENT — but valid — pair leaves the pin:
	// the service accepts the pair, so this exercises PinRepoHostKey's
	// WHERE ssh_host_key IS NULL guard itself, the property the threat
	// model pins (a compromised worker cannot re-trust a changed server —
	// the sink cannot overwrite a pin by itself; the worker's strict check
	// refusing a changed key is the first line, this query the second).
	j = syncJob()
	if err := queue.AppendEvent(ctx, j.ID, jobs.Event{
		Seq: 1, Task: "sync_repo", State: "ok",
		Payload: json.RawMessage(`{"repo_id":"` + repoID.String() + `","requirements_installed":false,"host_key":"` + fp2 + `","host_public_key":"` + pub2 + `"}`),
		At:      time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	hostKey, hostPub = pinOf(t)
	if hostKey == nil || *hostKey != fp1 || hostPub == nil || *hostPub != pub1 {
		t.Fatalf("the TOFU pin must never be overwritten by a valid second pair: %v / %v", hostKey, hostPub)
	}

	// A malformed pair (fingerprint and key that belong to different
	// servers) is dropped in the service — the sync still records, and the
	// pin is untouched.
	j = syncJob()
	if err := queue.AppendEvent(ctx, j.ID, jobs.Event{
		Seq: 1, Task: "sync_repo", State: "ok",
		Payload: json.RawMessage(`{"repo_id":"` + repoID.String() + `","requirements_installed":false,"host_key":"` + fp1 + `","host_public_key":"` + pub2 + `"}`),
		At:      time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	hostKey, hostPub = pinOf(t)
	if hostKey == nil || *hostKey != fp1 || hostPub == nil || *hostPub != pub1 {
		t.Fatalf("a malformed report must pin nothing: %v / %v", hostKey, hostPub)
	}

	// A failed sync reports no pin even if an event rode along.
	j = syncJob()
	if err := queue.AppendEvent(ctx, j.ID, jobs.Event{
		Seq: 1, Task: "sync_repo", State: "ok",
		Payload: json.RawMessage(`{"repo_id":"` + repoID.String() + `","requirements_installed":false}`),
		At:      time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Finish(ctx, j.ID, jobs.StatusFailed, nil, "git fetch failed"); err != nil {
		t.Fatal(err)
	}
	hostKey, hostPub = pinOf(t)
	if hostKey == nil || *hostKey != fp1 || hostPub == nil || *hostPub != pub1 {
		t.Fatalf("a failed sync must not change the pin: %v / %v", hostKey, hostPub)
	}
}

// TestQueueClaimLegacyVaultCredentialAloneResolvesToTheDefaultLabel is the
// write-time bridge's own regression test: a repository configured only
// through the old single VaultCredentialID column — the only vault control
// the product's web UI has today (RepoDialog.tsx has no VaultIdentities
// editor) — must still decrypt on a real claim. Through the real
// repos.Service, not a fake, so repos.Service.Create's bridge
// (legacyVaultIdentities) is what is actually under test, on the
// workerpb.Job the claim produces, not an intermediate. This is the
// regression the coordinator found: before the bridge, this resolved to
// zero vault identities, silently.
func TestQueueClaimLegacyVaultCredentialAloneResolvesToTheDefaultLabel(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-legacy-vault-org")
	box := crypto.NewBox(testBoxKey("jobs-legacy-vault-claim-key-0123"))

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
	vaultCred, err := credsSvc.Create(scoped, org, credentials.CreateInput{
		Name: "legacy-vault", Kind: credentials.KindVaultPassword,
		Material: credentials.Material{Password: "legacy-vault-passphrase"},
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
	if _, err := sshSvc.SetHostSSH(scoped, org, host.ID, sshconf.HostSSHInput{
		CredentialID: &hostCred.ID, Port: 22, BastionMode: sshconf.ModeInherit,
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

	templateStore := templates.NewSQLStore(d)
	playbooksStore := playbooks.NewSQLStore(d)
	lookup := app.NewTemplateLookup(templateStore, playbooksStore, box)
	resolver := jobs.NewScopeResolver(invSvc, invSvc, hostsSvc)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
	queue.SetKeyPins(hostsSvc)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, lookup, resolver, hostsSvc, hostsSvc, box, time.Now, rand.Reader)

	// The circular wiring app.Build uses: reposSvc needs the jobs service
	// for its own Sync action, so it is built after jobsSvc and wired onto
	// the queue afterward (SetRepoSource/SetVaultResolver).
	reposSvc := repos.NewService(repos.NewSQLStore(d), credsSvc, svc)
	queue.SetRepoSource(repoRefAdapter{reposSvc: reposSvc})
	queue.SetVaultResolver(vaultResolverAdapter{})
	// A real shared.Service, not a fake: this test never creates a shared
	// template definition, so CreateRepo's resolve call below always
	// touches zero rows — wiring the real implementation here is simpler
	// than a fake that would only ever be asked to do nothing.
	sharedSvc := shared.NewService(shared.NewSQLStore(d))
	playbooksSvc := playbooks.NewService(playbooksStore, reposSvc, sharedSvc)
	templatesSvc := templates.NewService(templateStore, playbooksSvc, invSvc, hostsSvc, credsSvc, box)

	repo, err := reposSvc.Create(scoped, org, repos.CreateInput{
		Name: "infra", URL: "https://git.example.com/acme/infra.git",
		// The legacy field alone — no VaultIdentities — is exactly what
		// RepoDialog.tsx still sends today.
		VaultCredentialID: &vaultCred.ID,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	pb, err := playbooksSvc.CreateRepo(scoped, org, repo.ID, "site.yml")
	if err != nil {
		t.Fatalf("create repo playbook: %v", err)
	}
	tmpl, err := templatesSvc.Create(scoped, org, templates.CreateInput{
		Name: "deploy", PlaybookID: pb.ID, Scope: templates.Scope{GroupID: &grp.ID},
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}

	created, err := svc.EnqueueRun(scoped, org, tmpl.ID, nil)
	if err != nil {
		t.Fatalf("EnqueueRun: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("enqueued = %+v, want one job", created)
	}

	claim, err := queue.Claim(ctx, org, []uuid.UUID{zone.ID}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	identities := claim.Job.GetVaultIdentities()
	if len(identities) != 1 || identities[0].GetCredentialId() != vaultCred.ID.String() || identities[0].GetLabel() != "default" {
		t.Fatalf("vault_identities = %v, want one entry {credential_id: %q, label: \"default\"}", identities, vaultCred.ID)
	}
	raw, ok := claim.Job.GetCredentials()[vaultCred.ID.String()]
	if !ok {
		t.Fatalf("the claim names the legacy vault credential but did not reveal it; credentials has %v", keysOf(claim.Job.GetCredentials()))
	}
	var secret credentials.Secret
	if err := json.Unmarshal([]byte(raw), &secret); err != nil {
		t.Fatalf("decode revealed vault secret: %v", err)
	}
	if secret.Password != "legacy-vault-passphrase" {
		t.Errorf("revealed vault secret = %+v, want the legacy passphrase", secret)
	}
}

// TestQueueClaimRecordsVaultIdentitiesImmuneToALaterRepoEdit is backlog
// 2.91's own reason for existing, driven at the store level rather than
// through the API's DTOs: a run is claimed with the repository's and
// template's vault identities as they stand right then; the repository is
// edited afterward (a different credential under the SAME label — the
// drift the old recompute-on-read code could never tell apart from the
// original); and the job row's own stored record must still read what the
// run actually received, not today's configuration. The API-level
// equivalent (TestRunDetailStillShowsTheClaimedVaultIdentitiesAfterARepoEdit
// in internal/api) is the one that also proves the HTTP response reads this
// record instead of recomputing; this one pins that the record itself is
// correct and durable once the queue's claim writes it.
func TestQueueClaimRecordsVaultIdentitiesImmuneToALaterRepoEdit(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-vault-record-org")
	box := crypto.NewBox(testBoxKey("jobs-vault-record-claim-key-0123"))

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
	oldCred, err := credsSvc.Create(scoped, org, credentials.CreateInput{
		Name: "prod-vault-old", Kind: credentials.KindVaultPassword, Material: credentials.Material{Password: "old-passphrase"},
	})
	if err != nil {
		t.Fatal(err)
	}
	newCred, err := credsSvc.Create(scoped, org, credentials.CreateInput{
		Name: "prod-vault-new", Kind: credentials.KindVaultPassword, Material: credentials.Material{Password: "new-passphrase"},
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := hostsSvc.CreateHost(scoped, org, hosts.HostInput{Name: "web1", Address: "10.0.0.1", ZoneID: zone.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sshSvc.SetHostSSH(scoped, org, host.ID, sshconf.HostSSHInput{
		CredentialID: &hostCred.ID, Port: 22, BastionMode: sshconf.ModeInherit,
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

	templateStore := templates.NewSQLStore(d)
	playbooksStore := playbooks.NewSQLStore(d)
	lookup := app.NewTemplateLookup(templateStore, playbooksStore, box)
	resolver := jobs.NewScopeResolver(invSvc, invSvc, hostsSvc)
	jobStore := jobs.NewSQLStore(d)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
	queue.SetKeyPins(hostsSvc)
	svc := jobs.NewService(jobStore, queue, lookup, resolver, hostsSvc, hostsSvc, box, time.Now, rand.Reader)

	reposSvc := repos.NewService(repos.NewSQLStore(d), credsSvc, svc)
	queue.SetRepoSource(repoRefAdapter{reposSvc: reposSvc})
	queue.SetVaultResolver(vaultResolverAdapter{})
	sharedSvc := shared.NewService(shared.NewSQLStore(d))
	playbooksSvc := playbooks.NewService(playbooksStore, reposSvc, sharedSvc)
	templatesSvc := templates.NewService(templateStore, playbooksSvc, invSvc, hostsSvc, credsSvc, box)

	repo, err := reposSvc.Create(scoped, org, repos.CreateInput{
		Name: "infra", URL: "https://git.example.com/acme/infra.git",
		VaultIdentities: []repos.VaultIdentity{{CredentialID: oldCred.ID, Label: "prod"}},
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	pb, err := playbooksSvc.CreateRepo(scoped, org, repo.ID, "site.yml")
	if err != nil {
		t.Fatalf("create repo playbook: %v", err)
	}
	tmpl, err := templatesSvc.Create(scoped, org, templates.CreateInput{
		Name: "deploy", PlaybookID: pb.ID, Scope: templates.Scope{GroupID: &grp.ID},
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	created, err := svc.EnqueueRun(scoped, org, tmpl.ID, nil)
	if err != nil {
		t.Fatalf("EnqueueRun: %v", err)
	}
	jobID := created[0].ID

	claim, err := queue.Claim(ctx, org, []uuid.UUID{zone.ID}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	if got := claim.Job.GetId(); got != jobID.String() {
		t.Fatalf("claimed %s, want %s", got, jobID)
	}

	// What the claim itself shipped: the old credential, under "prod".
	shipped := claim.Job.GetVaultIdentities()
	if len(shipped) != 1 || shipped[0].GetCredentialId() != oldCred.ID.String() || shipped[0].GetLabel() != "prod" {
		t.Fatalf("claim's own vault_identities = %v, want one entry {label: \"prod\", credential_id: %q}", shipped, oldCred.ID)
	}

	// The repository is edited after the claim: the SAME label "prod" now
	// names a different credential — the exact drift the old
	// recompute-on-read code could never distinguish from "this is what the
	// run always had".
	if _, err := reposSvc.Update(scoped, org, repo.ID, repos.UpdateInput{
		Name: repo.Name, URL: repo.URL, Branch: repo.Branch,
		VaultIdentities: []repos.VaultIdentity{{CredentialID: newCred.ID, Label: "prod"}},
	}); err != nil {
		t.Fatalf("update repo: %v", err)
	}

	// The job's own stored record must still read the credential the run
	// was actually given — not the repository's current one.
	got, err := jobStore.GetJob(scoped, org, jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !got.VaultIdentitiesRecorded {
		t.Fatal("VaultIdentitiesRecorded = false, want true: the claim already committed a record")
	}
	if len(got.ResolvedVaultIdentities) != 1 || got.ResolvedVaultIdentities[0].CredentialID != oldCred.ID || got.ResolvedVaultIdentities[0].Label != "prod" {
		t.Fatalf("stored ResolvedVaultIdentities = %+v, want one entry {label: \"prod\", credential_id: %s} (the OLD credential, unaffected by the later edit)", got.ResolvedVaultIdentities, oldCred.ID)
	}

	// A fresh resolution against the repository's CURRENT configuration
	// would instead name the new credential — proof this test's setup
	// really did change something, not merely re-asserted the original.
	live, err := reposSvc.Get(scoped, org, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.VaultIdentities) != 1 || live.VaultIdentities[0].CredentialID != newCred.ID {
		t.Fatalf("repository's live vault identities = %+v, want the NEW credential (setup check)", live.VaultIdentities)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
