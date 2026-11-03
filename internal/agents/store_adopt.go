package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// AdoptCertificate implements Store.AdoptCertificate in one transaction
// bound to a.OrgID: lock the agent rows by (org, agent name), require
// exactly one, read the host's certificates, ask allow and, unless the
// serial is already the host's live certificate, register it through the
// renewal path (the row takes the serial, older certificates are revoked).
func (s *SQLStore) AdoptCertificate(ctx context.Context, a Agent, allow func(AdoptionState) error) (AgentIdentity, error) {
	var out AgentIdentity
	err := s.d.WithWriteTx(ctx, a.OrgID, func(ctx context.Context) error {
		rows, err := s.q.LockHostAgentsByName(ctx, gen.LockHostAgentsByNameParams{OrgID: a.OrgID, AgentName: a.AgentName})
		if err != nil {
			return fmt.Errorf("lock agent: %w", err)
		}
		// Agent names are not unique: a certificate naming two hosts'
		// agents cannot say which one it belongs to.
		if len(rows) != 1 {
			return ErrNotFound
		}
		row := rows[0]
		state := AdoptionState{HostID: row.HostID}
		// Two calls on a fresh connection can race in with the same
		// unknown serial; the one that waited on the lock finds it the
		// host's live certificate. A serial registered to another host,
		// or revoked since, is not this adoption's to make.
		known, err := s.q.GetAgentCertificate(ctx, a.CertSerial)
		switch {
		case err == nil && known.HostID == row.HostID && !known.RevokedAt.Valid && row.CertSerial == a.CertSerial:
			state.Registered = true
		case err == nil:
			return ErrNotFound
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("look up certificate: %w", err)
		}
		newest, err := s.q.NewestAgentCertificateStart(ctx, gen.NewestAgentCertificateStartParams{OrgID: a.OrgID, HostID: row.HostID})
		switch {
		case err == nil:
			state.NewestCertificate = &newest
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("newest certificate: %w", err)
		}
		if err := allow(state); err != nil {
			return err
		}
		if !state.Registered {
			a.HostID, a.Version = row.HostID, row.Version
			if err := s.register(ctx, a); err != nil {
				// The serial is the primary key of every organization's
				// certificates: one this org scope cannot see is still
				// taken, and still not this adoption's.
				if db.IsUniqueViolation(err) {
					return ErrNotFound
				}
				return err
			}
		}
		adopted, err := s.q.LookupAgentSerial(ctx, a.CertSerial)
		if err != nil {
			return fmt.Errorf("read adopted agent: %w", err)
		}
		out = toIdentity(adopted)
		return nil
	})
	return out, err
}

// restoreMarkerKey and restoreMarker mirror backup.RestoreMarkerKey and
// backup.RestoreMarker, which write the row: this package reads it without
// importing internal/backup. The key and both JSON field names must stay in
// step with that package (adopt_test.go writes the marker through it).
const restoreMarkerKey = "restore"

type restoreMarker struct {
	BackupCreatedAt time.Time `json:"backup_created_at"`
	RestoredAt      time.Time `json:"restored_at"`
}

// RestoreWindow reads the last restore's marker. No marker, or one without
// both times or whose restore is not after its backup, is nil: adoption
// fails closed on a marker it cannot trust.
func (s *SQLStore) RestoreWindow(ctx context.Context) (*RestoreWindow, error) {
	row, err := s.q.GetInstanceSetting(ctx, restoreMarkerKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read restore marker: %w", err)
	}
	var m restoreMarker
	if err := json.Unmarshal(row.Value, &m); err != nil {
		return nil, fmt.Errorf("decode restore marker: %w", err)
	}
	if m.BackupCreatedAt.IsZero() || m.RestoredAt.IsZero() || !m.RestoredAt.After(m.BackupCreatedAt) {
		return nil, nil
	}
	return &RestoreWindow{BackupCreatedAt: m.BackupCreatedAt, RestoredAt: m.RestoredAt}, nil
}
