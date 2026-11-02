package supervisor

import (
	"reflect"
	"syscall"
	"testing"
	"time"
)

func TestCommandRoundTrips(t *testing.T) {
	bin := "/usr/local/bin/ansible-runner"
	args := []string{"run", "/w", "--json"}

	cmd := Command(5*time.Second, bin, args)
	if cmd[0] != Arg {
		t.Fatalf("Command()[0] = %q, want the reserved argument %q", cmd[0], Arg)
	}

	grace, argv, err := parseArgs(cmd[1:])
	if err != nil {
		t.Fatalf("parseArgs(Command(...)): %v", err)
	}
	if grace != 5*time.Second {
		t.Errorf("grace = %v, want 5s", grace)
	}
	want := []string{bin, "run", "/w", "--json"}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %q, want %q", argv, want)
	}
}

func TestCommandDoesNotAliasItsArguments(t *testing.T) {
	args := make([]string, 1, 8) // spare capacity: a careless append would write into it
	args[0] = "run"
	_ = Command(time.Second, "/bin/x", args)
	if full := args[:2]; full[1] != "" {
		t.Errorf("Command wrote into the caller's backing array: %q", full)
	}
}

func TestParseArgsRefuses(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no grace", []string{"--", "/bin/x"}},
		{"zero grace", []string{"--grace", "0s", "--", "/bin/x"}},
		{"negative grace", []string{"--grace", "-1s", "--", "/bin/x"}},
		{"unparsable grace", []string{"--grace", "x", "--", "/bin/x"}},
		{"grace without a value", []string{"--grace"}},
		{"missing separator", []string{"--grace", "5s", "/bin/x"}},
		{"nothing after the separator", []string{"--grace", "5s", "--"}},
		{"relative program", []string{"--grace", "5s", "--", "ansible-runner", "run"}},
		{"unknown flag", []string{"--foo", "--grace", "5s", "--", "/bin/x"}},
		{"no arguments at all", nil},
		// Backlog 2.114: like every other deviation, not "the last wins".
		{"repeated grace", []string{"--grace", "5s", "--grace", "1s", "--", "/bin/x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			grace, argv, err := parseArgs(tc.args)
			if err == nil {
				t.Fatalf("parseArgs(%q) = (%v, %q, nil), want an error", tc.args, grace, argv)
			}
		})
	}
}

func TestExitFor(t *testing.T) {
	tests := []struct {
		name     string
		ws       syscall.WaitStatus
		wantCode int
		wantSig  syscall.Signal
	}{
		{"exit 0", syscall.WaitStatus(0 << 8), 0, 0},
		{"exit 2 (failed)", syscall.WaitStatus(2 << 8), 2, 0},
		{"exit 4 (unreachable)", syscall.WaitStatus(4 << 8), 4, 0},
		{"killed", syscall.WaitStatus(syscall.SIGKILL), 0, syscall.SIGKILL},
		{"terminated", syscall.WaitStatus(syscall.SIGTERM), 0, syscall.SIGTERM},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, sig := exitFor(tc.ws)
			if code != tc.wantCode || sig != tc.wantSig {
				t.Errorf("exitFor(%#x) = (%d, %v), want (%d, %v)", uint32(tc.ws), code, sig, tc.wantCode, tc.wantSig)
			}
		})
	}
}
