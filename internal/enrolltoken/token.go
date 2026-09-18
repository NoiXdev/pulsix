// Package enrolltoken is the worker enrollment token shared by the manager
// (which prints it) and the worker (which parses it): pxw1.<id>.<secret>.
// <ca-sha256>, every part unpadded base64url. The id names the enrollment
// row, the 32-byte secret proves possession (the manager stores only its
// SHA-256), and the CA fingerprint lets the worker pin the manager's CA
// before its first connection (spec 2026-09-18 §4).
package enrolltoken

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/google/uuid"
)

const prefix = "pxw1"

// ErrMalformed is returned for anything that is not a well-formed token.
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
