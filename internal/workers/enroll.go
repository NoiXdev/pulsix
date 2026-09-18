package workers

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/hosts"
)

// EnrollmentTTL is how long a token is valid (spec §4).
const EnrollmentTTL = time.Hour

var (
	// ErrEnrollmentRefused covers unknown, used, expired and wrong-secret
	// tokens alike: the caller cannot tell which.
	ErrEnrollmentRefused = errors.New("enrollment refused")
	ErrBadCSR            = errors.New("certificate request rejected")
)

// Enrollment is a one-time worker enrollment token record.
type Enrollment struct {
	ID, OrgID, ZoneID uuid.UUID
	Name              string
	ExpiresAt         time.Time
	UsedAt            *time.Time
	CreatedBy         *uuid.UUID
	CreatedAt         time.Time
}

// EnrollInput is a redeemed enrollment token plus the worker's CSR.
type EnrollInput struct {
	TokenID uuid.UUID
	Secret  []byte
	CSRPEM  []byte
	Version string
}

// EnrollResult is the outcome of a successful Enroll.
type EnrollResult struct {
	Worker  Worker
	CertPEM []byte
	CAPEM   []byte
}

// Signer is the slice of the CA an enrolling/renewing worker needs.
type Signer interface {
	SignCSR(csrPEM []byte, orgID, zoneID uuid.UUID, workerName string) ([]byte, *x509.Certificate, error)
	CAPEM() []byte
	CAPin() [32]byte
}

// ZoneLister is the slice of the hosts service CreateEnrollment validates
// its zone argument against.
type ZoneLister interface {
	ListZones(ctx context.Context, orgID uuid.UUID) ([]hosts.Zone, error)
}

// CreateEnrollment creates a one-time token for a worker named name in
// zoneID. The token is returned once; only SHA-256(secret) is stored.
func (s *Service) CreateEnrollment(ctx context.Context, orgID uuid.UUID, actor *uuid.UUID, zoneID uuid.UUID, name string) (Enrollment, string, error) {
	if s.signer == nil || s.zones == nil {
		return Enrollment{}, "", errors.New("workers: create enrollment: service was built without WithSigner/WithZones")
	}
	if !namePattern.MatchString(name) {
		return Enrollment{}, "", fmt.Errorf("%w: name must be 1-63 lowercase letters, digits or hyphens, starting with a letter or digit", ErrInvalidInput)
	}
	zones, err := s.zones.ListZones(ctx, orgID)
	if err != nil {
		return Enrollment{}, "", fmt.Errorf("list zones: %w", err)
	}
	found := false
	for _, z := range zones {
		found = found || z.ID == zoneID
	}
	if !found {
		return Enrollment{}, "", fmt.Errorf("%w: unknown zone", ErrInvalidInput)
	}
	if _, err := s.store.GetByName(ctx, orgID, name); err == nil {
		return Enrollment{}, "", ErrNameTaken
	} else if !errors.Is(err, ErrNotFound) {
		return Enrollment{}, "", err
	}
	secret := make([]byte, 32)
	if _, err := io.ReadFull(s.rand, secret); err != nil {
		return Enrollment{}, "", fmt.Errorf("draw enrollment secret: %w", err)
	}
	hash := sha256.Sum256(secret)
	now := s.now()
	id, err := uuid.NewV7FromReader(s.rand)
	if err != nil {
		return Enrollment{}, "", err
	}
	// Computed before the store write: a signer that is wired but not
	// Ensure()'d panics inside CAPin (it has no ErrNotReady guard, unlike
	// SignCSR), and doing this after CreateEnrollment would leave an
	// orphaned pending row behind that panic.
	pin := s.signer.CAPin()
	e, err := s.store.CreateEnrollment(ctx, Enrollment{ID: id, OrgID: orgID, ZoneID: zoneID, Name: name, ExpiresAt: now.Add(EnrollmentTTL), CreatedBy: actor, CreatedAt: now}, hash[:])
	if err != nil {
		return Enrollment{}, "", err
	}
	token := enrolltoken.Token{ID: e.ID, Secret: secret, CAPin: pin}.String()
	return e, token, nil
}

// wrapSignError distinguishes a rejected certificate request (client
// fault, workers.ErrBadCSR — Task 13 answers InvalidArgument) from every
// other SignCSR failure (a server fault: the CA not ready, entropy
// exhausted, and the like — an internal error, not a client mistake).
// Without this split, every signer failure looked like a bad CSR, and the
// message doubled ("certificate request rejected: certificate request
// rejected: ...") because ca.ErrBadCSR and workers.ErrBadCSR share their
// text.
func wrapSignError(err error) error {
	if errors.Is(err, ca.ErrBadCSR) {
		return fmt.Errorf("%w: %v", ErrBadCSR, err)
	}
	return fmt.Errorf("sign certificate: %w", err)
}

// ListEnrollments returns orgID's pending (unused, unexpired) enrollments.
func (s *Service) ListEnrollments(ctx context.Context, orgID uuid.UUID) ([]Enrollment, error) {
	return s.store.ListEnrollments(ctx, orgID, s.now())
}

// WithdrawEnrollment deletes a pending enrollment before it is redeemed.
func (s *Service) WithdrawEnrollment(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.DeleteEnrollment(ctx, orgID, id)
}

// Enroll redeems a token: unused, unexpired, secret compared in constant
// time; the CSR is signed; worker, certificate and the used mark are
// written in one transaction (the store runs fn while holding the row).
func (s *Service) Enroll(ctx context.Context, in EnrollInput) (EnrollResult, error) {
	var certPEM []byte
	w, err := s.store.Enroll(ctx, in.TokenID, func(e Enrollment, secretHash []byte) (Worker, Certificate, error) {
		sum := sha256.Sum256(in.Secret)
		if subtle.ConstantTimeCompare(sum[:], secretHash) != 1 || e.UsedAt != nil || !s.now().Before(e.ExpiresAt) {
			return Worker{}, Certificate{}, ErrEnrollmentRefused
		}
		pemBytes, crt, err := s.signer.SignCSR(in.CSRPEM, e.OrgID, e.ZoneID, e.Name)
		if err != nil {
			return Worker{}, Certificate{}, wrapSignError(err)
		}
		certPEM = pemBytes
		id, err := uuid.NewV7FromReader(s.rand)
		if err != nil {
			return Worker{}, Certificate{}, err
		}
		w := Worker{ID: id, OrgID: e.OrgID, ZoneID: e.ZoneID, Name: e.Name, CertSerial: crt.SerialNumber, Version: in.Version, Status: StatusActive}
		return w, Certificate{Serial: crt.SerialNumber, OrgID: e.OrgID, WorkerID: id, NotBefore: crt.NotBefore, NotAfter: crt.NotAfter}, nil
	})
	// ErrNotFound (unknown or withdrawn-mid-race token) and ErrEnrollmentRefused
	// (the closure's own checks, or the store's atomic single-use guard
	// losing a race — see SQLStore.Enroll) both collapse to the one
	// refusal a caller can observe; everything else (a bad CSR, a store
	// fault) passes through unchanged.
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrEnrollmentRefused) {
		return EnrollResult{}, ErrEnrollmentRefused
	}
	if err != nil {
		return EnrollResult{}, err
	}
	return EnrollResult{Worker: w, CertPEM: certPEM, CAPEM: s.signer.CAPEM()}, nil
}

// Renew signs a fresh certificate for an authenticated worker; the old one
// stays valid until it expires (open streams are not cut).
func (s *Service) Renew(ctx context.Context, w Worker, csrPEM []byte) ([]byte, error) {
	pemBytes, crt, err := s.signer.SignCSR(csrPEM, w.OrgID, w.ZoneID, w.Name)
	if err != nil {
		return nil, wrapSignError(err)
	}
	if err := s.store.AddCertificate(ctx, Certificate{Serial: crt.SerialNumber, OrgID: w.OrgID, WorkerID: w.ID, NotBefore: crt.NotBefore, NotAfter: crt.NotAfter}); err != nil {
		return nil, fmt.Errorf("record renewed certificate: %w", err)
	}
	return pemBytes, nil
}
