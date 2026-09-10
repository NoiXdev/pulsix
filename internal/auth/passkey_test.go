package auth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// makeAssertionResponse builds a syntactically valid (but unsigned, thus
// never cryptographically verifiable — Task 15's job) WebAuthn assertion
// response body: enough for protocol.ParseCredentialRequestResponseBody and
// go-webauthn's structural checks up through invoking the
// DiscoverableUserHandler to succeed, so FinishLogin's own handler closure
// (the user-handle-matches-owner check) is reachable from a fake with no
// real authenticator.
func makeAssertionResponse(t *testing.T, rawID, userHandle []byte) json.RawMessage {
	t.Helper()
	authData := make([]byte, 37) // rpIdHash(32) + flags(1, none set) + counter(4); exactly 37 bytes needs no attested/extension data.
	body := map[string]any{
		"id": base64.RawURLEncoding.EncodeToString(rawID), "rawId": base64.RawURLEncoding.EncodeToString(rawID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString([]byte(`{}`)),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString([]byte("sig")),
			"userHandle":        base64.RawURLEncoding.EncodeToString(userHandle),
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// memCeremony is one in-flight WebAuthn ceremony, keyed by its id in
// memPasskeyStore.ceremonies.
type memCeremony struct {
	kind    string
	user    *uuid.UUID
	data    []byte
	expires time.Time
}

// memPasskeyStore is a fake auth.PasskeyStore: no credentials are ever
// registered against it directly (tests exercise ceremony lifecycle, not
// credential storage — that is covered by passkey_store_sql_test.go against
// the real database), but every method is implemented so the fake satisfies
// the interface.
type memPasskeyStore struct {
	ceremonies map[uuid.UUID]memCeremony
	now        func() time.Time

	// Error injection for branches a happy-path fake can never exercise:
	// listErr makes ListCredentials fail (loadUser's second lookup, used
	// by both BeginRegistration and FinishRegistration/FinishLogin after
	// the user itself resolves), putErr makes PutCeremony fail (saveCeremony,
	// shared by BeginRegistration and BeginLogin), deleteErr/deleteCalls and
	// listedFor/listResult back List/Delete's thin service-level wrappers.
	listErr    error
	putErr     error
	deleteErr  error
	deleteArgs []uuid.UUID
	listResult []auth.Credential
	addCalls   []addCredentialCall

	// findUserID/findErr back FindUserByCredentialID: by default it behaves
	// like "no credential with this id was ever registered" (ErrNotFound),
	// matching the passkeys that only ever go through BeginRegistration in
	// these tests (which never calls AddCredential — see the comment above).
	findUserID *uuid.UUID
	findErr    error

	// updateCounterCalls records every UpdateCredentialCounter invocation,
	// so a test can assert the stored counter was left untouched — the
	// clone-signal path (Finding 1, fix round 1) must not call this at all.
	updateCounterCalls []updateCounterCall
}

type addCredentialCall struct {
	userID uuid.UUID
	name   string
	cred   auth.Credential
}

type updateCounterCall struct {
	credentialID []byte
	counter      uint32
}

func newMemPasskeyStore(now func() time.Time) *memPasskeyStore {
	return &memPasskeyStore{ceremonies: map[uuid.UUID]memCeremony{}, now: now}
}

func (m *memPasskeyStore) ListCredentials(context.Context, uuid.UUID) ([]auth.Credential, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResult, nil
}
func (m *memPasskeyStore) AddCredential(_ context.Context, userID uuid.UUID, name string, c auth.Credential) error {
	m.addCalls = append(m.addCalls, addCredentialCall{userID: userID, name: name, cred: c})
	return nil
}
func (m *memPasskeyStore) UpdateCredentialCounter(_ context.Context, credentialID []byte, counter uint32) error {
	m.updateCounterCalls = append(m.updateCounterCalls, updateCounterCall{credentialID: credentialID, counter: counter})
	return nil
}
func (m *memPasskeyStore) DeleteCredential(_ context.Context, userID, id uuid.UUID) error {
	m.deleteArgs = []uuid.UUID{userID, id}
	return m.deleteErr
}
func (m *memPasskeyStore) FindUserByCredentialID(context.Context, []byte) (uuid.UUID, error) {
	if m.findErr != nil {
		return uuid.Nil, m.findErr
	}
	if m.findUserID != nil {
		return *m.findUserID, nil
	}
	return uuid.Nil, auth.ErrNotFound
}
func (m *memPasskeyStore) PutCeremony(_ context.Context, id uuid.UUID, kind string, user *uuid.UUID, data []byte, exp time.Time) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.ceremonies[id] = memCeremony{kind, user, data, exp}
	return nil
}
func (m *memPasskeyStore) TakeCeremony(_ context.Context, id uuid.UUID) (string, *uuid.UUID, []byte, error) {
	c, ok := m.ceremonies[id]
	delete(m.ceremonies, id)
	if !ok || !c.expires.After(m.now()) {
		return "", nil, nil, auth.ErrNotFound
	}
	return c.kind, c.user, c.data, nil
}

// testRPID and testOrigin are the relying party id/origin every fake
// *auth.Passkeys in this file is built with; buildNoneAttestationResponse
// (passkey_registration_success_test.go) uses the same two values so the
// hand-built attestation it constructs actually matches what NewPasskeys
// configured go-webauthn to expect.
const (
	testRPID   = "localhost"
	testOrigin = "http://localhost:5173"
)

// newPasskeys builds a *auth.Passkeys wired to a single seeded user and
// returns that user's id, since BeginRegistration needs a userID the users
// store can resolve. The brief's original helper referenced a nonexistent
// userIDOf(store) function; returning the id directly from here is the fix
// noted in the task-8 brief.
func newPasskeys(t *testing.T, store auth.PasskeyStore, now func() time.Time) (*auth.Passkeys, uuid.UUID) {
	t.Helper()
	pk, uid, _ := newPasskeysTwoUsers(t, store, now)
	return pk, uid
}

// newPasskeysTwoUsers is newPasskeys plus a second seeded user ("other"),
// for tests that must prove a check rejects a real, resolvable user who
// simply isn't the right one — not a user the store happens to fail to
// resolve for an unrelated reason (see
// TestFinishRegistrationRejectsAnotherUsersCeremony's comment).
func newPasskeysTwoUsers(t *testing.T, store auth.PasskeyStore, now func() time.Time) (*auth.Passkeys, uuid.UUID, uuid.UUID) {
	t.Helper()
	uid := uuid.Must(uuid.NewV7())
	other := uuid.Must(uuid.NewV7())
	users := &fakeStore{users: map[string]auth.User{
		"p@x": {ID: uid, Email: "p@x", DisplayName: "P"},
		"o@x": {ID: other, Email: "o@x", DisplayName: "O"},
	}}
	pk, err := auth.NewPasskeys(store, users, &fakeSessions{}, testRPID, "Pulsix Test", testOrigin, now)
	if err != nil {
		t.Fatal(err)
	}
	return pk, uid, other
}

func TestBeginRegistrationStoresCeremonyAndReturnsOptions(t *testing.T) {
	now := time.Now()
	store := newMemPasskeyStore(func() time.Time { return now })
	pk, uid := newPasskeys(t, store, func() time.Time { return now })
	opts, cid, err := pk.BeginRegistration(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RP        struct {
				ID string `json:"id"`
			} `json:"rp"`
			AuthenticatorSelection struct {
				ResidentKey      string `json:"residentKey"`
				UserVerification string `json:"userVerification"`
			} `json:"authenticatorSelection"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(opts, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.PublicKey.Challenge == "" || parsed.PublicKey.RP.ID != "localhost" {
		t.Fatalf("options %s", opts)
	}
	if parsed.PublicKey.AuthenticatorSelection.ResidentKey != "required" || parsed.PublicKey.AuthenticatorSelection.UserVerification != "required" {
		t.Fatalf("passkeys must require resident key + user verification: %s", opts)
	}
	if _, ok := store.ceremonies[cid]; !ok {
		t.Fatal("ceremony not stored")
	}
}

func TestBeginRegistrationRejectsUnknownUser(t *testing.T) {
	now := time.Now()
	store := newMemPasskeyStore(func() time.Time { return now })
	pk, _ := newPasskeys(t, store, func() time.Time { return now })
	if _, _, err := pk.BeginRegistration(context.Background(), uuid.Must(uuid.NewV7())); err == nil {
		t.Fatal("expected an error for a user the store cannot resolve")
	}
}

// TestFinishRegistrationRejectsExpiredCeremony uses a real, verifiable "none"
// attestation response (buildNoneAttestationResponse, from
// passkey_registration_success_test.go) rather than json.RawMessage(`{}`):
// a bogus body fails at go-webauthn's own parse step regardless of expiry,
// so a test built on one would still turn red with the expiry check itself
// deleted — see this file's package comment. With a body that actually
// parses and verifies, expiry is the only thing left that can reject the
// call (Finding 3, fix round 1; discrimination evidence recorded in the
// task-8 fix report).
func TestFinishRegistrationRejectsExpiredCeremony(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid := newPasskeys(t, store, clock)
	opts, cid, err := pk.BeginRegistration(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	response := buildNoneAttestationResponse(t, opts, testRPID, testOrigin, []byte("expired-ceremony-cred"))
	now = now.Add(10 * time.Minute) // past the 5 minute ceremony TTL
	if err := pk.FinishRegistration(context.Background(), uid, cid, "YubiKey", response); err == nil {
		t.Fatal("expired ceremony accepted")
	}
	if len(store.addCalls) != 0 {
		t.Fatal("AddCredential was called despite the ceremony having expired")
	}
}

func TestFinishRegistrationRejectsUnknownCeremony(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid := newPasskeys(t, store, clock)
	err := pk.FinishRegistration(context.Background(), uid, uuid.Must(uuid.NewV7()), "YubiKey", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("unknown ceremony id accepted")
	}
}

// TestFinishRegistrationRejectsAnotherUsersCeremony isolates this
// codebase's own ceremony-ownership binding (the userID PutCeremony
// recorded in PasskeyStore) from go-webauthn's own, independent check of
// essentially the same property: CreateCredential compares the User it is
// given against the UserID embedded inside the session data it captured at
// BeginRegistration (webauthn/registration.go, "if
// !bytes.Equal(user.WebAuthnID(), session.UserID)"). Simply finishing a
// registration begun for uid with a caller-supplied userID of other is
// caught by *that* check too — loadUser(ctx, other) feeds go-webauthn a
// user whose WebAuthnID no longer matches the embedded session — so
// deleting only this codebase's own check would still leave such a test
// red, for the wrong reason (confirmed by actually running that
// experiment first; see the task-8 fix report).
//
// To isolate this package's own check, the exact session bytes
// BeginRegistration produced for uid (still embedding uid as
// session.UserID) are re-stored under a fresh ceremony id, but associated
// with other in the store's own record — decoupling the two normally-
// coupled facts. Finishing that ceremony as uid then satisfies
// go-webauthn's embedded-session check (uid == uid) but not this
// package's own ceremony-ownership check (other != uid): only the local
// check can still reject the call (Finding 3, fix round 1; discrimination
// evidence in the task-8 fix report).
func TestFinishRegistrationRejectsAnotherUsersCeremony(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid, other := newPasskeysTwoUsers(t, store, clock)
	opts, cid, err := pk.BeginRegistration(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	rebound := uuid.Must(uuid.NewV7())
	if err := store.PutCeremony(context.Background(), rebound, "register", &other, store.ceremonies[cid].data, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	response := buildNoneAttestationResponse(t, opts, testRPID, testOrigin, []byte("another-users-cred"))
	if err := pk.FinishRegistration(context.Background(), uid, rebound, "YubiKey", response); err == nil {
		t.Fatal("a ceremony this package's store recorded for another user was finished as uid")
	}
	if len(store.addCalls) != 0 {
		t.Fatal("AddCredential was called despite the ceremony belonging to another user")
	}
}

// TestFinishRegistrationRejectsLoginCeremony isolates the kind check the
// same way TestFinishRegistrationRejectsAnotherUsersCeremony isolates the
// ownership check, and for the same reason: finishing with a ceremony id
// that was actually begun by BeginLogin also changes the ceremony's
// *owner* (BeginLogin always records a nil userID, since the user isn't
// known yet) and the *embedded session data* (a login session has no
// UserID for go-webauthn's own CreateCredential check to match against
// either) — so a naive version of this test would be rejected by the
// ownership check, or by go-webauthn itself, even with the kind check
// deleted, and would not actually be exercising it.
//
// To isolate the kind check alone, a real registration ceremony's exact
// session bytes (still embedding uid, so go-webauthn's own check is
// satisfied) are re-stored under a fresh id with owner uid unchanged (so
// this package's own ownership check is also satisfied) but kind flipped
// to "login" — the one property that differs from a genuine registration
// ceremony. Only the kind check can still reject it (Finding 3, fix round
// 1; discrimination evidence in the task-8 fix report).
func TestFinishRegistrationRejectsLoginCeremony(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid := newPasskeys(t, store, clock)
	opts, cid, err := pk.BeginRegistration(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	rebound := uuid.Must(uuid.NewV7())
	if err := store.PutCeremony(context.Background(), rebound, "login", &uid, store.ceremonies[cid].data, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	response := buildNoneAttestationResponse(t, opts, testRPID, testOrigin, []byte("login-ceremony-cred"))
	if err := pk.FinishRegistration(context.Background(), uid, rebound, "YubiKey", response); err == nil {
		t.Fatal("a ceremony stored as kind \"login\" finished registration")
	}
	if len(store.addCalls) != 0 {
		t.Fatal("AddCredential was called despite the ceremony being a login ceremony")
	}
}

// TestCeremonyIsSingleUse drives a real, verifiable response through a
// first, successful FinishRegistration, then retries the identical call:
// only the ceremony having been consumed can reject the retry, since the
// response itself is unchanged and would otherwise still verify (Finding 3,
// fix round 1; discrimination evidence in the task-8 fix report).
func TestCeremonyIsSingleUse(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid := newPasskeys(t, store, clock)
	opts, cid, err := pk.BeginRegistration(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	response := buildNoneAttestationResponse(t, opts, testRPID, testOrigin, []byte("single-use-cred"))
	if err := pk.FinishRegistration(context.Background(), uid, cid, "YubiKey", response); err != nil {
		t.Fatalf("first finish: %v", err)
	}
	if err := pk.FinishRegistration(context.Background(), uid, cid, "YubiKey", response); err == nil {
		t.Fatal("ceremony was reusable")
	}
	if len(store.addCalls) != 1 {
		t.Fatalf("AddCredential called %d times, want 1", len(store.addCalls))
	}
}

func TestBeginLoginIsDiscoverable(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	pk, _ := newPasskeys(t, newMemPasskeyStore(clock), clock)
	opts, _, err := pk.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		PublicKey struct {
			AllowCredentials []any  `json:"allowCredentials"`
			UserVerification string `json:"userVerification"`
		} `json:"publicKey"`
	}
	_ = json.Unmarshal(opts, &parsed)
	if len(parsed.PublicKey.AllowCredentials) != 0 || parsed.PublicKey.UserVerification != "required" {
		t.Fatalf("login options %s", opts)
	}
}

func TestFinishLoginRejectsExpiredCeremony(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, _ := newPasskeys(t, store, clock)
	_, cid, err := pk.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	if _, err := pk.FinishLogin(context.Background(), cid, json.RawMessage(`{}`)); err == nil {
		t.Fatal("expired login ceremony accepted")
	}
}

func TestFinishLoginRejectsUnknownCeremony(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	pk, _ := newPasskeys(t, newMemPasskeyStore(clock), clock)
	if _, err := pk.FinishLogin(context.Background(), uuid.Must(uuid.NewV7()), json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown login ceremony accepted")
	}
}

func TestFinishLoginRejectsRegistrationCeremony(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid := newPasskeys(t, store, clock)
	_, cid, err := pk.BeginRegistration(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pk.FinishLogin(context.Background(), cid, json.RawMessage(`{}`)); err == nil {
		t.Fatal("a registration ceremony finished login")
	}
}

// TestFinishLoginRejectsMalformedResponse reaches past the ceremony
// take/kind check (a valid, unexpired login ceremony) into the assertion
// parse step: protocol.ParseCredentialRequestResponseBody must reject a
// body that isn't a WebAuthn assertion at all, before ValidatePasskeyLogin
// (which needs real authenticator-signed data this fake cannot produce —
// see Task 15) is ever reached.
func TestFinishLoginRejectsMalformedResponse(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	pk, _ := newPasskeys(t, newMemPasskeyStore(clock), clock)
	_, cid, err := pk.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pk.FinishLogin(context.Background(), cid, json.RawMessage(`{}`)); err == nil {
		t.Fatal("malformed assertion response accepted")
	}
}

// TestFinishRegistrationFailsWhenUserVanishes covers the loadUser call
// inside FinishRegistration (after the ceremony/ownership checks already
// passed): a ceremony can be genuinely bound to a userID the users store no
// longer resolves, and that must surface as an error, not a nil-pointer
// panic or a silently empty credential list.
func TestFinishRegistrationFailsWhenUserVanishes(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, _ := newPasskeys(t, store, clock)
	ghost := uuid.Must(uuid.NewV7())
	cid := uuid.Must(uuid.NewV7())
	if err := store.PutCeremony(context.Background(), cid, "register", &ghost, []byte(`{"challenge":"x"}`), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := pk.FinishRegistration(context.Background(), ghost, cid, "Key", json.RawMessage(`{}`)); err == nil {
		t.Fatal("FinishRegistration succeeded for a user the store cannot resolve")
	}
}

// TestBeginRegistrationPropagatesListCredentialsError covers loadUser's
// second failure mode (GetUserByID succeeds, ListCredentials does not) —
// distinct from TestBeginRegistrationRejectsUnknownUser, which only
// exercises the first.
func TestBeginRegistrationPropagatesListCredentialsError(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	store.listErr = errors.New("store unavailable")
	pk, uid := newPasskeys(t, store, clock)
	if _, _, err := pk.BeginRegistration(context.Background(), uid); err == nil {
		t.Fatal("expected ListCredentials' error to propagate")
	}
}

// TestSaveCeremonyPropagatesStoreError covers saveCeremony's own error
// branch (shared by BeginRegistration and BeginLogin) via whichever ceremony
// kind is cheaper to drive: BeginLogin needs no seeded user at all.
func TestSaveCeremonyPropagatesStoreError(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	store.putErr = errors.New("write failed")
	pk, _ := newPasskeys(t, store, clock)
	if _, _, err := pk.BeginLogin(context.Background()); err == nil {
		t.Fatal("expected PutCeremony's error to propagate")
	}
}

func TestNewPasskeysRejectsInvalidConfig(t *testing.T) {
	// An RPID that is an IP address, not a domain, fails go-webauthn's own
	// config validation; NewPasskeys must wrap and return that error rather
	// than panicking or silently building an unusable *Passkeys.
	if _, err := auth.NewPasskeys(newMemPasskeyStore(time.Now), nil, nil, "127.0.0.1", "Test", "http://127.0.0.1", time.Now); err == nil {
		t.Fatal("expected an error for a non-domain RPID")
	}
}

func TestPasskeysListReturnsStoreCredentials(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	credID := uuid.Must(uuid.NewV7())
	store.listResult = []auth.Credential{{ID: credID, Name: "YubiKey"}}
	pk, uid := newPasskeys(t, store, clock)
	got, err := pk.List(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != credID || got[0].Name != "YubiKey" {
		t.Fatalf("List = %+v", got)
	}
}

func TestPasskeysDeleteDelegatesToStoreAndPropagatesErrors(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid := newPasskeys(t, store, clock)
	id := uuid.Must(uuid.NewV7())

	if err := pk.Delete(context.Background(), uid, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(store.deleteArgs) != 2 || store.deleteArgs[0] != uid || store.deleteArgs[1] != id {
		t.Fatalf("DeleteCredential called with %v, want [%v %v]", store.deleteArgs, uid, id)
	}

	store.deleteErr = auth.ErrNotFound
	if err := pk.Delete(context.Background(), uid, id); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("Delete err = %v, want ErrNotFound", err)
	}
}

// TestBeginRegistrationExcludesExistingCredentials covers the exclude-list
// loop: an account that already has passkeys must have them named in the
// new ceremony's excludeCredentials, so the browser won't offer to register
// the same authenticator twice.
func TestBeginRegistrationExcludesExistingCredentials(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	store.listResult = []auth.Credential{{CredentialID: []byte("existing-cred")}}
	pk, uid := newPasskeys(t, store, clock)

	opts, _, err := pk.BeginRegistration(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		PublicKey struct {
			ExcludeCredentials []struct {
				ID string `json:"id"`
			} `json:"excludeCredentials"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(opts, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.PublicKey.ExcludeCredentials) != 1 {
		t.Fatalf("excludeCredentials = %+v, want the one existing credential", parsed.PublicKey.ExcludeCredentials)
	}
}

// TestBeginRegistrationPropagatesSaveCeremonyError exercises
// BeginRegistration's own call site of saveCeremony's error branch — a
// separate statement from BeginLogin's, which
// TestSaveCeremonyPropagatesStoreError already covers.
func TestBeginRegistrationPropagatesSaveCeremonyError(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	store.putErr = errors.New("write failed")
	pk, uid := newPasskeys(t, store, clock)
	if _, _, err := pk.BeginRegistration(context.Background(), uid); err == nil {
		t.Fatal("expected PutCeremony's error to propagate")
	}
}

// TestTakeCeremonyRejectsCorruptSessionData covers takeCeremony's own
// json.Unmarshal failure: a ceremony whose stored session data is not valid
// JSON (which can only happen if the store's contents were corrupted or
// tampered with, since saveCeremony always writes JSON it produced itself)
// must fail closed rather than proceed with a zero-value SessionData.
func TestTakeCeremonyRejectsCorruptSessionData(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid := newPasskeys(t, store, clock)
	cid := uuid.Must(uuid.NewV7())
	if err := store.PutCeremony(context.Background(), cid, "register", &uid, []byte("not json"), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := pk.FinishRegistration(context.Background(), uid, cid, "Key", json.RawMessage(`{}`)); err == nil {
		t.Fatal("corrupt session data accepted")
	}
}

// TestFinishLoginRejectsUnknownCredential drives FinishLogin's handler
// closure with a structurally valid (but unsigned) assertion so the
// DiscoverableUserHandler actually runs: FindUserByCredentialID's default
// ErrNotFound (no such credential was ever registered) must surface as
// ErrInvalidCredentials, not a panic or a misleading different error.
func TestFinishLoginRejectsUnknownCredential(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, _ := newPasskeys(t, store, clock)
	_, cid, err := pk.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	someHandle := uuid.Must(uuid.NewV7())
	response := makeAssertionResponse(t, []byte("some-credential-id"), someHandle[:])
	if _, err := pk.FinishLogin(context.Background(), cid, response); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("FinishLogin err = %v, want ErrInvalidCredentials", err)
	}
}

// TestFinishLoginRejectsUserHandleMismatch is the black-box regression test
// for the security property the task is explicitly judged on: an assertion
// whose authenticator-reported user handle does not match the credential's
// actual owner must be rejected, even though FindUserByCredentialID itself
// resolved a real user. Without this check, a credential's rawID together
// with an attacker-chosen userHandle could be replayed to sign in as an
// arbitrary account.
//
// It does NOT, on its own, discriminate this codebase's own check from
// go-webauthn's independent re-check of the identical property
// (validateLogin's own Step 2): removing only the local check here still
// leaves this test red, because the library catches the mismatch on its
// own and FinishLogin folds either rejection into the same generic
// ErrInvalidCredentials (Finding 4, fix round 1). TestUserHandleMismatchDetectsForgedHandle
// and TestLocalUserHandleCheckReallyRuns (passkey_userhandle_test.go,
// package auth) isolate this codebase's own check specifically; this test
// remains a valid, valuable regression test of the end-to-end behavior.
func TestFinishLoginRejectsUserHandleMismatch(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	owner := uuid.Must(uuid.NewV7())
	store.findUserID = &owner
	pk, _ := newPasskeys(t, store, clock)
	_, cid, err := pk.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	forgedHandle := uuid.Must(uuid.NewV7()) // deliberately not owner
	response := makeAssertionResponse(t, []byte("some-credential-id"), forgedHandle[:])
	if _, err := pk.FinishLogin(context.Background(), cid, response); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("FinishLogin err = %v, want ErrInvalidCredentials", err)
	}
}

// TestFinishLoginPropagatesStoreErrorInsteadOfMaskingIt guards against
// FinishLogin flattening every failure inside its DiscoverableUserHandler
// into ErrInvalidCredentials: a genuine store outage while resolving the
// credential's owner is not "this assertion is invalid" and must not be
// reported to the client (and, via mapAuthErr, silently 401'd) as if it
// were — the rest of this package (see totp.go's Enabled/verifyAndConsume
// comments) deliberately fails closed and lets an infrastructure error
// propagate rather than folding it into a domain rejection, and FinishLogin
// must do the same.
func TestFinishLoginPropagatesStoreErrorInsteadOfMaskingIt(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	store.findErr = errors.New("store outage")
	pk, _ := newPasskeys(t, store, clock)
	_, cid, err := pk.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	someHandle := uuid.Must(uuid.NewV7())
	response := makeAssertionResponse(t, []byte("some-credential-id"), someHandle[:])
	if _, err := pk.FinishLogin(context.Background(), cid, response); err == nil || errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("FinishLogin err = %v, want the store error propagated, not ErrInvalidCredentials", err)
	}
}
