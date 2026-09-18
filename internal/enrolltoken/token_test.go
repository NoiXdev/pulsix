package enrolltoken_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/enrolltoken"
)

func TestRoundTrip(t *testing.T) {
	tok := enrolltoken.Token{ID: uuid.Must(uuid.NewV7()), Secret: bytes.Repeat([]byte{7}, 32), CAPin: sha256.Sum256([]byte("ca"))}
	s := tok.String()
	if !strings.HasPrefix(s, "pxw1.") || strings.Count(s, ".") != 3 || strings.ContainsAny(s, "=+/") {
		t.Fatalf("token = %q", s)
	}
	got, err := enrolltoken.Parse("  " + s + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != tok.ID || !bytes.Equal(got.Secret, tok.Secret) || got.CAPin != tok.CAPin {
		t.Fatalf("parsed = %+v", got)
	}
	if enrolltoken.Pin([]byte("ca")) != tok.CAPin {
		t.Fatal("Pin is SHA-256 of the CA's DER")
	}
}

func TestParseRejects(t *testing.T) {
	good := enrolltoken.Token{ID: uuid.Must(uuid.NewV7()), Secret: bytes.Repeat([]byte{1}, 32), CAPin: sha256.Sum256(nil)}.String()
	parts := strings.Split(good, ".")
	for name, in := range map[string]string{
		"empty":         "",
		"wrong prefix":  "pxw2." + strings.Join(parts[1:], "."),
		"three parts":   strings.Join(parts[:3], "."),
		"five parts":    good + ".x",
		"bad base64":    parts[0] + ".!!!." + parts[2] + "." + parts[3],
		"short id":      parts[0] + ".AAAA." + parts[2] + "." + parts[3],
		"short secret":  parts[0] + "." + parts[1] + ".AAAA." + parts[3],
		"short pin":     parts[0] + "." + parts[1] + "." + parts[2] + ".AAAA",
		"padded base64": parts[0] + "." + parts[1] + "=." + parts[2] + "." + parts[3],
	} {
		if _, err := enrolltoken.Parse(in); !errors.Is(err, enrolltoken.ErrMalformed) {
			t.Errorf("%s: Parse(%q) = %v", name, in, err)
		}
	}
}
