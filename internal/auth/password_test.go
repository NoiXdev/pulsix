package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/auth"
)

func TestPasswordHashVerify(t *testing.T) {
	h, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("unexpected format %s", h)
	}
	ok, err := auth.VerifyPassword(h, "correct horse")
	if err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	ok, _ = auth.VerifyPassword(h, "wrong")
	if ok {
		t.Fatal("wrong password accepted")
	}
}

// TestHashPasswordProducesUniqueSalts asserts that two hashes of the same
// password differ, which only holds if HashPassword draws a fresh random
// salt on every call.
func TestHashPasswordProducesUniqueSalts(t *testing.T) {
	first, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	second, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two hashes of the same password are identical; salt is not fresh")
	}
}

// TestVerifyPasswordMalformedHash exercises every malformed-hash shape
// VerifyPassword must reject without panicking, per the task brief's
// requirement that it "must not panic on a malformed hash string". hash
// comes straight from a database row in production, so this table includes
// shapes that parse structurally but carry parameter values that make
// golang.org/x/crypto/argon2 panic (a zero-length key, t=0, p=0) or that a
// hostile/corrupted row could use to force a huge allocation (out-of-range
// memory), plus a hash claiming an argon2 version this code does not
// implement.
//
// Each case runs in its own subtest with a recover so that a regression
// that reintroduces a panic is reported as that subtest failing, rather
// than crashing the whole test binary and hiding the remaining cases.
func TestVerifyPasswordMalformedHash(t *testing.T) {
	valid, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	// A real salt/hash pair, reused by cases below that only need to
	// mutate the params or version segment.
	validParts := strings.Split(valid, "$")
	salt, key := validParts[4], validParts[5]

	cases := map[string]string{
		"empty string":        "",
		"too few $ segments":  "$argon2id$v=19$m=65536,t=3,p=2$salt",
		"too many $ segments": valid + "$extra",
		"wrong algorithm":     "$argon2i$v=19$m=65536,t=3,p=2$c29tZXNhbHQ$c29tZWhhc2g",
		"non-numeric params":  "$argon2id$v=19$m=x,t=3,p=2$c29tZXNhbHQ$c29tZWhhc2g",
		"corrupt salt base64": "$argon2id$v=19$m=65536,t=3,p=2$not!base64$c29tZWhhc2g",
		"corrupt hash base64": "$argon2id$v=19$m=65536,t=3,p=2$c29tZXNhbHQ$not!base64",
		"empty final segment": "$argon2id$v=19$m=65536,t=3,p=2$" + salt + "$",
		"time zero":           "$argon2id$v=19$m=65536,t=0,p=2$" + salt + "$" + key,
		"parallelism zero":    "$argon2id$v=19$m=65536,t=3,p=0$" + salt + "$" + key,
		"memory out of range": "$argon2id$v=19$m=4294967295,t=3,p=2$" + salt + "$" + key,
		"unsupported version": "$argon2id$v=99$m=65536,t=3,p=2$" + salt + "$" + key,
		"non-numeric version": "$argon2id$v=x$m=65536,t=3,p=2$" + salt + "$" + key,
	}

	for name, hash := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("VerifyPassword panicked for %s: %v", name, r)
				}
			}()
			ok, err := auth.VerifyPassword(hash, "correct horse")
			if err == nil {
				t.Fatalf("expected error for %s, got ok=%v", name, ok)
			}
			if ok {
				t.Fatalf("expected ok=false for %s", name)
			}
		})
	}
}

func TestCheckPasswordPolicy(t *testing.T) {
	for name, tc := range map[string]struct {
		pw      string
		wantErr bool
	}{
		"empty":                  {"", true},
		"eleven characters":      {"elevenchars", true},
		"twelve characters":      {"twelve chars", false},
		"long":                   {"correct horse battery staple", false},
		"eleven multibyte runes": {string([]rune("äääääääääääää")[:11]), true},
		"twelve multibyte runes": {string([]rune("äääääääääääää")[:12]), false},
	} {
		err := auth.CheckPasswordPolicy(tc.pw)
		if tc.wantErr {
			if !errors.Is(err, auth.ErrPasswordTooShort) || !errors.Is(err, auth.ErrInvalidInput) {
				t.Errorf("%s: err = %v, want ErrPasswordTooShort wrapping ErrInvalidInput", name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: err = %v, want nil", name, err)
		}
	}
	if auth.MinPasswordLength != 12 {
		t.Errorf("MinPasswordLength = %d, want 12", auth.MinPasswordLength)
	}
}
