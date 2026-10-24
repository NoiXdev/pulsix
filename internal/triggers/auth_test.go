package triggers

import (
	"net/http"
	"os"
	"strconv"
	"strings"
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

// TestEqualiseSignatureWorkCostsWhatAnHMACCosts is backlog 2.62's
// timing half. receive answers an unknown trigger id with the uniform
// 404 without computing an HMAC, while a known id's rejection computes
// one over the whole request body — so the two differed by an amount
// the caller chooses, by choosing how much body to send.
//
// A wall-clock assertion on the two receive paths would be a flake
// generator, so what is pinned here is the thing that actually has to
// hold: the equaliser does the same body-proportional hashing the real
// check does. A 1 MiB body makes that measurable well above scheduling
// noise, and the bound is deliberately loose — this is a check that
// the work HAPPENS, not a benchmark.
func TestEqualiseSignatureWorkCostsWhatAnHMACCosts(t *testing.T) {
	body := make([]byte, 1<<20)
	real0 := time.Now()
	_ = mac("a-real-secret", "", body)
	realCost := time.Since(real0)

	decoy0 := time.Now()
	equaliseSignatureWork(body)
	decoyCost := time.Since(decoy0)

	if decoyCost < realCost/4 {
		t.Fatalf("the equaliser took %s against the real check's %s; it is not doing the work", decoyCost, realCost)
	}
}

// TestUnknownTriggerAndRejectedSignatureBothHashTheBody pins the
// call sites rather than the clock: every path that answers the uniform
// 404 after a parseable id must run the equaliser, so adding a new one
// that forgets it is a visible change here.
func TestUnknownTriggerAndRejectedSignatureBothHashTheBody(t *testing.T) {
	src, err := os.ReadFile("receive.go")
	if err != nil {
		t.Fatal(err)
	}
	// Every OutcomeNotFound returned from receive() after the id parses
	// — Resolve missing, kind mismatch, and an unopenable secret.
	if got := strings.Count(string(src), "equaliseSignatureWork(req.Body)"); got != 3 {
		t.Fatalf("receive.go calls the equaliser %d times, want 3 (unknown id, wrong kind, unopenable secret); a 404 path without it answers faster than a real rejection", got)
	}
}
