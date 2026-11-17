// A signed-in person re-proves a passkey (backlog 3.93): the step-up of the
// current session and the passkey renewal both run a ceremony bound to the
// session's own account. Unlike the discoverable sign-in ceremony, an
// assertion of another account's passkey must never pass it. These tests
// drive the real database with assertions signed by keys the test holds
// (buildSignedAssertionResponse, passkey_clone_test.go).
package auth_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// beginReauth starts a re-authentication ceremony for uid and signs its
// assertion with k, presenting owner as the authenticator's user handle.
func beginReauth(t *testing.T, pk *auth.Passkeys, uid, owner uuid.UUID, k loginKey, counter uint32) (uuid.UUID, json.RawMessage) {
	t.Helper()
	opts, cid, err := pk.BeginReauth(ctx, uid)
	if err != nil {
		t.Fatalf("BeginReauth: %v", err)
	}
	return cid, buildSignedAssertionResponse(t, opts, testRPID, testOrigin, k.credID, owner[:], counter, k.priv)
}

func (w *resetWorld) signCount(t *testing.T, k loginKey) int64 {
	t.Helper()
	var n int64
	if err := w.d.Pool().QueryRow(ctx, `SELECT sign_count FROM passkeys WHERE credential_id = $1`, k.credID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReauthAcceptsTheOwnPasskeyAndAdvancesItsCounter(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "reauth-own@example.test")
	k := w.seedLoginKey(t, uid)
	before := w.sessionCount(t, uid)

	cid, response := beginReauth(t, w.passkeys, uid, uid, k, 1)
	if err := w.passkeys.FinishReauth(ctx, uid, cid, response); err != nil {
		t.Fatalf("FinishReauth: %v", err)
	}
	if n := w.signCount(t, k); n != 1 {
		t.Fatalf("sign_count = %d, want 1", n)
	}
	if after := w.sessionCount(t, uid); after != before {
		t.Fatalf("session rows %d -> %d: a re-authentication opens no session", before, after)
	}
}

// The ceremony offers the account's own passkeys and nothing else
// (non-discoverable, allowCredentials).
func TestBeginReauthOffersOnlyTheOwnPasskeys(t *testing.T) {
	w := newResetWorld(t)
	a := w.seedAccount(t, "reauth-offer-a@example.test")
	b := w.seedAccount(t, "reauth-offer-b@example.test")
	ka1, ka2 := w.seedLoginKey(t, a), w.seedLoginKey(t, a)
	w.seedLoginKey(t, b)

	opts, _, err := w.passkeys.BeginReauth(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		PublicKey struct {
			AllowCredentials []struct {
				ID string `json:"id"`
			} `json:"allowCredentials"`
			UserVerification string `json:"userVerification"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(opts, &parsed); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range parsed.PublicKey.AllowCredentials {
		got[c.ID] = true
	}
	want := map[string]bool{
		base64.RawURLEncoding.EncodeToString(ka1.credID): true,
		base64.RawURLEncoding.EncodeToString(ka2.credID): true,
	}
	if len(got) != len(want) || !got[base64.RawURLEncoding.EncodeToString(ka1.credID)] || !got[base64.RawURLEncoding.EncodeToString(ka2.credID)] {
		t.Fatalf("allowCredentials = %v, want exactly %v", got, want)
	}
	if parsed.PublicKey.UserVerification != "required" {
		t.Fatalf("userVerification = %q, want required", parsed.PublicKey.UserVerification)
	}
}

func TestBeginReauthWithoutAPasskeyIsNotFound(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "reauth-none@example.test")
	if _, _, err := w.passkeys.BeginReauth(ctx, uid); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("BeginReauth without a passkey = %v, want ErrNotFound", err)
	}
}

// The flaw the user-bound ceremony closes: user B's passkey, presented on
// user A's ceremony — with B's user handle, or with A's forged in — is
// refused, B's counter stays as it was, and A's session is untouched.
func TestReauthRefusesOtherUsersPasskey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		handle func(a, b uuid.UUID) uuid.UUID
	}{
		{"with the other account's user handle", func(_, b uuid.UUID) uuid.UUID { return b }},
		{"with a forged user handle", func(a, _ uuid.UUID) uuid.UUID { return a }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newResetWorld(t)
			a := w.seedAccount(t, "reauth-a@example.test")
			b := w.seedAccount(t, "reauth-b@example.test")
			w.seedLoginKey(t, a)
			kb := w.seedLoginKey(t, b)
			sid, cookie := w.openSession(t, a)
			before, err := w.sessions.Lookup(ctx, cookie)
			if err != nil {
				t.Fatal(err)
			}

			cid, response := beginReauth(t, w.passkeys, a, tc.handle(a, b), kb, 1)
			if err := w.passkeys.FinishReauth(ctx, a, cid, response); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("FinishReauth(A, B's passkey) = %v, want ErrInvalidCredentials", err)
			}
			if n := w.signCount(t, kb); n != 0 {
				t.Fatalf("B's sign_count = %d, want 0", n)
			}
			after, err := w.sessions.Lookup(ctx, cookie)
			if err != nil {
				t.Fatalf("A's session after the refusal: %v", err)
			}
			if after.SessionID != sid || !after.MFAAt.Equal(*before.MFAAt) {
				t.Fatalf("A's session changed: mfa_at %v -> %v", before.MFAAt, after.MFAAt)
			}
		})
	}
}

// A ceremony begun for one account cannot be finished for another, even
// with the finishing account's own, valid passkey.
func TestReauthRefusesAnotherUsersCeremony(t *testing.T) {
	w := newResetWorld(t)
	a := w.seedAccount(t, "reauth-cer-a@example.test")
	b := w.seedAccount(t, "reauth-cer-b@example.test")
	ka := w.seedLoginKey(t, a)
	w.seedLoginKey(t, b)

	opts, cid, err := w.passkeys.BeginReauth(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	response := buildSignedAssertionResponse(t, opts, testRPID, testOrigin, ka.credID, a[:], 1, ka.priv)
	if err := w.passkeys.FinishReauth(ctx, a, cid, response); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("FinishReauth(A, B's ceremony) = %v, want ErrNotFound", err)
	}
	if n := w.signCount(t, ka); n != 0 {
		t.Fatalf("sign_count = %d, want 0", n)
	}
}

// The sign-in ceremony and the re-authentication ceremony do not finish
// each other: a discoverable ceremony carries no account, and a
// re-authentication must not open a session.
func TestReauthAndLoginCeremoniesAreDistinct(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "reauth-kind@example.test")
	k := w.seedLoginKey(t, uid)

	loginCID, loginResponse := beginLogin(t, w.passkeys, uid, k)
	if err := w.passkeys.FinishReauth(ctx, uid, loginCID, loginResponse); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("FinishReauth with a login ceremony = %v, want ErrNotFound", err)
	}
	reauthCID, reauthResponse := beginReauth(t, w.passkeys, uid, uid, k, 1)
	if _, err := w.passkeys.FinishLogin(ctx, reauthCID, reauthResponse); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("FinishLogin with a re-authentication ceremony = %v, want ErrNotFound", err)
	}
}

func TestReauthCeremonyIsSingleUse(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "reauth-once@example.test")
	k := w.seedLoginKey(t, uid)
	cid, response := beginReauth(t, w.passkeys, uid, uid, k, 1)
	if err := w.passkeys.FinishReauth(ctx, uid, cid, response); err != nil {
		t.Fatal(err)
	}
	if err := w.passkeys.FinishReauth(ctx, uid, cid, response); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("second FinishReauth = %v, want ErrNotFound", err)
	}
}

// The checks a verified assertion still has to pass at sign-in apply here
// too: a disabled account, a counter that did not advance (a cloned
// authenticator), a malformed body.
func TestReauthRefusesWhatSignInRefuses(t *testing.T) {
	t.Run("disabled account", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "reauth-disabled@example.test")
		k := w.seedLoginKey(t, uid)
		cid, response := beginReauth(t, w.passkeys, uid, uid, k, 1)
		if _, err := w.d.Pool().Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id = $1`, uid); err != nil {
			t.Fatal(err)
		}
		if err := w.passkeys.FinishReauth(ctx, uid, cid, response); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("FinishReauth(disabled) = %v, want ErrInvalidCredentials", err)
		}
		if n := w.signCount(t, k); n != 0 {
			t.Fatalf("sign_count = %d, want 0", n)
		}
	})
	t.Run("counter did not advance", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "reauth-clone@example.test")
		k := w.seedLoginKey(t, uid)
		if _, err := w.d.Pool().Exec(ctx, `UPDATE passkeys SET sign_count = 5 WHERE credential_id = $1`, k.credID); err != nil {
			t.Fatal(err)
		}
		cid, response := beginReauth(t, w.passkeys, uid, uid, k, 5)
		if err := w.passkeys.FinishReauth(ctx, uid, cid, response); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("FinishReauth(clone signal) = %v, want ErrInvalidCredentials", err)
		}
		if n := w.signCount(t, k); n != 5 {
			t.Fatalf("sign_count = %d, want 5 (unchanged)", n)
		}
	})
	t.Run("malformed response", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "reauth-malformed@example.test")
		w.seedLoginKey(t, uid)
		_, cid, err := w.passkeys.BeginReauth(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.passkeys.FinishReauth(ctx, uid, cid, json.RawMessage(`{}`)); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("FinishReauth(malformed) = %v, want ErrInvalidCredentials", err)
		}
	})
	t.Run("passkey removed after the ceremony began", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "reauth-removed@example.test")
		k := w.seedLoginKey(t, uid)
		cid, response := beginReauth(t, w.passkeys, uid, uid, k, 1)
		w.reset(t, uid)
		if err := w.passkeys.FinishReauth(ctx, uid, cid, response); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("FinishReauth after a reset = %v, want ErrInvalidCredentials", err)
		}
	})
}

// The passkey step-up (Enrolment.CompleteVerifiedSession after
// FinishReauth) refreshes the current session in place: same id, new
// proof time, no second row.
func TestPasskeyStepUpRefreshesTheCurrentSession(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "reauth-stepup@example.test")
	k := w.seedLoginKey(t, uid)
	sid, cookie := w.openSession(t, uid)
	before := w.sessionCount(t, uid)

	cid, response := beginReauth(t, w.passkeys, uid, uid, k, 1)
	if err := w.passkeys.FinishReauth(ctx, uid, cid, response); err != nil {
		t.Fatal(err)
	}
	newCookie, err := w.enrol.CompleteVerifiedSession(ctx, uid, sid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.sessions.Lookup(ctx, cookie); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("the step-up's old cookie still resolves: %v", err)
	}
	p, err := w.sessions.Lookup(ctx, newCookie)
	if err != nil {
		t.Fatal(err)
	}
	if p.SessionID != sid || !p.FactorFresh(time.Now()) {
		t.Fatalf("session after step-up = %+v, want %s with a fresh factor", p, sid)
	}
	if after := w.sessionCount(t, uid); after != before {
		t.Fatalf("session rows %d -> %d, want the same session", before, after)
	}
}
