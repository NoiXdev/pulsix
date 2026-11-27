package app

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestBuildListensForWorkerRevocations (integration, backlog 2.54): a
// manager with the worker endpoint holds one session listening for worker
// revocations, a manager without it holds none, and Close ends the
// session.
func TestBuildListensForWorkerRevocations(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	admin, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	listeners := func() int {
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE query LIKE 'LISTEN %' AND query LIKE '%pulsix_worker_revoked%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	waitListeners := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for listeners() != want {
			if time.Now().After(deadline) {
				t.Fatalf("listening sessions = %d, want %d", listeners(), want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	withEndpoint := cfg
	withEndpoint.GRPCListen = ":0"
	a, err := Build(ctx, withEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	waitListeners(1)

	plain, err := Build(ctx, cfg)
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	defer plain.Close()
	time.Sleep(300 * time.Millisecond)
	if n := listeners(); n != 1 {
		a.Close()
		t.Fatalf("listening sessions with a manager without the worker endpoint = %d, want still 1", n)
	}

	a.Close()
	waitListeners(0)
}
