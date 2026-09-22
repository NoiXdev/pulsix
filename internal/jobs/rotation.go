// Credential rotation (spec §5, backlog 2.7): the enqueue half and the
// finish half. EnqueueRotation is the concrete implementation of
// credentials.RotationEnqueuer (declared over there as a narrow port, to
// keep the import arrow jobs → credentials): it lists the managed hosts
// and jump hosts referencing the credential and inserts one rotate_key job
// per target plus one follow-up connection_test job per HOST target — the
// group's verification, authenticating with the pending new key — all
// sharing one run group and inserted inside ONE transaction (a failure
// halfway leaves nothing behind; an orphaned partial group could run and
// finalize on its own). Every job's payload carries the pending new key
// SEALED with the box (AAD job:<orgID>:<jobID>) before insert — the
// pending key never exists anywhere else in the clear, not even in a table
// column.
//
// The finish half rides SQLQueue.Finish (recordRotation): when the last
// job of a rotation group — rotate_key OR the follow-up connection_test —
// reports terminal, the outcome flows through the RotationFinisher port to
// credentials.Service.FinishRotation, which seals the new key onto the
// credential (all succeeded) or leaves it untouched (anything else — a
// failed rotation playbook, or a follow-up test that could not get in with
// the new key). Double-finalization is safe by value: two finishes
// observing the same completed group seal the identical key.
package jobs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// rotationTargetKind names the side of a rotation target.
const (
	rotationTargetHost    = "host"
	rotationTargetBastion = "bastion"
)

// rotationJobPayload is the sealed kind payload one rotate_key job
// carries: which credential rotates, which target this job installs the
// new key on, and the pending new key material (sealed with the rest).
type rotationJobPayload struct {
	CredentialID   uuid.UUID          `json:"credential_id"`
	Target         rotationTarget     `json:"target"`
	NewPublicKey   string             `json:"new_public_key"`
	NewFingerprint string             `json:"new_fingerprint"`
	NewKeyType     string             `json:"new_key_type"`
	NewPrivateKey  credentials.Secret `json:"new_private_key"`
	// OldPublicKey is the credential's public key when the rotation
	// started — what the group's revocations remove once it is verified.
	OldPublicKey string `json:"old_public_key"`
}

// revocationJobPayload is the sealed payload of one revoke_key job: which
// credential rotated, the target, and the previous public key to remove.
// The login is the credential's CURRENT secret (the new key, sealed onto it
// by the finalize that enqueued the revocation) — no key material rides in
// the payload itself.
type revocationJobPayload struct {
	CredentialID uuid.UUID      `json:"credential_id"`
	Target       rotationTarget `json:"target"`
	OldPublicKey string         `json:"old_public_key"`
}

type rotationTarget struct {
	Kind string    `json:"kind"`
	ID   uuid.UUID `json:"id"`
}

// rotationTargetSnapshot is the persisted revocation-target list (backlog
// 2.46): the referencing host and jump host ids as they stood at enqueue
// time, stored in the group's rotation_groups row inside the enqueue's own
// transaction. The revoke_key phase reads ITS targets from here — not from
// the live reference lists — because the old key is still in a dropped
// target's authorized_keys and must be removed: revoking a key that no
// longer authenticates anything is harmless, leaving it is not. Ids only;
// no keys, no secrets.
type rotationTargetSnapshot struct {
	HostIDs    []uuid.UUID `json:"host_ids"`
	BastionIDs []uuid.UUID `json:"bastion_ids"`
}

// ListCredentialHosts reads the managed hosts referencing a credential,
// org-scoped (the caller's ctx carries the scope — the service passes the
// request's own).
func (q *SQLQueue) ListCredentialHosts(ctx context.Context, orgID, credentialID uuid.UUID) ([]RotationHost, error) {
	rows, err := gen.New(q.db).ListCredentialHosts(ctx, gen.ListCredentialHostsParams{
		OrgID: orgID, CredentialID: pgtype.UUID{Bytes: credentialID, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list rotation hosts: %w", err)
	}
	out := make([]RotationHost, 0, len(rows))
	for _, r := range rows {
		out = append(out, RotationHost{ID: r.ID, ZoneID: r.ZoneID})
	}
	return out, nil
}

// ListCredentialBastions reads the jump hosts referencing a credential.
func (q *SQLQueue) ListCredentialBastions(ctx context.Context, orgID, credentialID uuid.UUID) ([]sshconf.Bastion, error) {
	rows, err := gen.New(q.db).ListCredentialBastions(ctx, gen.ListCredentialBastionsParams{
		OrgID: orgID, CredentialID: pgtype.UUID{Bytes: credentialID, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list rotation bastions: %w", err)
	}
	out := make([]sshconf.Bastion, 0, len(rows))
	for _, r := range rows {
		out = append(out, sshconf.Bastion{
			ID: r.ID, OrgID: r.OrgID, Name: r.Name, Address: r.Address, Port: int(r.Port),
			Username: r.Username, CredentialID: db.UUIDPtr(r.CredentialID), ViaBastionID: db.UUIDPtr(r.ViaBastionID),
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		})
	}
	return out, nil
}

// ListOrgBastions reads every jump host of the organization (ids only on
// the wire that matters): the rotation enqueue walks each target's
// via-chain over them, so a hop that does not itself reference the rotating
// credential is still checked and still dialed.
func (q *SQLQueue) ListOrgBastions(ctx context.Context, orgID uuid.UUID) ([]sshconf.Bastion, error) {
	rows, err := gen.New(q.db).ListBastions(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list bastions: %w", err)
	}
	out := make([]sshconf.Bastion, 0, len(rows))
	for _, r := range rows {
		out = append(out, sshconf.Bastion{
			ID: r.ID, OrgID: r.OrgID, Name: r.Name, Address: r.Address, Port: int(r.Port),
			Username: r.Username, CredentialID: db.UUIDPtr(r.CredentialID), ViaBastionID: db.UUIDPtr(r.ViaBastionID),
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		})
	}
	return out, nil
}

// SetRotationFinisher injects the port recordRotation flows through on the
// finish path, the rotation sibling of SetSyncSink/SetTestSink. A nil
// finisher finalizes nothing — a wiring gap an operator sees as a
// credential that never changes, never as a failed job.
func (q *SQLQueue) SetRotationFinisher(finisher RotationFinisher) { q.rotation = finisher }

// groupInserter is the store capability the rotation enqueue needs: the
// whole group's inserts inside one transaction — the rotation_groups
// snapshot row with them (backlog 2.46) — refused with
// ErrRotationInProgress while an earlier rotation group of the credential
// is not finished. The real SQLStore implements it; a store that does not
// makes the enqueue fail closed — per-row inserts would leave an orphaned
// partial group on a mid-loop failure, whose jobs could run and finalize
// on their own.
type groupInserter interface {
	CreateRotationGroup(ctx context.Context, orgID, credentialID, groupID uuid.UUID, targets json.RawMessage, jobs []Job, payloads map[uuid.UUID]json.RawMessage) error
}

// EnqueueRotation enqueues the credential's rotation group: one rotate_key
// job per managed host and jump host referencing it, plus one follow-up
// connection_test job per HOST target — the group's verification, which
// authenticates with the pending new key (sealed in the test job's own
// payload) — all sharing one run group and all inserted inside one
// transaction. Host targets claim in the host's zone; bastion targets
// claim in the organization's default zone (bastions are not zone-bound
// and have no host-bound test history, so a bastion target verifies only
// through its playbook run). The follow-up tests carry a strictly later
// created_at (one microsecond per job) so the claim order puts every
// playbook run before the tests that verify it. Returns the group id, the
// group's first job id (a rotate_key job) and the job count — count 0
// means nothing references the credential (the caller re-keys it
// immediately).
func (s *Service) EnqueueRotation(ctx context.Context, orgID, credentialID uuid.UUID, key credentials.PendingKey, actor *uuid.UUID) (uuid.UUID, uuid.UUID, int, error) {
	if s.rotationTargets == nil {
		return uuid.Nil, uuid.Nil, 0, fmt.Errorf("rotation enqueue: no rotation target source wired")
	}
	hosts, err := s.rotationTargets.ListCredentialHosts(ctx, orgID, credentialID)
	if err != nil {
		return uuid.Nil, uuid.Nil, 0, fmt.Errorf("list rotation hosts: %w", err)
	}
	bastions, err := s.rotationTargets.ListCredentialBastions(ctx, orgID, credentialID)
	if err != nil {
		return uuid.Nil, uuid.Nil, 0, fmt.Errorf("list rotation bastions: %w", err)
	}
	if len(hosts) == 0 && len(bastions) == 0 {
		return uuid.Nil, uuid.Nil, 0, nil
	}
	// A rotation logs into every host target over SSH: a host whose key
	// changed refuses the whole rotation (a partial one would leave the
	// credential's key diverged across hosts).
	hostIDs := make([]uuid.UUID, len(hosts))
	for i, h := range hosts {
		hostIDs[i] = h.ID
	}
	// The revocation targets snapshot (backlog 2.46): the references as
	// they stand NOW, written into the group's rotation_groups row inside
	// the same transaction as the jobs. The revoke_key phase targets
	// exactly these, so a reference dropped mid-rotation is still revoked.
	bastionIDs := make([]uuid.UUID, len(bastions))
	for i, b := range bastions {
		bastionIDs[i] = b.ID
	}
	snapshot, err := json.Marshal(rotationTargetSnapshot{HostIDs: hostIDs, BastionIDs: bastionIDs})
	if err != nil {
		return uuid.Nil, uuid.Nil, 0, fmt.Errorf("encode rotation targets: %w", err)
	}
	if err := s.refuseChangedHosts(ctx, orgID, hostIDs); err != nil {
		return uuid.Nil, uuid.Nil, 0, err
	}
	// The pin requirement (backlog 2.44) refuses the rotation when a HOST
	// target is not fully pinned; the jump hosts are pinned too since
	// backlog 2.40. And a rotation INTO a jump host must not be routed
	// through a changed one: a bastion target whose via-chain includes a
	// changed jump host refuses the rotation — fail closed, the playbook
	// dial cannot trust the hop it tunnels through. An unwired guard skips
	// the check here, the same convention the host key guard follows at
	// enqueue; the claim-time guard is mandatory and is what actually
	// refuses.
	if err := s.refuseUnpinnedHosts(ctx, orgID, hostIDs); err != nil {
		return uuid.Nil, uuid.Nil, 0, err
	}
	if s.bastionKeyGuard != nil && len(bastions) > 0 {
		all, err := s.rotationTargets.ListOrgBastions(ctx, orgID)
		if err != nil {
			return uuid.Nil, uuid.Nil, 0, fmt.Errorf("list bastions: %w", err)
		}
		allByID := make(map[uuid.UUID]sshconf.Bastion, len(all))
		for _, b := range all {
			allByID[b.ID] = b
		}
		ids := make([]uuid.UUID, 0, len(bastions))
		for _, b := range bastions {
			ids = append(ids, b.ID)
		}
		changed, err := s.bastionKeyGuard.ChangedBastions(ctx, orgID, ids)
		if err != nil {
			return uuid.Nil, uuid.Nil, 0, fmt.Errorf("resolve changed bastion keys: %w", err)
		}
		// The via-chain of each target, walked over the org's full set: a
		// chain hop that does not itself reference the rotating credential
		// is still checked (and still dialed).
		for _, b := range bastions {
			chain, err := sshconf.Chain(allByID, b.ID)
			if err != nil {
				// A broken via-chain cannot be walked: refuse closed (the
				// rotation must not dial an unresolvable route).
				return uuid.Nil, uuid.Nil, 0, fmt.Errorf("resolve rotation bastion %s: %w", b.ID, err)
			}
			for _, hop := range chain {
				if changed[hop.ID] {
					return uuid.Nil, uuid.Nil, 0, fmt.Errorf("%w: jump host %s", ErrHostKeyChanged, hop.ID)
				}
			}
		}
	}
	inserter, ok := s.store.(groupInserter)
	if !ok {
		return uuid.Nil, uuid.Nil, 0, fmt.Errorf("rotation enqueue: the store cannot insert the group atomically")
	}
	group := s.newID()
	now := s.now()
	var jobs []Job
	payloads := make(map[uuid.UUID]json.RawMessage, 2*len(hosts)+len(bastions))
	// appendJob builds one group job with its sealed rotation payload.
	// CreatedAt advances one microsecond per job, so the claim's
	// created_at order always puts the playbook runs before the follow-up
	// tests that verify them.
	appendJob := func(kind Kind, zone uuid.UUID, hostIDs []uuid.UUID, target rotationTarget, dependsOn *uuid.UUID) (uuid.UUID, error) {
		credID := credentialID
		j := Job{
			ID: s.newID(), OrgID: orgID, ZoneID: zone,
			Kind: kind, HostIDs: hostIDs,
			Status: StatusQueued, RunGroupID: group, Trigger: TriggerManual,
			ActorID: actor, CreatedAt: now.Add(time.Duration(len(jobs)) * time.Microsecond),
			DependsOn: dependsOn, RotationCredentialID: &credID,
		}
		payload, err := json.Marshal(rotationJobPayload{
			CredentialID: credentialID, Target: target,
			NewPublicKey: key.PublicKey, NewFingerprint: key.Fingerprint, NewKeyType: key.KeyType,
			NewPrivateKey: key.PrivateKey, OldPublicKey: key.PreviousPublicKey,
		})
		if err != nil {
			return uuid.Nil, fmt.Errorf("encode rotation payload: %w", err)
		}
		stored, err := s.sealPayload(j, payload)
		if err != nil {
			return uuid.Nil, err
		}
		payloads[j.ID] = stored
		jobs = append(jobs, j)
		return j.ID, nil
	}
	// The playbook runs first: one per host target, in the host's zone.
	rotateJobOf := make(map[uuid.UUID]uuid.UUID, len(hosts))
	for _, h := range hosts {
		id, err := appendJob(KindRotateKey, h.ZoneID, []uuid.UUID{h.ID}, rotationTarget{Kind: rotationTargetHost, ID: h.ID}, nil)
		if err != nil {
			return uuid.Nil, uuid.Nil, 0, err
		}
		rotateJobOf[h.ID] = id
	}
	// Then the verification: one connection_test per host target, same
	// group and zone, whose sealed payload carries the pending new key the
	// probe authenticates with. Each depends on its host's rotate_key run:
	// the claim holds it back until that run succeeded (with several
	// workers a created_at offset alone orders nothing), and a run that
	// ends otherwise cancels it.
	for _, h := range hosts {
		rotateID := rotateJobOf[h.ID]
		if _, err := appendJob(KindConnectionTest, h.ZoneID, []uuid.UUID{h.ID}, rotationTarget{Kind: rotationTargetHost, ID: h.ID}, &rotateID); err != nil {
			return uuid.Nil, uuid.Nil, 0, err
		}
	}
	// One job per bastion target, in the default zone: a bastion is
	// organization-level infrastructure, not bound to a host's zone, and
	// has no host-bound connection-test history.
	if len(bastions) > 0 {
		zone, err := s.defaultZone(ctx, orgID)
		if err != nil {
			return uuid.Nil, uuid.Nil, 0, fmt.Errorf("resolve rotation zone: %w", err)
		}
		for _, b := range bastions {
			if _, err := appendJob(KindRotateKey, zone, []uuid.UUID{}, rotationTarget{Kind: rotationTargetBastion, ID: b.ID}, nil); err != nil {
				return uuid.Nil, uuid.Nil, 0, err
			}
		}
	}
	if err := inserter.CreateRotationGroup(ctx, orgID, credentialID, group, snapshot, jobs, payloads); err != nil {
		return uuid.Nil, uuid.Nil, 0, fmt.Errorf("insert rotation group: %w", err)
	}
	return group, jobs[0].ID, len(jobs), nil
}

// recordRotation finalizes a rotation group whose last job just went
// terminal, inside Finish's transaction (ctx carries it; Finish holds the
// group's advisory lock, so the completeness count sees every other
// member's committed status): the group is complete when every job of it
// — the rotate_key playbook runs AND the follow-up connection_test
// verifications — is terminal, and allSucceeded only when every one of
// them succeeded. A cancelled or failed job (a playbook that could not
// install the key, or a test that could not get in with it) leaves the
// credential untouched. A connection_test job that carries no rotation
// payload is a plain test, not a group member — skipped silently, the
// common case. A payload that cannot be opened or a credential deleted
// meanwhile is logged and skipped (a retry could never fix it); a database
// or finisher failure is returned and rolls the whole finish back, so the
// retry finalizes. The finished job's payload (sealed) is still on the row
// at this point; clearing happens after the sinks.
func (q *SQLQueue) recordRotation(ctx context.Context, jobID uuid.UUID, status Status) error {
	if q.rotation == nil || status == StatusCancelled {
		return nil
	}
	var orgID, groupID uuid.UUID
	var kind string
	var payload json.RawMessage
	if err := q.db.QueryRow(ctx,
		`SELECT org_id, kind, payload, run_group_id FROM jobs WHERE id = $1`, jobID,
	).Scan(&orgID, &kind, &payload, &groupID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("finalize rotation: look up finished job: %w", err)
	}
	switch Kind(kind) {
	case KindRotateKey, KindConnectionTest:
	default:
		return nil
	}
	plain, err := q.openKindPayload(Job{OrgID: orgID, ID: jobID, Payload: payload})
	if err != nil {
		slog.ErrorContext(ctx, "finalize rotation: open job payload", "err", err, "job", jobID)
		return nil
	}
	var rp rotationJobPayload
	if err := json.Unmarshal([]byte(plain), &rp); err != nil || rp.CredentialID == uuid.Nil {
		// A connection_test job without a rotation payload is a plain
		// test — the everyday case, silently out. A rotate_key job whose
		// payload carries no credential is an anomaly worth a log line.
		if Kind(kind) == KindRotateKey {
			slog.ErrorContext(ctx, "finalize rotation: job payload carries no credential", "job", jobID)
		}
		return nil
	}

	var total, terminal, succeeded int
	if err := q.db.QueryRow(ctx,
		`SELECT count(*),
			count(*) FILTER (WHERE status IN ('succeeded','failed','cancelled','error')),
			count(*) FILTER (WHERE status = 'succeeded')
		FROM jobs WHERE org_id = $1 AND run_group_id = $2 AND kind IN ('rotate_key','connection_test')`,
		orgID, groupID,
	).Scan(&total, &terminal, &succeeded); err != nil {
		return fmt.Errorf("finalize rotation: count group: %w", err)
	}
	if total == 0 || terminal < total {
		return nil // the group is not done yet
	}
	allSucceeded := succeeded == total
	if err := q.rotation.FinishRotation(ctx, orgID, rp.CredentialID, groupID, allSucceeded,
		rp.NewPrivateKey, rp.NewPublicKey, rp.NewFingerprint, rp.NewKeyType); err != nil {
		if sinkGone(err) {
			slog.WarnContext(ctx, "finalize rotation: credential no longer exists", "group", groupID)
			return nil
		}
		return fmt.Errorf("finalize rotation: %w", err)
	}
	if !allSucceeded {
		return nil
	}
	return q.enqueueRevocations(ctx, orgID, groupID, rp)
}

// enqueueRevocations queues the group's last phase once every rotation and
// verification succeeded and the new key is sealed onto the credential:
// one revoke_key job per target of the group's SNAPSHOT (backlog 2.46 —
// the referencing hosts and jump hosts as they stood at enqueue time, so a
// target that stopped referencing the credential mid-rotation is still
// revoked), removing the PREVIOUS public key from its authorized_keys.
// Removing it only now is what keeps a partial rotation safe: while any
// host could not be verified, every host keeps the old key the credential
// still holds. The revocations belong to the group (and its credential),
// log in with the credential's current secret — the new key — and run
// inside the finish's transaction, so a crash either finalizes and queues
// them together or does neither. A group that already has revocations (a
// second finalize observing the completed group) queues none; a rotation
// without a recorded previous key (enqueued before revocations existed)
// queues none either; a group without a snapshot row (enqueued before the
// snapshot existed) falls back to the live reference lists, the accepted
// pre-2.46 semantics.
func (q *SQLQueue) enqueueRevocations(ctx context.Context, orgID, groupID uuid.UUID, rp rotationJobPayload) error {
	if rp.OldPublicKey == "" {
		slog.WarnContext(ctx, "finalize rotation: no previous public key recorded; the old key is not revoked", "group", groupID)
		return nil
	}
	var existing int
	if err := q.db.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE org_id = $1 AND run_group_id = $2 AND kind = 'revoke_key'`,
		orgID, groupID).Scan(&existing); err != nil {
		return fmt.Errorf("finalize rotation: count revocations: %w", err)
	}
	if existing > 0 {
		return nil
	}
	hostTargets, bastionTargets, err := q.revocationTargets(ctx, orgID, groupID, rp.CredentialID)
	if err != nil {
		return fmt.Errorf("finalize rotation: %w", err)
	}
	var defaultZone uuid.UUID
	if len(bastionTargets) > 0 {
		if err := q.db.QueryRow(ctx,
			`SELECT id FROM zones WHERE org_id = $1 AND slug = $2`, orgID, hosts.DefaultZoneSlug).Scan(&defaultZone); err != nil {
			return fmt.Errorf("finalize rotation: resolve default zone: %w", err)
		}
	}
	now := q.now()
	credID := rp.CredentialID
	insert := func(zone uuid.UUID, hostIDs []uuid.UUID, target rotationTarget) error {
		id, err := uuid.NewV7FromReader(q.rand)
		if err != nil {
			return fmt.Errorf("finalize rotation: new job id: %w", err)
		}
		j := Job{
			ID: id, OrgID: orgID, ZoneID: zone, Kind: KindRevokeKey, HostIDs: hostIDs,
			Status: StatusQueued, RunGroupID: groupID, Trigger: TriggerSystem, CreatedAt: now,
			RotationCredentialID: &credID,
		}
		plain, err := json.Marshal(revocationJobPayload{CredentialID: rp.CredentialID, Target: target, OldPublicKey: rp.OldPublicKey})
		if err != nil {
			return fmt.Errorf("finalize rotation: encode revocation: %w", err)
		}
		sealed, err := q.box.SealAAD(plain, JobAAD(orgID, id))
		if err != nil {
			return fmt.Errorf("finalize rotation: seal revocation: %w", err)
		}
		stored := json.RawMessage(`"` + base64.StdEncoding.EncodeToString(sealed) + `"`)
		if _, err := gen.New(q.db).CreateJob(ctx, createJobParams(j, stored)); err != nil {
			return fmt.Errorf("finalize rotation: queue revocation: %w", err)
		}
		return nil
	}
	for _, h := range hostTargets {
		if err := insert(h.ZoneID, []uuid.UUID{h.ID}, rotationTarget{Kind: rotationTargetHost, ID: h.ID}); err != nil {
			return err
		}
	}
	for _, b := range bastionTargets {
		if err := insert(defaultZone, []uuid.UUID{}, rotationTarget{Kind: rotationTargetBastion, ID: b}); err != nil {
			return err
		}
	}
	return nil
}

// revocationTargets resolves the revoke_key phase's targets: the group's
// rotation_groups snapshot first (backlog 2.46 — read inside the finish's
// transaction, org-scoped like everything else), the live reference lists
// only as the legacy fallback for a group enqueued before the snapshot
// existed. Snapshot host ids resolve to their current zone for the claim;
// a target whose host row is gone by now is skipped with a log line — its
// authorized_keys left with the host, there is nothing left to revoke.
func (q *SQLQueue) revocationTargets(ctx context.Context, orgID, groupID, credentialID uuid.UUID) ([]RotationHost, []uuid.UUID, error) {
	raw, err := gen.New(q.db).GetRotationGroupTargets(ctx, gen.GetRotationGroupTargetsParams{OrgID: orgID, ID: groupID})
	switch {
	case err == nil:
		var snap rotationTargetSnapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			return nil, nil, fmt.Errorf("decode rotation targets: %w", err)
		}
		hosts, err := q.snapshotHostTargets(ctx, orgID, groupID, snap.HostIDs)
		if err != nil {
			return nil, nil, err
		}
		return hosts, snap.BastionIDs, nil
	case errors.Is(err, pgx.ErrNoRows):
		// A pre-2.46 group: no snapshot was ever written. The revocations
		// go to the references as they stand now — the previously accepted
		// semantics — rather than to nobody.
		hosts, err := q.ListCredentialHosts(ctx, orgID, credentialID)
		if err != nil {
			return nil, nil, err
		}
		bastions, err := q.ListCredentialBastions(ctx, orgID, credentialID)
		if err != nil {
			return nil, nil, err
		}
		ids := make([]uuid.UUID, len(bastions))
		for i, b := range bastions {
			ids[i] = b.ID
		}
		return hosts, ids, nil
	default:
		return nil, nil, fmt.Errorf("read rotation targets: %w", err)
	}
}

// snapshotHostTargets resolves the snapshot's host ids onto their current
// zone (the revoke_key job claims in the host's zone). Deleted hosts are
// absent from the query's result and dropped from the target list.
func (q *SQLQueue) snapshotHostTargets(ctx context.Context, orgID, groupID uuid.UUID, ids []uuid.UUID) ([]RotationHost, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := gen.New(q.db).ListHostZones(ctx, gen.ListHostZonesParams{OrgID: orgID, HostIds: ids})
	if err != nil {
		return nil, fmt.Errorf("resolve rotation target zones: %w", err)
	}
	out := make([]RotationHost, 0, len(rows))
	for _, r := range rows {
		out = append(out, RotationHost{ID: r.ID, ZoneID: r.ZoneID})
	}
	if missing := len(ids) - len(out); missing > 0 {
		slog.WarnContext(ctx, "finalize rotation: snapshot targets whose host is gone are not revoked",
			"count", missing, "group", groupID)
	}
	return out, nil
}

// ClearTerminalPayload is the exported clear Service.Cancel reaches
// through its payloadCleaner hook: a job cancelled before its claim never
// ships, so its sealed payload's at-rest lifetime ends immediately — and
// the jobs depending on it can never run, so they are cancelled too. A
// failure is logged — the cancel itself already stands.
func (q *SQLQueue) ClearTerminalPayload(ctx context.Context, jobID uuid.UUID) {
	if err := q.clearPayload(ctx, jobID); err != nil {
		slog.ErrorContext(ctx, "clear terminal job payload", "err", err, "job", jobID)
	}
	var status string
	if err := q.db.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, jobID).Scan(&status); err != nil {
		slog.ErrorContext(ctx, "cancel dependents: look up job", "err", err, "job", jobID)
		return
	}
	if err := q.cancelDependents(ctx, jobID, Status(status)); err != nil {
		slog.ErrorContext(ctx, "cancel dependents of a cancelled job", "err", err, "job", jobID)
	}
	q.recordCancelledRunGroup(ctx, jobID)
}

// cancelDependents cancels every queued job that (transitively) depends on
// jobID when jobID ended without succeeding — they could never run. Their
// sealed payloads are wiped with the cancel.
func (q *SQLQueue) cancelDependents(ctx context.Context, jobID uuid.UUID, status Status) error {
	if status == StatusSucceeded || !status.Terminal() {
		return nil
	}
	if _, err := gen.New(q.db).CancelQueuedDependents(ctx, gen.CancelQueuedDependentsParams{
		JobID:  jobID,
		Reason: fmt.Sprintf("cancelled: the job it depends on (%s) ended %s", jobID, status),
	}); err != nil {
		return fmt.Errorf("cancel dependent jobs: %w", err)
	}
	return nil
}

// clearPayload wipes a terminal job's payload of any kind: the one-time
// bootstrap login, the pending rotation key, the connection-test target
// and a run's sealed per-run overrides (schedules and webhooks can carry
// secret-typed extra vars in them) have all served their purpose the
// moment the job is terminal — the sealed ciphertext must not sit at rest
// any longer than the run needs it (backlog 2.7c at-rest lifetime).
// Nothing depends on it afterwards: the job's error text, stats and events
// carry the record. No kind filter: a sync_repo job's plain (unsealed)
// {"repo_id":...} payload is wiped the same way, safely, because
// recordSync has already read it earlier in the same transaction — see
// terminate's call order. On the finish path it runs inside Finish's
// transaction.
func (q *SQLQueue) clearPayload(ctx context.Context, jobID uuid.UUID) error {
	if _, err := q.db.Exec(ctx,
		`UPDATE jobs SET payload = NULL
		 WHERE id = $1 AND payload IS NOT NULL
		   AND status IN ('succeeded','failed','cancelled','error')`,
		jobID); err != nil {
		return fmt.Errorf("clear terminal job payload: %w", err)
	}
	return nil
}
