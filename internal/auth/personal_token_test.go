package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// addPersonal stores a personal token in the fake under the hash of a freshly
// minted secret and returns the plaintext.
func addPersonal(t *testing.T, m *memTokenStore, tok auth.PersonalToken) string {
	t.Helper()
	plain, hash, err := auth.MintPersonalSecret()
	if err != nil {
		t.Fatal(err)
	}
	m.personal[string(hash)] = tok
	return plain
}

func TestMintPersonalSecretShape(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		plain, hash, err := auth.MintPersonalSecret()
		if err != nil {
			t.Fatal(err)
		}
		// pxp_ + base64url(32 random bytes), unpadded: 4 + 43 = 47.
		if !strings.HasPrefix(plain, "pxp_") || len(plain) != 47 {
			t.Fatalf("plain = %q (%d chars), want pxp_ and 47 chars", plain, len(plain))
		}
		secret, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(plain, "pxp_"))
		if err != nil || len(secret) != 32 {
			t.Fatalf("secret of %q: %d bytes, %v", plain, len(secret), err)
		}
		sum := sha256.Sum256(secret)
		if string(hash) != string(sum[:]) {
			t.Fatal("hash is not the SHA-256 of the secret")
		}
		if seen[plain] {
			t.Fatalf("secret %q minted twice", plain)
		}
		seen[plain] = true
	}
}

func TestResolvePersonalToken(t *testing.T) {
	store := newMemTokenStore()
	tk := auth.NewTokens(store, time.Now)
	tok := auth.PersonalToken{ID: uuid.Must(uuid.NewV7()), UserID: uuid.Must(uuid.NewV7()), Name: "laptop",
		Kind: auth.TokenKindAssistant, Areas: []string{"jobs.read"}, CreatedAt: time.Now()}
	// The owner is an instance administrator: a personal token never
	// carries that (controller ruling P5).
	store.admin[tok.UserID] = true
	plain := addPersonal(t, store, tok)

	p, scope, err := tk.Resolve(context.Background(), plain)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != tok.UserID || p.TokenID == nil || *p.TokenID != tok.ID || !p.MFAComplete || p.InstanceAdmin {
		t.Fatalf("principal = %+v", p)
	}
	if !scope.Personal || scope.OrgID != uuid.Nil || scope.Role != "" || scope.Kind != auth.TokenKindAssistant ||
		len(scope.Areas) != 1 || scope.Areas[0] != "jobs.read" {
		t.Fatalf("scope = %+v", scope)
	}
	if len(store.touchedPersonal) != 1 || store.touchedPersonal[0] != tok.ID {
		t.Fatalf("personal touches = %v, want one for %v", store.touchedPersonal, tok.ID)
	}
	if len(store.touched) != 0 {
		t.Fatalf("organization token touches = %v, want none", store.touched)
	}
}

func TestResolvePersonalTokenRevokedExpiredDisabled(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)
	cases := map[string]func(m *memTokenStore, tok *auth.PersonalToken){
		"revoked":  func(m *memTokenStore, tok *auth.PersonalToken) { m.revoke[tok.ID] = true },
		"expired":  func(_ *memTokenStore, tok *auth.PersonalToken) { tok.ExpiresAt = &past },
		"disabled": func(m *memTokenStore, tok *auth.PersonalToken) { m.disabled[tok.UserID] = true },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			store := newMemTokenStore()
			tk := auth.NewTokens(store, func() time.Time { return now })
			tok := auth.PersonalToken{ID: uuid.Must(uuid.NewV7()), UserID: uuid.Must(uuid.NewV7()), Name: "x", Kind: auth.TokenKindAPI}
			spoil(store, &tok)
			plain := addPersonal(t, store, tok)
			if _, _, err := tk.Resolve(context.Background(), plain); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("Resolve err = %v, want ErrUnauthenticated", err)
			}
			if usable, err := tk.StillUsable(context.Background(), tok.ID); err != nil || usable {
				t.Fatalf("StillUsable = %v, %v; want false", usable, err)
			}
		})
	}
}

func TestResolvePersonalTokenMalformedAndStoreFailure(t *testing.T) {
	store := newMemTokenStore()
	tk := auth.NewTokens(store, time.Now)
	for _, plain := range []string{"pxp_", "pxp_!!!", "pxp_" + strings.Repeat("A", 43), "pxP_" + strings.Repeat("A", 43)} {
		if _, _, err := tk.Resolve(context.Background(), plain); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("Resolve(%q) err = %v, want ErrUnauthenticated", plain, err)
		}
	}
	// A store failure is not "no such token": it propagates, so the bearer
	// middleware fails closed.
	boom := errors.New("connection reset")
	tok := auth.PersonalToken{ID: uuid.Must(uuid.NewV7()), UserID: uuid.Must(uuid.NewV7()), Kind: auth.TokenKindAPI}
	plain := addPersonal(t, store, tok)
	store.getErr = boom
	if _, _, err := tk.Resolve(context.Background(), plain); !errors.Is(err, boom) {
		t.Fatalf("Resolve err = %v, want the store's error", err)
	}
	if _, err := tk.StillUsable(context.Background(), tok.ID); !errors.Is(err, boom) {
		t.Fatalf("StillUsable err = %v, want the store's error", err)
	}
}

// A failed last-use write is bookkeeping, not an authentication decision.
func TestResolvePersonalTokenTouchFailureDoesNotReject(t *testing.T) {
	store := newMemTokenStore()
	store.touchErr = errors.New("write failed")
	tk := auth.NewTokens(store, time.Now)
	tok := auth.PersonalToken{ID: uuid.Must(uuid.NewV7()), UserID: uuid.Must(uuid.NewV7()), Kind: auth.TokenKindAPI}
	plain := addPersonal(t, store, tok)
	if _, scope, err := tk.Resolve(context.Background(), plain); err != nil || !scope.Personal {
		t.Fatalf("Resolve = %+v, %v", scope, err)
	}
}

func TestStillUsableCoversPersonalTokens(t *testing.T) {
	store := newMemTokenStore()
	tk := auth.NewTokens(store, time.Now)
	personal := auth.PersonalToken{ID: uuid.Must(uuid.NewV7()), UserID: uuid.Must(uuid.NewV7()), Kind: auth.TokenKindAPI}
	addPersonal(t, store, personal)
	_, org, err := tk.Create(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "ci", "operator", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]uuid.UUID{"personal": personal.ID, "organization": org.ID} {
		if usable, err := tk.StillUsable(context.Background(), id); err != nil || !usable {
			t.Fatalf("%s token: StillUsable = %v, %v; want true", name, usable, err)
		}
	}
	if usable, err := tk.StillUsable(context.Background(), uuid.Must(uuid.NewV7())); err != nil || usable {
		t.Fatalf("unknown token: StillUsable = %v, %v; want false", usable, err)
	}
	store.revoke[personal.ID] = true
	if usable, err := tk.StillUsable(context.Background(), personal.ID); err != nil || usable {
		t.Fatalf("revoked personal token: StillUsable = %v, %v; want false", usable, err)
	}
}
