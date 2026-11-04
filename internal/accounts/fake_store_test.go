package accounts_test

import (
	"context"
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
	// actions may find, by id, with whether each has a password.
	accounts  map[uuid.UUID]bool
	recipient accounts.Recipient

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

func (f *fakeStore) LockPassword(_ context.Context, _ uuid.UUID) (accounts.PasswordState, error) {
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

func (f *fakeStore) LockAccount(_ context.Context, id uuid.UUID) (bool, error) {
	hasPassword, ok := f.accounts[id]
	if !ok {
		return false, accounts.ErrNotFound
	}
	return hasPassword, nil
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

func (f *fakeStore) NoticeRecipient(_ context.Context, _ uuid.UUID) (accounts.Recipient, error) {
	return f.recipient, nil
}

// fakeMailer satisfies accounts.Mailer and records what it sent.
type fakeMailer struct {
	unconfigured bool
	sendErr      error
	sent         []sentMail
}

type sentMail struct{ to, subject, body string }

func (m *fakeMailer) Send(_ context.Context, to, subject, body string) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sent = append(m.sent, sentMail{to, subject, body})
	return nil
}

func (m *fakeMailer) Configured(context.Context) (bool, error) { return !m.unconfigured, nil }
