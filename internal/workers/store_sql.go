package workers

import (
	"context"
	"encoding/json"
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
		if errors.Is(err, pgx.ErrNoRows) {
			// Withdrawn between GetWorkerEnrollmentOrg and here: fold into
			// the same ErrNotFound the service maps to ErrEnrollmentRefused,
			// so a withdraw racing a redeem never surfaces a raw error.
			return ErrNotFound
		}
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
		// The single-use guard: a conditional UPDATE, not the closure
		// above. 0 rows means another redeem (or a lapsed expiry, judged
		// by the database's own now()) won the race after the lock was
		// taken but before this statement ran; the whole transaction —
		// including the worker and certificate just inserted — rolls back.
		n, err := s.q.MarkWorkerEnrollmentUsed(ctx, gen.MarkWorkerEnrollmentUsedParams{OrgID: orgID, ID: id, WorkerID: db.NullUUID(&w.ID)})
		if err != nil {
			return fmt.Errorf("mark enrollment used: %w", err)
		}
		if n == 0 {
			return ErrEnrollmentRefused
		}
		out = w
		return nil
	})
	return out, err
}

// AdoptCertificate implements Store.AdoptCertificate in one transaction
// bound to c.OrgID: lock the worker row by (org, name), read its
// certificates, ask allow and, unless the serial is already registered for
// that worker, insert the certificate.
func (s *SQLStore) AdoptCertificate(ctx context.Context, name string, c Certificate, allow func(AdoptionState) error) (Worker, error) {
	var out Worker
	err := s.d.WithWriteTx(ctx, c.OrgID, func(ctx context.Context) error {
		row, err := s.q.LockWorkerByName(ctx, gen.LockWorkerByNameParams{OrgID: c.OrgID, Name: name})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock worker: %w", err)
		}
		w, err := toWorker(row)
		if err != nil {
			return err
		}
		state := AdoptionState{Worker: w}
		// Two calls on a fresh connection can race in with the same
		// unknown serial; the one that waited on the lock finds it
		// registered by the other. A serial registered anywhere else, or
		// revoked since, is not this adoption's to make.
		known, err := s.q.LookupWorkerSerial(ctx, c.Serial.String())
		switch {
		case err == nil && known.Worker.ID == w.ID && !known.CertRevokedAt.Valid:
			state.Registered = true
		case err == nil:
			return ErrNotFound
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("look up certificate: %w", err)
		}
		newest, err := s.q.NewestWorkerCertificateStart(ctx, gen.NewestWorkerCertificateStartParams{OrgID: c.OrgID, WorkerID: w.ID})
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
			c.WorkerID = w.ID
			if err := s.insertCert(ctx, c); err != nil {
				return fmt.Errorf("insert certificate: %w", err)
			}
		}
		w.CertSerial = c.Serial
		out = w
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

func (s *SQLStore) Get(ctx context.Context, orgID, id uuid.UUID) (Worker, error) {
	row, err := s.q.GetWorker(ctx, gen.GetWorkerParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, err
	}
	return toWorker(row)
}

// Revoke marks the worker and every certificate it holds revoked, in one
// transaction bound to the worker's organization.
func (s *SQLStore) Revoke(ctx context.Context, orgID, id uuid.UUID) (Worker, error) {
	var out Worker
	err := s.d.WithWriteTx(ctx, orgID, func(ctx context.Context) error {
		row, err := s.q.RevokeWorker(ctx, gen.RevokeWorkerParams{OrgID: orgID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("revoke worker: %w", err)
		}
		if err := s.q.RevokeWorkerCertificates(ctx, gen.RevokeWorkerCertificatesParams{OrgID: orgID, WorkerID: id}); err != nil {
			return fmt.Errorf("revoke worker certificates: %w", err)
		}
		out, err = toWorker(row)
		return err
	})
	return out, err
}

func (s *SQLStore) ViewDetails(ctx context.Context, orgID uuid.UUID) (ViewDetails, error) {
	d := ViewDetails{Expiry: map[uuid.UUID]time.Time{}, CurrentJob: map[uuid.UUID]uuid.UUID{}}
	exp, err := s.q.ListWorkerCertExpiries(ctx, orgID)
	if err != nil {
		return d, fmt.Errorf("list certificate expiries: %w", err)
	}
	for _, e := range exp {
		d.Expiry[e.WorkerID] = e.ExpiresAt
	}
	cur, err := s.q.ListWorkerCurrentJobs(ctx, orgID)
	if err != nil {
		return d, fmt.Errorf("list current jobs: %w", err)
	}
	for _, c := range cur {
		if w := db.UUIDPtr(c.WorkerID); w != nil {
			d.CurrentJob[*w] = c.ID
		}
	}
	return d, nil
}
