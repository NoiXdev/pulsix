package setup

import (
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// tokenInfo labels the HKDF derivation of the setup token. Changing it
// changes every instance's token.
const tokenInfo = "pulsix setup token v1"

// tokenBytes is the setup token's entropy: 80 bits, which base32 spells in
// exactly 16 characters with no padding.
const tokenBytes = 10

// Token returns the setup token of the instance whose master key is
// masterKey: HKDF-SHA256(masterKey, no salt, "pulsix setup token v1",
// 10 bytes), in uppercase RFC 4648 base32 without padding, grouped as
// XXXX-XXXX-XXXX-XXXX. It is derived rather than stored, so every manager
// of the instance, before and after a restart, shows the same token and no
// database state is needed. It is the same shape of derivation as
// backup.DeriveKey and backup.HeaderKey, under its own label.
func Token(masterKey [32]byte) string {
	okm, err := hkdf.Key(sha256.New, masterKey[:], nil, tokenInfo, tokenBytes)
	if err != nil {
		// Only a malformed length or hash can fail, and both are constants.
		panic(fmt.Sprintf("derive setup token: %v", err))
	}
	s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(okm)
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

// Link is the wizard's address with token filled in: the SPA's /setup
// page pre-fills its token field from the query.
func Link(publicURL, token string) string {
	return strings.TrimRight(publicURL, "/") + "/setup?" + url.Values{"token": {token}}.Encode()
}

// normalizeToken drops what a person may add or lose copying a token —
// spaces and dashes — and folds the case, base32 being case-insensitive.
func normalizeToken(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToUpper(r)
	}, s)
}

// tokenMatches compares got against want in constant time once both are
// normalized. An empty want never matches: a service built without a
// token fails closed.
func tokenMatches(want, got string) bool {
	w, g := normalizeToken(want), normalizeToken(got)
	if w == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(w), []byte(g)) == 1
}
