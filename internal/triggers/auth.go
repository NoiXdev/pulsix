package triggers

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// replayWindow bounds the optional timestamp header (spec §3).
const replayWindow = 5 * time.Minute

// mac is HMAC-SHA256 over the raw body, or over "<timestamp>.<body>" when
// the trigger has a timestamp header, so the timestamp cannot be swapped.
func mac(secret, timestamp string, body []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	if timestamp != "" {
		m.Write([]byte(timestamp))
		m.Write([]byte{'.'})
	}
	m.Write(body)
	return m.Sum(nil)
}

// Sign returns the header value a sender presents for preset p: the
// prefixed hex HMAC, or the prefixed token itself.
func Sign(p Preset, secret, timestamp string, body []byte) string {
	spec, _ := preset(p)
	if spec.auth == AuthToken {
		return spec.prefix + secret
	}
	return spec.prefix + hex.EncodeToString(mac(secret, timestamp, body))
}

// verify checks the credential (and the timestamp window) in constant time.
func verify(t Trigger, secret string, header func(string) string, body []byte, now time.Time) bool {
	ts := ""
	if t.TimestampHeader != "" {
		ts = strings.TrimSpace(header(t.TimestampHeader))
		sec, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return false
		}
		if d := now.Sub(time.Unix(sec, 0)); d > replayWindow || d < -replayWindow {
			return false
		}
	}
	spec, ok := preset(t.Preset)
	if !ok {
		return false
	}
	presented := strings.TrimSpace(header(spec.header))
	if spec.prefix != "" {
		if len(presented) < len(spec.prefix) || !strings.EqualFold(presented[:len(spec.prefix)], spec.prefix) {
			return false
		}
		presented = presented[len(spec.prefix):]
	}
	switch spec.auth {
	case AuthHMAC:
		got, err := hex.DecodeString(presented)
		if err != nil {
			return false
		}
		return hmac.Equal(got, mac(secret, ts, body))
	case AuthToken:
		return subtle.ConstantTimeCompare([]byte(presented), []byte(secret)) == 1
	}
	return false
}

// decoySecret is a fixed, meaningless key for equaliseSignatureWork. It
// authenticates nothing; its only job is to make an HMAC cost what an
// HMAC costs.
const decoySecret = "pulsix-timing-equaliser"

// equaliseSignatureWork does the work a real trigger's signature check
// would do (backlog 2.62). The digest is returned only so a test can
// prove the hashing happened; every caller discards it.
//
// receive returns the uniform 404 for an unknown trigger id WITHOUT
// computing an HMAC, while a known id's rejection computes one over the
// whole request body — so the two answers differed in a way a caller
// could measure, and the difference GROWS with the body the caller
// chooses. That is the only term worth equalising: the rest of the
// known path (an AEAD open, a preset lookup, header parsing) is
// constant-size work far below network noise, and chasing exact
// equality between two different code paths is a game with no end.
//
// What this is worth, stated plainly: trigger ids are UUIDv7, so even a
// perfect oracle answers "this id I guessed does not exist" against a
// space no one can enumerate. This removes the class rather than
// closing a live hole.
func equaliseSignatureWork(body []byte) []byte {
	sum := mac(decoySecret, "", body)
	runtime.KeepAlive(sum)
	return sum
}
