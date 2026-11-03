package workers

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

// AdoptionState is what the store reads, under the worker row's lock, for
// the service to decide an adoption on.
type AdoptionState struct {
	// Worker is the row the certificate's org and name resolve to.
	Worker Worker
	// NewestCertificate is the latest NotBefore among the worker's
	// certificates, revoked ones included; nil when it holds none.
	NewestCertificate *time.Time
	// Registered says the certificate's serial is already registered,
	// unrevoked, for Worker: a concurrent adoption of the same serial got
	// there first.
	Registered bool
}

// AdoptCertificate registers a verified worker certificate the database
// does not know because a restore rolled the registry back past its
// issuance (backlog 7.47). It applies only when a restore happened, the
// certificate's NotBefore lies strictly between the restored backup's
// creation and the restore, the org and name resolve to an active worker
// enrolled in the certificate's zone, and the certificate is newer than
// every one the worker holds. Whenever it does not apply the answer is
// ErrNotFound, so the caller refuses exactly as for an unknown serial.
func (s *Service) AdoptCertificate(ctx context.Context, leaf *x509.Certificate, id *ca.ClientIdentity) (Worker, error) {
	if leaf == nil || id == nil || id.Kind != ca.KindWorker || id.OrgID == nil || id.Serial == nil || id.Serial.Sign() <= 0 {
		return Worker{}, ErrNotFound
	}
	// The window is checked before the store locks anything, so an
	// unknown serial on an instance no restore touched costs one read.
	window, err := s.store.RestoreWindow(ctx)
	if err != nil {
		return Worker{}, fmt.Errorf("adopt worker certificate: %w", err)
	}
	if !window.contains(leaf.NotBefore) {
		return Worker{}, ErrNotFound
	}
	c := Certificate{Serial: id.Serial, OrgID: *id.OrgID, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}
	w, err := s.store.AdoptCertificate(ctx, id.WorkerName, c, func(st AdoptionState) error {
		if !adoptable(st, window, id.ZoneID, leaf.NotBefore) {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Worker{}, ErrNotFound
		}
		return Worker{}, fmt.Errorf("adopt worker certificate: %w", err)
	}
	return w, nil
}

// contains reports whether t lies strictly inside the window; a nil
// window (no restore) contains nothing.
func (w *RestoreWindow) contains(t time.Time) bool {
	return w != nil && t.After(w.BackupCreatedAt) && t.Before(w.RestoredAt)
}

// adoptable applies the adoption rules to what the store read. Every
// comparison is strict: a certificate issued at the very second of the
// backup, the restore or the newest registered certificate is refused. A
// serial a concurrent adoption already registered for the worker is the
// newest certificate itself, so only that comparison is skipped for it.
func adoptable(st AdoptionState, window *RestoreWindow, zoneID uuid.UUID, notBefore time.Time) bool {
	if st.Worker.Status != StatusActive || st.Worker.ZoneID != zoneID || !window.contains(notBefore) {
		return false
	}
	return st.Registered || st.NewestCertificate == nil || notBefore.After(*st.NewestCertificate)
}
