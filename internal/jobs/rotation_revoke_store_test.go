// Rotation completes the key's lifecycle (final review I1), against the real
// store: a host's verification test waits for its own rotate_key run (the
// claim gate), a failed run cancels its verification, a fully verified
// group revokes the old public key with a revoke_key job per target —
// logging in with the new key, which is the credential's by then — and a
// second rotation of the same credential is refused while one is still in
// flight.
package jobs_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// rotationFixture is one ssh_key credential referenced by one managed host.
type rotationFixture struct {
	w      rotationStoreWiring
	cred   credentials.Credential
	hostID uuid.UUID
}

func newRotationFixture(t *testing.T, slug string) rotationFixture {
	t.Helper()
	w := newRotationStoreWiring(t, slug)
	cred, err := w.credsSvc.Create(w.scoped, w.org, credentials.CreateInput{
		Name: "deploy", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := w.hostsSvc.CreateHost(w.scoped, w.org, hosts.HostInput{Name: "web-01", Address: "10.0.0.1", ZoneID: w.zone(t), Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.sshSvc.SetHostSSH(w.scoped, w.org, h.ID, sshconf.HostSSHInput{
		CredentialID: &cred.ID, Port: 22, BastionMode: sshconf.ModeInherit,
	}); err != nil {
		t.Fatal(err)
	}
	return rotationFixture{w: w, cred: cred, hostID: h.ID}
}

// groupJobs lists the rotation group's jobs by kind.
func (f rotationFixture) groupJobs(t *testing.T, group uuid.UUID) map[jobs.Kind][]jobs.Job {
	t.Helper()
	list, _, err := f.w.svc.List(f.w.scoped, f.w.org, jobs.Filter{RunGroupID: &group}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	out := map[jobs.Kind][]jobs.Job{}
	for _, j := range list {
		out[j.Kind] = append(out[j.Kind], j)
	}
	return out
}

func (f rotationFixture) claim(t *testing.T) *jobs.Claim {
	t.Helper()
	c, err := f.w.queue.Claim(ctx, f.w.org, []uuid.UUID{f.w.zone(t)}, "v1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRotationVerificationWaitsForItsRotation(t *testing.T) {
	f := newRotationFixture(t, "jobs-rotation-gate")
	result, err := f.w.credsSvc.Rotate(f.w.scoped, f.w.org, f.cred.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	group := f.groupJobs(t, result.GroupID)
	rotate, verify := group[jobs.KindRotateKey][0], group[jobs.KindConnectionTest][0]
	// Put the verification FIRST in claim order: only the gate can keep it
	// from running before the key is installed (with two workers a created_at
	// microsecond is no ordering at all).
	if _, err := f.w.d.Pool().Exec(ctx, `UPDATE jobs SET created_at = created_at - interval '1 hour' WHERE id = $1`, verify.ID); err != nil {
		t.Fatal(err)
	}
	first := f.claim(t)
	if first == nil || first.Job.GetId() != rotate.ID.String() {
		t.Fatalf("first claim = %v, want the rotate_key run %s", first, rotate.ID)
	}
	if second := f.claim(t); second != nil {
		t.Fatalf("the verification %s was claimable while its rotation still runs", second.Job.GetId())
	}
	if err := f.w.queue.Finish(ctx, rotate.ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	if next := f.claim(t); next == nil || next.Job.GetId() != verify.ID.String() {
		t.Fatalf("after the rotation succeeded the claim = %v, want the verification %s", next, verify.ID)
	}
}

func TestRotationFailureCancelsItsVerification(t *testing.T) {
	f := newRotationFixture(t, "jobs-rotation-cancel-dependent")
	result, err := f.w.credsSvc.Rotate(f.w.scoped, f.w.org, f.cred.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	group := f.groupJobs(t, result.GroupID)
	rotate, verify := group[jobs.KindRotateKey][0], group[jobs.KindConnectionTest][0]
	if c := f.claim(t); c == nil || c.Job.GetId() != rotate.ID.String() {
		t.Fatalf("claim = %v", c)
	}
	if err := f.w.queue.Finish(ctx, rotate.ID, jobs.StatusFailed, nil, "could not install"); err != nil {
		t.Fatal(err)
	}
	got, err := f.w.svc.Get(f.w.scoped, f.w.org, verify.ID)
	if err != nil || got.Status != jobs.StatusCancelled || !strings.Contains(got.Error, rotate.ID.String()) {
		t.Fatalf("verification after a failed rotation = %+v, %v; want cancelled naming its dependency", got, err)
	}
	var hasPayload bool
	if err := f.w.d.Pool().QueryRow(ctx, `SELECT payload IS NOT NULL FROM jobs WHERE id = $1`, verify.ID).Scan(&hasPayload); err != nil || hasPayload {
		t.Fatalf("the cancelled verification keeps its sealed pending key at rest (%v)", err)
	}
	// The group ended unsuccessfully: the credential is untouched and no
	// revocation was queued.
	after, err := f.w.credsSvc.Get(f.w.scoped, f.w.org, f.cred.ID)
	if err != nil || after.PublicKey != f.cred.PublicKey {
		t.Fatalf("credential after a failed rotation = %+v, %v", after, err)
	}
	if revokes := f.groupJobs(t, result.GroupID)[jobs.KindRevokeKey]; len(revokes) != 0 {
		t.Fatalf("a failed rotation queued %d revocation(s)", len(revokes))
	}
	if next := f.claim(t); next != nil {
		t.Fatalf("something is still claimable: %s %s", next.Job.GetKind(), next.Job.GetId())
	}
}

func TestRotationRevokesTheOldKeyOnlyAfterVerification(t *testing.T) {
	f := newRotationFixture(t, "jobs-rotation-revoke")
	result, err := f.w.credsSvc.Rotate(f.w.scoped, f.w.org, f.cred.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	group := f.groupJobs(t, result.GroupID)
	rotate, verify := group[jobs.KindRotateKey][0], group[jobs.KindConnectionTest][0]
	for _, j := range []jobs.Job{rotate, verify} {
		if c := f.claim(t); c == nil || c.Job.GetId() != j.ID.String() {
			t.Fatalf("claim = %v, want %s", c, j.ID)
		}
		if revokes := f.groupJobs(t, result.GroupID)[jobs.KindRevokeKey]; len(revokes) != 0 {
			t.Fatalf("revocation queued before the group was verified")
		}
		if err := f.w.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	rotated, err := f.w.credsSvc.Get(f.w.scoped, f.w.org, f.cred.ID)
	if err != nil || rotated.PublicKey == f.cred.PublicKey {
		t.Fatalf("credential after the verified rotation = %+v, %v; want the new key sealed", rotated, err)
	}
	revokes := f.groupJobs(t, result.GroupID)[jobs.KindRevokeKey]
	if len(revokes) != 1 || revokes[0].HostIDs[0] != f.hostID || revokes[0].Status != jobs.StatusQueued {
		t.Fatalf("revocations = %+v, want one queued for the host", revokes)
	}

	claim := f.claim(t)
	if claim == nil || claim.Job.GetKind() != string(jobs.KindRevokeKey) {
		t.Fatalf("claim = %v, want the revocation", claim)
	}
	var payload struct {
		Host struct {
			ID string `json:"id"`
		} `json:"host"`
		Username        string `json:"username"`
		LoginCredential string `json:"login_credential"`
		OldPublicKey    string `json:"old_public_key"`
	}
	if err := json.Unmarshal([]byte(claim.Job.GetKindPayload()), &payload); err != nil {
		t.Fatal(err)
	}
	newSecret, err := f.w.credsSvc.OpenSecret(f.w.scoped, f.w.org, f.cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantLogin, err := json.Marshal(newSecret)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Host.ID != f.hostID.String() || payload.OldPublicKey != f.cred.PublicKey || payload.Username != "ansible" ||
		payload.LoginCredential != string(wantLogin) {
		t.Fatalf("revocation payload = host %s user %q old key %q; want the host, the account, the PREVIOUS key and the new login",
			payload.Host.ID, payload.Username, payload.OldPublicKey)
	}
	if strings.Contains(claim.Job.GetKindPayload(), rotated.PublicKey) {
		t.Fatal("the revocation must never name the new public key")
	}
	// A revocation that fails leaves the (already verified, already sealed)
	// credential alone.
	if err := f.w.queue.Finish(ctx, uuid.MustParse(claim.Job.GetId()), jobs.StatusFailed, nil, "unreachable"); err != nil {
		t.Fatal(err)
	}
	if still, _ := f.w.credsSvc.Get(f.w.scoped, f.w.org, f.cred.ID); still.PublicKey != rotated.PublicKey {
		t.Fatalf("a failed revocation changed the credential: %+v", still)
	}
}

func TestRotationRefusedWhileOneIsInFlight(t *testing.T) {
	f := newRotationFixture(t, "jobs-rotation-exclusive")
	first, err := f.w.credsSvc.Rotate(f.w.scoped, f.w.org, f.cred.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.w.credsSvc.Rotate(f.w.scoped, f.w.org, f.cred.ID, nil); !errors.Is(err, jobs.ErrRotationInProgress) {
		t.Fatalf("second Rotate = %v, want ErrRotationInProgress", err)
	}
	// Run the first group to its end, revocation included.
	for {
		c := f.claim(t)
		if c == nil {
			break
		}
		if err := f.w.queue.Finish(ctx, uuid.MustParse(c.Job.GetId()), jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	for kind, js := range f.groupJobs(t, first.GroupID) {
		for _, j := range js {
			if !j.Status.Terminal() {
				t.Fatalf("%s %s still %s", kind, j.ID, j.Status)
			}
		}
	}
	if _, err := f.w.credsSvc.Rotate(f.w.scoped, f.w.org, f.cred.ID, nil); err != nil {
		t.Fatalf("Rotate after the first rotation ended = %v", err)
	}
}

// TestRotationRevokesOnJumpHostsToo: a jump host referencing the credential
// gets its own revocation, in the organization's default zone, once the
// whole group verified.
func TestRotationRevokesOnJumpHostsToo(t *testing.T) {
	f := newRotationFixture(t, "jobs-rotation-revoke-bastion")
	bastion, err := f.w.sshSvc.CreateBastion(f.w.scoped, f.w.org, sshconf.BastionInput{
		Name: "jump", Address: "10.0.0.9", Username: "ops", CredentialID: &f.cred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.w.credsSvc.Rotate(f.w.scoped, f.w.org, f.cred.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for {
		c := f.claim(t)
		if c == nil {
			break
		}
		if c.Job.GetKind() == string(jobs.KindRevokeKey) {
			continue // leave the revocations queued-then-running; only count them
		}
		if err := f.w.queue.Finish(ctx, uuid.MustParse(c.Job.GetId()), jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	revokes := f.groupJobs(t, result.GroupID)[jobs.KindRevokeKey]
	if len(revokes) != 2 {
		t.Fatalf("revocations = %d, want the host and the jump host", len(revokes))
	}
	var bastionRevoke *jobs.Job
	for i := range revokes {
		if len(revokes[i].HostIDs) == 0 {
			bastionRevoke = &revokes[i]
		}
	}
	if bastionRevoke == nil || bastionRevoke.ZoneID != f.w.zone(t) || bastionRevoke.Trigger != jobs.TriggerSystem {
		t.Fatalf("jump-host revocation = %+v (bastion %s)", bastionRevoke, bastion.ID)
	}
}

// driveRotation finishes every non-revocation job of the group
// successfully; the revocations it leaves queued-then-running are the
// outcome under test, so they are skipped.
func (f rotationFixture) driveRotation(t *testing.T, group uuid.UUID) {
	t.Helper()
	for {
		c := f.claim(t)
		if c == nil {
			return
		}
		if c.Job.GetKind() == string(jobs.KindRevokeKey) {
			continue
		}
		if err := f.w.queue.Finish(ctx, uuid.MustParse(c.Job.GetId()), jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
}

// revokeHosts collects the group's revocation jobs' single host ids.
func revokeHosts(t *testing.T, f rotationFixture, group uuid.UUID) []uuid.UUID {
	t.Helper()
	revokes := f.groupJobs(t, group)[jobs.KindRevokeKey]
	out := make([]uuid.UUID, 0, len(revokes))
	for _, r := range revokes {
		if len(r.HostIDs) != 1 {
			t.Fatalf("revocation %s carries host ids %v, want exactly one", r.ID, r.HostIDs)
		}
		out = append(out, r.HostIDs[0])
	}
	return out
}

// TestRevocationTargetsAreTheEnqueuedSnapshot pins backlog 2.46: the
// revocation phase reads its targets from the SNAPSHOT taken at enqueue
// time, not from the references as they stand when the group verifies — a
// host that stopped referencing the credential mid-rotation is still
// revoked, because the OLD key is still in its authorized_keys and must be
// removed (removing a stale entry for a key that no longer authenticates
// anything is harmless; leaving it is not).
func TestRevocationTargetsAreTheEnqueuedSnapshot(t *testing.T) {
	f := newRotationFixture(t, "jobs-rotation-revoke-snapshot")
	h2, err := f.w.hostsSvc.CreateHost(f.w.scoped, f.w.org, hosts.HostInput{
		Name: "web-02", Address: "10.0.0.2", ZoneID: f.w.zone(t), Managed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.w.sshSvc.SetHostSSH(f.w.scoped, f.w.org, h2.ID, sshconf.HostSSHInput{
		CredentialID: &f.cred.ID, Port: 22, BastionMode: sshconf.ModeInherit,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := f.w.credsSvc.Rotate(f.w.scoped, f.w.org, f.cred.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The snapshot exists at enqueue time and names exactly the two hosts.
	var targets []byte
	if err := f.w.d.Pool().QueryRow(ctx,
		`SELECT targets FROM rotation_groups WHERE org_id = $1 AND id = $2`, f.w.org, result.GroupID,
	).Scan(&targets); err != nil {
		t.Fatalf("read the rotation's snapshot: %v", err)
	}
	var snap struct {
		HostIDs    []uuid.UUID `json:"host_ids"`
		BastionIDs []uuid.UUID `json:"bastion_ids"`
	}
	if err := json.Unmarshal(targets, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.HostIDs) != 2 || len(snap.BastionIDs) != 0 ||
		!uuidIn(snap.HostIDs, f.hostID) || !uuidIn(snap.HostIDs, h2.ID) {
		t.Fatalf("snapshot = %s, want exactly the two referencing hosts", targets)
	}

	// Mid-rotation, web-02 stops referencing the credential.
	if _, err := f.w.sshSvc.SetHostSSH(f.w.scoped, f.w.org, h2.ID, sshconf.HostSSHInput{
		Port: 22, BastionMode: sshconf.ModeInherit,
	}); err != nil {
		t.Fatal(err)
	}
	refs, err := f.w.queue.ListCredentialHosts(f.w.scoped, f.w.org, f.cred.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("live references after the drop = %+v, %v; want only web-01", refs, err)
	}

	f.driveRotation(t, result.GroupID)
	got := revokeHosts(t, f, result.GroupID)
	if len(got) != 2 || !uuidIn(got, f.hostID) || !uuidIn(got, h2.ID) {
		t.Fatalf("revocation targets = %v, want exactly the SNAPSHOT's two hosts (the dropped reference too)", got)
	}
}

// TestRevocationFallsBackToLiveTargetsForLegacyGroups pins the upgrade
// path: a rotation group enqueued before the snapshot existed has no
// rotation_groups row — its revocations resolve the live reference lists
// (the accepted pre-2.46 semantics) instead of revoking nothing.
func TestRevocationFallsBackToLiveTargetsForLegacyGroups(t *testing.T) {
	f := newRotationFixture(t, "jobs-rotation-revoke-legacy")
	result, err := f.w.credsSvc.Rotate(f.w.scoped, f.w.org, f.cred.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the pre-migration group: no snapshot row at all.
	if _, err := f.w.d.Pool().Exec(ctx,
		`DELETE FROM rotation_groups WHERE org_id = $1 AND id = $2`, f.w.org, result.GroupID,
	); err != nil {
		t.Fatal(err)
	}
	f.driveRotation(t, result.GroupID)
	got := revokeHosts(t, f, result.GroupID)
	if len(got) != 1 || got[0] != f.hostID {
		t.Fatalf("legacy-group revocations = %v, want the host's live reference", got)
	}
}

// TestRevocationSkipsADeletedSnapshotTarget: a snapshot target whose host
// row is gone by verification time has nothing left to revoke — its
// authorized_keys left with the host. The surviving snapshot targets still
// revoke; the finalize does not fail. The host is deleted AFTER its own
// jobs verified: a host deleted while its jobs are still queued errors
// them at claim time and dooms the group instead (the claim's payload
// build needs the row).
func TestRevocationSkipsADeletedSnapshotTarget(t *testing.T) {
	f := newRotationFixture(t, "jobs-rotation-revoke-deleted")
	h2, err := f.w.hostsSvc.CreateHost(f.w.scoped, f.w.org, hosts.HostInput{
		Name: "web-02", Address: "10.0.0.2", ZoneID: f.w.zone(t), Managed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.w.sshSvc.SetHostSSH(f.w.scoped, f.w.org, h2.ID, sshconf.HostSSHInput{
		CredentialID: &f.cred.ID, Port: 22, BastionMode: sshconf.ModeInherit,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := f.w.credsSvc.Rotate(f.w.scoped, f.w.org, f.cred.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Finish web-02's jobs, hold the others' claims: the deleted host's
	// snapshot entry stays behind while the row is gone.
	var held []uuid.UUID
	for {
		c := f.claim(t)
		if c == nil {
			break
		}
		id := uuid.MustParse(c.Job.GetId())
		if jobHost(t, f, result.GroupID, id) == h2.ID {
			if err := f.w.queue.Finish(ctx, id, jobs.StatusSucceeded, nil, ""); err != nil {
				t.Fatal(err)
			}
			continue
		}
		held = append(held, id)
	}
	if err := f.w.hostsSvc.DeleteHost(f.w.scoped, f.w.org, h2.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range held {
		if err := f.w.queue.Finish(ctx, id, jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	f.driveRotation(t, result.GroupID)

	got := revokeHosts(t, f, result.GroupID)
	if len(got) != 1 || got[0] != f.hostID {
		t.Fatalf("revocations = %v, want only the surviving snapshot target", got)
	}
}

// jobHost reads a group job's single host id.
func jobHost(t *testing.T, f rotationFixture, group, id uuid.UUID) uuid.UUID {
	t.Helper()
	for _, js := range f.groupJobs(t, group) {
		for _, j := range js {
			if j.ID == id {
				return j.HostIDs[0]
			}
		}
	}
	t.Fatalf("job %s not found in group %s", id, group)
	return uuid.Nil
}

// TestRotationGroupsNeverCrossOrganizations: the rotation_groups row is
// org-owned by its row-level security pair — reading the snapshot under
// another organization's scope finds nothing, even with the group id in
// hand; the owner's own scope reads it.
func TestRotationGroupsNeverCrossOrganizations(t *testing.T) {
	f := newRotationFixture(t, "jobs-rotation-groups-orgs")
	result, err := f.w.credsSvc.Rotate(f.w.scoped, f.w.org, f.cred.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	other := db.WithOrgScope(ctx, uuid.Must(uuid.NewV7()))
	if _, err := gen.New(f.w.d).GetRotationGroupTargets(other, gen.GetRotationGroupTargetsParams{
		OrgID: uuid.Must(uuid.NewV7()), ID: result.GroupID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("another org's read of the snapshot = %v, want nothing", err)
	}
	if _, err := gen.New(f.w.d).GetRotationGroupTargets(f.w.scoped, gen.GetRotationGroupTargetsParams{
		OrgID: f.w.org, ID: result.GroupID,
	}); err != nil {
		t.Fatalf("the owner's read of the snapshot: %v", err)
	}
}

// TestRotationWithoutReferencesSnapshotsNothing: a credential referenced
// by nothing re-keys immediately — no group, therefore no snapshot row.
func TestRotationWithoutReferencesSnapshotsNothing(t *testing.T) {
	w := newRotationStoreWiring(t, "jobs-rotation-no-refs")
	cred, err := w.credsSvc.Create(w.scoped, w.org, credentials.CreateInput{
		Name: "lonely", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := w.credsSvc.Rotate(w.scoped, w.org, cred.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 0 || result.GroupID != uuid.Nil {
		t.Fatalf("rotation result = %+v, want the immediate re-key", result)
	}
	var n int
	if err := w.d.Pool().QueryRow(ctx, `SELECT count(*) FROM rotation_groups`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rotation_groups rows = %d, want none", n)
	}
}

func uuidIn(list []uuid.UUID, id uuid.UUID) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}
