package app

import (
	"log/slog"
	"strings"
	"testing"
)

// TestWarnMFAOptional: with PULSIX_MFA_OPTIONAL on, the manager says at
// start that the second factor is optional and that this is not for a
// production instance.
func TestWarnMFAOptional(t *testing.T) {
	var buf strings.Builder
	warnMFAOptional(slog.New(slog.NewTextHandler(&buf, nil)))
	out := buf.String()
	for _, want := range []string{"level=WARN", "second factor is optional (PULSIX_MFA_OPTIONAL)", "do not use this on a production instance"} {
		if !strings.Contains(out, want) {
			t.Fatalf("warning %q misses %q", out, want)
		}
	}
}
