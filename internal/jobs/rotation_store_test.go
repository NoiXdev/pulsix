// The rotation loop against the real store (package jobs_test, where the
// existing external helpers live): two managed hosts reference a
// credential — both jobs succeed → the new key seals onto the credential
// in one finalize; one fails → the credential is untouched and the job's
// error names the host; the terminal payloads are cleared in both cases.
package jobs_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
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

// rotationStoreWiring bundles the real store wiring the integration tests
// share: a real credentials service (sealed with a fixed box) behind a
// real queue, with the hosts service supplying the rotation targets and
// the real renderer resolving the claim connections.
type rotationStoreWiring struct {
	d        *db.DB
	svc      *jobs.Service
	queue    *jobs.SQLQueue
	credsSvc *credentials.Service
	sshSvc   *sshconf.Service
	hostsSvc *hosts.Service
	org      uuid.UUID
	scoped   context.Context
}

func newRotationStoreWiring(t *testing.T, slug string) rotationStoreWiring {
	t.Helper()
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), slug)
	box := crypto.NewBox(testBoxKey("jobs-rotation-store-key"))

	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	credsSvc := credentials.NewService(credentials.NewSQLStore(d), box, time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(d), credsSvc, hostsSvc)
	invStore := inventory.NewSQLStore(d)
	invSvc := inventory.NewService(invStore, hostsSvc, sshSvc, box)
	renderer := inventory.NewRenderer(invStore, hostsSvc, sshSvc, credsSvc, box)
	lookup := app.NewTemplateLookup(templates.NewSQLStore(d), playbooks.NewSQLStore(d))
	queue := jobs.NewSQLQueue(d, box, renderer, credsSvc, lookup, sshSvc, time.Now, rand.Reader)
	resolver := jobs.NewScopeResolver(invSvc, invSvc, hostsSvc)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, lookup, resolver, hostsSvc, hostsSvc, box, time.Now, rand.Reader)
	credsSvc.SetRotationEnqueuer(svc)
	queue.SetKeyPins(hostsSvc)
	// The finisher sink IS the credentials service: the loop under test is
	// enqueue (through the enqueuer port) and finalize (through the
	// RotationFinisher port), so both halves ride the real service.
	queue.SetRotationFinisher(credsSvc)
	if _, err := hostsSvc.CreateZone(db.WithOrgScope(ctx, org), org, hosts.DefaultZoneSlug, hosts.DefaultZoneName); err != nil {
		t.Fatal(err)
	}
	return rotationStoreWiring{
		d: d, svc: svc, queue: queue, credsSvc: credsSvc, sshSvc: sshSvc, hostsSvc: hostsSvc,
		org: org, scoped: db.WithOrgScope(ctx, org),
	}
}

func (w rotationStoreWiring) zone(t *testing.T) uuid.UUID {
	t.Helper()
	zones, err := w.hostsSvc.ListZones(context.Background(), w.org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	return zones[0].ID
}

// TestRotationGroupFinalizesThroughTheRealLoop drives the whole rotation
// against the store: two managed hosts reference a credential, both jobs
// succeed → the credential's key material is replaced (one finalize); the
// terminal payloads are cleared and the claim payloads carry the full
// rotation material.
func TestRotationGroupFinalizesThroughTheRealLoop(t *testing.T) {
	w := newRotationStoreWiring(t, "jobs-rotation-store")
	box := crypto.NewBox(testBoxKey("jobs-rotation-store-key"))

	created, err := w.credsSvc.Create(w.scoped, w.org, credentials.CreateInput{
		Name: "acme-deploy", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	for _, name := range []string{"web-01", "web-02"} {
		h, err := w.hostsSvc.CreateHost(w.scoped, w.org, hosts.HostInput{Name: name, Address: "10.0.0.1", ZoneID: w.zone(t), Managed: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.sshSvc.SetHostSSH(w.scoped, w.org, h.ID, sshconf.HostSSHInput{
			CredentialID: &created.ID, Port: 22, BastionMode: sshconf.ModeInherit,
		}); err != nil {
			t.Fatal(err)
		}
	}

	actor := uuid.Must(uuid.NewV7())
	result, err := w.credsSvc.Rotate(w.scoped, w.org, created.ID, &actor)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if result.Count != 2 || result.GroupID == uuid.Nil {
		t.Fatalf("rotation result = %+v", result)
	}

	// The credential is still the OLD one right after enqueue.
	kind, sealed, err := credentials.NewSQLStore(w.d).LoadSecret(w.scoped, w.org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = kind
	oldPlain, err := box.OpenAAD(sealed, credentials.AAD(w.org, created.ID))
	if err != nil || !strings.Contains(string(oldPlain), "private_key") {
		t.Fatalf("old secret = %s, %v", oldPlain, err)
	}

	// The group's two jobs, one per host, in the org's default zone.
	rotateKind := jobs.KindRotateKey
	list, _, err := w.svc.List(w.scoped, w.org, jobs.Filter{Kind: &rotateKind}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var groupJobs []jobs.Job
	for _, j := range list {
		if j.RunGroupID == result.GroupID {
			groupJobs = append(groupJobs, j)
		}
	}
	if len(groupJobs) != 2 {
		t.Fatalf("group jobs = %d, want 2", len(groupJobs))
	}
	// Claim both (any order): the claim is keyed by zone, so the two
	// queued jobs come out in whatever order the claimer serves them.
	claims := map[uuid.UUID]*jobs.Claim{}
	for len(claims) < len(groupJobs) {
		claim, err := w.queue.Claim(ctx, []uuid.UUID{w.zone(t)}, "v1")
		if err != nil || claim == nil {
			t.Fatalf("claim = %v, %v", claim, err)
		}
		claims[uuid.MustParse(claim.Job.GetId())] = claim
	}
	claimedHosts := map[uuid.UUID]bool{}
	for _, j := range groupJobs {
		claim := claims[j.ID]
		if claim == nil {
			t.Fatalf("job %s was never claimed", j.ID)
		}
		if claim.Job.GetKind() != "rotate_key" {
			t.Fatalf("claim kind = %q", claim.Job.GetKind())
		}
		var payload struct {
			Host struct {
				ID       string `json:"id"`
				Address  string `json:"address"`
				Port     int    `json:"port"`
				Username string `json:"username"`
			} `json:"host"`
			Username       string `json:"username"`
			OldCredential  string `json:"old_credential"`
			NewPublicKey   string `json:"new_public_key"`
			NewPrivateKey  string `json:"new_private_key"`
			NewFingerprint string `json:"new_fingerprint"`
			PulsixUser     string `json:"pulsix_user"`
		}
		if err := json.Unmarshal([]byte(claim.Job.GetKindPayload()), &payload); err != nil {
			t.Fatalf("kind payload of %s: %v\n%s", j.ID, err, claim.Job.GetKindPayload())
		}
		if payload.Username != "ansible" || payload.PulsixUser != "ansible" {
			t.Fatalf("login user = %q / %q", payload.Username, payload.PulsixUser)
		}
		if !strings.Contains(payload.OldCredential, "private_key") {
			t.Fatalf("old credential = %q, want the opened old secret", payload.OldCredential)
		}
		if !strings.Contains(payload.NewPrivateKey, "private_key") || payload.NewPublicKey == "" {
			t.Fatalf("new key material = %q / %q", payload.NewPrivateKey, payload.NewPublicKey)
		}
		if payload.Host.Username != "ansible" || payload.Host.Address == "" || payload.Host.ID == "" {
			t.Fatalf("host object = %+v", payload.Host)
		}
		if payload.Host.ID != j.HostIDs[0].String() {
			t.Fatalf("payload host = %q, job host = %s", payload.Host.ID, j.HostIDs[0])
		}
		id, _ := uuid.Parse(payload.Host.ID)
		claimedHosts[id] = true
	}
	if len(claimedHosts) != 2 {
		t.Fatalf("claimed hosts = %v", claimedHosts)
	}

	// Both jobs succeed; the last finish finalizes exactly once, and the
	// credential's metadata and secret are the NEW ones.
	for _, j := range groupJobs {
		if err := w.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatalf("finish %s: %v", j.ID, err)
		}
	}
	after, err := w.credsSvc.Get(w.scoped, w.org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PublicKey == created.PublicKey || after.Fingerprint == created.Fingerprint {
		t.Fatalf("credential metadata did not rotate: %+v", after)
	}
	secret, err := w.credsSvc.OpenSecret(w.scoped, w.org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secret.PrivateKey == "" {
		t.Fatal("the rotated credential carries no private key")
	}

	// The terminal payloads are cleared: no sealed ciphertext at rest.
	for _, j := range groupJobs {
		var payload []byte
		if err := w.d.Pool().QueryRow(ctx, `SELECT payload FROM jobs WHERE id = $1`, j.ID).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload) != 0 {
			t.Fatalf("payload of %s survived: %s", j.ID, payload)
		}
	}
}

// TestRotationGroupFailureLeavesTheCredentialUntouched drives the failed
// case: the credential keeps the OLD secret, and the job's error names the
// host (what the worker reports through the result).
func TestRotationGroupFailureLeavesTheCredentialUntouched(t *testing.T) {
	w := newRotationStoreWiring(t, "jobs-rotation-store-fail")

	created, err := w.credsSvc.Create(w.scoped, w.org, credentials.CreateInput{
		Name: "acme-deploy", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := w.hostsSvc.CreateHost(w.scoped, w.org, hosts.HostInput{Name: "web-01", Address: "10.0.0.1", ZoneID: w.zone(t), Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.sshSvc.SetHostSSH(w.scoped, w.org, h.ID, sshconf.HostSSHInput{
		CredentialID: &created.ID, Port: 22, BastionMode: sshconf.ModeInherit,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := w.credsSvc.Rotate(w.scoped, w.org, created.ID, nil)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("rotation result = %+v", result)
	}
	if _, err := w.queue.Claim(ctx, []uuid.UUID{w.zone(t)}, "v1"); err != nil {
		t.Fatal(err)
	}
	// The run failed: the result's error names the host.
	if err := w.queue.Finish(ctx, result.FirstJobID, jobs.StatusFailed,
		map[string]any{"failed_hosts": []string{h.ID.String()}},
		"host "+h.ID.String()+" failed"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	var jobError string
	if err := w.d.Pool().QueryRow(ctx, `SELECT error FROM jobs WHERE id = $1`, result.FirstJobID).Scan(&jobError); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jobError, h.ID.String()) {
		t.Fatalf("job error = %q, want it to name the failing host", jobError)
	}

	// The credential keeps the OLD key and the OLD secret.
	after, err := w.credsSvc.Get(w.scoped, w.org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PublicKey != created.PublicKey || after.Fingerprint != created.Fingerprint {
		t.Fatalf("credential metadata changed: %+v", after)
	}
	secret, err := w.credsSvc.OpenSecret(w.scoped, w.org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secret.PrivateKey == "" {
		t.Fatal("the credential lost its secret")
	}
	// The terminal payload is cleared.
	var payload []byte
	if err := w.d.Pool().QueryRow(ctx, `SELECT payload FROM jobs WHERE id = $1`, result.FirstJobID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 0 {
		t.Fatalf("payload survived: %s", payload)
	}
}

// TestRotationGroupFinalizesOnlyWhenComplete pins the group's completion
// gate: with two targets, finishing only the FIRST job leaves the
// credential untouched — the finalize waits for the group's last terminal
// report.
func TestRotationGroupFinalizesOnlyWhenTheGroupIsDone(t *testing.T) {
	w := newRotationStoreWiring(t, "jobs-rotation-partial")

	created, err := w.credsSvc.Create(w.scoped, w.org, credentials.CreateInput{
		Name: "acme-deploy", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web-01", "web-02"} {
		h, err := w.hostsSvc.CreateHost(w.scoped, w.org, hosts.HostInput{Name: name, Address: "10.0.0.1", ZoneID: w.zone(t), Managed: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.sshSvc.SetHostSSH(w.scoped, w.org, h.ID, sshconf.HostSSHInput{
			CredentialID: &created.ID, Port: 22, BastionMode: sshconf.ModeInherit,
		}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := w.credsSvc.Rotate(w.scoped, w.org, created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.queue.Claim(ctx, []uuid.UUID{w.zone(t)}, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := w.queue.Finish(ctx, result.FirstJobID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	// One of two terminal: the credential is untouched.
	after, err := w.credsSvc.Get(w.scoped, w.org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PublicKey != created.PublicKey || after.Fingerprint != created.Fingerprint {
		t.Fatalf("credential rotated early: %+v", after)
	}
	secret, err := w.credsSvc.OpenSecret(w.scoped, w.org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secret.PrivateKey == "" {
		t.Fatal("the credential lost its secret")
	}
}

// TestRotationFinishErrorPathsAreLoggedNotFatal pins the finish-path
// posture: a rotate_key job whose payload carries no credential, a non-
// rotation job and a cancelled rotation all finish cleanly (the bookkeeping
// never breaks the terminal record), and a cancel wipes the queued job's
// sealed payload.
func TestRotationFinishErrorPathsAreLoggedNotFatal(t *testing.T) {
	w := newRotationStoreWiring(t, "jobs-rotation-finish-paths")
	store := jobs.NewSQLStore(w.d)

	// A rotate_key job whose payload is a plain document without a
	// credential id: the finalize is skipped, the finish stands.
	j := newJob(w.org, uuid.Must(uuid.NewV7()), jobs.KindRotateKey, jobs.StatusQueued)
	if err := store.CreateJob(db.WithOrgScope(ctx, w.org), j, json.RawMessage(`{"nope":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.queue.Claim(ctx, []uuid.UUID{j.ZoneID}, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := w.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("finish with a broken rotation payload: %v", err)
	}

	// A run job with the finisher wired records nothing.
	runJob := newJob(w.org, uuid.Must(uuid.NewV7()), jobs.KindRun, jobs.StatusQueued)
	runJob.HostIDs = []uuid.UUID{uuid.Must(uuid.NewV7())}
	if err := store.CreateJob(db.WithOrgScope(ctx, w.org), runJob, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.queue.Claim(ctx, []uuid.UUID{runJob.ZoneID}, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := w.queue.Finish(ctx, runJob.ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("finish a run job: %v", err)
	}

	// A queued rotate_key job cancelled before its claim: the payload's
	// at-rest lifetime ends immediately.
	cancelled := newJob(w.org, uuid.Must(uuid.NewV7()), jobs.KindRotateKey, jobs.StatusQueued)
	cancelled.HostIDs = []uuid.UUID{uuid.Must(uuid.NewV7())}
	if err := store.CreateJob(db.WithOrgScope(ctx, w.org), cancelled, json.RawMessage(`{"credential_id":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Cancel(db.WithOrgScope(ctx, w.org), w.org, cancelled.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	var payload []byte
	if err := w.d.Pool().QueryRow(ctx, `SELECT payload FROM jobs WHERE id = $1`, cancelled.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 0 {
		t.Fatalf("a cancelled-before-claim job keeps its payload: %s", payload)
	}
}
