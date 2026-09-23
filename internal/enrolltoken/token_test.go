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
		"agent prefix":  "pxa1." + strings.Join(parts[1:], "."),
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

// TestAgentTokens pins the pxa1 half: a round trip through AgentString
// and ParseAgent, the pxa1 shape itself, and the cross-prefix rejection —
// a worker token is malformed in the agent parser and vice versa.
func TestAgentTokens(t *testing.T) {
	tok := enrolltoken.Token{ID: uuid.Must(uuid.NewV7()), Secret: bytes.Repeat([]byte{7}, 32), CAPin: sha256.Sum256([]byte("ca"))}
	s := enrolltoken.AgentString(tok)
	if !strings.HasPrefix(s, "pxa1.") || strings.Count(s, ".") != 3 || strings.ContainsAny(s, "=+/") {
		t.Fatalf("agent token = %q", s)
	}
	got, err := enrolltoken.ParseAgent("  " + s + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != tok.ID || !bytes.Equal(got.Secret, tok.Secret) || got.CAPin != tok.CAPin {
		t.Fatalf("parsed = %+v", got)
	}

	// Cross-prefix: each kind's parser refuses the other's tokens — a
	// worker token parsed as agent fails, and vice versa. The parts are
	// otherwise valid, so ONLY the prefix can be the reason.
	worker := enrolltoken.Token{ID: uuid.Must(uuid.NewV7()), Secret: bytes.Repeat([]byte{3}, 32), CAPin: sha256.Sum256([]byte("ca"))}
	if _, err := enrolltoken.ParseAgent(worker.String()); !errors.Is(err, enrolltoken.ErrMalformed) {
		t.Errorf("worker token accepted by ParseAgent: %v", err)
	}
	if _, err := enrolltoken.Parse(s); !errors.Is(err, enrolltoken.ErrMalformed) {
		t.Fatalf("agent token accepted by Parse: %v", err)
	}

	// The same malformed-shape table worker Parse enforces applies to
	// ParseAgent (the two share the decoder).
	agentParts := strings.Split(s, ".")
	for name, in := range map[string]string{
		"empty":        "",
		"wrong prefix": "pxa2." + strings.Join(agentParts[1:], "."),
		"worker token": worker.String(),
		"short secret": agentParts[0] + "." + agentParts[1] + ".AAAA." + agentParts[3],
	} {
		if _, err := enrolltoken.ParseAgent(in); !errors.Is(err, enrolltoken.ErrMalformed) {
			t.Errorf("%s: ParseAgent(%q) = %v", name, in, err)
		}
	}
}
