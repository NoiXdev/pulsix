// The rotation loop against the real store (package jobs_test, where the
// existing external helpers live): two managed hosts reference a
// credential — both jobs succeed → the new key seals onto the credential
// in one finalize; one fails → the credential is untouched and the job's
// error names the host; the terminal payloads are cleared in both cases.
package jobs_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
	// The test sink records every connection test's outcome into the
	// host's history — including the rotation group's follow-up tests.
	queue.SetTestSink(hostsSvc)
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
	return zoneOf(t, w.hostsSvc, w.org)
}

// zoneOf is the org's default zone, resolved through the hosts service.
func zoneOf(t *testing.T, s *hosts.Service, org uuid.UUID) uuid.UUID {
	t.Helper()
	zones, err := s.ListZones(context.Background(), org)
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
	if result.Count != 4 || result.GroupID == uuid.Nil {
		t.Fatalf("rotation result = %+v, want 2 runs + 2 follow-up tests", result)
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

	// The group carries one rotate_key run per host plus one follow-up
	// connection_test per host, all in one zone.
	list, _, err := w.svc.List(w.scoped, w.org, jobs.Filter{}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var groupJobs []jobs.Job
	for _, j := range list {
		if j.RunGroupID == result.GroupID {
			groupJobs = append(groupJobs, j)
		}
	}
	runs, tests := 0, 0
	for _, j := range groupJobs {
		switch j.Kind {
		case jobs.KindRotateKey:
			runs++
		case jobs.KindConnectionTest:
			tests++
		default:
			t.Fatalf("unexpected kind %q in the rotation group", j.Kind)
		}
	}
	if runs != 2 || tests != 2 {
		t.Fatalf("group = %d runs / %d tests, want 2 / 2", runs, tests)
	}
	// Claim all four. The claim is keyed by zone, but each follow-up test
	// depends on its host's rotate_key run: the two runs come out first,
	// and the tests only once the runs succeeded.
	claims := map[uuid.UUID]*jobs.Claim{}
	claimAll := func() {
		for {
			claim, err := w.queue.Claim(ctx, w.org, []uuid.UUID{w.zone(t)}, "v1")
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if claim == nil {
				return
			}
			claims[uuid.MustParse(claim.Job.GetId())] = claim
		}
	}
	claimAll()
	if len(claims) != 2 {
		t.Fatalf("claimable before the runs finished = %d, want only the 2 rotate_key runs", len(claims))
	}
	for _, c := range claims {
		if c.Job.GetKind() != "rotate_key" {
			t.Fatalf("a %s was claimable before its rotation succeeded", c.Job.GetKind())
		}
	}

	// The playbook runs succeed first; the follow-up tests are still
	// queued → the credential is untouched (the finalize gate counts both
	// kinds and waits for the whole group).
	for _, j := range groupJobs {
		if j.Kind != jobs.KindRotateKey {
			continue
		}
		if err := w.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatalf("finish %s: %v", j.ID, err)
		}
	}
	touched, err := w.credsSvc.Get(w.scoped, w.org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if touched.PublicKey != created.PublicKey || touched.Fingerprint != created.Fingerprint {
		t.Fatalf("the credential must stay untouched while a test is queued: %+v", touched)
	}
	claimAll()
	if len(claims) != len(groupJobs) {
		t.Fatalf("claimed %d of %d group jobs", len(claims), len(groupJobs))
	}

	claimedHosts := map[uuid.UUID]bool{}
	for _, j := range groupJobs {
		claim := claims[j.ID]
		if claim == nil {
			t.Fatalf("job %s was never claimed", j.ID)
		}
		var payload struct {
			Host struct {
				ID       string `json:"id"`
				Address  string `json:"address"`
				Port     int    `json:"port"`
				Username string `json:"username"`
			} `json:"host"`
			Username      string `json:"username"`
			Credential    string `json:"credential"`
			OldCredential string `json:"old_credential"`
			NewPrivateKey string `json:"new_private_key"`
			NewPublicKey  string `json:"new_public_key"`
		}
		if err := json.Unmarshal([]byte(claim.Job.GetKindPayload()), &payload); err != nil {
			t.Fatalf("kind payload of %s: %v\n%s", j.ID, err, claim.Job.GetKindPayload())
		}
		if payload.Host.ID != j.HostIDs[0].String() {
			t.Fatalf("payload host = %q, job host = %s", payload.Host.ID, j.HostIDs[0])
		}
		switch claim.Job.GetKind() {
		case "rotate_key":
			// The playbook run authenticates with the OLD credential and
			// installs the new public key (the private key stays home).
			if payload.Username != "ansible" || !strings.Contains(payload.OldCredential, "private_key") || payload.NewPrivateKey != "" {
				t.Fatalf("rotate claim of %s = %+v", j.ID, payload)
			}
		case "connection_test":
			// The follow-up test authenticates with the PENDING NEW key —
			// the verification the group's finalize gate waits for.
			if !strings.Contains(payload.Credential, "private_key") {
				t.Fatalf("test claim of %s carries no probe credential: %q", j.ID, payload.Credential)
			}
			// The old secret is a different key's PEM; the probe rides
			// the pending one, not what the credential row still holds.
			if len(payload.Credential) == 0 || payload.Credential == string(oldPlain) {
				t.Fatalf("the test must probe with the pending new key, not %q", payload.Credential)
			}
		default:
			t.Fatalf("claim kind = %q", claim.Job.GetKind())
		}
		id, _ := uuid.Parse(payload.Host.ID)
		claimedHosts[id] = true
	}
	if len(claimedHosts) != 2 {
		t.Fatalf("claimed hosts = %v", claimedHosts)
	}

	// Then the follow-up tests succeed; the last finish finalizes exactly
	// once, and the credential's metadata and secret are the NEW ones.
	for _, j := range groupJobs {
		if j.Kind != jobs.KindConnectionTest {
			continue
		}
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
// case the fix round pins: the playbook run succeeds, the follow-up
// connection_test FAILS (the new key did not get the host in) → the
// credential keeps the OLD secret, and the failure is surfaced in the
// group — the test job's error names the host and its outcome lands in the
// host's test history through the same recordTest path every connection
// test takes.
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
	if result.Count != 2 {
		t.Fatalf("rotation result = %+v, want the run plus its follow-up test", result)
	}

	// The group's jobs, in created_at order: the run first, the test after.
	rotateKind := jobs.KindRotateKey
	testKind := jobs.KindConnectionTest
	runs, _, err := w.svc.List(w.scoped, w.org, jobs.Filter{Kind: &rotateKind}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	tests, _, err := w.svc.List(w.scoped, w.org, jobs.Filter{Kind: &testKind}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var run, test jobs.Job
	for _, j := range runs {
		if j.RunGroupID == result.GroupID {
			run = j
		}
	}
	for _, j := range tests {
		if j.RunGroupID == result.GroupID {
			test = j
		}
	}
	if run.ID == uuid.Nil || test.ID == uuid.Nil {
		t.Fatalf("group jobs not found: run %+v test %+v", run, test)
	}

	// The run succeeds (the new key installed); the follow-up test is
	// claimed next and FAILS: the new key did not get the host in. The
	// probe's result event rides the bound stream, exactly like any
	// connection test.
	if _, err := w.queue.Claim(ctx, w.org, []uuid.UUID{run.ZoneID}, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := w.queue.Finish(ctx, run.ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("finish the run: %v", err)
	}
	testClaim, err := w.queue.Claim(ctx, w.org, []uuid.UUID{test.ZoneID}, "v1")
	if err != nil || testClaim == nil {
		t.Fatalf("claim the test: %v, %v", testClaim, err)
	}
	stream, err := w.queue.BindStream(ctx, test.ID, testClaim.Token)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute)
	probeResult := `{"reachable":true,"host_key":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","host_key_state":"unpinned","auth":false,"sudo":false,"python":false,"checks":[{"name":"auth","detail":"unable to authenticate with the new key","ok":false}]}`
	if err := stream.Append(ctx, jobs.Event{Seq: 1, Host: "web-01", Task: "connection_test", State: "failed", Payload: json.RawMessage(probeResult), At: at}); err != nil {
		t.Fatal(err)
	}
	if err := w.queue.Finish(ctx, test.ID, jobs.StatusFailed,
		map[string]any{"failed_hosts": []string{h.ID.String()}},
		"host "+h.ID.String()+" failed"); err != nil {
		t.Fatalf("finish the test: %v", err)
	}

	// The failure is surfaced in the group: the test job's error names the
	// host, and the host's test history records the outcome.
	var jobError string
	if err := w.d.Pool().QueryRow(ctx, `SELECT error FROM jobs WHERE id = $1`, test.ID).Scan(&jobError); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jobError, h.ID.String()) {
		t.Fatalf("test job error = %q, want it to name the failing host", jobError)
	}
	var testCount int
	if err := w.d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_connection_tests WHERE org_id = $1 AND host_id = $2`,
		w.org, h.ID,
	).Scan(&testCount); err != nil {
		t.Fatal(err)
	}
	if testCount != 1 {
		t.Fatalf("host test history rows = %d, want the follow-up test's outcome", testCount)
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

	// Both terminal payloads are cleared.
	for _, j := range []jobs.Job{run, test} {
		var payload []byte
		if err := w.d.Pool().QueryRow(ctx, `SELECT payload FROM jobs WHERE id = $1`, j.ID).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload) != 0 {
			t.Fatalf("payload of %s survived: %s", j.ID, payload)
		}
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
	if _, err := w.queue.Claim(ctx, w.org, []uuid.UUID{w.zone(t)}, "v1"); err != nil {
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
	j := newJob(w.org, w.zone(t), jobs.KindRotateKey, jobs.StatusQueued)
	if err := store.CreateJob(db.WithOrgScope(ctx, w.org), j, json.RawMessage(`{"nope":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.queue.Claim(ctx, w.org, []uuid.UUID{j.ZoneID}, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := w.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("finish with a broken rotation payload: %v", err)
	}

	// A run job with the finisher wired records nothing.
	runJob := newJob(w.org, w.zone(t), jobs.KindRun, jobs.StatusQueued)
	runJob.HostIDs = []uuid.UUID{uuid.Must(uuid.NewV7())}
	if err := store.CreateJob(db.WithOrgScope(ctx, w.org), runJob, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.queue.Claim(ctx, w.org, []uuid.UUID{runJob.ZoneID}, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := w.queue.Finish(ctx, runJob.ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("finish a run job: %v", err)
	}

	// A queued rotate_key job cancelled before its claim: the payload's
	// at-rest lifetime ends immediately.
	cancelled := newJob(w.org, w.zone(t), jobs.KindRotateKey, jobs.StatusQueued)
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

// TestRotationTargetsNeverCrossOrganizations pins the two-org isolation:
// two organizations each with a host and a jump host referencing their OWN
// credential — the org-scoped target queries never return the other org's
// rows, and rotating org A's credential enqueues jobs only for org A's
// targets; org B's host carries no job of the group.
func TestRotationTargetsNeverCrossOrganizations(t *testing.T) {
	w := newRotationStoreWiring(t, "jobs-rotation-two-org")
	// A second organization, seeded through the same services under its
	// own scope: its own credential, host and jump host.
	orgB := uuid.Must(uuid.NewV7())
	if _, err := gen.New(w.d.Pool()).CreateOrganization(ctx, gen.CreateOrganizationParams{ID: orgB, Slug: "jobs-rotation-org-b", Name: "b"}); err != nil {
		t.Fatal(err)
	}
	scopedB := db.WithOrgScope(ctx, orgB)
	if _, err := w.hostsSvc.CreateZone(scopedB, orgB, hosts.DefaultZoneSlug, hosts.DefaultZoneName); err != nil {
		t.Fatal(err)
	}

	credA, err := w.credsSvc.Create(w.scoped, w.org, credentials.CreateInput{
		Name: "acme-a", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	credB, err := w.credsSvc.Create(scopedB, orgB, credentials.CreateInput{
		Name: "acme-b", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostA, err := w.hostsSvc.CreateHost(w.scoped, w.org, hosts.HostInput{Name: "web-a", Address: "10.0.0.1", ZoneID: w.zone(t), Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.sshSvc.SetHostSSH(w.scoped, w.org, hostA.ID, sshconf.HostSSHInput{
		CredentialID: &credA.ID, Port: 22, BastionMode: sshconf.ModeInherit,
	}); err != nil {
		t.Fatal(err)
	}
	hostB, err := w.hostsSvc.CreateHost(scopedB, orgB, hosts.HostInput{Name: "web-b", Address: "10.0.0.2", ZoneID: zoneOf(t, w.hostsSvc, orgB), Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.sshSvc.SetHostSSH(scopedB, orgB, hostB.ID, sshconf.HostSSHInput{
		CredentialID: &credB.ID, Port: 22, BastionMode: sshconf.ModeInherit,
	}); err != nil {
		t.Fatal(err)
	}
	// A jump host per org, each logging in with its own credential.
	bastionB, err := w.sshSvc.CreateBastion(scopedB, orgB, sshconf.BastionInput{
		Name: "jump-b", Address: "10.0.1.2", Port: 22, Username: "jumper", CredentialID: &credB.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	bastionA, err := w.sshSvc.CreateBastion(w.scoped, w.org, sshconf.BastionInput{
		Name: "jump-a", Address: "10.0.1.1", Port: 22, Username: "jumper", CredentialID: &credA.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The org-scoped target queries return exactly the org's own rows.
	hostsA, err := w.queue.ListCredentialHosts(w.scoped, w.org, credA.ID)
	if err != nil || len(hostsA) != 1 || hostsA[0].ID != hostA.ID {
		t.Fatalf("org A hosts = %+v, %v", hostsA, err)
	}
	bastionsA, err := w.queue.ListCredentialBastions(w.scoped, w.org, credA.ID)
	if err != nil || len(bastionsA) != 1 || bastionsA[0].ID != bastionA.ID {
		t.Fatalf("org A bastions = %+v, %v", bastionsA, err)
	}
	hostsSeenFromB, err := w.queue.ListCredentialHosts(scopedB, orgB, credB.ID)
	if err != nil || len(hostsSeenFromB) != 1 || hostsSeenFromB[0].ID != hostB.ID {
		t.Fatalf("org B hosts = %+v, %v", hostsSeenFromB, err)
	}
	bastionsSeenFromB, err := w.queue.ListCredentialBastions(scopedB, orgB, credB.ID)
	if err != nil || len(bastionsSeenFromB) != 1 || bastionsSeenFromB[0].ID != bastionB.ID {
		t.Fatalf("org B bastions = %+v, %v", bastionsSeenFromB, err)
	}

	// Rotating org A's credential enqueues only org A's jobs.
	result, err := w.credsSvc.Rotate(w.scoped, w.org, credA.ID, nil)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	// One run + one follow-up test for the host, one run for the bastion.
	if result.Count != 3 {
		t.Fatalf("rotation result = %+v, want 3 jobs (2 runs + 1 test)", result)
	}
	list, _, err := w.svc.List(w.scoped, w.org, jobs.Filter{}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var groupHosts []uuid.UUID
	for _, j := range list {
		if j.RunGroupID != result.GroupID {
			continue
		}
		if j.OrgID != w.org {
			t.Fatalf("a job of another organization landed in the group: %+v", j)
		}
		for _, id := range j.HostIDs {
			if id == hostB.ID {
				t.Fatalf("org B's host got a job in org A's rotation group: %+v", j)
			}
			groupHosts = append(groupHosts, id)
		}
	}
	if len(groupHosts) != 2 {
		t.Fatalf("the group's host jobs = %v, want exactly org A's host twice (run + test)", groupHosts)
	}
	// Org B's host carries no job at all.
	var orgBJobs int
	if err := w.d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE org_id = $1`, orgB,
	).Scan(&orgBJobs); err != nil {
		t.Fatal(err)
	}
	if orgBJobs != 0 {
		t.Fatalf("org B carries %d jobs after org A's rotation", orgBJobs)
	}
}

// TestCreateJobGroupIsAtomic pins the transaction at the store level: the
// second insert trips the primary key (its id is pre-inserted), the whole
// transaction rolls back, and the FIRST insert leaves nothing behind.
func TestCreateJobGroupIsAtomic(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-rotation-group-atomic")
	store := jobs.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)

	// A pre-inserted row whose id the group's second job will collide with.
	tripID := uuid.Must(uuid.NewV7())
	if err := store.CreateJob(scoped, jobs.Job{
		ID: tripID, OrgID: org, ZoneID: seedZone(t, d, org), Kind: jobs.KindRun,
		Status: jobs.StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now(),
	}, nil); err != nil {
		t.Fatal(err)
	}

	zone := seedZone(t, d, org)
	first := jobs.Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: jobs.KindRotateKey,
		Status: jobs.StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	second := first
	second.ID = tripID // violates jobs_pkey on the second insert
	err := store.CreateJobGroup(ctx, org, []jobs.Job{first, second},
		map[uuid.UUID]json.RawMessage{first.ID: nil, second.ID: nil})
	if err == nil {
		t.Fatal("a duplicate primary key must fail the group insert")
	}
	// The first insert rolled back with the transaction: nothing persists.
	var count int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM jobs WHERE id = $1`, first.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("the first insert survived a failed group insert: %d rows", count)
	}
	var total int
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE org_id = $1`, org,
	).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 { // only the pre-inserted trip row
		t.Fatalf("org carries %d jobs after the failed group insert, want exactly the pre-existing one", total)
	}
}

// TestFinishClearsSystemPayloads covers the at-rest lifetime for the other
// two secret-carrying kinds: a bootstrap job's one-time login and a
// connection_test job's rotation payload both drop at the terminal finish,
// exactly like the rotate_key jobs the rotation loop asserts.
func TestFinishClearsSystemPayloads(t *testing.T) {
	w := newRotationStoreWiring(t, "jobs-rotation-clear-payloads")
	box := crypto.NewBox(testBoxKey("jobs-rotation-store-key"))
	store := jobs.NewSQLStore(w.d)

	created, err := w.credsSvc.Create(w.scoped, w.org, credentials.CreateInput{
		Name: "acme-clear", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := w.hostsSvc.CreateHost(w.scoped, w.org, hosts.HostInput{Name: "clear-01", Address: "10.0.0.1", ZoneID: w.zone(t), Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.sshSvc.SetHostSSH(w.scoped, w.org, h.ID, sshconf.HostSSHInput{
		CredentialID: &created.ID, Port: 22, BastionMode: sshconf.ModeInherit,
	}); err != nil {
		t.Fatal(err)
	}

	payloadOf := func(id uuid.UUID) []byte {
		var payload []byte
		if err := w.d.Pool().QueryRow(ctx, `SELECT payload FROM jobs WHERE id = $1`, id).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	finishCleared := func(j jobs.Job, kind string, status jobs.Status) {
		claim, err := w.queue.Claim(ctx, w.org, []uuid.UUID{j.ZoneID}, "v1")
		if err != nil || claim == nil {
			t.Fatalf("claim %s: %v, %v", kind, claim, err)
		}
		if got := uuid.MustParse(claim.Job.GetId()); got != j.ID {
			t.Fatalf("claim got job %s, want %s", got, j.ID)
		}
		if len(payloadOf(j.ID)) == 0 && kind != "connection_test" {
			t.Fatalf("%s job %s carries no payload before the finish", kind, j.ID)
		}
		if err := w.queue.Finish(ctx, j.ID, status, nil, ""); err != nil {
			t.Fatalf("finish %s: %v", kind, err)
		}
		if p := payloadOf(j.ID); len(p) != 0 {
			t.Fatalf("the terminal %s job's payload survived: %s", kind, p)
		}
	}

	// The bootstrap job: a sealed one-time login, terminal → dropped.
	bootstrap := jobs.Job{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: w.zone(t), Kind: jobs.KindBootstrap,
		HostIDs: []uuid.UUID{h.ID}, Status: jobs.StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	loginPayload, err := json.Marshal(map[string]any{
		"credential_id": created.ID.String(), "login_user": "root", "login": map[string]string{"password": "one-time-pw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateJob(w.scoped, bootstrap, sealFor(box, w.org, bootstrap.ID, loginPayload)); err != nil {
		t.Fatal(err)
	}
	finishCleared(bootstrap, "bootstrap", jobs.StatusSucceeded)

	// The connection_test job: a sealed rotation-shaped payload (what the
	// rotation group's follow-up tests carry), terminal → dropped.
	rotationPayload, err := json.Marshal(map[string]any{
		"credential_id":   created.ID.String(),
		"target":          map[string]any{"kind": "host", "id": h.ID.String()},
		"new_public_key":  "ssh-ed25519 AAAA pending",
		"new_private_key": map[string]string{"private_key": "pending-pem"},
	})
	if err != nil {
		t.Fatal(err)
	}
	probe := jobs.Job{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: w.zone(t), Kind: jobs.KindConnectionTest,
		HostIDs: []uuid.UUID{h.ID}, Status: jobs.StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	if err := store.CreateJob(w.scoped, probe, sealFor(box, w.org, probe.ID, rotationPayload)); err != nil {
		t.Fatal(err)
	}
	finishCleared(probe, "connection_test", jobs.StatusSucceeded)

	// A plain connection_test (no payload) finishes cleanly and stays
	// payload-less.
	plain := jobs.Job{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: w.zone(t), Kind: jobs.KindConnectionTest,
		HostIDs: []uuid.UUID{h.ID}, Status: jobs.StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	if err := store.CreateJob(w.scoped, plain, nil); err != nil {
		t.Fatal(err)
	}
	finishCleared(plain, "connection_test", jobs.StatusSucceeded)
}

// sealFor seals a payload for one job row the way the service does
// (external-test helper; the internal one is white-box only).
func sealFor(box *crypto.Box, org, jobID uuid.UUID, payload json.RawMessage) json.RawMessage {
	sealed, err := box.SealAAD(payload, jobs.JobAAD(org, jobID))
	if err != nil {
		panic(err)
	}
	return json.RawMessage(`"` + base64.StdEncoding.EncodeToString(sealed) + `"`)
}
