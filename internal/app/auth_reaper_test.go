package app

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// TestBuildRunsAuthReaperAndStopsCleanly (backlog 1.21): Build starts the
// expired-ceremony and expired-session reaper with the app — the seeded
// expired rows disappear — and Close ends it: a row seeded afterwards (or
// one still inside its grace window) stays for good, because no sweep can
// run once startLoop's stop has returned.
func TestBuildRunsAuthReaperAndStopsCleanly(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)

	// Seed the rows through a plain admin connection before Build, so the
	// reaper (once started) finds them. Anchored on the real clock: the
	// production wiring runs on time.Now, so "beyond the grace window" is
	// relative to it.
	base := time.Now().UTC()
	admin, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	q := gen.New(admin.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "reaper@example.test", DisplayName: "Reaper"}); err != nil {
		t.Fatal(err)
	}
	// Gone once the reaper has swept: an expired ceremony and a session
	// expired beyond the 7-day grace.
	expiredCeremony := uuid.Must(uuid.NewV7())
	if err := q.PutCeremony(ctx, gen.PutCeremonyParams{
		ID: expiredCeremony, Kind: "login", Data: []byte(`{}`), ExpiresAt: base.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	expiredSession := uuid.Must(uuid.NewV7())
	if err := q.CreateSession(ctx, gen.CreateSessionParams{
		ID: expiredSession, UserID: uid, SecretHash: []byte("hash"), ExpiresAt: base.Add(-8 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// Stays: a valid session and one expired only within the grace window
	// (the reaper bounds growth, it never shortens the forensic window).
	liveSession := uuid.Must(uuid.NewV7())
	if err := q.CreateSession(ctx, gen.CreateSessionParams{
		ID: liveSession, UserID: uid, SecretHash: []byte("hash"), ExpiresAt: base.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	graceSession := uuid.Must(uuid.NewV7())
	if err := q.CreateSession(ctx, gen.CreateSessionParams{
		ID: graceSession, UserID: uid, SecretHash: []byte("hash"), ExpiresAt: base.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	a, err := Build(ctx, cfg, WithAuthReaperInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			a.Close()
		}
	}()

	// The expired rows disappear (polled: the first sweep lands on the
	// first tick); the live and grace-window rows never do.
	expired := func() bool {
		return countRows(t, admin.Pool(), expiredSession) != 0 || countCeremonies(t, admin.Pool(), expiredCeremony) != 0
	}
	deadline := time.Now().Add(3 * time.Second)
	for expired() {
		if time.Now().After(deadline) {
			t.Fatalf("the reaper never reaped the expired rows (session=%d, ceremony=%d)",
				countRows(t, admin.Pool(), expiredSession), countCeremonies(t, admin.Pool(), expiredCeremony))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := countRows(t, admin.Pool(), expiredSession); n != 0 {
		t.Fatalf("expired session rows = %d, want 0", n)
	}
	if n := countRows(t, admin.Pool(), liveSession); n != 1 {
		t.Fatalf("live session rows = %d, want 1 — the reaper must leave it alone", n)
	}
	if n := countRows(t, admin.Pool(), graceSession); n != 1 {
		t.Fatalf("grace-window session rows = %d, want 1 — the reaper must leave it alone", n)
	}

	// Close returns (startLoop's stop waits for the goroutine to exit) and
	// no sweep runs afterwards: a session seeded past the grace window
	// after Close stays, even given several intervals' worth of time.
	a.Close()
	closed = true
	postCloseSession := uuid.Must(uuid.NewV7())
	if err := q.CreateSession(ctx, gen.CreateSessionParams{
		ID: postCloseSession, UserID: uid, SecretHash: []byte("hash"), ExpiresAt: base.Add(-8 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := countRows(t, admin.Pool(), postCloseSession); n != 1 {
		t.Fatal("a session seeded after Close must survive — the reaper must be stopped")
	}
}

// countRows counts the sessions rows with the given id.
func countRows(t *testing.T, pool *pgxpool.Pool, sessionID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE id = $1`, sessionID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// countCeremonies counts the webauthn_ceremonies rows with the given id.
func countCeremonies(t *testing.T, pool *pgxpool.Pool, ceremonyID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM webauthn_ceremonies WHERE id = $1`, ceremonyID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
