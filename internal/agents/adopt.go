package agents

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
)

// RestoreWindow is the span between the restored backup's creation and the
// restore itself (backlog 7.47). A certificate the CA issued inside it is
// unknown to the restored database although the instance issued it.
type RestoreWindow struct {
	BackupCreatedAt time.Time
	RestoredAt      time.Time
}

// AdoptionState is what the store reads, under the agent row's lock, for
// the service to decide an adoption on.
type AdoptionState struct {
	// HostID is the host whose agent row the certificate's org and agent
	// name resolve to.
	HostID uuid.UUID
	// NewestCertificate is the latest start (not_before, else created_at)
	// among the host's certificates, revoked ones included; nil when it
	// holds none.
	NewestCertificate *time.Time
	// Registered says the certificate's serial is already the host's live
	// certificate: a concurrent adoption of the same serial got there
	// first.
	Registered bool
}

// AdoptCertificate registers a verified agent certificate the database
// does not know because a restore rolled the registry back past its
// issuance (backlog 7.47). It applies only when a restore happened, the
// certificate's NotBefore lies strictly between the restored backup's
// creation and the restore, the org and agent name resolve to exactly one
// agent row, and the certificate is newer than every one that host holds.
// It registers the serial the way a renewal does: the agent row carries
// it, the host's older certificates are revoked. Whenever it does not
// apply the answer is ErrNotFound, so the caller refuses exactly as for an
// unknown serial.
func (s *Service) AdoptCertificate(ctx context.Context, leaf *x509.Certificate, id *ca.ClientIdentity) (AgentIdentity, error) {
	if leaf == nil || id == nil || id.Kind != ca.KindAgent || id.OrgID == nil || id.WorkerName == "" ||
		id.Serial == nil || id.Serial.Sign() <= 0 {
		return AgentIdentity{}, ErrNotFound
	}
	// The window is checked before the store locks anything, so an
	// unknown serial on an instance no restore touched costs one read.
	window, err := s.store.RestoreWindow(ctx)
	if err != nil {
		return AgentIdentity{}, fmt.Errorf("adopt agent certificate: %w", err)
	}
	if !window.contains(leaf.NotBefore) {
		return AgentIdentity{}, ErrNotFound
	}
	serial := id.Serial.String()
	notAfter := leaf.NotAfter
	candidate := Agent{OrgID: *id.OrgID, AgentName: id.WorkerName, CertSerial: serial,
		CertNotBefore: leaf.NotBefore, CertNotAfter: &notAfter}
	got, err := s.store.AdoptCertificate(ctx, candidate, func(st AdoptionState) error {
		if !adoptable(st, window, leaf.NotBefore) {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return AgentIdentity{}, ErrNotFound
		}
		return AgentIdentity{}, fmt.Errorf("adopt agent certificate: %w", err)
	}
	got.Serial = serial
	return got, nil
}

// contains reports whether t lies strictly inside the window; a nil
// window (no restore) contains nothing.
func (w *RestoreWindow) contains(t time.Time) bool {
	return w != nil && t.After(w.BackupCreatedAt) && t.Before(w.RestoredAt)
}

// adoptable applies the adoption rules to what the store read. Every
// comparison is strict: a certificate issued at the very second of the
// backup, the restore or the newest registered certificate is refused. A
// serial a concurrent adoption already registered is the newest
// certificate itself, so only that comparison is skipped for it.
func adoptable(st AdoptionState, window *RestoreWindow, notBefore time.Time) bool {
	if !window.contains(notBefore) {
		return false
	}
	return st.Registered || st.NewestCertificate == nil || notBefore.After(*st.NewestCertificate)
}
