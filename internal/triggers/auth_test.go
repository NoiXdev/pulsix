package triggers

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func headerOf(h http.Header) func(string) string { return h.Get }

func TestVerifyPresets(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	body := []byte(`{"ref":"main"}`)
	secret := "s3cret-webhook"
	for _, p := range []Preset{PresetPulsix, PresetGitHub, PresetGitea, PresetGitLab, PresetBearer} {
		t.Run(string(p), func(t *testing.T) {
			tr := Trigger{Preset: p}
			good := http.Header{}
			good.Set(tr.SignatureHeader(), Sign(p, secret, "", body))
			if !verify(tr, secret, headerOf(good), body, now) {
				t.Fatalf("a correctly signed %s request was refused (%s: %q)", p, tr.SignatureHeader(), good.Get(tr.SignatureHeader()))
			}
			bad := http.Header{}
			bad.Set(tr.SignatureHeader(), Sign(p, "other-secret", "", body))
			if verify(tr, secret, headerOf(bad), body, now) {
				t.Fatal("a wrong secret was accepted")
			}
			if verify(tr, secret, headerOf(http.Header{}), body, now) {
				t.Fatal("a missing header was accepted")
			}
			if tr.Auth() == AuthHMAC && verify(tr, secret, headerOf(good), []byte(`{"ref":"evil"}`), now) {
				t.Fatal("a tampered body was accepted")
			}
		})
	}
}

func TestVerifyTimestampWindow(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	body := []byte(`{}`)
	tr := Trigger{Preset: PresetPulsix, TimestampHeader: "X-Pulsix-Timestamp"}
	sign := func(at time.Time) http.Header {
		ts := strconv.FormatInt(at.Unix(), 10)
		h := http.Header{}
		h.Set("X-Pulsix-Timestamp", ts)
		h.Set(tr.SignatureHeader(), Sign(PresetPulsix, "k", ts, body))
		return h
	}
	if !verify(tr, "k", headerOf(sign(now.Add(-4*time.Minute))), body, now) {
		t.Fatal("inside the window must pass")
	}
	for _, at := range []time.Time{now.Add(-6 * time.Minute), now.Add(6 * time.Minute)} {
		if verify(tr, "k", headerOf(sign(at)), body, now) {
			t.Fatalf("timestamp %v outside ±5 min passed", at)
		}
	}
	// The timestamp is covered by the signature: a fresh timestamp on an
	// old signature fails.
	h := sign(now.Add(-10 * time.Minute))
	h.Set("X-Pulsix-Timestamp", strconv.FormatInt(now.Unix(), 10))
	if verify(tr, "k", headerOf(h), body, now) {
		t.Fatal("a replayed signature with a fresh timestamp passed")
	}
	h = sign(now)
	h.Set("X-Pulsix-Timestamp", "yesterday")
	if verify(tr, "k", headerOf(h), body, now) {
		t.Fatal("a malformed timestamp passed")
	}
}
