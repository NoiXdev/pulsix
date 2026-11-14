package containertest

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestUnusable(t *testing.T) {
	tests := []struct {
		name   string
		osType string
		err    error
		want   string // substring of the reason; "" means usable
	}{
		{name: "a Linux daemon", osType: "linux"},
		{name: "no provider", err: errors.New("rootless Docker not found"), want: "rootless Docker not found"},
		{name: "a Windows-containers daemon", osType: "windows", want: `runs "windows" containers`},
		{name: "a daemon that reports no OS", osType: "", want: `runs "" containers`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := unusable(tc.osType, tc.err)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("unusable = %q, want usable", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("unusable = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// fakeTB records what gate does instead of stopping the test.
type fakeTB struct {
	skipped, failed string
}

func (*fakeTB) Helper() {}

func (f *fakeTB) Skipf(format string, args ...any) { f.skipped = fmt.Sprintf(format, args...) }

func (f *fakeTB) Fatalf(format string, args ...any) { f.failed = fmt.Sprintf(format, args...) }

func TestGate(t *testing.T) {
	tests := []struct {
		name              string
		required          bool
		reason            string
		wantSkip, wantErr bool
	}{
		{name: "usable, not required", reason: ""},
		{name: "usable, required", required: true, reason: ""},
		{name: "unusable, not required: skip", reason: "no Docker", wantSkip: true},
		{name: "unusable, required: fail", required: true, reason: "no Docker", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeTB{}
			gate(f, tc.required, tc.reason)
			if (f.skipped != "") != tc.wantSkip {
				t.Errorf("skipped = %q, want skip %v", f.skipped, tc.wantSkip)
			}
			if (f.failed != "") != tc.wantErr {
				t.Errorf("failed = %q, want failure %v", f.failed, tc.wantErr)
			}
			for _, msg := range []string{f.skipped, f.failed} {
				if msg != "" && (!strings.Contains(msg, tc.reason) || !strings.Contains(msg, RequiredEnv)) {
					t.Errorf("message %q should name the reason %q and %s", msg, tc.reason, RequiredEnv)
				}
			}
		})
	}
}

func TestRequired(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "1": true, "true": false} {
		t.Setenv(RequiredEnv, value)
		if got := required(); got != want {
			t.Errorf("%s=%q: required = %v, want %v", RequiredEnv, value, got, want)
		}
	}
}

// TestRequireLinuxDocker runs the real probe: it passes where Docker runs
// Linux containers, skips elsewhere, and fails where RequiredEnv is set but
// no such daemon answers. Calling it twice exercises the cached verdict.
func TestRequireLinuxDocker(t *testing.T) {
	RequireLinuxDocker(t)
	RequireLinuxDocker(t)
}
