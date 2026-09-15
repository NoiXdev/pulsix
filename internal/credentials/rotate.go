// Credential rotation (spec §5, backlog 2.7): re-key an ssh_key credential
// by installing its new public key on every host and jump host that
// references it, then sealing the new private key onto the credential. The
// pending new key exists ONLY inside the rotation group's sealed job
// payloads (jobs.payload, opened by the claim path over mTLS) until the
// group's last job reaches a terminal state: everything succeeded → the
// single-UPDATE finalize below replaces the credential's key material;
// anything failed, cancelled or errored → the credential stays untouched.
//
// The enqueue goes through the RotationEnqueuer port (implemented by
// internal/jobs, which lists the referencing targets itself); the
// finalize is what the queue's RotationFinisher port calls. Neither port
// imports the other package — both are structural, wired in internal/app.
package credentials

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// PendingKey is the rotation's new key material: generated at Rotate time,
// never stored anywhere in clear, sealed onto the credential only when the
// rotation group completes.
type PendingKey struct {
	// PrivateKey is the new private key's sealed payload (a Secret whose
	// PrivateKey field is set — the JSON form rides in the job payloads).
	PrivateKey Secret
	// PublicKey, Fingerprint and KeyType are the metadata that replace the
	// credential's on finalize.
	PublicKey   string
	Fingerprint string
	KeyType     string
}

// RotationEnqueuer is the port Rotate enqueues the rotation group through.
// The concrete implementation (internal/jobs.Service) lists the managed
// hosts and jump hosts referencing the credential itself and inserts one
// rotate_key job per target, all sharing a run group; count 0 means
// nothing references the credential and the caller re-keys immediately.
type RotationEnqueuer interface {
	EnqueueRotation(ctx context.Context, orgID, credentialID uuid.UUID, key PendingKey, actor *uuid.UUID) (groupID, firstJobID uuid.UUID, count int, err error)
}

// RotationResult is what a Rotate produced: the rotation group to watch,
// or the immediate re-key.
type RotationResult struct {
	// GroupID is the rotation group's run group id; zero when the
	// credential referenced nothing and re-keyed immediately.
	GroupID uuid.UUID
	// FirstJobID is the group's first job id (for the SPA's run view);
	// zero on the immediate path.
	FirstJobID uuid.UUID
	// Count is the number of rotate_key jobs in the group.
	Count int
}

// Rotate generates the credential's new ed25519 keypair and enqueues the
// rotation group through the port. A credential referenced by nothing
// re-keys immediately (the finalize runs right here, no jobs). The
// credential itself is untouched until the group's terminal outcome says
// otherwise — a failed rotation never half-updates anything.
func (s *Service) Rotate(ctx context.Context, orgID, id uuid.UUID, actor *uuid.UUID) (RotationResult, error) {
	c, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return RotationResult{}, err
	}
	if c.Kind != KindSSHKey {
		return RotationResult{}, fmt.Errorf("%w: only ssh_key credentials rotate (a %s credential re-keys through its update)", ErrInvalidInput, c.Kind)
	}
	if s.rotation == nil {
		return RotationResult{}, fmt.Errorf("rotate credential %s: no rotation enqueuer wired", id)
	}
	k, err := generateKey(s.rand)
	if err != nil {
		return RotationResult{}, err
	}
	key := PendingKey{
		PrivateKey:  Secret{PrivateKey: k.privatePEM},
		PublicKey:   k.publicKey,
		Fingerprint: k.fingerprint,
		KeyType:     k.keyType,
	}
	groupID, firstJob, count, err := s.rotation.EnqueueRotation(ctx, orgID, id, key, actor)
	if err != nil {
		return RotationResult{}, err
	}
	if count == 0 {
		// Nothing references the credential: re-key immediately — the
		// same single-UPDATE finalize the group's terminal outcome runs.
		return RotationResult{}, s.finishRotation(ctx, c, key)
	}
	return RotationResult{GroupID: groupID, FirstJobID: firstJob, Count: count}, nil
}

// FinishRotation is the queue-side port's concrete implementation: when
// the last job of the rotation group reached a terminal state,
// allSucceeded decides whether the pending new key seals onto the
// credential (single UPDATE — metadata and secret together) or the
// credential is left untouched. groupID names the group for
// observability; the finalize itself is idempotent by value (two finishes
// of the same group seal the identical key).
func (s *Service) FinishRotation(ctx context.Context, orgID, credentialID, groupID uuid.UUID, allSucceeded bool, newPrivateKey Secret, newPublicKey, newFingerprint, newKeyType string) error {
	_ = groupID // observability only: the finalize is keyed by the credential
	if !allSucceeded {
		return nil // anything else leaves the credential untouched
	}
	c, err := s.store.Get(ctx, orgID, credentialID)
	if err != nil {
		return err
	}
	return s.finishRotation(ctx, c, PendingKey{
		PrivateKey: newPrivateKey, PublicKey: newPublicKey,
		Fingerprint: newFingerprint, KeyType: newKeyType,
	})
}

// finishRotation seals the new key onto the credential in one statement:
// the metadata columns and the resealed secret replace together.
func (s *Service) finishRotation(ctx context.Context, c Credential, key PendingKey) error {
	next := c
	next.PublicKey, next.Fingerprint, next.KeyType, next.UpdatedAt = key.PublicKey, key.Fingerprint, key.KeyType, s.now()
	sealed, err := s.seal(next, key.PrivateKey)
	if err != nil {
		return err
	}
	if _, err := s.store.Update(ctx, next, sealed); err != nil {
		return fmt.Errorf("seal rotated credential: %w", err)
	}
	return nil
}
