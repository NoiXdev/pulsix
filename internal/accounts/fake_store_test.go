package accounts_test

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
)

// fakeStore is an in-memory accounts.Store. It records the writes the
// service makes, and InTx only reports whether fn ran inside it, so a test
// can tell that every write happened inside the transaction.
type fakeStore struct {
	// The password change (password_test.go).
	hash      *string
	tempUntil *time.Time
	lookupErr error

	// The admin views (admin_test.go).
	records     []accounts.AccountRecord
	listCalls   []listCall
	detail      *accounts.AccountDetailRecord
	memberships []accounts.Membership
	memberCalls []memberCall

	// The admin actions (actions_test.go). accounts holds the accounts the
	// actions may find, by id, with whether each has a password;
	// deactivated marks the ones among them that are deactivated.
	accounts    map[uuid.UUID]bool
	deactivated map[uuid.UUID]bool
	// mustChange marks the accounts that must change their password.
	mustChange map[uuid.UUID]bool
	recipient  accounts.Recipient

	// The reset flow (reset_test.go). emails maps an address to its
	// account; tokens is the password_reset_tokens table.
	emails map[string]uuid.UUID
	tokens []*fakeToken
	// release, when set, makes AccountByEmail wait until it is closed;
	// emailErr makes it fail.
	release  chan struct{}
	emailErr error
	// emailLookups counts the AccountByEmail calls that started;
	// emailPanic makes it panic; emailDeadlineAt holds the deadline of the
	// context it got last (nil when that context had none). It is a pointer
	// because several background jobs may store it at once.
	emailLookups    atomic.Int32
	emailPanic      bool
	emailDeadlineAt atomic.Pointer[time.Time]
	// beforeTokenLock, when set, runs between ResetTokenOwner and
	// LockResetToken: what a concurrent request does in between.
	beforeTokenLock func()
	// beforePasswordLock, when set, runs as LockPassword starts: what a
	// concurrent change does between the unlocked read and the lock.
	beforePasswordLock func()
	// locks counts the row locks taken (LockPassword, LockAccount).
	locks int

	inTx          bool
	setCalls      []setPasswordCall
	revokeCalls   []revokeCall
	tempCalls     []tempPasswordCall
	actions       []string
	writesOutside int
}

type setPasswordCall struct {
	userID          uuid.UUID
	hash            string
	clearMustChange bool
}

type revokeCall struct{ userID, keep uuid.UUID }

type listCall struct {
	query, after string
	limit        int
}

type memberCall struct{ user, actor uuid.UUID }

// fakeToken is one row of password_reset_tokens.
type fakeToken struct {
	accounts.ResetToken
	usedAt *time.Time
}

type tempPasswordCall struct {
	userID    uuid.UUID
	hash      string
	expiresAt time.Time
}

func (f *fakeStore) write() {
	if !f.inTx {
		f.writesOutside++
	}
}

func (f *fakeStore) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	f.inTx = true
	defer func() { f.inTx = false }()
	return fn(ctx)
}

func (f *fakeStore) ReadPassword(_ context.Context, _ uuid.UUID) (accounts.PasswordState, error) {
	if f.lookupErr != nil {
		return accounts.PasswordState{}, f.lookupErr
	}
	return accounts.PasswordState{Hash: f.hash, TempPasswordExpiresAt: f.tempUntil}, nil
}

func (f *fakeStore) LockPassword(_ context.Context, _ uuid.UUID) (accounts.PasswordState, error) {
	f.locks++
	if f.beforePasswordLock != nil {
		f.beforePasswordLock()
	}
	if f.lookupErr != nil {
		return accounts.PasswordState{}, f.lookupErr
	}
	return accounts.PasswordState{Hash: f.hash, TempPasswordExpiresAt: f.tempUntil}, nil
}

func (f *fakeStore) SetPassword(_ context.Context, userID uuid.UUID, hash string, clearMustChange bool) error {
	f.write()
	f.setCalls = append(f.setCalls, setPasswordCall{userID, hash, clearMustChange})
	return nil
}

func (f *fakeStore) RevokeSessionsExcept(_ context.Context, userID, keep uuid.UUID) error {
	f.write()
	f.revokeCalls = append(f.revokeCalls, revokeCall{userID, keep})
	return nil
}

func (f *fakeStore) ListAccounts(_ context.Context, query, after string, limit int) ([]accounts.AccountRecord, error) {
	f.listCalls = append(f.listCalls, listCall{query, after, limit})
	var out []accounts.AccountRecord
	for _, r := range f.records {
		if r.Email > after && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) GetAccount(_ context.Context, _ uuid.UUID) (accounts.AccountDetailRecord, error) {
	if f.detail == nil {
		return accounts.AccountDetailRecord{}, accounts.ErrNotFound
	}
	return *f.detail, nil
}

func (f *fakeStore) ListMemberships(_ context.Context, user, actor uuid.UUID) ([]accounts.Membership, error) {
	f.memberCalls = append(f.memberCalls, memberCall{user, actor})
	return f.memberships, nil
}

func (f *fakeStore) LockAccount(_ context.Context, id uuid.UUID) (accounts.LockedAccount, error) {
	f.locks++
	hasPassword, ok := f.accounts[id]
	if !ok {
		return accounts.LockedAccount{}, accounts.ErrNotFound
	}
	return accounts.LockedAccount{HasPassword: hasPassword, Deactivated: f.deactivated[id], MustChangePassword: f.mustChange[id]}, nil
}

func (f *fakeStore) SetTemporaryPassword(_ context.Context, id uuid.UUID, hash string, expiresAt time.Time) error {
	f.write()
	f.tempCalls = append(f.tempCalls, tempPasswordCall{id, hash, expiresAt})
	f.actions = append(f.actions, "set temporary password")
	return nil
}

func (f *fakeStore) RevokeAllSessions(_ context.Context, _ uuid.UUID) error {
	f.write()
	f.actions = append(f.actions, "revoke sessions")
	return nil
}

func (f *fakeStore) DeleteSecondFactors(_ context.Context, _ uuid.UUID) error {
	f.write()
	f.actions = append(f.actions, "delete second factors")
	return nil
}

func (f *fakeStore) Reactivate(_ context.Context, _ uuid.UUID) error {
	f.write()
	f.actions = append(f.actions, "reactivate")
	return nil
}

func (f *fakeStore) AccountByEmail(ctx context.Context, email string) (uuid.UUID, error) {
	f.emailLookups.Add(1)
	if d, ok := ctx.Deadline(); ok {
		f.emailDeadlineAt.Store(&d)
	}
	if f.emailPanic {
		panic("store exploded for " + email)
	}
	if f.release != nil {
		<-f.release
	}
	if f.emailErr != nil {
		return uuid.Nil, f.emailErr
	}
	id, ok := f.emails[email]
	if !ok {
		return uuid.Nil, accounts.ErrNotFound
	}
	return id, nil
}

func (f *fakeStore) CountResetTokensSince(_ context.Context, userID uuid.UUID, since time.Time) (int, error) {
	n := 0
	for _, t := range f.tokens {
		if t.UserID == userID && !t.CreatedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) VoidResetTokens(_ context.Context, userID uuid.UUID, at time.Time) error {
	f.write()
	f.actions = append(f.actions, "void reset tokens")
	for _, t := range f.tokens {
		if t.UserID == userID && t.usedAt == nil {
			t.usedAt = &at
		}
	}
	return nil
}

func (f *fakeStore) InsertResetToken(_ context.Context, t accounts.ResetToken) error {
	f.write()
	f.actions = append(f.actions, "insert reset token")
	f.tokens = append(f.tokens, &fakeToken{ResetToken: t})
	return nil
}

func (f *fakeStore) ResetTokenOwner(_ context.Context, hash []byte) (uuid.UUID, error) {
	for _, t := range f.tokens {
		if string(t.Hash) == string(hash) {
			if f.beforeTokenLock != nil {
				f.beforeTokenLock()
			}
			return t.UserID, nil
		}
	}
	return uuid.Nil, accounts.ErrNotFound
}

// LockResetToken mirrors the SQL: the token of userID, unused and
// unexpired at now.
func (f *fakeStore) LockResetToken(_ context.Context, hash []byte, userID uuid.UUID, now time.Time) error {
	for _, t := range f.tokens {
		if string(t.Hash) == string(hash) && t.UserID == userID && t.usedAt == nil && now.Before(t.ExpiresAt) {
			return nil
		}
	}
	return accounts.ErrNotFound
}

func (f *fakeStore) NoticeRecipient(_ context.Context, _ uuid.UUID) (accounts.Recipient, error) {
	return f.recipient, nil
}

// fakeMailer satisfies accounts.Mailer and records what it sent.
type fakeMailer struct {
	unconfigured bool
	sendErr      error
	// cancelAware makes Send fail with the context's error once it is
	// done, as a real SMTP conversation would.
	cancelAware bool
	// mu guards sent: concurrent sends happen in the SQL race test.
	mu   sync.Mutex
	sent []sentMail
}

type sentMail struct{ to, subject, body string }

func (m *fakeMailer) Send(ctx context.Context, to, subject, body string) error {
	if m.cancelAware && ctx.Err() != nil {
		return ctx.Err()
	}
	if m.sendErr != nil {
		return m.sendErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMail{to, subject, body})
	return nil
}

func (m *fakeMailer) Configured(context.Context) (bool, error) { return !m.unconfigured, nil }
