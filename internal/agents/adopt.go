package agents

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/restoremark"
)

// KeyFingerprint is the hex SHA-256 of the certificate's
// SubjectPublicKeyInfo: what agent_certificates.public_key_sha256 records
// for every certificate the registry writes.
func KeyFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// AdoptionState is what the store reads, under the agent row's lock, for
// the service to decide an adoption on.
type AdoptionState struct {
	// NewestCertificate is the latest start (not_before, else created_at)
	// among the host's certificates, revoked ones included; nil when it
	// holds none.
	NewestCertificate *time.Time
	// NewestPublicKeySHA256 is that certificate's key fingerprint; empty
	// when the host holds none or it was written before fingerprints were
	// recorded.
	NewestPublicKeySHA256 string
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
// agent row, and the certificate is newer than every one that host holds
// and carries the same key as the newest of them (an agent keeps its key
// across renewals; a host whose newest certificate records no key is
// never adopted into). It registers the serial the way a renewal does: the
// agent row carries it, the host's older certificates are revoked.
// Whenever it does not apply the answer is ErrNotFound, so the caller
// refuses exactly as for an unknown serial.
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
	if !window.Contains(leaf.NotBefore) {
		return AgentIdentity{}, ErrNotFound
	}
	serial := id.Serial.String()
	notAfter := leaf.NotAfter
	key := KeyFingerprint(leaf)
	candidate := Agent{OrgID: *id.OrgID, AgentName: id.WorkerName, CertSerial: serial,
		CertNotBefore: leaf.NotBefore, CertNotAfter: &notAfter, CertPublicKeySHA256: key}
	got, err := s.store.AdoptCertificate(ctx, candidate, func(st AdoptionState) error {
		if !adoptable(st, window, leaf.NotBefore, key) {
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

// adoptable applies the adoption rules to what the store read. Every time
// comparison is strict: a certificate issued at the very second of the
// backup, the restore or the newest registered certificate is refused.
// The newest registered certificate must record the presented key (key is
// never empty, so a certificate recording none never matches). A serial a
// concurrent adoption already registered is the newest certificate itself,
// so only the time comparison is skipped for it.
func adoptable(st AdoptionState, window *restoremark.Window, notBefore time.Time, key string) bool {
	if !window.Contains(notBefore) || st.NewestCertificate == nil || st.NewestPublicKeySHA256 != key {
		return false
	}
	return st.Registered || notBefore.After(*st.NewestCertificate)
}
