package workers

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries on *db.DB. The workers table
// carries no row-level security (migration 00017); its certificates and
// enrollments do, so gRPC-path writes bind the worker's org explicitly.
type SQLStore struct {
	d *db.DB
	q *gen.Queries
}

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{d: d, q: gen.New(d)} }

func (s *SQLStore) Create(ctx context.Context, w Worker, c Certificate) (Worker, error) {
	var out Worker
	err := s.d.WithWriteTx(ctx, w.OrgID, func(ctx context.Context) error {
		row, err := s.q.InsertWorker(ctx, gen.InsertWorkerParams{
			ID: w.ID, OrgID: w.OrgID, ZoneID: w.ZoneID, Name: w.Name,
			CertSerial: w.CertSerial.String(), Version: w.Version,
		})
		if err != nil {
			return err
		}
		if err := s.insertCert(ctx, c); err != nil {
			return err
		}
		out, err = toWorker(row)
		return err
	})
	if db.IsUniqueViolation(err) {
		return Worker{}, ErrNameTaken
	}
	return out, err
}

func (s *SQLStore) insertCert(ctx context.Context, c Certificate) error {
	return s.q.InsertWorkerCertificate(ctx, gen.InsertWorkerCertificateParams{
		Serial: c.Serial.String(), OrgID: c.OrgID, WorkerID: c.WorkerID, NotBefore: c.NotBefore, NotAfter: c.NotAfter,
	})
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

func (s *SQLStore) GetByName(ctx context.Context, orgID uuid.UUID, name string) (Worker, error) {
	row, err := s.q.GetWorkerByName(ctx, gen.GetWorkerByNameParams{OrgID: orgID, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, err
	}
	return toWorker(row)
}

// LookupSerial resolves a presented serial without any organization filter:
// the gRPC interceptor has no org context, and the serial is the primary
// key of worker_certificates.
func (s *SQLStore) LookupSerial(ctx context.Context, serial *big.Int) (Worker, Certificate, error) {
	if serial == nil {
		return Worker{}, Certificate{}, ErrNotFound
	}
	row, err := s.q.LookupWorkerSerial(ctx, serial.String())
	if errors.Is(err, pgx.ErrNoRows) {
		return Worker{}, Certificate{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, Certificate{}, err
	}
	w, err := toWorker(row.Worker)
	if err != nil {
		return Worker{}, Certificate{}, err
	}
	cs, ok := new(big.Int).SetString(row.CertSerialPresented, 10)
	if !ok {
		return Worker{}, Certificate{}, fmt.Errorf("certificate serial %q is not decimal", row.CertSerialPresented)
	}
	return w, Certificate{Serial: cs, OrgID: w.OrgID, WorkerID: w.ID, NotBefore: row.CertNotBefore, NotAfter: row.CertNotAfter, RevokedAt: db.TimePtr(row.CertRevokedAt)}, nil
}

func (s *SQLStore) Touch(ctx context.Context, workerID uuid.UUID, version string) error {
	return s.q.TouchWorkerByID(ctx, gen.TouchWorkerByIDParams{ID: workerID, Version: version})
}

func (s *SQLStore) AddCertificate(ctx context.Context, c Certificate) error {
	return s.insertCert(db.WithOrgScope(ctx, c.OrgID), c)
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

func (s *SQLStore) CreateEnrollment(ctx context.Context, e Enrollment, secretHash []byte) (Enrollment, error) {
	row, err := s.q.CreateWorkerEnrollment(ctx, gen.CreateWorkerEnrollmentParams{
		ID: e.ID, OrgID: e.OrgID, ZoneID: e.ZoneID, Name: e.Name, SecretHash: secretHash, ExpiresAt: e.ExpiresAt, CreatedBy: db.NullUUID(e.CreatedBy),
	})
	if err != nil {
		return Enrollment{}, fmt.Errorf("create enrollment: %w", err)
	}
	return toEnrollment(row), nil
}

func (s *SQLStore) ListEnrollments(ctx context.Context, orgID uuid.UUID, now time.Time) ([]Enrollment, error) {
	rows, err := s.q.ListPendingWorkerEnrollments(ctx, gen.ListPendingWorkerEnrollmentsParams{OrgID: orgID, Now: now})
	if err != nil {
		return nil, fmt.Errorf("list enrollments: %w", err)
	}
	out := make([]Enrollment, len(rows))
	for i, r := range rows {
		out[i] = toEnrollment(r)
	}
	return out, nil
}

func (s *SQLStore) DeleteEnrollment(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteWorkerEnrollment(ctx, gen.DeleteWorkerEnrollmentParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete enrollment: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Enroll locks the enrollment row, runs fn and, when it succeeds, writes
// worker, certificate and the used mark — one transaction. A concurrent
// second redeem blocks on the row lock and then sees used_at set.
func (s *SQLStore) Enroll(ctx context.Context, id uuid.UUID, fn func(e Enrollment, secretHash []byte) (Worker, Certificate, error)) (Worker, error) {
	orgID, err := s.q.GetWorkerEnrollmentOrg(ctx, id) // unscoped: the id is the lookup key
	if errors.Is(err, pgx.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, fmt.Errorf("find enrollment: %w", err)
	}
	var out Worker
	err = s.d.WithWriteTx(ctx, orgID, func(ctx context.Context) error {
		row, err := s.q.LockWorkerEnrollment(ctx, gen.LockWorkerEnrollmentParams{OrgID: orgID, ID: id})
		if err != nil {
			return fmt.Errorf("lock enrollment: %w", err)
		}
		w, c, err := fn(toEnrollment(row), row.SecretHash)
		if err != nil {
			return err
		}
		if _, err := s.q.InsertWorker(ctx, gen.InsertWorkerParams{ID: w.ID, OrgID: w.OrgID, ZoneID: w.ZoneID, Name: w.Name, CertSerial: w.CertSerial.String(), Version: w.Version}); err != nil {
			if db.IsUniqueViolation(err) {
				return ErrNameTaken
			}
			return fmt.Errorf("insert worker: %w", err)
		}
		if err := s.insertCert(ctx, c); err != nil {
			return fmt.Errorf("insert certificate: %w", err)
		}
		if err := s.q.MarkWorkerEnrollmentUsed(ctx, gen.MarkWorkerEnrollmentUsedParams{OrgID: orgID, ID: id, WorkerID: db.NullUUID(&w.ID)}); err != nil {
			return fmt.Errorf("mark enrollment used: %w", err)
		}
		out = w
		return nil
	})
	return out, err
}

func toWorker(r gen.Worker) (Worker, error) {
	serial, ok := new(big.Int).SetString(r.CertSerial, 10)
	if !ok {
		return Worker{}, fmt.Errorf("worker %s: parse cert serial %q: not a decimal integer", r.ID, r.CertSerial)
	}
	return Worker{
		ID: r.ID, OrgID: r.OrgID, ZoneID: r.ZoneID, Name: r.Name, CertSerial: serial,
		LastSeen: db.TimePtr(r.LastSeen), Version: r.Version, Status: Status(r.Status),
		RevokedAt: db.TimePtr(r.RevokedAt), CreatedAt: r.CreatedAt,
	}, nil
}

func toEnrollment(r gen.WorkerEnrollment) Enrollment {
	return Enrollment{
		ID: r.ID, OrgID: r.OrgID, ZoneID: r.ZoneID, Name: r.Name, ExpiresAt: r.ExpiresAt,
		UsedAt: db.TimePtr(r.UsedAt), CreatedBy: db.UUIDPtr(r.CreatedBy), CreatedAt: r.CreatedAt,
	}
}
