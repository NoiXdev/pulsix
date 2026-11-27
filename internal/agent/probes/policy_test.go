package probes

import (
	"errors"
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

// TestRunnerDerivesTheGuardFromItsDefault: a deny list adds to the
// runner's default guard — whatever that refuses stays refused (L5) — and
// the guard is rebuilt only when the list changes.
func TestRunnerDerivesTheGuardFromItsDefault(t *testing.T) {
	t.Parallel()
	deny := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	guardOf := func(r *Runner, clock *fakeClock, b *blockingProbe) *probe.Guard {
		t.Helper()
		runOnce(t, r, clock, spec(targetA, "web", "tcp", 60))
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.calls[len(b.calls)-1].guard
	}

	// The production default: loopback, link-local and metadata stay refused.
	b := newBlockingProbe(false, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	if !r.SetPolicy(true, deny) {
		t.Fatal("a new deny list is not reported as a change")
	}
	g := guardOf(r, clock, b)
	if err := g.Check(netip.MustParseAddr("192.0.2.7")); !errors.Is(err, probe.ErrLocalPolicy) {
		t.Fatalf("Check(192.0.2.7) = %v, want ErrLocalPolicy", err)
	}
	for _, addr := range []string{"127.0.0.1", "::1", "169.254.169.254", "64:ff9b::7f00:1"} {
		if g.Allowed(netip.MustParseAddr(addr)) {
			t.Errorf("with a deny list the guard allows %s", addr)
		}
	}
	if r.SetPolicy(true, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}) {
		t.Fatal("the same deny list again is reported as a change")
	}

	// A replaced default (Deps.Guard) is what the list is added to.
	b2 := newBlockingProbe(false, up("tcp"))
	r2 := NewRunner(Deps{Registry: probe.Registry{"tcp": b2.fn("tcp")}, Now: clock.Now, Ticker: (&fakeTickers{}).factory,
		Guard: probe.NewGuardForTest(true, nil)})
	r2.SetPolicy(true, deny)
	g2 := guardOf(r2, clock, b2)
	if !g2.Allowed(netip.MustParseAddr("127.0.0.1")) || g2.Allowed(netip.MustParseAddr("192.0.2.7")) {
		t.Fatal("the deny list was not added to the runner's own default guard")
	}

	// No list again: the default itself.
	r2.SetPolicy(true, nil)
	if g3 := guardOf(r2, clock, b2); !g3.Allowed(netip.MustParseAddr("192.0.2.7")) {
		t.Fatal("clearing the deny list left it applied")
	}
}

// TestRunnerAppliesTheLocalDeny runs the real tcp probe through the
// runner with a loopback-allowing guard narrowed by a local deny list:
// the round is the local refusal on every metric and nothing connects.
func TestRunnerAppliesTheLocalDeny(t *testing.T) {
	t.Parallel()
	port, accepted := loopbackListener(t)
	clock := newFakeClock()
	r := NewRunner(Deps{Now: clock.Now, Ticker: (&fakeTickers{}).factory, Guard: probe.NewGuardForTest(true, nil)})
	r.SetPolicy(true, []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
	sp := spec(targetA, "web", "tcp", 30)
	sp.ParamsJSON = `{"port":` + strconv.Itoa(port) + `,"timeout_s":2}`
	sp.Address = "127.0.0.1"
	got := runOnce(t, r, clock, sp)
	allErrors(t, got, "target address not allowed by the agent's local policy")
	if n := accepted.Load(); n != 0 {
		t.Fatalf("the denied target accepted %d connections", n)
	}
}
