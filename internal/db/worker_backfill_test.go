package db_test

import (
	"context"
	"net/url"
	"os"
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
	name := "backfill_" + uuid.Must(uuid.NewV7()).String()[:8]
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
