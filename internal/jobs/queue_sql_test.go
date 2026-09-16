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

	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d))
	resolver := jobs.NewScopeResolver(invSvc, invSvc, hostsSvc)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
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

	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d))
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
	zone := uuid.Must(uuid.NewV7())
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
}

// recordingSink is a SyncSink that records every call.
type recordingSink struct {
	mu    sync.Mutex
	calls []sinkCall
	err   error
}

func (s *recordingSink) RecordSyncResult(_ context.Context, orgID, repoID uuid.UUID, at time.Time, errText string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, sinkCall{orgID, repoID, at, errText})
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

	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d))
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
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

// repoRefAdapter adapts repos.Service to the queue's RepoSource port, the
// way internal/app wires it.
type repoRefAdapter struct{ reposSvc *repos.Service }

func (a repoRefAdapter) Get(ctx context.Context, orgID, repoID uuid.UUID) (jobs.RepoRef, error) {
	r, err := a.reposSvc.Get(ctx, orgID, repoID)
	if err != nil {
		return jobs.RepoRef{}, err
	}
	return jobs.RepoRef{ID: r.ID, URL: r.URL, Branch: r.Branch, Subpath: r.Subpath, CredentialID: r.CredentialID}, nil
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
	zone := uuid.Must(uuid.NewV7())
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
	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d))
	resolver := jobs.NewScopeResolver(invSvc, invSvc, hostsSvc)

	scoped := db.WithOrgScope(ctx, org)
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
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
	zone := uuid.Must(uuid.NewV7())
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
