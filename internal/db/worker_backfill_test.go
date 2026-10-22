package db_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestWorkerCertificateBackfill: a worker registered before 00027 keeps
// authenticating — its serial is copied into worker_certificates.
func TestWorkerCertificateBackfill(t *testing.T) {
	ctx := context.Background()
	adminURL := testdb.URL(t)
	admin, err := db.Open(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	// The trailing 12 hex characters of a UUIDv7 are its random tail, not
	// the timestamp (which dominates the leading characters and barely
	// changes within a test run) — two concurrent runs against the same
	// shared Postgres container must not pick the same database name.
	id := strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
	name := "backfill_" + id[20:]
	if _, err := admin.Pool().Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Pool().Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	fresh, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	sqlDB := stdlib.OpenDBFromPool(fresh.Pool())
	defer sqlDB.Close() //nolint:errcheck
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 26); err != nil {
		t.Fatal(err)
	}
	org, zone, worker := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, slug, name) VALUES ($1, 'bf', 'bf')`, []any{org}},
		{`INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'default', 'Default')`, []any{zone, org}},
		{`INSERT INTO workers (id, org_id, zone_id, name, cert_serial) VALUES ($1, $2, $3, 'old', '4242')`, []any{worker, org, zone}},
	} {
		if _, err := fresh.Pool().Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var got uuid.UUID
	var status string
	if err := fresh.Pool().QueryRow(ctx, `SELECT c.worker_id, w.status FROM worker_certificates c JOIN workers w ON w.id = c.worker_id WHERE c.serial = '4242'`).Scan(&got, &status); err != nil {
		t.Fatal(err)
	}
	if got != worker || status != "active" {
		t.Fatalf("backfill = %v %q", got, status)
	}
}

// TestWorkerEnrollmentOrphanBackfill is 00065's own regression test: a
// plain ADD CONSTRAINT validates every existing row, and worker_id has had
// no foreign key since 00027 while workers could always be deleted
// (workers.Delete) — on any installation where that already happened
// before this migration ran, a naive "just add the FK" would find a
// dangling worker_id and abort goose up outright, failing the deploy.
// Seeds exactly that shape (an enrollment pointing at a worker id that was
// never actually created, the same as one that existed and was deleted)
// at the schema version immediately before 00065, then migrates through
// it and asserts the dangling reference was nulled — matching the
// ON DELETE SET NULL the constraint itself now enforces going forward —
// rather than the migration failing.
func TestWorkerEnrollmentOrphanBackfill(t *testing.T) {
	ctx := context.Background()
	adminURL := testdb.URL(t)
	admin, err := db.Open(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	id := strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
	name := "backfill_" + id[20:]
	if _, err := admin.Pool().Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Pool().Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	fresh, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	sqlDB := stdlib.OpenDBFromPool(fresh.Pool())
	defer sqlDB.Close() //nolint:errcheck
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	// 64: immediately before 00065, with 00027's worker_enrollments table
	// (and its FK-less worker_id) already in place.
	if _, err := provider.UpTo(ctx, 64); err != nil {
		t.Fatal(err)
	}
	org, zone, enrollment, danglingWorker := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, slug, name) VALUES ($1, 'bf-enroll', 'bf-enroll')`, []any{org}},
		{`INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'default', 'Default')`, []any{zone, org}},
		// No foreign key exists yet at this schema version, so this insert
		// succeeds even though danglingWorker names no row in workers —
		// exactly the shape a worker's later deletion (workers.Delete) would
		// have left behind for real.
		{`INSERT INTO worker_enrollments (id, org_id, zone_id, name, secret_hash, expires_at, worker_id, used_at)
		  VALUES ($1, $2, $3, 'orphaned', '\x00', now() + interval '1 hour', $4, now())`, []any{enrollment, org, zone, danglingWorker}},
	} {
		if _, err := fresh.Pool().Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migrate through 00065 with a dangling worker_id present: %v", err)
	}
	var workerID *uuid.UUID
	if err := fresh.Pool().QueryRow(ctx, `SELECT worker_id FROM worker_enrollments WHERE id = $1`, enrollment).Scan(&workerID); err != nil {
		t.Fatal(err)
	}
	if workerID != nil {
		t.Fatalf("worker_id after migrating through 00065 = %v, want NULL (the dangling reference backfilled away)", *workerID)
	}
	// The constraint itself must actually be in place, not merely skipped:
	// a worker_id naming an org from a DIFFERENT organization than the
	// enrollment's own must now be refused (the composite FK, not just
	// "some FK or other").
	otherOrg := uuid.Must(uuid.NewV7())
	if _, err := fresh.Pool().Exec(ctx, `INSERT INTO organizations (id, slug, name) VALUES ($1, 'bf-enroll-other', 'bf-enroll-other')`, otherOrg); err != nil {
		t.Fatal(err)
	}
	otherZone := uuid.Must(uuid.NewV7())
	if _, err := fresh.Pool().Exec(ctx, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'default', 'Default')`, otherZone, otherOrg); err != nil {
		t.Fatal(err)
	}
	otherWorker := uuid.Must(uuid.NewV7())
	if _, err := fresh.Pool().Exec(ctx, `INSERT INTO workers (id, org_id, zone_id, name, cert_serial) VALUES ($1, $2, $3, 'w', '9999')`, otherWorker, otherOrg, otherZone); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Pool().Exec(ctx, `UPDATE worker_enrollments SET worker_id = $1 WHERE id = $2`, otherWorker, enrollment); err == nil {
		t.Fatal("pointing worker_id at a worker of a DIFFERENT organization must be refused by the new composite foreign key")
	}
}
