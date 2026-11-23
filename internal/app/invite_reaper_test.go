package app

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
)

// TestBuildRunsInviteReaperAndStopsCleanly (backlog 3.128): Build starts the
// stale-invitation purge — an unaccepted invitation that expired 31 days ago
// disappears, a fresher one stays — and Close ends it, so one seeded
// afterwards survives.
func TestBuildRunsInviteReaperAndStopsCleanly(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	admin, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	pool := admin.Pool()

	orgID, userID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, slug, name) VALUES ($1, 'invite-reaper', 'Invite reaper')`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, display_name) VALUES ($1, 'invite-reaper@example.test', 'R')`, userID); err != nil {
		t.Fatal(err)
	}
	seed := func(email string, expiresAgo time.Duration) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		if _, err := pool.Exec(ctx,
			`INSERT INTO invites (id, org_id, email, role, token_hash, invited_by, expires_at)
			 VALUES ($1, $2, $3, 'operator', $4, $5, now() - make_interval(secs => $6))`,
			id, orgID, email, id[:], userID, expiresAgo.Seconds()); err != nil {
			t.Fatal(err)
		}
		return id
	}
	count := func(id uuid.UUID) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM invites WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	day := 24 * time.Hour
	stale, fresh := seed("stale@invitee.test", 31*day), seed("fresh@invitee.test", 29*day)

	a, err := Build(ctx, cfg, WithInviteReaperInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			a.Close()
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for count(stale) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the invite reaper never removed the stale invitation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count(fresh) != 1 {
		t.Fatal("an invitation inside the list window must stay")
	}

	a.Close()
	closed = true
	afterClose := seed("after@invitee.test", 31*day)
	time.Sleep(50 * time.Millisecond)
	if count(afterClose) != 1 {
		t.Fatal("an invitation seeded after Close must survive — the reaper must be stopped")
	}
}
