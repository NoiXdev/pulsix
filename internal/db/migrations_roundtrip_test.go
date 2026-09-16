// The execution milestone's migrations roll back and re-apply cleanly
// (final review triage): each Down must undo its Up in a working order —
// 00017's down dropped the zones (org_id, id) constraint while the workers
// table's foreign key still depended on it.
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

// lastPreMilestoneVersion is the newest migration before v0.2b (00016_ca is
// the milestone's first).
const lastPreMilestoneVersion = 15

func TestExecutionMigrationsRollBackAndReapply(t *testing.T) {
	ctx := context.Background()
	adminURL := testdb.URL(t)
	admin, err := db.Open(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := "migrations_roundtrip_" + uuid.Must(uuid.NewV7()).String()[:8]
	if _, err := admin.Pool().Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Pool().Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })

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
	if err := fresh.Migrate(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	sqlDB := stdlib.OpenDBFromPool(fresh.Pool())
	defer sqlDB.Close() //nolint:errcheck
	goose.SetBaseFS(nil)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	fsys := os.DirFS("migrations")
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys)
	if err != nil {
		t.Fatal(err)
	}
	current, err := provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, lastPreMilestoneVersion); err != nil {
		t.Fatalf("roll back from %d to %d: %v", current, lastPreMilestoneVersion, err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-apply after rollback: %v", err)
	}
	if again, err := provider.GetDBVersion(ctx); err != nil || again != current {
		t.Fatalf("version after the round trip = %d, %v; want %d", again, err, current)
	}
}
