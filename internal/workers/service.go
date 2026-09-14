// Package workers is the zone-worker registry (spec §6): which workers an
// organization runs, in which zone, and by which client-certificate serial
// each one authenticates at the gRPC endpoint. The serial is the secret the
// gRPC auth layer looks a connection up by; Register, List and Delete are
// the operator-facing management surface (the `worker add` CLI lands with
// the gRPC wiring).
package workers

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors mapped to HTTP status by the transport layer.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	// ErrNameTaken means another worker of the same organization already
	// carries the name (mapped to 409 by the transport layer).
	ErrNameTaken = errors.New("name already in use")
)

// namePattern lets a name start with a lowercase letter or digit and
// continue with lowercase letters, digits or hyphens, at most 63 characters
// in total — the same shape host names follow, without dots or underscores.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Worker is a registered zone worker.
type Worker struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	ZoneID     uuid.UUID
	Name       string
	CertSerial *big.Int // decimal serial of the worker's client certificate
	LastSeen   *time.Time
	Version    string
	CreatedAt  time.Time
}

// RegisterInput is a new worker. CertSerial must be non-nil and positive —
// the serial the internal CA issued the worker's certificate with.
type RegisterInput struct {
	ZoneID     uuid.UUID
	Name       string
	CertSerial *big.Int
	Version    string
}

// Store persists workers. Create returns ErrNameTaken on any uniqueness
// clash; GetBySerial and Touch deliberately take no organization — they run
// outside the org scope, matching workers by serial alone.
type Store interface {
	Create(ctx context.Context, w Worker) (Worker, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Worker, error)
	GetBySerial(ctx context.Context, serial *big.Int) (Worker, error)
	Touch(ctx context.Context, serial *big.Int, version string) error
	// Delete removes an absent worker's row as ErrNotFound.
	Delete(ctx context.Context, orgID, id uuid.UUID) error
}

// Service implements the worker registry.
type Service struct{ store Store }

// NewService wires the store.
func NewService(store Store) *Service { return &Service{store: store} }

// Register validates the input and stores a new worker of orgID. The zone
// must exist and belong to orgID — a zone from another organization fails
// the composite foreign key, which surfaces as a plain wrapped store error,
// not as one of this package's sentinels.
func (s *Service) Register(ctx context.Context, orgID uuid.UUID, in RegisterInput) (Worker, error) {
	if !namePattern.MatchString(in.Name) {
		return Worker{}, fmt.Errorf("%w: name must be 1-63 lowercase letters, digits or hyphens, starting with a letter or digit", ErrInvalidInput)
	}
	if in.ZoneID == uuid.Nil {
		return Worker{}, fmt.Errorf("%w: zone is required", ErrInvalidInput)
	}
	if in.CertSerial == nil || in.CertSerial.Sign() <= 0 {
		return Worker{}, fmt.Errorf("%w: certificate serial must be a positive integer", ErrInvalidInput)
	}
	w := Worker{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, ZoneID: in.ZoneID,
		Name: in.Name, CertSerial: in.CertSerial, Version: in.Version,
	}
	w, err := s.store.Create(ctx, w)
	if err != nil {
		return Worker{}, fmt.Errorf("register worker: %w", err)
	}
	return w, nil
}

// List returns orgID's workers ordered by name.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Worker, error) {
	return s.store.List(ctx, orgID)
}

// GetBySerial is the gRPC authentication path: resolve the worker that
// authenticates with a client certificate of this serial, regardless of
// organization. An unknown serial — a revoked worker — is ErrNotFound.
func (s *Service) GetBySerial(ctx context.Context, serial *big.Int) (Worker, error) {
	return s.store.GetBySerial(ctx, serial)
}

// Touch records a heartbeat: last_seen becomes now and version the
// worker's self-reported build. An unknown serial is a silent no-op.
func (s *Service) Touch(ctx context.Context, serial *big.Int, version string) error {
	return s.store.Touch(ctx, serial, version)
}

// Delete revokes a worker; deleting an absent one is ErrNotFound.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.Delete(ctx, orgID, id)
}
