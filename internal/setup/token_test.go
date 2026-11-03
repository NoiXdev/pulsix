package setup_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/setup"
)

// testToken is the setup token of the all-zero master key every service in
// these tests is built with.
var testToken = setup.Token([32]byte{})

var tokenShape = regexp.MustCompile(`^[A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4}$`)

func TestTokenIsStablePerKeyAndDiffersAcrossKeys(t *testing.T) {
	a := [32]byte{'a'}
	b := [32]byte{'b'}
	first, again := setup.Token(a), setup.Token(a)
	if first != again {
		t.Fatal("the token of one key changed between calls")
	}
	if setup.Token(a) == setup.Token(b) {
		t.Fatal("two master keys derived the same setup token")
	}
	for _, tok := range []string{setup.Token(a), setup.Token(b), testToken} {
		if !tokenShape.MatchString(tok) {
			t.Errorf("token %q is not XXXX-XXXX-XXXX-XXXX in base32", tok)
		}
	}
}

// TestTokenKnownValue pins the derivation: changing it would make every
// token an operator already noted down (or a script already computes, like
// web/e2e/env.ts) stop matching.
func TestTokenKnownValue(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = 0xab
	}
	if got, want := setup.Token(key), "H2MA-EG3P-ZM6H-4K44"; got != want {
		t.Fatalf("Token(ab x32) = %q, want %q", got, want)
	}
}

func TestLink(t *testing.T) {
	for _, tc := range []struct{ publicURL, want string }{
		{"https://pulsix.example.com", "https://pulsix.example.com/setup?token=ABCD-EFGH-IJKL-MNOP"},
		{"https://pulsix.example.com/", "https://pulsix.example.com/setup?token=ABCD-EFGH-IJKL-MNOP"},
		{"http://localhost:8080/pulsix", "http://localhost:8080/pulsix/setup?token=ABCD-EFGH-IJKL-MNOP"},
	} {
		if got := setup.Link(tc.publicURL, "ABCD-EFGH-IJKL-MNOP"); got != tc.want {
			t.Errorf("Link(%q) = %q, want %q", tc.publicURL, got, tc.want)
		}
	}
}

func TestCompleteRefusesAMissingOrWrongToken(t *testing.T) {
	wrong := setup.Token([32]byte{'x'})
	for name, tok := range map[string]string{
		"missing":       "",
		"wrong":         wrong,
		"truncated":     testToken[:len(testToken)-1],
		"only dashes":   "----",
		"right + extra": testToken + "A",
	} {
		t.Run(name, func(t *testing.T) {
			st := newFakeSetupStore()
			svc := setup.NewService(st, &fakeSessions{}, crypto.NewBox([32]byte{}), time.Now, testToken)
			hashed := 0
			svc.SetPasswordHasher(func(string) (string, error) { hashed++; return "$argon2id$fake", nil })
			_, err := svc.Complete(ctx, setup.Input{SetupToken: tok, AdminEmail: "root@x.test", AdminPassword: "correct horse battery", AdminName: "Root", OrgSlug: "acme", OrgName: "Acme"})
			if !errors.Is(err, setup.ErrSetupToken) {
				t.Fatalf("Complete with token %q: %v, want ErrSetupToken", tok, err)
			}
			if hashed != 0 || len(st.users) != 0 {
				t.Fatalf("a refused token still hashed (%d) or created users (%d)", hashed, len(st.users))
			}
		})
	}
}

func TestCompleteAcceptsTheTokenLeniently(t *testing.T) {
	plain := strings.ReplaceAll(testToken, "-", "")
	for name, tok := range map[string]string{
		"as printed":   testToken,
		"lowercase":    strings.ToLower(testToken),
		"no dashes":    plain,
		"spaces":       " " + plain[:4] + " " + plain[4:8] + " " + plain[8:] + " ",
		"mixed groups": strings.ToLower(plain[:8]) + "-" + plain[8:],
	} {
		t.Run(name, func(t *testing.T) {
			svc := setup.NewService(newFakeSetupStore(), &fakeSessions{}, crypto.NewBox([32]byte{}), time.Now, testToken)
			if _, err := svc.Complete(ctx, setup.Input{SetupToken: tok, AdminEmail: "root@x.test", AdminPassword: "correct horse battery", AdminName: "Root", OrgSlug: "acme", OrgName: "Acme"}); err != nil {
				t.Fatalf("Complete with token %q: %v", tok, err)
			}
		})
	}
}

// A service built without an expected token (the zero value) refuses every
// token, the empty one included: it fails closed rather than open.
func TestCompleteWithoutAnExpectedTokenRefusesAll(t *testing.T) {
	svc := setup.NewService(newFakeSetupStore(), &fakeSessions{}, crypto.NewBox([32]byte{}), time.Now, "")
	for _, tok := range []string{"", "-", testToken} {
		if _, err := svc.Complete(ctx, setup.Input{SetupToken: tok, AdminEmail: "root@x.test", AdminPassword: "correct horse battery", AdminName: "Root", OrgSlug: "acme", OrgName: "Acme"}); !errors.Is(err, setup.ErrSetupToken) {
			t.Fatalf("Complete with token %q and none expected: %v, want ErrSetupToken", tok, err)
		}
	}
}

// Once setup is completed the token reveals nothing: a wrong or missing
// token answers exactly what the right one does, ErrAlreadyCompleted.
func TestCompleteOnACompletedInstanceIgnoresTheToken(t *testing.T) {
	svc := setup.NewService(newFakeSetupStore(), &fakeSessions{}, crypto.NewBox([32]byte{}), time.Now, testToken)
	in := setup.Input{SetupToken: testToken, AdminEmail: "root@x.test", AdminPassword: "correct horse battery", AdminName: "Root", OrgSlug: "acme", OrgName: "Acme"}
	if _, err := svc.Complete(ctx, in); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{testToken, "", "WRON-GTOK-ENXX-XXXX"} {
		in.SetupToken = tok
		if _, err := svc.Complete(ctx, in); !errors.Is(err, setup.ErrAlreadyCompleted) {
			t.Fatalf("second Complete with token %q: %v, want ErrAlreadyCompleted", tok, err)
		}
	}
}
