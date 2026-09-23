// Package enrolltoken is the worker and agent enrollment token shared by
// the manager (which prints it) and the client (which parses it):
// <prefix>.<id>.<secret>.<ca-sha256>, every part unpadded base64url. The
// prefix discriminates the two kinds — `pxw1` names a worker token,
// `pxa1` an agent token, and a parser only accepts its own kind — so a
// token pasted into the wrong enroll path is malformed there. The id names
// the enrollment row, the 32-byte secret proves possession (the manager
// stores only its SHA-256), and the CA fingerprint lets the client pin the
// manager's CA before its first connection (spec 2026-09-18 §4, v0.3a §8).
package enrolltoken

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// WorkerPrefix renders worker tokens; AgentPrefix agent tokens.
const (
	WorkerPrefix = "pxw1"
	AgentPrefix  = "pxa1"
)

const prefix = WorkerPrefix

// ErrMalformed is returned for anything that is not a well-formed token
// of the parsed kind — a foreign prefix is malformed exactly like a
// missing part.
var ErrMalformed = errors.New("malformed enrollment token")

// Token is a parsed enrollment token.
type Token struct {
	ID     uuid.UUID
	Secret []byte
	CAPin  [32]byte
}

// Strict: the plain RawURLEncoding decoder accepts non-canonical trailing
// bits, so several distinct strings could otherwise decode to the same
// token. Nothing this package encodes is ever non-canonical, so Strict
// costs nothing and closes that off.
var enc = base64.RawURLEncoding.Strict()

// String renders the token.
func (t Token) String() string {
	return strings.Join([]string{prefix, enc.EncodeToString(t.ID[:]), enc.EncodeToString(t.Secret), enc.EncodeToString(t.CAPin[:])}, ".")
}

// Parse reads a token, tolerating surrounding whitespace.
func Parse(s string) (Token, error) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 4 || parts[0] != prefix {
		return Token{}, ErrMalformed
	}
	id, err := enc.DecodeString(parts[1])
	if err != nil || len(id) != 16 {
		return Token{}, ErrMalformed
	}
	secret, err := enc.DecodeString(parts[2])
	if err != nil || len(secret) != 32 {
		return Token{}, ErrMalformed
	}
	pin, err := enc.DecodeString(parts[3])
	if err != nil || len(pin) != 32 {
		return Token{}, ErrMalformed
	}
	var t Token
	copy(t.ID[:], id)
	t.Secret = secret
	copy(t.CAPin[:], pin)
	return t, nil
}

// Pin is the fingerprint a token carries: SHA-256 of the CA certificate's DER.
func Pin(der []byte) [32]byte { return sha256.Sum256(der) }

// AgentString renders an agent token: the same four-part shape under the
// pxa1 prefix.
func AgentString(t Token) string { return render(AgentPrefix, t) }

// ParseAgent reads an agent token, tolerating surrounding whitespace. Only
// the pxa1 prefix is accepted — a worker token is malformed here.
func ParseAgent(s string) (Token, error) { return parse(AgentPrefix, s) }

// render joins the four parts under prefix; parse is the shared strict
// decoder the per-kind wrappers call with their own prefix.
func render(p string, t Token) string {
	return strings.Join([]string{p, enc.EncodeToString(t.ID[:]), enc.EncodeToString(t.Secret), enc.EncodeToString(t.CAPin[:])}, ".")
}

func parse(p, s string) (Token, error) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 4 || parts[0] != p {
		return Token{}, ErrMalformed
	}
	id, err := enc.DecodeString(parts[1])
	if err != nil || len(id) != 16 {
		return Token{}, ErrMalformed
	}
	secret, err := enc.DecodeString(parts[2])
	if err != nil || len(secret) != 32 {
		return Token{}, ErrMalformed
	}
	pin, err := enc.DecodeString(parts[3])
	if err != nil || len(pin) != 32 {
		return Token{}, ErrMalformed
	}
	var t Token
	copy(t.ID[:], id)
	t.Secret = secret
	copy(t.CAPin[:], pin)
	return t, nil
}
