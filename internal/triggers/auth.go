package triggers

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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
	spec := presets[p]
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
	spec, ok := presets[t.Preset]
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
