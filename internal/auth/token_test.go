package auth_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// memTokenStore is a hand-rolled auth.TokenStore, the same fake-store seam
// TOTP and Passkeys use: a map keyed by the SHA-256 hash (mirroring the
// real store's unique secret_hash lookup) lets every branch of Tokens be
// driven without a database.
type memTokenStore struct {
	byHash map[string]auth.Token
	admin  map[uuid.UUID]bool
	revoke map[uuid.UUID]bool

	insertErr error
	getErr    error // when non-nil, returned as-is instead of a real lookup
	listErr   error
	revokeErr error
	touchErr  error
	touched   []uuid.UUID
	updated   int // store.Update calls
}

func newMemTokenStore() *memTokenStore {
	return &memTokenStore{byHash: map[string]auth.Token{}, admin: map[uuid.UUID]bool{}, revoke: map[uuid.UUID]bool{}}
}

func (m *memTokenStore) Insert(_ context.Context, t auth.Token, secretHash []byte) error {
	if m.insertErr != nil {
		return m.insertErr
	}
	m.byHash[string(secretHash)] = t
	return nil
}

// GetByHash mirrors the real store's SQL filter (revoked_at IS NULL AND
// (expires_at IS NULL OR expires_at > now())) in Go, so both stores reject
// the same tokens for the same reasons.
func (m *memTokenStore) GetByHash(_ context.Context, secretHash []byte, now time.Time) (auth.Token, bool, error) {
	if m.getErr != nil {
		return auth.Token{}, false, m.getErr
	}
	tok, ok := m.byHash[string(secretHash)]
	if !ok {
		return auth.Token{}, false, auth.ErrNotFound
	}
	if m.revoke[tok.ID] {
		return auth.Token{}, false, auth.ErrNotFound
	}
	if tok.ExpiresAt != nil && !tok.ExpiresAt.After(now) {
		return auth.Token{}, false, auth.ErrNotFound
	}
	return tok, m.admin[tok.UserID], nil
}

// Usable mirrors GetByHash's filter, by id.
func (m *memTokenStore) Usable(_ context.Context, id uuid.UUID, now time.Time) (bool, error) {
	if m.getErr != nil {
		return false, m.getErr
	}
	for _, tok := range m.byHash {
		if tok.ID == id {
			return !m.revoke[tok.ID] && (tok.ExpiresAt == nil || tok.ExpiresAt.After(now)), nil
		}
	}
	return false, nil
}

func (m *memTokenStore) ListForOrg(_ context.Context, orgID uuid.UUID) ([]auth.Token, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var out []auth.Token
	for _, tok := range m.byHash {
		if tok.OrgID == orgID && !m.revoke[tok.ID] {
			out = append(out, tok)
		}
	}
	return out, nil
}

func (m *memTokenStore) Revoke(_ context.Context, orgID, id uuid.UUID) error {
	if m.revokeErr != nil {
		return m.revokeErr
	}
	// A token already marked revoked must report ErrNotFound on a second
	// Revoke, matching SQLTokenStore.Revoke's WHERE ... revoked_at IS NULL
	// clause (see TestSQLTokenStoreGetByHashExcludesRevoked's "second
	// Revoke" assertion) — without this check the fake would silently
	// accept a double revoke as success, a behavior the real store does
	// not have (fix round 1, adjacent item).
	if m.revoke[id] {
		return auth.ErrNotFound
	}
	for _, tok := range m.byHash {
		if tok.ID == id && tok.OrgID == orgID {
			m.revoke[id] = true
			return nil
		}
	}
	return auth.ErrNotFound
}

// Get mirrors GetAPITokenForOrg: any non-revoked token of the org, expired or not.
func (m *memTokenStore) Get(_ context.Context, orgID, id uuid.UUID) (auth.Token, error) {
	for _, tok := range m.byHash {
		if tok.ID == id && tok.OrgID == orgID && !m.revoke[id] {
			return tok, nil
		}
	}
	return auth.Token{}, auth.ErrNotFound
}

// Update mirrors UpdateAPIToken's WHERE clause: not revoked, not expired as of now.
func (m *memTokenStore) Update(_ context.Context, orgID, id uuid.UUID, name string, areas []string, expiresAt *time.Time, now time.Time) (auth.Token, error) {
	m.updated++
	for h, tok := range m.byHash {
		if tok.ID != id || tok.OrgID != orgID || m.revoke[id] {
			continue
		}
		if tok.ExpiresAt != nil && !tok.ExpiresAt.After(now) {
			return auth.Token{}, auth.ErrNotFound
		}
		tok.Name, tok.Areas, tok.ExpiresAt = name, areas, expiresAt
		m.byHash[h] = tok
		return tok, nil
	}
	return auth.Token{}, auth.ErrNotFound
}

func (m *memTokenStore) TouchLastUsed(_ context.Context, id uuid.UUID, _ time.Time) error {
	m.touched = append(m.touched, id)
	return m.touchErr
}

// tamperToken returns plain with the first character after "pxt_" changed
// ("A", or "B" when it already is "A"). Every bit of that character is
// significant, unlike the 43rd base64url character of 32 bytes, whose two
// low bits the decoder ignores: a change there can decode to the same secret.
func tamperToken(plain string) string {
	const prefix = "pxt_"
	repl := "A"
	if plain[len(prefix)] == 'A' {
		repl = "B"
	}
	return prefix + repl + plain[len(prefix)+1:]
}

func TestTamperTokenAlwaysChangesTheSecret(t *testing.T) {
	secret := func(p string) []byte {
		t.Helper()
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(p, "pxt_"))
		if err != nil {
			t.Fatalf("decode %q: %v", p, err)
		}
		return b
	}
	plains := []string{"pxt_A" + strings.Repeat("x", 42), "pxt_B" + strings.Repeat("x", 42)}
	tk := auth.NewTokens(newMemTokenStore(), time.Now)
	var created []string
	for range 1000 {
		plain, _, err := tk.Create(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "ci", "operator", nil)
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, plain)
	}
	plains = append(plains, created...)
	// A token whose secret was altered resolves to nothing.
	for _, p := range created {
		if _, _, err := tk.Resolve(context.Background(), tamperToken(p)); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("Resolve(tamperToken(%q)) err = %v, want ErrUnauthenticated", p, err)
		}
	}
	for _, p := range plains {
		got := tamperToken(p)
		if got == p || bytes.Equal(secret(got), secret(p)) {
			t.Fatalf("tamperToken(%q) = %q decodes to the same secret", p, got)
		}
	}
}

func TestTokenCreateResolveRevoke(t *testing.T) {
	store := newMemTokenStore()
	tk := auth.NewTokens(store, time.Now)
	uid, org := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	plain, tok, err := tk.Create(context.Background(), uid, org, "ci", "operator", nil)
	if err != nil || !strings.HasPrefix(plain, "pxt_") {
		t.Fatalf("create: %q %v", plain, err)
	}
	// pxt_ + base64url(32 random bytes), unpadded: 4 + 43 = 47 chars — the
	// corrected, id-less format (see token.go's Create doc comment).
	if len(plain) != 47 {
		t.Fatalf("plain length = %d, want 47 (pxt_<43 chars>)", len(plain))
	}
	p, scope, err := tk.Resolve(context.Background(), plain)
	if err != nil || p.UserID != uid || p.TokenID == nil || *p.TokenID != tok.ID || scope.OrgID != org || scope.Role != "operator" || !p.MFAComplete {
		t.Fatalf("resolve: %+v %+v %v", p, scope, err)
	}
	if len(store.touched) != 1 || store.touched[0] != tok.ID {
		t.Fatalf("TouchLastUsed calls = %+v, want one call for %v", store.touched, tok.ID)
	}
	if _, _, err := tk.Resolve(context.Background(), tamperToken(plain)); err == nil {
		t.Fatal("tampered token accepted")
	}
	if err := tk.Revoke(context.Background(), org, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tk.Resolve(context.Background(), plain); err == nil {
		t.Fatal("revoked token accepted")
	}
}

func TestTokenExpiry(t *testing.T) {
	now := time.Now()
	store := newMemTokenStore()
	tk := auth.NewTokens(store, func() time.Time { return now })
	exp := now.Add(time.Hour)
	plain, _, err := tk.Create(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "short", "viewer", &exp)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, _, err := tk.Resolve(context.Background(), plain); err == nil {
		t.Fatal("expired token accepted")
	}
}

// TestTokenCreateNeverReturnsThePlaintextAgain asserts the "shown once"
// contract at the type level: nothing in the Token struct Create hands back
// alongside the plaintext can be used to recover it, and List (which reads
// the same rows back from the store) exposes no secret material either.
func TestTokenCreateNeverReturnsThePlaintextAgain(t *testing.T) {
	store := newMemTokenStore()
	tk := auth.NewTokens(store, time.Now)
	org := uuid.Must(uuid.NewV7())
	_, tok, err := tk.Create(context.Background(), uuid.Must(uuid.NewV7()), org, "ci", "operator", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok.ID == uuid.Nil || tok.Name != "ci" || tok.Role != "operator" {
		t.Fatalf("returned Token = %+v", tok)
	}
	list, err := tk.List(context.Background(), org)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	// Token has no field capable of carrying the secret or its hash — this
	// is a compile-time guarantee (see the Token struct in token.go), so
	// there is nothing further to assert at runtime beyond the list
	// containing exactly the metadata Create already returned.
	if list[0].ID != tok.ID {
		t.Fatalf("list[0] = %+v, want id %v", list[0], tok.ID)
	}
}

// TestTokenResolveRejectsMalformedInput asserts Resolve never panics on
// hostile input and rejects every shape that is not a genuine token — while
// a real, valid token minted in the same store still resolves. Running
// this against an empty store would make every rejection trivially true
// for the wrong reason (nothing to find, not "the input was rejected"): an
// implementation that skipped the prefix/decode checks entirely and just
// hashed whatever it was given would still pass, since none of the
// malformed inputs happen to hash to a real row either. Seeding one real
// token first, and asserting it still resolves at the end, proves Resolve
// is actually doing the rejecting (Finding 4, fix round 1).
func TestTokenResolveRejectsMalformedInput(t *testing.T) {
	store := newMemTokenStore()
	tk := auth.NewTokens(store, time.Now)
	valid, _, err := tk.Create(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "ci", "viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{
		"",
		"pxt_",
		"pxt_not-valid-base64!!!",
		"nonsense",
		"pxt_" + strings.Repeat("a", 4096), // oversized, still decodes but won't match
		valid[:len(valid)-1],               // truncated by one character
	} {
		if _, _, err := tk.Resolve(context.Background(), plain); err == nil {
			t.Fatalf("Resolve(%q) succeeded, want rejection", plain)
		}
	}
	if _, _, err := tk.Resolve(context.Background(), valid); err != nil {
		t.Fatalf("Resolve(valid) = %v, want success — the malformed cases above must be rejected for their own sake, not because the store is empty", err)
	}
}

// TestTokenRevokeTwiceReturnsNotFound asserts the fake matches
// SQLTokenStore.Revoke's behavior on a token already revoked: ErrNotFound,
// not a silent second success (see memTokenStore.Revoke's fix-round-1
// comment, and TestSQLTokenStoreGetByHashExcludesRevoked's "second Revoke"
// assertion for the real store's own proof of this).
func TestTokenRevokeTwiceReturnsNotFound(t *testing.T) {
	store := newMemTokenStore()
	tk := auth.NewTokens(store, time.Now)
	org := uuid.Must(uuid.NewV7())
	_, tok, err := tk.Create(context.Background(), uuid.Must(uuid.NewV7()), org, "ci", "viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tk.Revoke(context.Background(), org, tok.ID); err != nil {
		t.Fatalf("first Revoke: %v", err)
	}
	if err := tk.Revoke(context.Background(), org, tok.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("second Revoke err = %v, want %v", err, auth.ErrNotFound)
	}
}

// TestTokenResolveFailsClosedOnStoreError is the fail-closed assertion this
// project has applied to TOTP.Enabled and TOTP.Verify already: a genuine
// store error (not "no such token") must propagate as itself, not be
// folded into ErrUnauthenticated the way an actually-unknown token is.
// Squashing it would make a database outage indistinguishable from "this
// bearer credential is invalid" to anything inspecting the error, including
// future callers that might want to answer differently (e.g. a 500 instead
// of a 401).
func TestTokenResolveFailsClosedOnStoreError(t *testing.T) {
	sentinel := errors.New("store unavailable")
	store := newMemTokenStore()
	tk := auth.NewTokens(store, time.Now)
	plain, _, err := tk.Create(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "x", "viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Inject the store failure only after the token exists, so Resolve's
	// lookup is what fails, not Create's own store write.
	store.getErr = sentinel
	if _, _, err := tk.Resolve(context.Background(), plain); !errors.Is(err, sentinel) {
		t.Fatalf("Resolve err = %v, want %v (propagated, not folded into ErrUnauthenticated)", err, sentinel)
	}
}

// TestTokenResolveTouchLastUsedFailureDoesNotRejectValidToken asserts that
// TouchLastUsed is best-effort bookkeeping: its failure must not turn an
// already-verified, valid token into a rejected request.
func TestTokenResolveTouchLastUsedFailureDoesNotRejectValidToken(t *testing.T) {
	store := newMemTokenStore()
	tk := auth.NewTokens(store, time.Now)
	plain, _, err := tk.Create(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "x", "viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	store.touchErr = errors.New("touch failed")
	if _, _, err := tk.Resolve(context.Background(), plain); err != nil {
		t.Fatalf("Resolve rejected a valid token because TouchLastUsed failed: %v", err)
	}
}

// TestTokenCreatePropagatesInsertError asserts a store write failure
// surfaces to the caller instead of reporting success with an unpersisted
// token (the caller would otherwise hand out a plaintext that resolves to
// nothing).
func TestTokenCreatePropagatesInsertError(t *testing.T) {
	sentinel := errors.New("insert failed")
	store := newMemTokenStore()
	store.insertErr = sentinel
	tk := auth.NewTokens(store, time.Now)
	if plain, _, err := tk.Create(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "x", "viewer", nil); !errors.Is(err, sentinel) || plain != "" {
		t.Fatalf("Create = (%q, %v), want (\"\", %v)", plain, err, sentinel)
	}
}

// TestTokenListRevokePropagateStoreErrors covers List and Revoke's own
// pass-through of a store failure.
func TestTokenListRevokePropagateStoreErrors(t *testing.T) {
	listErr, revokeErr := errors.New("list failed"), errors.New("revoke failed")
	store := newMemTokenStore()
	store.listErr, store.revokeErr = listErr, revokeErr
	tk := auth.NewTokens(store, time.Now)
	if _, err := tk.List(context.Background(), uuid.Must(uuid.NewV7())); !errors.Is(err, listErr) {
		t.Fatalf("List err = %v, want %v", err, listErr)
	}
	if err := tk.Revoke(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())); !errors.Is(err, revokeErr) {
		t.Fatalf("Revoke err = %v, want %v", err, revokeErr)
	}
}

// TestTokenRevokeUnknownReturnsNotFound asserts Revoke reports ErrNotFound
// (rather than a silent no-op success) when no matching, not-yet-revoked
// row exists — so a caller can never mistake "nothing happened" for
// "revoked", the same guarantee SQLPasskeyStore.DeleteCredential documents.
func TestTokenRevokeUnknownReturnsNotFound(t *testing.T) {
	store := newMemTokenStore()
	tk := auth.NewTokens(store, time.Now)
	if err := tk.Revoke(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("Revoke(unknown) err = %v, want %v", err, auth.ErrNotFound)
	}
}

func TestTokensUpdate(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	past := now.Add(-time.Hour)
	str := func(s string) *string { return &s }
	areas := func(a ...string) *[]string { return &a }
	org, user := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	// kind: "api" or "assistant"; expiry: nil for none.
	cases := []struct {
		name     string
		kind     auth.TokenKind
		expires  *time.Time
		revoked  bool
		otherOrg bool
		change   auth.TokenChange
		wantErr  error
		wantNoOp bool // the store must not be written
		check    func(t *testing.T, before, after auth.Token)
	}{
		{name: "rename", kind: auth.TokenKindAPI, change: auth.TokenChange{Name: str("renamed")},
			check: func(t *testing.T, b, a auth.Token) {
				if b.Name != "orig" || a.Name != "renamed" {
					t.Fatalf("names before=%q after=%q", b.Name, a.Name)
				}
			}},
		{name: "replace areas", kind: auth.TokenKindAssistant, change: auth.TokenChange{Areas: areas("monitoring", "activity")},
			check: func(t *testing.T, b, a auth.Token) {
				if len(b.Areas) != 1 || b.Areas[0] != "inventory" || len(a.Areas) != 2 || a.Areas[0] != "monitoring" {
					t.Fatalf("areas before=%v after=%v", b.Areas, a.Areas)
				}
				if a.Name != "orig" {
					t.Fatalf("name changed to %q", a.Name)
				}
			}},
		{name: "set expiry", kind: auth.TokenKindAPI, change: auth.TokenChange{Expiry: auth.ExpirySet, ExpiresAt: &future},
			check: func(t *testing.T, b, a auth.Token) {
				if b.ExpiresAt != nil || a.ExpiresAt == nil || !a.ExpiresAt.Equal(future) {
					t.Fatalf("expiry before=%v after=%v", b.ExpiresAt, a.ExpiresAt)
				}
			}},
		{name: "clear expiry", kind: auth.TokenKindAPI, expires: &future, change: auth.TokenChange{Expiry: auth.ExpiryClear},
			check: func(t *testing.T, b, a auth.Token) {
				if b.ExpiresAt == nil || a.ExpiresAt != nil {
					t.Fatalf("expiry before=%v after=%v", b.ExpiresAt, a.ExpiresAt)
				}
			}},
		{name: "keep expiry when only renaming", kind: auth.TokenKindAPI, expires: &future, change: auth.TokenChange{Name: str("x")},
			check: func(t *testing.T, b, a auth.Token) {
				if a.ExpiresAt == nil || !a.ExpiresAt.Equal(future) {
					t.Fatalf("expiry lost: %v", a.ExpiresAt)
				}
			}},
		{name: "expiry in the past", kind: auth.TokenKindAPI, change: auth.TokenChange{Expiry: auth.ExpirySet, ExpiresAt: &past},
			wantErr: auth.ErrExpiryInPast, wantNoOp: true},
		{name: "expiry exactly now", kind: auth.TokenKindAPI, change: auth.TokenChange{Expiry: auth.ExpirySet, ExpiresAt: &now},
			wantErr: auth.ErrExpiryInPast, wantNoOp: true},
		{name: "areas on an api token", kind: auth.TokenKindAPI, change: auth.TokenChange{Areas: areas("monitoring")},
			wantErr: auth.ErrAreasOnAPIToken, wantNoOp: true},
		{name: "empty areas", kind: auth.TokenKindAssistant, change: auth.TokenChange{Areas: areas()},
			wantErr: auth.ErrNoAreas, wantNoOp: true},
		{name: "empty change", kind: auth.TokenKindAPI, change: auth.TokenChange{},
			wantErr: auth.ErrNoChange, wantNoOp: true},
		{name: "expired token", kind: auth.TokenKindAPI, expires: &past, change: auth.TokenChange{Name: str("x")},
			wantErr: auth.ErrTokenExpired, wantNoOp: true},
		{name: "expired token with empty change reports expired", kind: auth.TokenKindAPI, expires: &past, change: auth.TokenChange{},
			wantErr: auth.ErrTokenExpired, wantNoOp: true},
		{name: "revoked token", kind: auth.TokenKindAPI, revoked: true, change: auth.TokenChange{Name: str("x")},
			wantErr: auth.ErrNotFound, wantNoOp: true},
		{name: "other org", kind: auth.TokenKindAPI, otherOrg: true, change: auth.TokenChange{Name: str("x")},
			wantErr: auth.ErrNotFound, wantNoOp: true},
		{name: "unchanged value still writes", kind: auth.TokenKindAPI, change: auth.TokenChange{Name: str("orig")},
			check: func(t *testing.T, b, a auth.Token) {
				if b.Name != a.Name || b.Name != "orig" {
					t.Fatalf("before=%+v after=%+v", b, a)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemTokenStore()
			tk := auth.NewTokens(store, func() time.Time { return now })
			// Seed the token directly so an expired one can exist.
			seeded := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: user, OrgID: org, Name: "orig", Role: "operator",
				ExpiresAt: tc.expires, Kind: tc.kind}
			if tc.kind == auth.TokenKindAssistant {
				seeded.Areas = []string{"inventory"}
			}
			if err := store.Insert(context.Background(), seeded, []byte(tc.name)); err != nil {
				t.Fatal(err)
			}
			if tc.revoked {
				store.revoke[seeded.ID] = true
			}
			orgID := org
			if tc.otherOrg {
				orgID = uuid.Must(uuid.NewV7())
			}
			before, after, err := tk.Update(context.Background(), orgID, seeded.ID, tc.change)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if tc.wantNoOp && store.updated != 0 {
					t.Fatalf("store.Update called %d times on a refused change", store.updated)
				}
				return
			}
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if store.updated != 1 {
				t.Fatalf("store.Update called %d times, want 1", store.updated)
			}
			tc.check(t, before, after)
		})
	}
}

func TestTokensUpdateErrorsWrapInvalidInput(t *testing.T) {
	for _, err := range []error{auth.ErrNoChange, auth.ErrAreasOnAPIToken, auth.ErrNoAreas, auth.ErrExpiryInPast} {
		if !errors.Is(err, auth.ErrInvalidInput) {
			t.Fatalf("%v does not wrap ErrInvalidInput", err)
		}
	}
	if errors.Is(auth.ErrTokenExpired, auth.ErrInvalidInput) {
		t.Fatal("ErrTokenExpired must not be an invalid-input error")
	}
}

func TestTokensGet(t *testing.T) {
	store := newMemTokenStore()
	tk := auth.NewTokens(store, time.Now)
	org := uuid.Must(uuid.NewV7())
	_, tok, err := tk.Create(context.Background(), uuid.Must(uuid.NewV7()), org, "g", "viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tk.Get(context.Background(), org, tok.ID)
	if err != nil || got.ID != tok.ID {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := tk.Get(context.Background(), uuid.Must(uuid.NewV7()), tok.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("Get other org err = %v", err)
	}
}
