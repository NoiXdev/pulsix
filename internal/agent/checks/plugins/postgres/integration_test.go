package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The image internal/db/testdb uses, so no further image is pulled.
const testImage = "timescale/timescaledb:2.30.0-pg18"

// TestAgainstARealServer runs the plugin against a real PostgreSQL with a
// dedicated pg_monitor user, the setup the docs tell an operator to make.
func TestAgainstARealServer(t *testing.T) {
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, testImage,
		tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("admin"), tcpostgres.WithPassword("admin-pw"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)))
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	port := mapped.Port()

	admin, err := pgx.Connect(ctx, fmt.Sprintf("postgres://admin:admin-pw@%s:%s/postgres?sslmode=disable", host, port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	for _, q := range []string{
		`CREATE ROLE monitor LOGIN PASSWORD 'monitor-pw'`,
		`CREATE ROLE plain LOGIN PASSWORD 'plain-pw'`,
		`GRANT pg_monitor TO monitor`,
		`CREATE DATABASE sized_db`,
		`CREATE DATABASE walled_db`,
		`REVOKE CONNECT ON DATABASE walled_db FROM PUBLIC`,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Some bulk in sized_db so its size is more than an empty catalogue's.
	sized, err := pgx.Connect(ctx, fmt.Sprintf("postgres://admin:admin-pw@%s:%s/sized_db?sslmode=disable", host, port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sized.Close(ctx) }()
	if _, err := sized.Exec(ctx, `CREATE TABLE bulk AS SELECT g, repeat('x', 200) AS pad FROM generate_series(1, 20000) g`); err != nil {
		t.Fatal(err)
	}

	fields := func(extra map[string]string) map[string]string {
		f := map[string]string{"host": host, "port": port, "user": "monitor", "password": "monitor-pw", "sslmode": "disable"}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	p := New().(plugin)

	t.Run("healthy", func(t *testing.T) {
		obs, err := p.CollectWithSecret(ctx, nil, fields(nil))
		if err != nil {
			t.Fatal(err)
		}
		mustValue(t, obs, "", "pg.up", 1)
		conns, _ := find(obs, "", "pg.connections")
		max, _ := find(obs, "", "pg.max_connections")
		pct, _ := find(obs, "", "pg.connections_pct")
		if conns.Value == nil || *conns.Value < 2 || max.Value == nil || *max.Value < 10 || pct.Value == nil || *pct.Value <= 0 {
			t.Errorf("connections %+v max %+v pct %+v", conns, max, pct)
		}
		mustValue(t, obs, "", "pg.in_recovery", 0)
		mustValue(t, obs, "", "pg.replication_lag_seconds", 0)
		mustValue(t, obs, "", "pg.replication_lag_bytes", 0)
		mustValue(t, obs, "", "pg.oldest_xact_seconds", 0)
		size, ok := find(obs, "sized_db", "pg.database_size_bytes")
		if !ok || size.Value == nil || *size.Value < 4_000_000 {
			t.Errorf("sized_db size = %+v", size)
		}
		if _, ok := find(obs, "template0", "pg.database_size_bytes"); ok {
			t.Error("a template database is reported")
		}
		if _, ok := find(obs, "template1", "pg.database_size_bytes"); ok {
			t.Error("a template database is reported")
		}
		// monitor holds pg_monitor (pg_read_all_stats), which may read sizes.
		if o, ok := find(obs, "walled_db", "pg.database_size_bytes"); !ok || o.Value == nil {
			t.Errorf("walled_db = %+v", o)
		}
	})

	t.Run("a user without pg_monitor", func(t *testing.T) {
		obs, err := p.CollectWithSecret(ctx, nil, fields(map[string]string{"user": "plain", "password": "plain-pw"}))
		if err != nil {
			t.Fatal(err)
		}
		mustValue(t, obs, "", "pg.up", 1)
		mustErr(t, obs, "walled_db", "pg.database_size_bytes", "postgres: no privilege to read the size")
		if o, ok := find(obs, "sized_db", "pg.database_size_bytes"); !ok || o.Value == nil {
			t.Errorf("sized_db = %+v", o)
		}
	})

	t.Run("databases filter", func(t *testing.T) {
		obs, err := p.CollectWithSecret(ctx, map[string]any{"databases": []any{"sized_db", "absent_db"}}, fields(nil))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := find(obs, "postgres", "pg.database_size_bytes"); ok {
			t.Error("unlisted database reported")
		}
		if o, ok := find(obs, "sized_db", "pg.database_size_bytes"); !ok || o.Value == nil {
			t.Errorf("sized_db = %+v", o)
		}
		mustErr(t, obs, "absent_db", "pg.database_size_bytes", "postgres: database does not exist")
	})

	t.Run("oldest transaction", func(t *testing.T) {
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT 1`); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1200 * time.Millisecond)
		obs, err := p.CollectWithSecret(ctx, nil, fields(nil))
		if err != nil {
			t.Fatal(err)
		}
		o, _ := find(obs, "", "pg.oldest_xact_seconds")
		if o.Value == nil || *o.Value < 1 {
			t.Errorf("oldest_xact_seconds = %+v", o)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		obs, err := p.CollectWithSecret(ctx, nil, fields(map[string]string{"password": "wrong-" + plantedPassword}))
		if err != nil {
			t.Fatal(err)
		}
		mustValue(t, obs, "", "pg.up", 0)
		mustErr(t, obs, "", "pg.connections", "postgres: authentication failed (SQLSTATE 28P01)")
		noSecretIn(t, obs, err)
	})

	t.Run("unknown database", func(t *testing.T) {
		obs, err := p.CollectWithSecret(ctx, nil, fields(map[string]string{"database": "nope"}))
		if err != nil {
			t.Fatal(err)
		}
		mustValue(t, obs, "", "pg.up", 0)
		mustErr(t, obs, "", "pg.connections", "postgres: server error (SQLSTATE 3D000)")
	})

	t.Run("TLS required but not offered by the server", func(t *testing.T) {
		obs, err := p.CollectWithSecret(ctx, nil, fields(map[string]string{"sslmode": "verify-full"}))
		if err != nil {
			t.Fatal(err)
		}
		mustValue(t, obs, "", "pg.up", 0)
		mustErr(t, obs, "", "pg.connections", "postgres: TLS handshake failed")
		noSecretIn(t, obs, err)
	})

	t.Run("prefer falls back to plain", func(t *testing.T) {
		obs, err := p.CollectWithSecret(ctx, nil, fields(map[string]string{"sslmode": "prefer"}))
		if err != nil {
			t.Fatal(err)
		}
		mustValue(t, obs, "", "pg.up", 1)
	})
}
