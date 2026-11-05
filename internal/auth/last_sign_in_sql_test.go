package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func lastSignIn(t *testing.T, d *db.DB, uid uuid.UUID) *time.Time {
	t.Helper()
	var at *time.Time
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT last_sign_in_at FROM users WHERE id = $1`, uid).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestLastSignInStampedOnCompletion(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	s := auth.NewSessions(q, time.Now)
	newUser := func(email string) uuid.UUID {
		uid := uuid.Must(uuid.NewV7())
		if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: email, DisplayName: "U"}); err != nil {
			t.Fatal(err)
		}
		return uid
	}

	t.Run("a password session is not a completed sign-in", func(t *testing.T) {
		uid := newUser("pw@example.test")
		if _, _, err := s.Create(ctx, uid, false); err != nil {
			t.Fatal(err)
		}
		if at := lastSignIn(t, d, uid); at != nil {
			t.Fatalf("last_sign_in_at = %v after an incomplete session, want NULL", at)
		}
	})

	t.Run("completing the second factor stamps once", func(t *testing.T) {
		uid := newUser("mfa@example.test")
		id, _, err := s.Create(ctx, uid, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.MarkMFAComplete(ctx, id); err != nil {
			t.Fatal(err)
		}
		first := lastSignIn(t, d, uid)
		if first == nil {
			t.Fatal("last_sign_in_at is NULL after MarkMFAComplete")
		}
		// A step-up re-proves the factor on an already complete session
		// and must not look like a new sign-in.
		time.Sleep(20 * time.Millisecond)
		if _, err := s.MarkMFAComplete(ctx, id); err != nil {
			t.Fatal(err)
		}
		second := lastSignIn(t, d, uid)
		if second == nil || !second.Equal(*first) {
			t.Fatalf("last_sign_in_at moved on a step-up: %v -> %v", first, second)
		}
	})

	t.Run("a session created complete stamps", func(t *testing.T) {
		uid := newUser("passkey@example.test")
		if _, _, err := s.Create(ctx, uid, true); err != nil {
			t.Fatal(err)
		}
		if at := lastSignIn(t, d, uid); at == nil {
			t.Fatal("last_sign_in_at is NULL after Create(…, true)")
		}
	})
}

// TestMarkMFACompleteStampsOnceUnderConcurrency is backlog 7.82 (2): two
// completions of one pending session race (a TOTP verify sent twice). The
// first, transaction A here, completes the session and holds its row; the
// second waits for it, and once A commits it must see the session complete
// and stamp nothing — a step-up, not a second sign-in. Without the row lock
// the second read the session as pending from its snapshot and stamped
// last_sign_in_at again.
func TestMarkMFACompleteStampsOnceUnderConcurrency(t *testing.T) {
	d := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	q := gen.New(d.Pool())
	s := auth.NewSessions(q, time.Now)
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "race@example.test", DisplayName: "R"}); err != nil {
		t.Fatal(err)
	}
	id, _, err := s.Create(ctx, uid, false)
	if err != nil {
		t.Fatal(err)
	}

	txA, err := d.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = txA.Rollback(context.Background()) }()
	if _, err := txA.Exec(ctx, `UPDATE sessions SET mfa_complete = true, mfa_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.MarkMFAComplete(ctx, id)
		done <- err
	}()
	waitForLockWait(ctx, t)

	// What A's own completion stamped is not what is under test: clear it,
	// so whatever is there after A commits is the second completion's.
	if _, err := d.Pool().Exec(ctx, `UPDATE users SET last_sign_in_at = NULL WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if err := txA.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the waiting MarkMFAComplete: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the waiting MarkMFAComplete never returned")
	}
	if at := lastSignIn(t, d, uid); at != nil {
		t.Fatalf("last_sign_in_at = %v: the completion that waited stamped a second sign-in", at)
	}
}

// waitForLockWait returns once a backend of the test's database waits on a
// lock — the only statement that can is the MarkMFAComplete under test — and
// fails the test when ctx ends first. It reads pg_stat_activity as the
// bootstrap superuser (testdb.URL): the application role sees no other
// backend's wait event.
func waitForLockWait(ctx context.Context, t *testing.T) {
	t.Helper()
	conn, err := pgx.Connect(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			                 WHERE datname = current_database() AND wait_event_type = 'Lock')`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("MarkMFAComplete never waited on the session row")
		}
	}
}

func TestLookupCarriesMustChangePassword(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	s := auth.NewSessions(q, time.Now)
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "must@example.test", DisplayName: "M"}); err != nil {
		t.Fatal(err)
	}
	_, cookie, err := s.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Lookup(ctx, cookie)
	if err != nil || p.MustChangePassword {
		t.Fatalf("default lookup = %+v, %v; want MustChangePassword false", p, err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE users SET must_change_password = true WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	p, err = s.Lookup(ctx, cookie)
	if err != nil || !p.MustChangePassword {
		t.Fatalf("lookup = %+v, %v; want MustChangePassword true", p, err)
	}
}
