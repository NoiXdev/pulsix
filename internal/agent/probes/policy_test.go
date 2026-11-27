package probes

import (
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probeconfig"
)

// TestRunnerDisabledRefusesApply: an agent whose agent.yaml turns probing
// off (backlog 3.82) drops its running set, forgets the applied version
// and refuses a set that arrives anyway.
func TestRunnerDisabledRefusesApply(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(false, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	if !r.Enabled() {
		t.Fatal("a new runner is not enabled")
	}
	specs := []probeconfig.Spec{spec(targetA, "web", "tcp", 60)}
	if !r.Apply(specs) {
		t.Fatal("an enabled runner refused a set")
	}

	if !r.SetPolicy(false, nil) {
		t.Fatal("turning probing off is not reported as a change")
	}
	if r.Enabled() || r.Version() != "" || r.hasSlots() {
		t.Fatalf("after turning off: enabled %v, version %q, slots %v", r.Enabled(), r.Version(), r.hasSlots())
	}
	if r.Apply(specs) {
		t.Fatal("a disabled runner accepted a set")
	}
	if r.Version() != "" || r.hasSlots() {
		t.Fatal("a refused set was installed")
	}
	clock.advance(time.Hour)
	s := newSink()
	r.dispatch(t.Context(), s.add)
	r.wg.Wait()
	if n := b.callCount(); n != 0 {
		t.Fatalf("a disabled runner probed (%d calls)", n)
	}
	if r.SetPolicy(false, nil) {
		t.Fatal("the same policy again is reported as a change")
	}
}

// TestRunnerReenableForgetsVersion: turning probing back on starts from
// no applied set, so the manager — which still holds the old assignments
// — sees a version that differs and sends them again.
func TestRunnerReenableForgetsVersion(t *testing.T) {
	t.Parallel()
	r, _, _ := newTestRunner(probe.Registry{})
	specs := []probeconfig.Spec{spec(targetA, "web", "tcp", 60)}
	r.Apply(specs)
	r.SetPolicy(false, nil)
	if !r.SetPolicy(true, nil) {
		t.Fatal("turning probing on is not reported as a change")
	}
	if !r.Enabled() || r.Version() != "" {
		t.Fatalf("after turning on again: enabled %v, version %q", r.Enabled(), r.Version())
	}
	if !r.Apply(specs) || r.Version() != probeconfig.Version(specs) {
		t.Fatal("the re-enabled runner did not take the set")
	}
}

// TestRunnerDisabledDiscardsTheInFlightRound: a round running when
// probing is turned off finishes, but its result is discarded.
func TestRunnerDisabledDiscardsTheInFlightRound(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(true, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	r.Apply([]probeconfig.Spec{spec(targetA, "web", "tcp", 60)})
	clock.advance(time.Minute)
	s := newSink()
	r.dispatch(t.Context(), s.add)
	b.waitStarted(t, 1)
	r.SetPolicy(false, nil)
	b.release()
	r.wg.Wait()
	if got := s.all(); len(got) != 0 {
		t.Fatalf("a round of a disabled runner reached the sink: %+v", got)
	}
}

// TestRunnerUsesThePolicysGuard: the guard SetPolicy installs is the one
// the next round dials through; nil keeps the default.
func TestRunnerUsesThePolicysGuard(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(false, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	g := probe.NewGuard(nil, nil).WithLocalDeny([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")})
	if !r.SetPolicy(true, g) {
		t.Fatal("a new guard is not reported as a change")
	}
	runOnce(t, r, clock, spec(targetA, "web", "tcp", 60))
	if got := b.calls[0].guard; got != g {
		t.Fatal("the probe did not get the policy's guard")
	}
	if r.SetPolicy(true, g) {
		t.Fatal("the same guard again is reported as a change")
	}
}

// TestRunnerAppliesTheLocalDeny runs the real tcp probe through the
// runner with a loopback-allowing guard narrowed by a local deny list:
// the round is the local refusal on every metric and nothing connects.
func TestRunnerAppliesTheLocalDeny(t *testing.T) {
	t.Parallel()
	port, accepted := loopbackListener(t)
	clock := newFakeClock()
	r := NewRunner(Deps{Now: clock.Now, Ticker: (&fakeTickers{}).factory})
	r.SetPolicy(true, probe.NewGuardForTest(true, nil).WithLocalDeny([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}))
	sp := spec(targetA, "web", "tcp", 30)
	sp.ParamsJSON = `{"port":` + strconv.Itoa(port) + `,"timeout_s":2}`
	sp.Address = "127.0.0.1"
	got := runOnce(t, r, clock, sp)
	allErrors(t, got, "target address not allowed by the agent's local policy")
	if n := accepted.Load(); n != 0 {
		t.Fatalf("the denied target accepted %d connections", n)
	}
}
