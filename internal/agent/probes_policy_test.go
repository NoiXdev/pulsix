// Tests for agent.yaml's probes section in the running agent (backlog
// 3.82): the heartbeat's probes_supported follows probes.enabled, an
// opted-out agent drops its probes and refuses a pushed set, a refused
// file turns probing off, and the deny list reaches the probes' guard.
package agent

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/secrets"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probeconfig"
)

// nextHeartbeat fires the heartbeat ticker and returns the heartbeat it
// sent.
func (lt *loopTest) nextHeartbeat(t *testing.T, stream *fakeStream) *agentpb.Heartbeat {
	t.Helper()
	n := len(heartbeats(stream))
	tickerOf(t, lt.ticks, lt.cfg.HeartbeatInterval).c <- time.Now()
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > n })
	return heartbeats(stream)[n]
}

func TestLoopOptOutStopsProbingAndReportsIt(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	clock := &settableClock{now: time.Now()}
	lt.setNow(clock.Now)
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	logs := &lockedLog{}
	l := NewLoop(lt.cfg, lt.dialer, lt.facts, append(lt.options(), WithLogger(slog.New(slog.NewTextHandler(logs, nil))))...)
	cancel, done := runLoop(t, l)
	defer stopLoop(t, cancel, done)

	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	if !heartbeats(stream)[0].GetProbesSupported() {
		t.Fatal("without agent.yaml the agent does not report probes_supported")
	}
	specs := probeSpecs()
	stream.pushManagerMessage(&agentpb.ManagerMessage{ProbeConfig: probeconfig.ToProto(specs)})
	waitFor(t, 2*time.Second, func() bool { return l.probes.Version() == probeconfig.Version(specs) })
	lt.waitForTickers(3)

	// Opting out: the next look turns probing off, the heartbeat says so,
	// and the running set and its version are gone.
	writeSecretsFile(t, lt.cfg.SecretsPath, "probes:\n  enabled: false\n")
	clock.advance(2 * time.Minute)
	if hb := lt.nextHeartbeat(t, stream); hb.GetProbesSupported() || hb.GetProbeConfigVersion() != "" {
		t.Fatalf("opted out: probes_supported %v, probe_config_version %q", hb.GetProbesSupported(), hb.GetProbeConfigVersion())
	}
	if l.probes.Enabled() || l.probes.Version() != "" {
		t.Fatal("the runner still probes after the opt-out")
	}
	waitFor(t, 2*time.Second, func() bool { return containsAll(logs.String(), "probing for other hosts is off") })

	// A set arriving anyway (a heartbeat sent before the edit) is refused.
	stream.pushManagerMessage(&agentpb.ManagerMessage{ProbeConfig: probeconfig.ToProto(specs)})
	waitFor(t, 2*time.Second, func() bool { return containsAll(logs.String(), "probe configuration refused") })
	if l.probes.Version() != "" {
		t.Fatal("an opted-out agent installed a pushed set")
	}

	// Opting back in.
	writeSecretsFile(t, lt.cfg.SecretsPath, "probes:\n  enabled: true\n")
	clock.advance(2 * time.Minute)
	if hb := lt.nextHeartbeat(t, stream); !hb.GetProbesSupported() {
		t.Fatal("opted back in: probes_supported is false")
	}
	stream.pushManagerMessage(&agentpb.ManagerMessage{ProbeConfig: probeconfig.ToProto(specs)})
	waitFor(t, 2*time.Second, func() bool { return l.probes.Version() == probeconfig.Version(specs) })
}

// TestLoopRefusedFileTurnsProbingOff: a file the agent refuses may hold an
// opt-out or a deny list, so probing stays off until it is fixed.
func TestLoopRefusedFileTurnsProbingOff(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	writeSecretsFile(t, lt.cfg.SecretsPath, "probes:\n  enabled: no\n")
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	defer stopLoop(t, cancel, done)

	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	hb := heartbeats(stream)[0]
	if hb.GetSecretsState() == "ok" || hb.GetSecretsState() == "absent" {
		t.Fatalf("secrets_state = %q, want the refusal", hb.GetSecretsState())
	}
	if hb.GetProbesSupported() || l.probes.Enabled() {
		t.Fatal("a refused file left probing on")
	}
}

func TestProbePolicyOf(t *testing.T) {
	if on, deny := probePolicyOf(secretsSet(t, ""), "absent"); !on || deny != nil {
		t.Errorf("absent: %v %v", on, deny)
	}
	if on, _ := probePolicyOf(secretsSet(t, ""), "line 1: not valid YAML"); on {
		t.Error("a refused file leaves probing on")
	}
	if on, _ := probePolicyOf(secretsSet(t, "probes: {enabled: false}\n"), "ok"); on {
		t.Error("enabled: false leaves probing on")
	}
	on, deny := probePolicyOf(secretsSet(t, "probes: {deny_cidrs: [10.0.0.0/8]}\n"), "ok")
	if !on || len(deny) != 1 || deny[0] != netip.MustParsePrefix("10.0.0.0/8") {
		t.Errorf("deny list: %v %v", on, deny)
	}
}

// secretsSet loads content as an agent.yaml.
func secretsSet(t *testing.T, content string) secrets.Set {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	writeSecretsFile(t, path, content)
	set, err := secrets.Load(path, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// guardRecorder is a probe that records the guard of every call.
type guardRecorder struct {
	mu     sync.Mutex
	guards []*probe.Guard
}

func (g *guardRecorder) probe(_ context.Context, gd *probe.Guard, _ map[string]any, _ string, _ map[string]string) []probe.Observation {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.guards = append(g.guards, gd)
	return []probe.Observation{probe.Value("tcp.up", 1)}
}

func (g *guardRecorder) last() *probe.Guard {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.guards) == 0 {
		return nil
	}
	return g.guards[len(g.guards)-1]
}

// TestLoopDenyListReachesTheProbes: the file's deny_cidrs are refused by
// the guard every probe round dials through, with the local text.
func TestLoopDenyListReachesTheProbes(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	clock := &settableClock{now: time.Now()}
	lt.setNow(clock.Now)
	writeSecretsFile(t, lt.cfg.SecretsPath, "probes:\n  deny_cidrs: [192.0.2.0/24]\n")
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	rec := &guardRecorder{}
	probeTicks := &fakeTickers{}
	l := NewLoop(lt.cfg, lt.dialer, lt.facts, append(lt.options(),
		withProbeRegistry(probe.Registry{"tcp": rec.probe}), withProbeTicker(probeTicks.factory()))...)
	cancel, done := runLoop(t, l)
	defer stopLoop(t, cancel, done)
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	if !heartbeats(stream)[0].GetProbesSupported() {
		t.Fatal("a deny list alone turned probing off")
	}

	stream.pushManagerMessage(&agentpb.ManagerMessage{ProbeConfig: probeconfig.ToProto(probeSpecs())})
	waitFor(t, 2*time.Second, func() bool { _, ts := probeTicks.recorded(); return len(ts) == 1 })
	clock.advance(2 * time.Minute)
	_, ts := probeTicks.recorded()
	ts[0].c <- clock.Now()
	waitFor(t, 2*time.Second, func() bool { return rec.last() != nil })
	g := rec.last()
	if err := g.Check(netip.MustParseAddr("192.0.2.7")); !errors.Is(err, probe.ErrLocalPolicy) {
		t.Fatalf("the probe's guard: Check(192.0.2.7) = %v, want ErrLocalPolicy", err)
	}
	if !g.Allowed(netip.MustParseAddr("198.51.100.1")) {
		t.Fatal("the probe's guard refuses an address outside the deny list")
	}
	if g.Allowed(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("the probe's guard lost the built-in refusals")
	}
}
