// Credential rotation (spec §5, backlog 2.7): the enqueue half and the
// finish half. EnqueueRotation is the concrete implementation of
// credentials.RotationEnqueuer (declared over there as a narrow port, to
// keep the import arrow jobs → credentials): it lists the managed hosts
// and jump hosts referencing the credential and inserts one rotate_key job
// per target, all sharing one run group. Each job's payload carries the
// pending new key SEALED with the box (AAD job:<orgID>:<jobID>) before
// insert — the pending key never exists anywhere else in the clear, not
// even in a table column.
//
// The finish half rides SQLQueue.Finish (recordRotation): when the last
// job of a rotate_key group reports terminal, the outcome flows through
// the RotationFinisher port to credentials.Service.FinishRotation, which
// seals the new key onto the credential (all succeeded) or leaves it
// untouched (anything else). Double-finalization is safe by value: two
// finishes observing the same completed group seal the identical key.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
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
}

type rotationTarget struct {
	Kind string    `json:"kind"`
	ID   uuid.UUID `json:"id"`
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

// SetRotationFinisher injects the port recordRotation flows through on the
// finish path, the rotation sibling of SetSyncSink/SetTestSink. A nil
// finisher finalizes nothing — a wiring gap an operator sees as a
// credential that never changes, never as a failed job.
func (q *SQLQueue) SetRotationFinisher(finisher RotationFinisher) { q.rotation = finisher }

// EnqueueRotation enqueues the credential's rotation group: one
// rotate_key job per managed host and jump host referencing it, all
// sharing one run group, each payload carrying the pending new key sealed
// with the job's own AAD. Host targets claim in the host's zone; bastion
// targets claim in the organization's default zone (bastions are not
// zone-bound; a sync-shaped job with no host scope). Returns the group id,
// the group's first job id and the job count — count 0 means nothing
// references the credential (the caller re-keys it immediately).
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
	group := s.newID()
	now := s.now()
	count := 0
	first := uuid.Nil
	// One job per host target, in the host's zone.
	for _, h := range hosts {
		j := Job{
			ID: s.newID(), OrgID: orgID, ZoneID: h.ZoneID,
			Kind: KindRotateKey, HostIDs: []uuid.UUID{h.ID},
			Status: StatusQueued, RunGroupID: group, Trigger: TriggerManual,
			ActorID: actor, CreatedAt: now,
		}
		if err := s.createRotationJob(ctx, j, credentialID, rotationTarget{Kind: rotationTargetHost, ID: h.ID}, key); err != nil {
			return uuid.Nil, uuid.Nil, 0, err
		}
		count++
		if first == uuid.Nil {
			first = j.ID
		}
	}
	// One job per bastion target, in the default zone: a bastion is
	// organization-level infrastructure, not bound to a host's zone.
	if len(bastions) > 0 {
		zone, err := s.defaultZone(ctx, orgID)
		if err != nil {
			return uuid.Nil, uuid.Nil, 0, fmt.Errorf("resolve rotation zone: %w", err)
		}
		for _, b := range bastions {
			j := Job{
				ID: s.newID(), OrgID: orgID, ZoneID: zone,
				Kind: KindRotateKey, HostIDs: []uuid.UUID{},
				Status: StatusQueued, RunGroupID: group, Trigger: TriggerManual,
				ActorID: actor, CreatedAt: now,
			}
			if err := s.createRotationJob(ctx, j, credentialID, rotationTarget{Kind: rotationTargetBastion, ID: b.ID}, key); err != nil {
				return uuid.Nil, uuid.Nil, 0, err
			}
			count++
			if first == uuid.Nil {
				first = j.ID
			}
		}
	}
	return group, first, count, nil
}

// createRotationJob seals the rotation payload and inserts the job.
func (s *Service) createRotationJob(ctx context.Context, j Job, credentialID uuid.UUID, target rotationTarget, key credentials.PendingKey) error {
	payload, err := json.Marshal(rotationJobPayload{
		CredentialID: credentialID, Target: target,
		NewPublicKey: key.PublicKey, NewFingerprint: key.Fingerprint, NewKeyType: key.KeyType,
		NewPrivateKey: key.PrivateKey,
	})
	if err != nil {
		return fmt.Errorf("encode rotation payload: %w", err)
	}
	stored, err := s.sealPayload(j, payload)
	if err != nil {
		return err
	}
	return s.store.CreateJob(ctx, j, stored)
}

// recordRotation finalizes a rotation group whose last job just went
// terminal: the group is complete when every rotate_key job of the group
// is terminal, and allSucceeded only when every one of them succeeded. A
// cancelled or failed job leaves the credential untouched. The finisher
// is bookkeeping, not bookkeeping-breaking — its failure is logged, the
// terminal status already stands. The finished job's payload (sealed) is
// still on the row at this point; clearing happens after the sinks.
func (q *SQLQueue) recordRotation(ctx context.Context, jobID uuid.UUID, status Status) {
	if q.rotation == nil || status == StatusCancelled {
		return
	}
	var orgID, groupID uuid.UUID
	var kind string
	var payload json.RawMessage
	if err := q.db.Pool().QueryRow(ctx,
		`SELECT org_id, kind, payload, run_group_id FROM jobs WHERE id = $1`, jobID,
	).Scan(&orgID, &kind, &payload, &groupID); err != nil {
		slog.ErrorContext(ctx, "finalize rotation: look up finished job", "err", err, "job", jobID)
		return
	}
	if Kind(kind) != KindRotateKey {
		return
	}
	plain, err := q.openKindPayload(Job{OrgID: orgID, ID: jobID, Payload: payload})
	if err != nil {
		slog.ErrorContext(ctx, "finalize rotation: open job payload", "err", err, "job", jobID)
		return
	}
	var rp rotationJobPayload
	if err := json.Unmarshal([]byte(plain), &rp); err != nil || rp.CredentialID == uuid.Nil {
		slog.ErrorContext(ctx, "finalize rotation: job payload carries no credential", "job", jobID)
		return
	}

	var total, terminal, succeeded int
	if err := q.db.Pool().QueryRow(ctx,
		`SELECT count(*),
			count(*) FILTER (WHERE status IN ('succeeded','failed','cancelled','error')),
			count(*) FILTER (WHERE status = 'succeeded')
		FROM jobs WHERE org_id = $1 AND run_group_id = $2 AND kind = 'rotate_key'`,
		orgID, groupID,
	).Scan(&total, &terminal, &succeeded); err != nil {
		slog.ErrorContext(ctx, "finalize rotation: count group", "err", err, "group", groupID)
		return
	}
	if total == 0 || terminal < total {
		return // the group is not done yet
	}
	allSucceeded := succeeded == total
	if err := q.rotation.FinishRotation(ctx, orgID, rp.CredentialID, groupID, allSucceeded,
		rp.NewPrivateKey, rp.NewPublicKey, rp.NewFingerprint, rp.NewKeyType); err != nil {
		// The job's terminal status is already recorded; a failed
		// bookkeeping write is logged so an operator sees the gap, the
		// same tradeoff the sync/test sinks and the audit middleware make.
		slog.ErrorContext(ctx, "finalize rotation", "err", err, "group", groupID)
	}
}

// ClearTerminalPayload is the exported clear Service.Cancel reaches
// through its payloadCleaner hook: a job cancelled before its claim never
// ships, so its sealed payload's at-rest lifetime ends immediately.
func (q *SQLQueue) ClearTerminalPayload(ctx context.Context, jobID uuid.UUID) {
	q.clearPayload(ctx, jobID)
}

// clearPayload wipes a terminal system job's sealed payload: the one-time
// bootstrap login, the pending rotation key and the connection-test target
// have served their purpose the moment the job is terminal — the sealed
// ciphertext must not sit at rest any longer than the run needs it
// (backlog 2.7c at-rest lifetime). Nothing depends on it afterwards: the
// job's error text, stats and events carry the record.
func (q *SQLQueue) clearPayload(ctx context.Context, jobID uuid.UUID) {
	if _, err := q.db.Pool().Exec(ctx,
		`UPDATE jobs SET payload = NULL
		 WHERE id = $1 AND kind IN ('connection_test','bootstrap','rotate_key') AND payload IS NOT NULL`,
		jobID); err != nil {
		slog.ErrorContext(ctx, "clear terminal job payload", "err", err, "job", jobID)
	}
}
