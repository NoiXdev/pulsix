// Confirming a second factor is one state change, not two (backlog 1.26):
// the account gains a confirmed TOTP secret with its recovery codes, and
// the session that was pending only for want of a factor becomes complete.
// These tests drive the real database, because the property under test is
// that a failure halfway leaves nothing behind — which only a transaction
// can provide and only a real transaction can prove.
package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// enrolmentWorld wires the real stores against a test database. The TOTP
// clock is driven by the test: codes are bound to 30-second steps and the
// replay guard claims each step once, so a scenario that spends several
// codes has to move time rather than crowd them into one window.
func enrolmentWorld(t *testing.T) (*auth.Enrolment, *auth.TOTP, *gen.Queries, *db.DB, *mutableClock) {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d)
	var key [32]byte
	copy(key[:], "enrolment-test-key-0000000000001")
	clock := newMutableClock(time.Now().Truncate(30 * time.Second))
	tp := auth.NewTOTP(auth.NewSQLTOTPStore(d), crypto.NewBox(key), "Pulsix", clock.Now)
	sessions := auth.NewSessions(q, time.Now)
	return auth.NewEnrolment(tp, sessions, d), tp, gen.New(d.Pool()), d, clock
}

// codeAt is a valid code for secret at the clock's current step.
func codeAt(t *testing.T, secret string, clock *mutableClock) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// enrol runs BeginEnrol and returns a code valid for the enrolling secret.
func enrol(t *testing.T, tp *auth.TOTP, clock *mutableClock, userID uuid.UUID) string {
	t.Helper()
	secret, _, err := tp.BeginEnrol(ctx, userID, "user@example.test", "")
	if err != nil {
		t.Fatal(err)
	}
	return codeAt(t, secret, clock)
}

func confirmedAndCodes(t *testing.T, raw *gen.Queries, d *db.DB, userID uuid.UUID) (bool, int) {
	t.Helper()
	row, err := raw.GetTOTPSecret(ctx, userID)
	if err != nil {
		t.Fatalf("read the TOTP row back: %v", err)
	}
	var codes int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id = $1`, userID).Scan(&codes); err != nil {
		t.Fatal(err)
	}
	return row.ConfirmedAt.Valid, codes
}

// The happy path: one call confirms the factor, issues the recovery codes
// and completes the session.
func TestEnrolmentConfirmTOTPCompletesTheSession(t *testing.T) {
	e, tp, raw, d, clock := enrolmentWorld(t)
	uid := seedReaperUser(t, raw, "enrol-ok@example.test")
	sessionID := createSessionAt(t, raw, uid, time.Now().Add(time.Hour), false)

	codes, err := e.ConfirmTOTP(ctx, uid, sessionID, enrol(t, tp, clock, uid))
	if err != nil {
		t.Fatalf("ConfirmTOTP: %v", err)
	}
	if len(codes) != 8 {
		t.Fatalf("recovery codes = %d, want 8", len(codes))
	}
	confirmed, stored := confirmedAndCodes(t, raw, d, uid)
	if !confirmed || stored != 8 {
		t.Fatalf("after confirmation: confirmed=%v, stored codes=%d", confirmed, stored)
	}
	var complete bool
	if err := d.Pool().QueryRow(ctx, `SELECT mfa_complete FROM sessions WHERE id = $1`, sessionID).Scan(&complete); err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Error("the session must be MFA-complete once the factor is confirmed")
	}
}

// The property the transaction exists for: when the session upgrade fails,
// the account must not be left carrying a confirmed factor and eight
// recovery codes the user never saw. The failure is a session that does
// not exist — which, before this, the upgrade reported as success.
func TestEnrolmentConfirmTOTPRollsBackWhenTheSessionUpgradeFails(t *testing.T) {
	e, tp, raw, d, clock := enrolmentWorld(t)
	uid := seedReaperUser(t, raw, "enrol-rollback@example.test")

	_, err := e.ConfirmTOTP(ctx, uid, uuid.Must(uuid.NewV7()), enrol(t, tp, clock, uid))
	if err == nil {
		t.Fatal("ConfirmTOTP with an unknown session returned no error")
	}
	confirmed, stored := confirmedAndCodes(t, raw, d, uid)
	if confirmed {
		t.Error("the secret must not be confirmed when the session upgrade failed")
	}
	if stored != 0 {
		t.Errorf("recovery codes stored = %d, want 0 — nobody ever saw them", stored)
	}
	// The user can simply try again once the session is real.
	sessionID := createSessionAt(t, raw, uid, time.Now().Add(time.Hour), false)
	if _, err := e.ConfirmTOTP(ctx, uid, sessionID, enrol(t, tp, clock, uid)); err != nil {
		t.Fatalf("retry after the failure: %v", err)
	}
}

// A wrong code changes nothing either, and says so distinguishably.
func TestEnrolmentConfirmTOTPRejectsAWrongCode(t *testing.T) {
	e, tp, raw, d, clock := enrolmentWorld(t)
	uid := seedReaperUser(t, raw, "enrol-wrong@example.test")
	sessionID := createSessionAt(t, raw, uid, time.Now().Add(time.Hour), false)
	enrol(t, tp, clock, uid)

	_, err := e.ConfirmTOTP(ctx, uid, sessionID, "000000")
	if err == nil || !strings.Contains(err.Error(), "code") {
		t.Fatalf("ConfirmTOTP with a wrong code = %v, want an invalid-code error", err)
	}
	if confirmed, stored := confirmedAndCodes(t, raw, d, uid); confirmed || stored != 0 {
		t.Fatalf("a wrong code changed state: confirmed=%v, codes=%d", confirmed, stored)
	}
}

// An abandoned re-enrolment must not disarm the account (backlog 1.27).
// Asking for a new secret used to overwrite the confirmed one and clear
// its confirmation, so a user who opened the dialog and walked away was
// left with password-only sign-in and no sign that anything had changed.
func TestBeginEnrolKeepsTheConfirmedFactorUntilTheNewOneIsConfirmed(t *testing.T) {
	e, tp, raw, d, clock := enrolmentWorld(t)
	uid := seedReaperUser(t, raw, "reenrol@example.test")
	sessionID := createSessionAt(t, raw, uid, time.Now().Add(time.Hour), false)

	first, _, err := tp.BeginEnrol(ctx, uid, "reenrol@example.test", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ConfirmTOTP(ctx, uid, sessionID, codeAt(t, first, clock)); err != nil {
		t.Fatalf("first confirmation: %v", err)
	}

	// Re-enrolling requires proving the current factor, and then the user
	// abandons the dialog.
	clock.Advance(30 * time.Second)
	second, _, err := tp.BeginEnrol(ctx, uid, "reenrol@example.test", codeAt(t, first, clock))
	if err != nil {
		t.Fatalf("re-enrolment: %v", err)
	}
	if second == first {
		t.Fatal("re-enrolment handed back the same secret")
	}

	// The old factor still works: asking for a new one took nothing away.
	clock.Advance(30 * time.Second)
	if ok, err := tp.Verify(ctx, uid, codeAt(t, first, clock)); err != nil || !ok {
		t.Fatalf("the confirmed factor stopped working during an unfinished re-enrolment: ok=%v err=%v", ok, err)
	}
	if confirmed, _ := confirmedAndCodes(t, raw, d, uid); !confirmed {
		t.Error("the account must still count as having a confirmed factor")
	}
	// And the new secret cannot be used for anything until it is confirmed.
	clock.Advance(30 * time.Second)
	if ok, _ := tp.Verify(ctx, uid, codeAt(t, second, clock)); ok {
		t.Error("an unconfirmed secret must not verify")
	}

	// Finishing the re-enrolment swaps them over.
	clock.Advance(30 * time.Second)
	session2 := createSessionAt(t, raw, uid, time.Now().Add(time.Hour), false)
	if _, err := e.ConfirmTOTP(ctx, uid, session2, codeAt(t, second, clock)); err != nil {
		t.Fatalf("confirming the new secret: %v", err)
	}
	clock.Advance(30 * time.Second)
	if ok, err := tp.Verify(ctx, uid, codeAt(t, second, clock)); err != nil || !ok {
		t.Fatalf("the new factor must work after confirmation: ok=%v err=%v", ok, err)
	}
	clock.Advance(30 * time.Second)
	if ok, _ := tp.Verify(ctx, uid, codeAt(t, first, clock)); ok {
		t.Error("the replaced secret must stop working")
	}
}

// The account-security actions need a recently proven factor (backlog
// 1.28): a session left open on an unlocked screen must not be enough to
// add a credential to the account. The session carries when it last
// proved one, and proving it again is what refreshes that.
func TestSessionCarriesFactorFreshness(t *testing.T) {
	e, tp, raw, d, clock := enrolmentWorld(t)
	uid := seedReaperUser(t, raw, "freshness@example.test")
	sessions := auth.NewSessions(gen.New(d), time.Now)
	sessionID, cookie, err := sessions.Create(ctx, uid, false)
	if err != nil {
		t.Fatal(err)
	}

	// A session that has never proven a factor is not fresh, and says so
	// without pretending it was proven at some arbitrary time.
	p, err := sessions.Lookup(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	if p.MFAAt != nil {
		t.Errorf("a session that never proved a factor carries %v, want nothing", p.MFAAt)
	}
	if p.FactorFresh(time.Now()) {
		t.Error("a session that never proved a factor must not count as fresh")
	}

	// Confirming the factor proves it: the session is complete and fresh.
	if _, err := e.ConfirmTOTP(ctx, uid, sessionID, enrol(t, tp, clock, uid)); err != nil {
		t.Fatal(err)
	}
	p, err = sessions.Lookup(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	if p.MFAAt == nil || !p.FactorFresh(time.Now()) {
		t.Fatalf("after confirming the factor: MFAAt=%v, fresh=%v", p.MFAAt, p.FactorFresh(time.Now()))
	}

	// It goes stale on its own, with no job to run: the verdict is the
	// age of the stamp, read when it matters.
	if p.FactorFresh(time.Now().Add(auth.StepUpWindow + time.Second)) {
		t.Error("the freshness must expire once the window has passed")
	}

	// Proving it again — the same endpoint a pending session uses —
	// refreshes the stamp rather than needing a route of its own.
	before := *p.MFAAt
	time.Sleep(10 * time.Millisecond)
	if err := sessions.MarkMFAComplete(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	p, err = sessions.Lookup(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	if p.MFAAt == nil || !p.MFAAt.After(before) {
		t.Fatalf("re-proving the factor did not refresh the stamp: %v then %v", before, p.MFAAt)
	}
}
