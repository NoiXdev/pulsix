package auth_test

import (
	"context"
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
	for _, tok := range m.byHash {
		if tok.ID == id && tok.OrgID == orgID {
			m.revoke[id] = true
			return nil
		}
	}
	return auth.ErrNotFound
}

func (m *memTokenStore) TouchLastUsed(_ context.Context, id uuid.UUID, _ time.Time) error {
	m.touched = append(m.touched, id)
	return m.touchErr
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
	if _, _, err := tk.Resolve(context.Background(), plain[:len(plain)-2]+"zz"); err == nil {
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
// hostile input and rejects every shape that is not a genuine token.
func TestTokenResolveRejectsMalformedInput(t *testing.T) {
	tk := auth.NewTokens(newMemTokenStore(), time.Now)
	for _, plain := range []string{
		"",
		"pxt_",
		"pxt_not-valid-base64!!!",
		"nonsense",
		"pxt_" + strings.Repeat("a", 4096), // oversized, still decodes but won't match
	} {
		if _, _, err := tk.Resolve(context.Background(), plain); err == nil {
			t.Fatalf("Resolve(%q) succeeded, want rejection", plain)
		}
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
