package accounts_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/auth"
)

// resetLink is the link a reset mail carries; the token is in the fragment.
var resetLink = regexp.MustCompile(`https://pulsix\.example/reset-password#token=([A-Za-z0-9_-]{43})`)

// captureLog routes the default slog logger into a buffer for the rest of
// the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// resetFixture is one account with a password, known by its address.
type resetFixture struct {
	store  *fakeStore
	mailer *fakeMailer
	svc    *accounts.Service
	user   uuid.UUID
	clock  *time.Time
}

func newResetFixture() *resetFixture {
	user := uuid.Must(uuid.NewV7())
	now := testNow
	f := &resetFixture{
		store: &fakeStore{
			accounts:    map[uuid.UUID]bool{user: true},
			deactivated: map[uuid.UUID]bool{},
			emails:      map[string]uuid.UUID{"person@example.test": user},
			recipient:   accounts.Recipient{Email: "person@example.test"},
		},
		mailer: &fakeMailer{},
		user:   user,
		clock:  &now,
	}
	f.svc = accounts.NewService(f.store, f.mailer, "https://pulsix.example", func() time.Time { return *f.clock }, &cyclingReader{})
	return f
}

// mailedToken requests a reset and returns the token the mail carries.
func (f *resetFixture) mailedToken(t *testing.T) string {
	t.Helper()
	sentBefore := len(f.mailer.sent)
	if err := f.svc.RequestReset(context.Background(), "person@example.test"); err != nil {
		t.Fatal(err)
	}
	f.svc.Wait()
	if len(f.mailer.sent) != sentBefore+1 {
		t.Fatalf("mails %d, want one more than %d", len(f.mailer.sent), sentBefore)
	}
	m := resetLink.FindStringSubmatch(f.mailer.sent[len(f.mailer.sent)-1].body)
	if m == nil {
		t.Fatalf("no reset link in %q", f.mailer.sent[len(f.mailer.sent)-1].body)
	}
	return m[1]
}

func TestRequestResetSilentCases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		email   string
		prepare func(f *resetFixture)
		// wantRows is how many token rows exist afterwards.
		wantRows int
	}{
		{name: "unknown address", email: "nobody@example.test"},
		{name: "deactivated", email: "person@example.test", prepare: func(f *resetFixture) { f.store.deactivated[f.user] = true }},
		{name: "invite pending", email: "person@example.test", prepare: func(f *resetFixture) { f.store.accounts[f.user] = false }},
		{
			name: "the 4th request in an hour", email: "person@example.test", wantRows: 3,
			prepare: func(f *resetFixture) {
				for i := range 3 {
					f.store.tokens = append(f.store.tokens, &fakeToken{ResetToken: accounts.ResetToken{
						ID: uuid.Must(uuid.NewV7()), UserID: f.user, Hash: []byte{byte(i)},
						CreatedAt: testNow.Add(-59 * time.Minute), ExpiresAt: testNow.Add(time.Minute),
					}})
				}
			},
		},
		{name: "mail not configured", email: "person@example.test", prepare: func(f *resetFixture) { f.mailer.unconfigured = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newResetFixture()
			if tc.prepare != nil {
				tc.prepare(f)
			}
			// The background work itself answers nil (it is not a store
			// failure) and does nothing; so does the whole request.
			if !f.mailer.unconfigured {
				if err := accounts.RequestResetNow(f.svc, context.Background(), tc.email); err != nil {
					t.Fatalf("background work err %v, want nil", err)
				}
			}
			if err := f.svc.RequestReset(context.Background(), tc.email); err != nil {
				t.Fatalf("err %v, want nil", err)
			}
			f.svc.Wait()
			if len(f.mailer.sent) != 0 || len(f.store.tokens) != tc.wantRows {
				t.Fatalf("mails %d, token rows %d; want no mail and %d rows", len(f.mailer.sent), len(f.store.tokens), tc.wantRows)
			}
		})
	}

	t.Run("tokens older than an hour do not count", func(t *testing.T) {
		f := newResetFixture()
		for i := range 3 {
			f.store.tokens = append(f.store.tokens, &fakeToken{ResetToken: accounts.ResetToken{
				ID: uuid.Must(uuid.NewV7()), UserID: f.user, Hash: []byte{byte(i)},
				CreatedAt: testNow.Add(-61 * time.Minute), ExpiresAt: testNow.Add(-time.Minute),
			}})
		}
		f.mailedToken(t)
	})
}

func TestRequestResetMailsFragmentLink(t *testing.T) {
	f := newResetFixture()
	earlier := &fakeToken{ResetToken: accounts.ResetToken{
		ID: uuid.Must(uuid.NewV7()), UserID: f.user, Hash: []byte("earlier"),
		CreatedAt: testNow.Add(-10 * time.Minute), ExpiresAt: testNow.Add(50 * time.Minute),
	}}
	f.store.tokens = append(f.store.tokens, earlier)

	token := f.mailedToken(t)
	m := f.mailer.sent[0]
	if m.to != "person@example.test" || !strings.Contains(m.body, "https://pulsix.example/reset-password#token=") || strings.Contains(m.body, "?token=") {
		t.Fatalf("mail %+v, want the fragment link to the person", m)
	}
	if len(f.store.tokens) != 2 {
		t.Fatalf("token rows %d, want 2", len(f.store.tokens))
	}
	issued := f.store.tokens[1]
	sum := sha256.Sum256([]byte(token))
	if string(issued.Hash) != string(sum[:]) || issued.UserID != f.user {
		t.Fatal("the stored hash is not the SHA-256 of the mailed token")
	}
	if !issued.CreatedAt.Equal(testNow) || !issued.ExpiresAt.Equal(testNow.Add(60*time.Minute)) || issued.usedAt != nil {
		t.Fatalf("issued token %+v, want created now, expiring in 60 min, unused", issued)
	}
	if earlier.usedAt == nil {
		t.Fatal("the earlier unused token was not voided")
	}
	if f.store.writesOutside != 0 {
		t.Fatalf("%d writes outside the transaction", f.store.writesOutside)
	}
}

// TestRequestResetReturnsBeforeTheStore: past the mail check, nothing
// RequestReset does depends on the address before it returns — the store
// is not even asked yet.
func TestRequestResetReturnsBeforeTheStore(t *testing.T) {
	f := newResetFixture()
	release := make(chan struct{})
	f.store.release = release
	done := make(chan error, 1)
	go func() { done <- f.svc.RequestReset(context.Background(), "person@example.test") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("err %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("RequestReset waited for the store")
	}
	if len(f.mailer.sent) != 0 {
		t.Fatal("a mail went out before the store answered")
	}
	close(release)
	f.svc.Wait()
	if len(f.mailer.sent) != 1 || len(f.store.tokens) != 1 {
		t.Fatalf("mails %d, token rows %d after the store answered; want 1 and 1", len(f.mailer.sent), len(f.store.tokens))
	}
}

// TestRequestResetCapsWorkInFlight: with the store blocked, only
// MaxResetJobs requests start background work; the rest are dropped with a
// warning that names no address, and every call still answers nil.
func TestRequestResetCapsWorkInFlight(t *testing.T) {
	f := newResetFixture()
	logs := captureLog(t)
	release := make(chan struct{})
	f.store.release = release
	for range accounts.MaxResetJobs + 1 {
		if err := f.svc.RequestReset(context.Background(), "person@example.test"); err != nil {
			t.Fatalf("err %v, want nil", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.store.emailLookups.Load() < accounts.MaxResetJobs && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// The released jobs end at the lookup: the fake is not safe for the
	// concurrent transactions the rest of the flow would open.
	f.store.emailErr = errors.New("stop here")
	close(release)
	f.svc.Wait()
	f.store.emailErr = nil
	if got := f.store.emailLookups.Load(); got != accounts.MaxResetJobs {
		t.Fatalf("%d jobs started, want the cap %d", got, accounts.MaxResetJobs)
	}
	if !strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), "person@example.test") {
		t.Fatalf("log %s; want a warning without the address", logs)
	}
	// The slots are free again once the work is done.
	f.store.release = nil
	before := f.store.emailLookups.Load()
	if err := f.svc.RequestReset(context.Background(), "person@example.test"); err != nil {
		t.Fatal(err)
	}
	f.svc.Wait()
	if f.store.emailLookups.Load() != before+1 {
		t.Fatal("a request after the work drained was dropped")
	}
}

// TestRequestResetWorkHasADeadline: the detached context carries a
// deadline, so a hung store cannot pin the goroutine forever.
func TestRequestResetWorkHasADeadline(t *testing.T) {
	f := newResetFixture()
	if err := f.svc.RequestReset(context.Background(), "person@example.test"); err != nil {
		t.Fatal(err)
	}
	f.svc.Wait()
	if !f.store.emailDeadline.Load() {
		t.Fatalf("the background context has no deadline (want %v)", accounts.ResetJobTimeout)
	}
}

// TestRequestResetRecoversFromPanic: a store that panics does not take the
// process down, Wait still returns, and the log carries the panic's kind
// but not the address.
func TestRequestResetRecoversFromPanic(t *testing.T) {
	f := newResetFixture()
	logs := captureLog(t)
	f.store.emailPanic = true
	if err := f.svc.RequestReset(context.Background(), "person@example.test"); err != nil {
		t.Fatalf("err %v, want nil", err)
	}
	f.svc.Wait()
	if !strings.Contains(logs.String(), "panicked") || strings.Contains(logs.String(), "person@example.test") {
		t.Fatalf("log %s; want the panic without the address", logs)
	}
}

// TestRequestResetStoreFailure: the background work reports a store
// failure, and RequestReset itself still answers nil; the log names
// neither the address nor a link.
func TestRequestResetStoreFailure(t *testing.T) {
	f := newResetFixture()
	f.store.emailErr = errors.New("db down")
	if err := accounts.RequestResetNow(f.svc, context.Background(), "person@example.test"); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("background work err %v, want the store failure", err)
	}
	logs := captureLog(t)
	if err := f.svc.RequestReset(context.Background(), "person@example.test"); err != nil {
		t.Fatalf("RequestReset err %v, want nil", err)
	}
	f.svc.Wait()
	if !strings.Contains(logs.String(), "db down") || strings.Contains(logs.String(), "person@example.test") {
		t.Fatalf("log %s; want the failure without the address", logs)
	}
}

func TestRequestResetSendFailureIsSilent(t *testing.T) {
	f := newResetFixture()
	logs := captureLog(t)
	f.mailer.sendErr = errors.New("smtp down")
	if err := f.svc.RequestReset(context.Background(), "person@example.test"); err != nil {
		t.Fatalf("a failed send failed the request: %v", err)
	}
	f.svc.Wait()
	if !strings.Contains(logs.String(), "smtp down") {
		t.Fatalf("the failure was not logged: %s", logs)
	}
	if strings.Contains(logs.String(), "reset-password") || strings.Contains(logs.String(), "token=") {
		t.Fatalf("the log carries the link: %s", logs)
	}
}

func TestResetPassword(t *testing.T) {
	ctx := context.Background()
	const next = "a brand new password"

	t.Run("a valid token", func(t *testing.T) {
		f := newResetFixture()
		token := f.mailedToken(t)
		f.store.actions = nil
		owner, err := f.svc.ResetPassword(ctx, token, next)
		if err != nil || owner != f.user {
			t.Fatalf("ResetPassword = %v, %v; want the user's account", owner, err)
		}
		if len(f.store.setCalls) != 1 || f.store.setCalls[0].userID != f.user || !f.store.setCalls[0].clearMustChange {
			t.Fatalf("SetPassword calls %+v, want one for the user that clears the forced change", f.store.setCalls)
		}
		if ok, err := auth.VerifyPassword(f.store.setCalls[0].hash, next); err != nil || !ok {
			t.Fatalf("the stored hash does not verify the new password: %v %v", ok, err)
		}
		if strings.Join(f.store.actions, ", ") != "void reset tokens, revoke sessions" || f.store.writesOutside != 0 {
			t.Fatalf("writes %v (%d outside the transaction), want the tokens voided and the sessions revoked inside it", f.store.actions, f.store.writesOutside)
		}
		if _, err := f.svc.ResetPassword(ctx, token, next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("the same token again: err %v, want ErrInvalidResetToken", err)
		}
	})

	t.Run("an older token after a newer one was issued", func(t *testing.T) {
		f := newResetFixture()
		older := f.mailedToken(t)
		f.mailedToken(t)
		if _, err := f.svc.ResetPassword(ctx, older, next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("err %v, want ErrInvalidResetToken", err)
		}
	})

	t.Run("61 minutes later", func(t *testing.T) {
		f := newResetFixture()
		token := f.mailedToken(t)
		*f.clock = testNow.Add(61 * time.Minute)
		if _, err := f.svc.ResetPassword(ctx, token, next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("err %v, want ErrInvalidResetToken", err)
		}
	})

	t.Run("a deactivated account", func(t *testing.T) {
		f := newResetFixture()
		token := f.mailedToken(t)
		f.store.deactivated[f.user] = true
		if _, err := f.svc.ResetPassword(ctx, token, next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("err %v, want ErrInvalidResetToken", err)
		}
	})

	// A concurrent reset uses the token up between the unlocked owner
	// lookup and the lock: the locked re-check refuses it.
	t.Run("a token used in between", func(t *testing.T) {
		f := newResetFixture()
		token := f.mailedToken(t)
		f.store.beforeTokenLock = func() {
			for _, tok := range f.store.tokens {
				used := testNow
				tok.usedAt = &used
			}
		}
		if _, err := f.svc.ResetPassword(ctx, token, next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("err %v, want ErrInvalidResetToken", err)
		}
		if len(f.store.setCalls) != 0 {
			t.Fatal("a password was stored with a used token")
		}
	})

	t.Run("an unknown token", func(t *testing.T) {
		f := newResetFixture()
		if _, err := f.svc.ResetPassword(ctx, "not-a-token", next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("err %v, want ErrInvalidResetToken", err)
		}
	})

	t.Run("an 11-character password", func(t *testing.T) {
		f := newResetFixture()
		token := f.mailedToken(t)
		if _, err := f.svc.ResetPassword(ctx, token, "elevenchars"); !errors.Is(err, auth.ErrPasswordTooShort) {
			t.Fatalf("err %v, want ErrPasswordTooShort", err)
		}
		if len(f.store.setCalls) != 0 {
			t.Fatal("a refused password was stored")
		}
		if _, err := f.svc.ResetPassword(ctx, token, next); err != nil {
			t.Fatalf("the token was consumed by the refused password: %v", err)
		}
	})

	if strings.Contains(accounts.ErrInvalidResetToken.Error(), "token=") {
		t.Fatal("the sentinel names a token")
	}
}

func TestSendResetLink(t *testing.T) {
	ctx := context.Background()
	actor := uuid.Must(uuid.NewV7())

	t.Run("mails a link without the per-address limit", func(t *testing.T) {
		f := newResetFixture()
		for i := range 3 {
			f.store.tokens = append(f.store.tokens, &fakeToken{ResetToken: accounts.ResetToken{
				ID: uuid.Must(uuid.NewV7()), UserID: f.user, Hash: []byte{byte(i)},
				CreatedAt: testNow.Add(-time.Minute), ExpiresAt: testNow.Add(59 * time.Minute),
			}})
		}
		notice, err := f.svc.SendResetLink(ctx, true, actor, f.user)
		if err != nil || notice != accounts.NoticeSent {
			t.Fatalf("notice %q, err %v; want sent", notice, err)
		}
		if len(f.mailer.sent) != 1 || !resetLink.MatchString(f.mailer.sent[0].body) || len(f.store.tokens) != 4 {
			t.Fatalf("mails %+v, token rows %d; want one link and a 4th row", f.mailer.sent, len(f.store.tokens))
		}
		for _, tok := range f.store.tokens[:3] {
			if tok.usedAt == nil {
				t.Fatal("an earlier token was not voided")
			}
		}
	})

	t.Run("mail not configured", func(t *testing.T) {
		f := newResetFixture()
		f.mailer.unconfigured = true
		if _, err := f.svc.SendResetLink(ctx, true, actor, f.user); !errors.Is(err, accounts.ErrMailNotConfigured) {
			t.Fatalf("err %v, want ErrMailNotConfigured", err)
		}
		if len(f.store.tokens) != 0 || len(f.mailer.sent) != 0 {
			t.Fatal("a token or a mail without outbound mail")
		}
	})

	t.Run("a deactivated account gets nothing", func(t *testing.T) {
		f := newResetFixture()
		f.store.deactivated[f.user] = true
		notice, err := f.svc.SendResetLink(ctx, true, actor, f.user)
		if err != nil || notice != accounts.NoticeSkipped {
			t.Fatalf("notice %q, err %v; want skipped", notice, err)
		}
		if len(f.store.tokens) != 0 || len(f.mailer.sent) != 0 {
			t.Fatal("a deactivated account got a reset link")
		}
	})

	t.Run("a failed send", func(t *testing.T) {
		f := newResetFixture()
		logs := captureLog(t)
		f.mailer.sendErr = errors.New("smtp down")
		notice, err := f.svc.SendResetLink(ctx, true, actor, f.user)
		if err != nil || notice != accounts.NoticeFailed {
			t.Fatalf("notice %q, err %v; want failed", notice, err)
		}
		if len(f.store.tokens) != 1 || !strings.Contains(logs.String(), "smtp down") || strings.Contains(logs.String(), "token=") {
			t.Fatalf("token rows %d, log %s; want the token issued and the failure logged without the link", len(f.store.tokens), logs)
		}
	})

	t.Run("a browser that went away", func(t *testing.T) {
		f := newResetFixture()
		f.mailer.cancelAware = true
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if notice, err := f.svc.SendResetLink(cancelled, true, actor, f.user); err != nil || notice != accounts.NoticeSent {
			t.Fatalf("notice %q, err %v; want the send to go ahead", notice, err)
		}
	})

	for _, tc := range []struct {
		name    string
		isAdmin bool
		actor   func(f *resetFixture) uuid.UUID
		target  func(f *resetFixture) uuid.UUID
		prepare func(f *resetFixture)
		want    error
	}{
		{"not an admin", false, func(*resetFixture) uuid.UUID { return actor }, func(f *resetFixture) uuid.UUID { return f.user }, nil, accounts.ErrForbidden},
		{"own account", true, func(f *resetFixture) uuid.UUID { return f.user }, func(f *resetFixture) uuid.UUID { return f.user }, nil, accounts.ErrNotOnOwnAccount},
		{"unknown account", true, func(*resetFixture) uuid.UUID { return actor }, func(*resetFixture) uuid.UUID { return uuid.Must(uuid.NewV7()) }, nil, accounts.ErrNotFound},
		{"invite pending", true, func(*resetFixture) uuid.UUID { return actor }, func(f *resetFixture) uuid.UUID { return f.user }, func(f *resetFixture) { f.store.accounts[f.user] = false }, accounts.ErrInvitePending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newResetFixture()
			if tc.prepare != nil {
				tc.prepare(f)
			}
			if _, err := f.svc.SendResetLink(ctx, tc.isAdmin, tc.actor(f), tc.target(f)); !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
			if len(f.store.tokens) != 0 || len(f.mailer.sent) != 0 {
				t.Fatal("a refused call issued a token or sent a mail")
			}
		})
	}
}
