// Package workers is the zone-worker registry (spec §6, spec 2026-09-18
// §4): which workers an organization runs, in which zone, which client
// certificates each one authenticates with, and the one-time enrollment
// tokens new workers redeem. The gRPC interceptor resolves every presented
// certificate serial here; a revoked certificate or worker is refused.
package workers

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/restoremark"
)

// Sentinel errors mapped to HTTP / gRPC status by the transport layers.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	// ErrNameTaken means another worker of the same organization already
	// carries the name (mapped to 409 by the transport layer).
	ErrNameTaken = errors.New("name already in use")
	// ErrRevoked refuses a revoked certificate or a revoked worker.
	ErrRevoked = errors.New("worker or certificate revoked")
	// ErrZoneMismatch refuses attaching a certificate for one zone to a
	// worker row enrolled in another.
	ErrZoneMismatch = errors.New("worker is enrolled in another zone")
)

// EmbeddedName is the name the manager's embedded worker registers under.
// It is reserved: Register and CreateEnrollment refuse it, only
// RegisterOrAttach (the embedded worker's own path) may use it.
const EmbeddedName = "embedded"

// namePattern lets a name start with a lowercase letter or digit and
// continue with lowercase letters, digits or hyphens, at most 63 characters
// in total — the same shape host names follow, without dots or underscores.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Status is a worker's lifecycle state; online/offline is derived from
// last_seen, never stored.
type Status string

const (
	StatusActive  Status = "active"
	StatusRevoked Status = "revoked"
)

// Worker is a registered zone worker. CertSerial is the serial the worker
// authenticated with (GetBySerial) or its first certificate's serial.
type Worker struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	ZoneID     uuid.UUID
	Name       string
	CertSerial *big.Int
	LastSeen   *time.Time
	Version    string
	Status     Status
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// Certificate is one client certificate a worker holds.
type Certificate struct {
	Serial              *big.Int
	OrgID, WorkerID     uuid.UUID
	NotBefore, NotAfter time.Time
	RevokedAt           *time.Time
}

// RegisterInput is a new worker with its first certificate. CertSerial
// must be positive; a zero NotBefore/NotAfter records now / now +
// ca.LeafValidity (the certificate was just issued with that window).
type RegisterInput struct {
	ZoneID              uuid.UUID
	Name                string
	CertSerial          *big.Int
	Version             string
	NotBefore, NotAfter time.Time
}

// Store persists workers, certificates and enrollments. LookupSerial and
// Touch run outside any org scope (the gRPC interceptor has none).
type Store interface {
	Create(ctx context.Context, w Worker, c Certificate) (Worker, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Worker, error)
	GetByName(ctx context.Context, orgID uuid.UUID, name string) (Worker, error)
	LookupSerial(ctx context.Context, serial *big.Int) (Worker, Certificate, error)
	Touch(ctx context.Context, workerID uuid.UUID, version string) error
	AddCertificate(ctx context.Context, c Certificate) error
	Get(ctx context.Context, orgID, id uuid.UUID) (Worker, error)
	Revoke(ctx context.Context, orgID, id uuid.UUID) (Worker, error) // worker + all its certificates, one tx
	ViewDetails(ctx context.Context, orgID uuid.UUID) (ViewDetails, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	CreateEnrollment(ctx context.Context, e Enrollment, secretHash []byte) (Enrollment, error)
	ListEnrollments(ctx context.Context, orgID uuid.UUID, now time.Time) ([]Enrollment, error)
	DeleteEnrollment(ctx context.Context, orgID, id uuid.UUID) error
	Enroll(ctx context.Context, id uuid.UUID, fn func(e Enrollment, secretHash []byte) (Worker, Certificate, error)) (Worker, error)
	// RestoreWindow reads the last restore's window, outside any org scope
	// and without a lock; nil when no usable restore marker exists.
	RestoreWindow(ctx context.Context) (*restoremark.Window, error)
	// AdoptCertificate locks the worker named name in c.OrgID, hands what
	// it read to allow and, when allow returns nil, inserts c for that
	// worker unless it is already registered there — one transaction. No
	// such worker, or c's serial registered to another worker or revoked,
	// is ErrNotFound.
	AdoptCertificate(ctx context.Context, name string, c Certificate, allow func(AdoptionState) error) (Worker, error)
}

// Service implements the worker registry.
type Service struct {
	store  Store
	signer Signer
	zones  ZoneLister
	now    func() time.Time
	rand   io.Reader
	// streams and jobs are the revocation hooks (SetRevokeHooks); nil
	// leaves a revocation at the registry rows.
	streams StreamCloser
	jobs    JobRequeuer
}

// NewService wires the store; enrollment and renewal need WithSigner and
// WithZones.
func NewService(store Store, opts ...Option) *Service {
	s := &Service{store: store, now: time.Now, rand: rand.Reader}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Register validates the input and stores a new worker with its first
// certificate. The reserved EmbeddedName is refused.
func (s *Service) Register(ctx context.Context, orgID uuid.UUID, in RegisterInput) (Worker, error) {
	if err := checkNotReserved(in.Name); err != nil {
		return Worker{}, err
	}
	return s.register(ctx, orgID, in)
}

// checkNotReserved refuses EmbeddedName for every path but the embedded
// worker's own.
func checkNotReserved(name string) error {
	if name == EmbeddedName {
		return fmt.Errorf("%w: the name %q is reserved for the manager's embedded worker", ErrInvalidInput, name)
	}
	return nil
}

// register is Register without the reserved-name check.
func (s *Service) register(ctx context.Context, orgID uuid.UUID, in RegisterInput) (Worker, error) {
	if !namePattern.MatchString(in.Name) {
		return Worker{}, fmt.Errorf("%w: name must be 1-63 lowercase letters, digits or hyphens, starting with a letter or digit", ErrInvalidInput)
	}
	if in.ZoneID == uuid.Nil {
		return Worker{}, fmt.Errorf("%w: zone is required", ErrInvalidInput)
	}
	if in.CertSerial == nil || in.CertSerial.Sign() <= 0 {
		return Worker{}, fmt.Errorf("%w: certificate serial must be a positive integer", ErrInvalidInput)
	}
	id, err := uuid.NewV7FromReader(s.rand)
	if err != nil {
		return Worker{}, err
	}
	w := Worker{ID: id, OrgID: orgID, ZoneID: in.ZoneID, Name: in.Name, CertSerial: in.CertSerial, Version: in.Version, Status: StatusActive}
	w, err = s.store.Create(ctx, w, s.certificateFor(orgID, id, in))
	if err != nil {
		return Worker{}, fmt.Errorf("register worker: %w", err)
	}
	return w, nil
}

// RegisterOrAttach is Register for a worker whose name may already exist
// (the embedded worker, re-issued on every manager start), and the one
// path that may use EmbeddedName: an existing active row gets the new
// certificate; a revoked one stays refused (ErrRevoked), and so does a row
// enrolled in another zone (ErrZoneMismatch) — the interceptor would
// refuse a certificate whose zone differs from its row anyway.
func (s *Service) RegisterOrAttach(ctx context.Context, orgID uuid.UUID, in RegisterInput) (Worker, error) {
	cur, err := s.store.GetByName(ctx, orgID, in.Name)
	if errors.Is(err, ErrNotFound) {
		return s.register(ctx, orgID, in)
	}
	if err != nil {
		return Worker{}, err
	}
	if cur.Status == StatusRevoked {
		return Worker{}, ErrRevoked
	}
	if cur.ZoneID != in.ZoneID {
		return Worker{}, fmt.Errorf("%w: worker %q belongs to zone %s, not %s", ErrZoneMismatch, cur.Name, cur.ZoneID, in.ZoneID)
	}
	if in.CertSerial == nil || in.CertSerial.Sign() <= 0 {
		return Worker{}, fmt.Errorf("%w: certificate serial must be a positive integer", ErrInvalidInput)
	}
	if err := s.store.AddCertificate(ctx, s.certificateFor(cur.OrgID, cur.ID, in)); err != nil {
		return Worker{}, fmt.Errorf("attach certificate: %w", err)
	}
	cur.CertSerial = in.CertSerial
	return cur, nil
}

func (s *Service) certificateFor(orgID, workerID uuid.UUID, in RegisterInput) Certificate {
	nb, na := in.NotBefore, in.NotAfter
	if nb.IsZero() {
		nb = s.now()
	}
	if na.IsZero() {
		na = nb.Add(ca.LeafValidity)
	}
	return Certificate{Serial: in.CertSerial, OrgID: orgID, WorkerID: workerID, NotBefore: nb, NotAfter: na}
}

// List returns orgID's workers ordered by name.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Worker, error) {
	return s.store.List(ctx, orgID)
}

// GetBySerial is the gRPC authentication path. The returned Worker carries
// the presented serial in CertSerial. Unknown serials are ErrNotFound; a
// revoked certificate or worker is ErrRevoked — both refuse the call.
func (s *Service) GetBySerial(ctx context.Context, serial *big.Int) (Worker, error) {
	if serial == nil {
		return Worker{}, ErrNotFound
	}
	w, c, err := s.store.LookupSerial(ctx, serial)
	if err != nil {
		return Worker{}, err
	}
	if c.RevokedAt != nil || w.Status == StatusRevoked {
		return Worker{}, ErrRevoked
	}
	w.CertSerial = c.Serial
	return w, nil
}

// Touch records a heartbeat: last_seen becomes now and version the
// worker's self-reported build.
func (s *Service) Touch(ctx context.Context, workerID uuid.UUID, version string) error {
	return s.store.Touch(ctx, workerID, version)
}
