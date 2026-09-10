package auth_test

import (
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
// requirement that it "must not panic on a malformed hash string".
func TestVerifyPasswordMalformedHash(t *testing.T) {
	valid, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"empty string":        "",
		"too few $ segments":  "$argon2id$v=19$m=65536,t=3,p=2$salt",
		"too many $ segments": valid + "$extra",
		"wrong algorithm":     "$argon2i$v=19$m=65536,t=3,p=2$c29tZXNhbHQ$c29tZWhhc2g",
		"non-numeric params":  "$argon2id$v=19$m=x,t=3,p=2$c29tZXNhbHQ$c29tZWhhc2g",
		"corrupt salt base64": "$argon2id$v=19$m=65536,t=3,p=2$not!base64$c29tZWhhc2g",
		"corrupt hash base64": "$argon2id$v=19$m=65536,t=3,p=2$c29tZXNhbHQ$not!base64",
	}

	for name, hash := range cases {
		t.Run(name, func(t *testing.T) {
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
