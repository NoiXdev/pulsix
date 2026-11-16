package probetest_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// failCaseEnv names the assertion TestAssertionHelperProcess runs. The
// assertions fail their *testing.T, so each failing case runs in a child
// test process and TestAssertionsFail checks that the child failed, and why.
const failCaseEnv = "PROBETEST_FAIL_CASE"

func value(v float64) *float64 { return &v }

func label(s string) *string { return &s }

// failCases are calls that must fail the test, each with a fragment of the
// message it must fail with.
var failCases = map[string]struct {
	run  func(t *testing.T)
	want string
}{
	"duplicate metric": {func(t *testing.T) {
		probetest.ByMetric(t, []probe.Observation{probe.Value("x.up", 1), probe.Value("x.up", 0)})
	}, `metric "x.up" reported twice`},
	"value and error": {func(t *testing.T) {
		probetest.ByMetric(t, []probe.Observation{{Metric: "x.up", Value: value(1), Err: "boom"}})
	}, "must carry exactly one of a value, a label and an error"},
	"value and label": {func(t *testing.T) {
		probetest.ByMetric(t, []probe.Observation{{Metric: "x.up", Value: value(1), Label: label("a")}})
	}, "must carry exactly one of a value, a label and an error"},
	"neither value nor error": {func(t *testing.T) {
		probetest.ByMetric(t, []probe.Observation{{Metric: "x.up"}})
	}, "must carry exactly one of a value, a label and an error"},
	"label missing": {func(t *testing.T) {
		probetest.WantLabel(t, map[string]probe.Observation{}, "x.failure")
	}, `metric "x.failure" missing`},
	"label is a value": {func(t *testing.T) {
		probetest.WantLabel(t, probetest.ByMetric(t, []probe.Observation{probe.Value("x.failure", 1)}), "x.failure")
	}, `metric "x.failure" has value`},
	"down: up": {func(t *testing.T) {
		probetest.WantDown(t, []probe.Observation{probe.Value("x.up", 1), probe.Text("x.failure", "timeout")}, "x", "timeout")
	}, "x.up = 1, want 0"},
	"down: other reason": {func(t *testing.T) {
		probetest.WantDown(t, probe.Down("x", probe.FailureRefused), "x", "timeout")
	}, `x.failure = "refused", want "timeout"`},
	"down: more": {func(t *testing.T) {
		probetest.WantDown(t, append(probe.Down("x", "timeout"), probe.Value("x.ms", 1)), "x", "timeout")
	}, "want only x.up=0 and x.failure=timeout"},
	"up: down": {func(t *testing.T) {
		probetest.WantUp(t, probe.Down("x", probe.FailureRefused), "x")
	}, "x.up = 0, want 1"},
	"up: failure": {func(t *testing.T) {
		probetest.WantUp(t, []probe.Observation{probe.Value("x.up", 1), probe.Text("x.failure", "timeout")}, "x")
	}, `x.failure = "timeout", want none`},
	"value missing": {func(t *testing.T) {
		probetest.WantValue(t, map[string]probe.Observation{}, "x.up")
	}, `metric "x.up" missing`},
	"value is an error": {func(t *testing.T) {
		probetest.WantValue(t, map[string]probe.Observation{"x.up": {Metric: "x.up", Err: "boom"}}, "x.up")
	}, `has error "boom", want a value`},
	"only: more than one": {func(t *testing.T) {
		probetest.WantOnly(t, []probe.Observation{probe.Value("x.up", 0), probe.Value("x.time_ms", 1)}, "x.up", 0)
	}, "want only x.up=0"},
	"only: other value": {func(t *testing.T) {
		probetest.WantOnly(t, []probe.Observation{probe.Value("x.up", 0)}, "x.up", 1)
	}, "x.up = 0, want 1"},
	"errors: other count": {func(t *testing.T) {
		probetest.WantErrors(t, probe.FailAll("boom", "x.up"), "boom", "x.up", "x.time_ms")
	}, "want errors for [x.up x.time_ms]"},
	"errors: other metric": {func(t *testing.T) {
		probetest.WantErrors(t, probe.FailAll("boom", "x.code"), "boom", "x.up")
	}, `metric "x.up" missing`},
	"errors: other message": {func(t *testing.T) {
		probetest.WantErrors(t, probe.FailAll("bang", "x.up"), "boom", "x.up")
	}, `err "bang", value <nil>; want err "boom"`},
}

// TestAssertionHelperProcess is the child of TestAssertionsFail; run on its
// own it does nothing.
func TestAssertionHelperProcess(t *testing.T) {
	name := os.Getenv(failCaseEnv)
	if name == "" {
		return
	}
	c, ok := failCases[name]
	if !ok {
		t.Fatalf("no fail case %q", name)
	}
	c.run(t)
	t.Log("assertion passed")
}

// TestAssertionsFail: every assertion fails the test when what it checks is
// not so, which is what makes the passing tests of the probe packages mean
// something.
func TestAssertionsFail(t *testing.T) {
	for name, c := range failCases {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestAssertionHelperProcess$", "-test.count=1", "-test.v")
			cmd.Env = append(os.Environ(), failCaseEnv+"="+name)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("the assertion did not fail the test:\n%s", out)
			}
			if !strings.Contains(string(out), "--- FAIL: TestAssertionHelperProcess") || !strings.Contains(string(out), c.want) {
				t.Fatalf("the child did not fail with %q:\n%s", c.want, out)
			}
			if strings.Contains(string(out), "assertion passed") {
				t.Fatalf("the assertion failed but let the test go on:\n%s", out)
			}
		})
	}
}
