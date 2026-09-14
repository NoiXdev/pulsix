package workers

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries on *db.DB, the same way the
// credentials store is built. Every statement runs inside the request's
// organization scope; for the org-unscoped workers table that scope is
// inert and each query filters by org_id or matches by serial alone (see
// the migration and GetBySerial/Touch below).
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

func (s *SQLStore) Create(ctx context.Context, w Worker) (Worker, error) {
	row, err := s.q.InsertWorker(ctx, gen.InsertWorkerParams{
		ID: w.ID, OrgID: w.OrgID, ZoneID: w.ZoneID, Name: w.Name,
		CertSerial: w.CertSerial.String(), Version: w.Version,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Worker{}, ErrNameTaken
		}
		return Worker{}, err
	}
	return toWorker(row)
}

func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Worker, error) {
	rows, err := s.q.ListWorkers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Worker, len(rows))
	for i, r := range rows {
		w, err := toWorker(r)
		if err != nil {
			return nil, err
		}
		out[i] = w
	}
	return out, nil
}

// GetBySerial looks a worker up by certificate serial alone, deliberately
// without any organization filter: the gRPC interceptor has no org context,
// and the UNIQUE (cert_serial) constraint guarantees the serial resolves at
// most one row. An unknown (or nil, which can never belong to a stored
// worker) serial returns ErrNotFound.
func (s *SQLStore) GetBySerial(ctx context.Context, serial *big.Int) (Worker, error) {
	if serial == nil {
		return Worker{}, ErrNotFound
	}
	row, err := s.q.GetWorkerBySerial(ctx, serial.String())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Worker{}, ErrNotFound
		}
		return Worker{}, err
	}
	return toWorker(row)
}

// Touch updates last_seen and version of the worker holding serial; an
// unknown serial is a silent no-op, exactly like a worker that vanished
// between heartbeat scheduling and execution.
func (s *SQLStore) Touch(ctx context.Context, serial *big.Int, version string) error {
	if serial == nil {
		return nil
	}
	_, err := s.q.TouchWorker(ctx, gen.TouchWorkerParams{CertSerial: serial.String(), Version: version})
	return err
}

func (s *SQLStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteWorker(ctx, gen.DeleteWorkerParams{OrgID: orgID, ID: id})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func toWorker(r gen.Worker) (Worker, error) {
	serial, ok := new(big.Int).SetString(r.CertSerial, 10)
	if !ok {
		return Worker{}, fmt.Errorf("worker %s: parse cert serial %q: not a decimal integer", r.ID, r.CertSerial)
	}
	return Worker{
		ID: r.ID, OrgID: r.OrgID, ZoneID: r.ZoneID, Name: r.Name, CertSerial: serial,
		LastSeen: db.TimePtr(r.LastSeen), Version: r.Version, CreatedAt: r.CreatedAt,
	}, nil
}
