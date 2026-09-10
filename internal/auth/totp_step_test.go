package auth

import (
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// These are the step-arithmetic unit tests Finding 4 asked for: matchStep
// and dedupSteps are pure functions of (code, secret, now) with no store
// dependency, so they can — and should — be tested against a fixed clock
// with no container involved. This file is package auth (not auth_test)
// because both functions are unexported; the replay-window behaviour they
// implement was reviewed and confirmed correct in fix round 1's review and
// is not being changed here, only covered directly instead of only via the
// database-backed replay test.
const testStepSecret = "JBSWY3DPEHPK3PXP"

func fixedStepNow() time.Time { return time.Unix(1_700_000_000, 0) }

func codeForStepOffset(t *testing.T, offsetSteps int64) string {
	t.Helper()
	target := fixedStepNow().Add(time.Duration(offsetSteps) * totpStep)
	code, err := totp.GenerateCodeCustom(testStepSecret, target, otpValidateOpts())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func otpValidateOpts() totp.ValidateOpts {
	return totp.ValidateOpts{Period: uint(totpStep / time.Second), Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}
}

func TestMatchStepAcceptsCurrentStep(t *testing.T) {
	now := fixedStepNow()
	code := codeForStepOffset(t, 0)
	step, ok, err := matchStep(code, testStepSecret, now)
	if err != nil || !ok {
		t.Fatalf("matchStep(current) = (%d, %v, %v), want ok", step, ok, err)
	}
	if want := now.Unix() / int64(totpStep/time.Second); step != want {
		t.Fatalf("matched step = %d, want %d", step, want)
	}
}

func TestMatchStepAcceptsOneStepOfSkewEitherDirection(t *testing.T) {
	now := fixedStepNow()
	current := now.Unix() / int64(totpStep/time.Second)
	for _, offset := range []int64{1, -1} {
		code := codeForStepOffset(t, offset)
		step, ok, err := matchStep(code, testStepSecret, now)
		if err != nil || !ok {
			t.Fatalf("offset %d: matchStep = (%d, %v, %v), want ok", offset, step, ok, err)
		}
		if step != current+offset {
			t.Fatalf("offset %d: matched step = %d, want %d", offset, step, current+offset)
		}
	}
}

func TestMatchStepRejectsBeyondSkewWindow(t *testing.T) {
	now := fixedStepNow()
	for _, offset := range []int64{2, -2, 5} {
		code := codeForStepOffset(t, offset)
		if _, ok, err := matchStep(code, testStepSecret, now); ok || err != nil {
			t.Fatalf("offset %d: matchStep = (ok=%v, err=%v), want (false, nil)", offset, ok, err)
		}
	}
}

func TestMatchStepRejectsWrongCode(t *testing.T) {
	if _, ok, err := matchStep("000000", testStepSecret, fixedStepNow()); ok || err != nil {
		t.Fatalf("matchStep(wrong code) = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestDedupSteps(t *testing.T) {
	if got := dedupSteps(5, 5); len(got) != 1 || got[0] != 5 {
		t.Fatalf("dedupSteps(5, 5) = %v, want [5]", got)
	}
	if got := dedupSteps(6, 4); len(got) != 2 || got[0] != 6 || got[1] != 4 {
		t.Fatalf("dedupSteps(6, 4) = %v, want [6 4]", got)
	}
}
