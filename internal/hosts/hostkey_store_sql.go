// Host-key pins and connection-test history: the SQLStore half of
// hostkey.go, on the hostkeys.sql queries. Every statement runs inside the
// request's organization scope (db.WithOrgScope), like every other method
// of this store. The org-unscoped job-queue paths call the service —
// RecordConnectionTest, PinnedKeys — with explicit org ids through the
// same db, whose org_isolation_bypass policy then applies; each of those
// statements still filters by org_id in its own WHERE clause.
package hosts

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// foreignKeyViolationCode is PostgreSQL's code for a foreign key
// violation: here the only realistic source is host_ssh's
// (org_id, host_id) → hosts (org_id, id) reference, i.e. a host that does
// not exist in the organization — reported exactly like any other missing
// host.
const foreignKeyViolationCode = "23503"

// ListOrgIDs returns every organization id (see hostkey.go).
func (s *SQLStore) ListOrgIDs(ctx context.Context) ([]uuid.UUID, error) {
	return s.q.ListOrgIDs(ctx)
}

// SetHostKeyPin implements Store (see the interface's own comment).
func (s *SQLStore) SetHostKeyPin(ctx context.Context, orgID, hostID uuid.UUID, fingerprint, publicKey string) error {
	var pub *string
	if publicKey != "" {
		pub = &publicKey
	}
	rowsAffected, err := s.q.UpsertHostKeyPin(ctx, gen.UpsertHostKeyPinParams{OrgID: orgID, HostID: hostID, SshHostKey: &fingerprint, SshHostPublicKey: pub})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolationCode {
			return ErrNotFound
		}
		return err
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearHostKeyPin implements Store (see the interface's own comment).
func (s *SQLStore) ClearHostKeyPin(ctx context.Context, orgID, hostID uuid.UUID) error {
	_, err := s.q.ClearHostKeyPin(ctx, gen.ClearHostKeyPinParams{OrgID: orgID, HostID: hostID})
	return err
}

// BackfillHostKeyPin implements Store (see the interface's own comment).
func (s *SQLStore) BackfillHostKeyPin(ctx context.Context, orgID, hostID uuid.UUID, fingerprint, publicKey string) error {
	_, err := s.q.BackfillHostKeyPinPublicKey(ctx, gen.BackfillHostKeyPinPublicKeyParams{
		OrgID: orgID, HostID: hostID, Fingerprint: &fingerprint, PublicKey: &publicKey,
	})
	return err
}

// ListPinnedKeys implements Store (see the interface's own comment).
func (s *SQLStore) ListPinnedKeys(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]HostKeyPin, error) {
	rows, err := s.q.ListPinnedHostKeys(ctx, gen.ListPinnedHostKeysParams{OrgID: orgID, HostIds: hostIDs})
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]HostKeyPin, len(rows))
	for _, r := range rows {
		if r.SshHostKey == nil {
			continue // the query filters to non-null pins; never expected
		}
		pin := HostKeyPin{Fingerprint: *r.SshHostKey}
		if r.SshHostPublicKey != nil {
			pin.PublicKey = *r.SshHostPublicKey
		}
		out[r.HostID] = pin
	}
	return out, nil
}

// ListChangedKeys implements Store (see the interface's own comment).
func (s *SQLStore) ListChangedKeys(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) ([]uuid.UUID, error) {
	return s.q.ListHostKeyChanged(ctx, gen.ListHostKeyChangedParams{OrgID: orgID, HostIds: hostIDs})
}

// CreateHostTest implements Store (see the interface's own comment).
func (s *SQLStore) CreateHostTest(ctx context.Context, t HostTest) error {
	checks, err := json.Marshal(t.Checks)
	if err != nil {
		return err
	}
	if checks == nil {
		checks = []byte("[]")
	}
	hops, err := json.Marshal(t.HopKeys)
	if err != nil {
		return err
	}
	if hops == nil {
		hops = []byte("[]")
	}
	_, err = s.q.CreateHostConnectionTest(ctx, gen.CreateHostConnectionTestParams{
		ID: t.ID, OrgID: t.OrgID, HostID: t.HostID, At: t.At,
		Ok: t.OK, HostKey: t.HostKey, HostKeyState: t.HostKeyState, Checks: checks,
		HostPublicKey: t.HostPublicKey, HopKeys: hops,
	})
	return err
}

// LatestHostTest implements Store (see the interface's own comment).
func (s *SQLStore) LatestHostTest(ctx context.Context, orgID, hostID uuid.UUID) (HostTest, error) {
	row, err := s.q.GetLatestHostConnectionTest(ctx, gen.GetLatestHostConnectionTestParams{OrgID: orgID, HostID: hostID})
	if err != nil {
		return HostTest{}, mapErr(err)
	}
	return toHostTest(row)
}

// ListHostsDueForTest implements Store (see the interface's own comment).
func (s *SQLStore) ListHostsDueForTest(ctx context.Context, orgID uuid.UUID, since time.Time) ([]Host, error) {
	rows, err := s.q.ListHostsDueForTest(ctx, gen.ListHostsDueForTestParams{OrgID: orgID, Since: since})
	if err != nil {
		return nil, err
	}
	out := make([]Host, len(rows))
	for i, r := range rows {
		h, err := fromHostRow(r)
		if err != nil {
			return nil, err
		}
		out[i] = h
	}
	return out, nil
}

// toHostTest decodes one stored row.
func toHostTest(r gen.HostConnectionTest) (HostTest, error) {
	var checks []HostTestCheck
	if len(r.Checks) > 0 {
		if err := json.Unmarshal(r.Checks, &checks); err != nil {
			return HostTest{}, errors.New("host connection test: checks are not a check list")
		}
	}
	var hops []HopKey
	if len(r.HopKeys) > 0 {
		if err := json.Unmarshal(r.HopKeys, &hops); err != nil {
			return HostTest{}, errors.New("host connection test: hop keys are not a hop key list")
		}
	}
	return HostTest{
		ID: r.ID, OrgID: r.OrgID, HostID: r.HostID, At: r.At,
		OK: r.Ok, HostKey: r.HostKey, HostPublicKey: r.HostPublicKey, HostKeyState: r.HostKeyState, Checks: checks,
		HopKeys: hops,
	}, nil
}

// SetBastionKeyPin implements Store (see the interface's own comment).
func (s *SQLStore) SetBastionKeyPin(ctx context.Context, orgID, bastionID uuid.UUID, fingerprint, publicKey string) error {
	var pub *string
	if publicKey != "" {
		pub = &publicKey
	}
	rowsAffected, err := s.q.UpsertBastionKeyPin(ctx, gen.UpsertBastionKeyPinParams{OrgID: orgID, ID: bastionID, SshHostKey: &fingerprint, SshHostPublicKey: pub})
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearBastionKeyPin implements Store (see the interface's own comment).
func (s *SQLStore) ClearBastionKeyPin(ctx context.Context, orgID, bastionID uuid.UUID) error {
	rowsAffected, err := s.q.ClearBastionKeyPin(ctx, gen.ClearBastionKeyPinParams{OrgID: orgID, ID: bastionID})
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// BackfillBastionKeyPin implements Store (see the interface's own comment).
func (s *SQLStore) BackfillBastionKeyPin(ctx context.Context, orgID, bastionID uuid.UUID, fingerprint, publicKey string) error {
	_, err := s.q.BackfillBastionKeyPinPublicKey(ctx, gen.BackfillBastionKeyPinPublicKeyParams{
		OrgID: orgID, BastionID: bastionID, Fingerprint: &fingerprint, PublicKey: &publicKey,
	})
	return err
}

// ListPinnedBastionKeys implements Store (see the interface's own comment).
func (s *SQLStore) ListPinnedBastionKeys(ctx context.Context, orgID uuid.UUID, bastionIDs []uuid.UUID) (map[uuid.UUID]HostKeyPin, error) {
	rows, err := s.q.ListPinnedBastionKeys(ctx, gen.ListPinnedBastionKeysParams{OrgID: orgID, BastionIds: bastionIDs})
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]HostKeyPin, len(rows))
	for _, r := range rows {
		if r.SshHostKey == nil {
			continue // the query filters to non-null pins; never expected
		}
		pin := HostKeyPin{Fingerprint: *r.SshHostKey}
		if r.SshHostPublicKey != nil {
			pin.PublicKey = *r.SshHostPublicKey
		}
		out[r.ID] = pin
	}
	return out, nil
}

// ListChangedBastionKeys implements Store (see the interface's own comment).
func (s *SQLStore) ListChangedBastionKeys(ctx context.Context, orgID uuid.UUID, bastionIDs []uuid.UUID) ([]uuid.UUID, error) {
	return s.q.ListBastionKeyChanged(ctx, gen.ListBastionKeyChangedParams{OrgID: orgID, BastionIds: bastionIDs})
}

// ListLatestBastionObservations implements Store (see the interface's own
// comment).
func (s *SQLStore) ListLatestBastionObservations(ctx context.Context, orgID uuid.UUID, bastionIDs []uuid.UUID) (map[uuid.UUID]BastionHopObservation, error) {
	rows, err := s.q.ListLatestBastionObservations(ctx, gen.ListLatestBastionObservationsParams{OrgID: orgID, BastionIds: bastionIDs})
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]BastionHopObservation, len(rows))
	for _, r := range rows {
		// The rows are ordered oldest first, so the last row per bastion
		// is its newest observation (a tie keeps the later id).
		out[r.BastionID] = BastionHopObservation{
			At: r.At, Fingerprint: r.Fingerprint.(string), PublicKey: r.Key.(string), State: r.State.(string),
		}
	}
	return out, nil
}
