package notify

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	got, err := normalizeEmail(&EmailConfig{Recipients: []string{" ops@example.test ", "dba@example.test", "OPS@example.test"}})
	if err != nil || len(got.Recipients) != 2 || got.Recipients[0] != "ops@example.test" {
		t.Fatalf("normalizeEmail = %+v, %v", got, err)
	}
	bad := []*EmailConfig{
		nil,
		{},
		{Recipients: []string{"not an address"}},
		{Recipients: []string{"Ops <ops@example.test>"}},
		{Recipients: []string{"ops@example.test\r\nBcc: x@example.test"}},
		{Recipients: strings.Split(strings.Repeat("a@example.test,", 21), ",")[:21]},
	}
	for _, in := range bad {
		if _, err := normalizeEmail(in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("normalizeEmail(%+v) = %v, want ErrInvalidInput", in, err)
		}
	}
}

func TestCheckURL(t *testing.T) {
	cases := []struct {
		raw, display string
	}{
		{"https://hooks.slack.com/services/T0/B0/secret", "https://hooks.slack.com/…"},
		{"http://ops.internal:8080", "http://ops.internal:8080"},
		{"https://ops.example.test/", "https://ops.example.test"},
		{"https://ops.example.test/?token=x", "https://ops.example.test/…"},
		{"https://[2001:db8::1]:8443/hook", "https://[2001:db8::1]:8443/…"},
	}
	for _, c := range cases {
		got, err := checkURL(c.raw)
		if err != nil || got != c.display {
			t.Errorf("checkURL(%q) = %q, %v; want %q", c.raw, got, err, c.display)
		}
	}
	for _, bad := range []string{"", "ftp://x.test/", "https://", "https://user:pw@x.test/", "https://x.test/#frag", "://x", "https://x.test/" + strings.Repeat("a", 2048)} {
		if _, err := checkURL(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("checkURL(%q) = %v, want ErrInvalidInput", bad, err)
		}
	}
}

func TestNormalizeMethod(t *testing.T) {
	for in, want := range map[string]string{"": "POST", "post": "POST", "PUT": "PUT"} {
		if got, err := normalizeMethod(in); err != nil || got != want {
			t.Errorf("normalizeMethod(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := normalizeMethod("GET"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("GET = %v", err)
	}
}

func TestCheckHeader(t *testing.T) {
	v := "Bearer abc"
	if err := checkHeader("Authorization", &v); err != nil {
		t.Fatal(err)
	}
	if err := checkHeader("X-Team", nil); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", maxHeaderValue+1)
	crlf := "a\r\nX-Evil: 1"
	cases := []struct {
		name  string
		value *string
	}{
		{"Bad Name", nil}, {"Host", nil}, {"content-length", nil}, {"X-Pulsix-Signature-256", nil},
		{"X-Long", &long}, {"X-Split", &crlf},
	}
	for _, c := range cases {
		if err := checkHeader(c.name, c.value); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("checkHeader(%q) = %v, want ErrInvalidInput", c.name, err)
		}
	}
}

func TestNormalizeNameAndSecret(t *testing.T) {
	if n, err := normalizeName("  ops mail "); err != nil || n != "ops mail" {
		t.Fatalf("normalizeName = %q, %v", n, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 101), "a\tb"} {
		if _, err := normalizeName(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("normalizeName(%q) = %v", bad, err)
		}
	}
	if err := checkSigningSecret("short"); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("a short secret was accepted")
	}
	if err := checkSigningSecret(strings.Repeat("s", 32)); err != nil {
		t.Fatal(err)
	}
}
