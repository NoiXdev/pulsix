// Tests for how the loop and stream session wire the probe runner in
// (backlog 3.13a): the heartbeat's probes_supported and
// probe_config_version, a pushed ProbeConfig reaching the runner (and an
// unreadable one not), a round's results reaching the stream as
// ProbeResults in chunks, and the disconnect ring's replay of probe results
// next to check results. The runner itself is pinned in
// internal/agent/probes.
package agent

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agent/probes"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probeconfig"
)

var probeTarget = uuid.MustParse("0198a0b0-0000-7000-8000-0000000000aa")

// settableClock is a wall clock a test moves.
type settableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *settableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *settableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func probeSpecs() []probeconfig.Spec {
	return []probeconfig.Spec{{
		TargetHostID: probeTarget, CheckKey: "web", Plugin: "tcp",
		ParamsJSON: `{"port":443}`, IntervalSeconds: 60, Address: "192.0.2.7",
	}}
}

// probeResultMessages returns every ProbeResults message sent so far.
func probeResultMessages(stream interface {
	sentMessages() []*agentpb.AgentMessage
},
) []*agentpb.ProbeResults {
	var out []*agentpb.ProbeResults
	for _, m := range stream.sentMessages() {
		if pr := m.GetProbeResults(); pr != nil {
			out = append(out, pr)
		}
	}
	return out
}

func TestHeartbeatReportsProbeSupportAndTheAppliedVersion(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	defer stopLoop(t, cancel, done)

	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	first := heartbeats(stream)[0]
	if !first.GetProbesSupported() {
		t.Error("the heartbeat does not report probes_supported")
	}
	if v := first.GetProbeConfigVersion(); v != "" {
		t.Errorf("probe_config_version before any config = %q, want empty", v)
	}

	specs := probeSpecs()
	stream.pushManagerMessage(&agentpb.ManagerMessage{Ack: true, ProbeConfig: probeconfig.ToProto(specs)})
	want := probeconfig.Version(specs)
	waitFor(t, 2*time.Second, func() bool { return l.probes.Version() == want })

	_, tickers := lt.waitForTickers(1)
	before := len(heartbeats(stream))
	tickers[0].c <- time.Now()
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > before })
	last := heartbeats(stream)[len(heartbeats(stream))-1]
	if v := last.GetProbeConfigVersion(); v != want {
		t.Errorf("probe_config_version after the config = %q, want %q", v, want)
	}

	// The empty set is a version of its own, not "".
	stream.pushManagerMessage(&agentpb.ManagerMessage{ProbeConfig: probeconfig.ToProto(nil)})
	waitFor(t, 2*time.Second, func() bool { return l.probes.Version() == probeconfig.Version(nil) })
}

func TestUnreadableProbeConfigKeepsTheRunningSet(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	logs := &lockedLog{}
	l := NewLoop(lt.cfg, lt.dialer, lt.facts, append(lt.options(), WithLogger(slog.New(slog.NewTextHandler(logs, nil))))...)
	cancel, done := runLoop(t, l)
	defer stopLoop(t, cancel, done)

	specs := probeSpecs()
	stream.pushManagerMessage(&agentpb.ManagerMessage{ProbeConfig: probeconfig.ToProto(specs)})
	want := probeconfig.Version(specs)
	waitFor(t, 2*time.Second, func() bool { return l.probes.Version() == want })

	bad := probeconfig.ToProto(nil)
	bad.Version = "other"
	bad.Probes = []*agentpb.ProbeSpec{{TargetHostId: "0198A0B0-0000-7000-8000-0000000000AA", CheckKey: "web", Plugin: "tcp"}}
	stream.pushManagerMessage(&agentpb.ManagerMessage{ProbeConfig: bad})
	waitFor(t, 2*time.Second, func() bool {
		return containsAll(logs.String(), "probe configuration", "not a canonical UUID")
	})
	if v := l.probes.Version(); v != want {
		t.Fatalf("version after an unreadable config = %q, want the running set's %q", v, want)
	}
}

// stopLoop cancels a loop started with runLoop and checks it ended cleanly.
func stopLoop(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
}

// lockedLog is a log sink safe to read while the loop writes to it.
type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *lockedLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedLog) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// TestProbeRoundReachesTheStreamInChunks runs one round of a probe that
// reports 450 observations: they go out as ProbeResults of 200, 200 and
// 50, each naming the target and sharing the round's At.
func TestProbeRoundReachesTheStreamInChunks(t *testing.T) {
	lt := newLoopTest(t)
	clock := &settableClock{now: time.Now()}
	lt.now = clock.Now
	lt.withIdentity()
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	const n = 2*maxResultsPerMessage + 50
	reg := probe.Registry{"tcp": func(context.Context, *probe.Guard, map[string]any, string, map[string]string) []probe.Observation {
		out := make([]probe.Observation, n)
		for i := range out {
			out[i] = probe.Observation{Metric: "tcp.up", Item: "i" + strconv.Itoa(i), Value: floatPtr(1)}
		}
		return out
	}}
	probeTicks := &fakeTickers{}
	l := NewLoop(lt.cfg, lt.dialer, lt.facts, append(lt.options(),
		withProbeRegistry(reg), withProbeTicker(probeTicks.factory()))...)
	cancel, done := runLoop(t, l)
	defer stopLoop(t, cancel, done)
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })

	stream.pushManagerMessage(&agentpb.ManagerMessage{ProbeConfig: probeconfig.ToProto(probeSpecs())})
	waitFor(t, 2*time.Second, func() bool { _, ts := probeTicks.recorded(); return len(ts) == 1 })
	clock.advance(2 * time.Minute)
	_, ts := probeTicks.recorded()
	ts[0].c <- clock.Now()

	waitFor(t, 2*time.Second, func() bool { return len(probeResultMessages(stream)) == 3 })
	msgs := probeResultMessages(stream)
	var at time.Time
	for i, want := range []int{maxResultsPerMessage, maxResultsPerMessage, 50} {
		got := msgs[i].GetResults()
		if len(got) != want {
			t.Fatalf("message %d carries %d results, want %d", i, len(got), want)
		}
		for _, r := range got {
			if r.GetTargetHostId() != probeTarget.String() || r.GetCheckKey() != "web" || r.GetValue() != 1 {
				t.Fatalf("result %v does not carry its slot and value", r)
			}
			if at.IsZero() {
				at = r.GetAt().AsTime()
			}
			if !r.GetAt().AsTime().Equal(at) {
				t.Fatalf("results of one round carry different At: %s and %s", at, r.GetAt().AsTime())
			}
		}
	}
	if len(checkResultMessages(stream)) != 0 {
		t.Fatal("probe results went out as check results")
	}
}

// TestProbeResultsAreBufferedAndReplayedAfterCheckResults: while no
// stream is up, probe results land in their own 15-minute ring; the
// reconnect replays check results first, as before, and then probe
// results, in chunks.
func TestProbeResultsAreBufferedAndReplayedAfterCheckResults(t *testing.T) {
	lt := newLoopTest(t)
	l := lt.newLoop()
	now := lt.now()
	l.handleResults(manyResults(3, now))
	l.handleProbeResults(manyProbeResults(maxResultsPerMessage+1, now))

	stream := &chunkingStream{}
	if err := l.activateSender(newStreamSender(stream)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	sent := stream.sentMessages()
	if len(sent) != 3 {
		t.Fatalf("replayed %d messages, want 1 check results + 2 probe results", len(sent))
	}
	if sent[0].GetCheckResults() == nil || len(sent[0].GetCheckResults().GetResults()) != 3 {
		t.Fatalf("first replayed message = %v, want the 3 check results", sent[0])
	}
	if n := len(sent[1].GetProbeResults().GetResults()); n != maxResultsPerMessage {
		t.Fatalf("second message carries %d probe results, want %d", n, maxResultsPerMessage)
	}
	if n := len(sent[2].GetProbeResults().GetResults()); n != 1 {
		t.Fatalf("third message carries %d probe results, want 1", n)
	}
	if len(l.probeBuffer.Drain()) != 0 || len(l.buffer.Drain()) != 0 {
		t.Fatal("a successful replay left results in a ring")
	}

	// Live: with the sender active, results go straight out.
	l.handleProbeResults(manyProbeResults(2, now))
	if got := len(probeResultMessages(stream)); got != 3 {
		t.Fatalf("probe results messages = %d, want 3", got)
	}
}

// TestFailedProbeReplayPutsBackOnlyTheUndelivered: the probe ring follows
// the check ring's no-duplicate rule — what went out before the stream
// died is not replayed again.
func TestFailedProbeReplayPutsBackOnlyTheUndelivered(t *testing.T) {
	lt := newLoopTest(t)
	l := lt.newLoop()
	n := 3 * maxResultsPerMessage
	l.handleProbeResults(manyProbeResults(n, lt.now()))

	stream := &chunkingStream{failAfter: 1}
	if err := l.activateSender(newStreamSender(stream)); err == nil {
		t.Fatal("a probe replay that hits a dead stream must fail the activation")
	}
	if l.sender != nil {
		t.Fatal("a failed activation must not install the sender")
	}
	left := l.probeBuffer.Drain()
	if len(left) != n-maxResultsPerMessage {
		t.Fatalf("probe ring holds %d, want the %d unreplayed", len(left), n-maxResultsPerMessage)
	}
	if left[0].Item != "i"+strconv.Itoa(maxResultsPerMessage) {
		t.Fatalf("probe ring starts at %q, want the first undelivered result", left[0].Item)
	}
}

func TestHandleProbeResultsBuffersOnlyTheUndeliveredRemainder(t *testing.T) {
	lt := newLoopTest(t)
	l := lt.newLoop()
	stream := &chunkingStream{failAfter: 1}
	l.sender = newStreamSender(stream)
	n := 2 * maxResultsPerMessage
	l.handleProbeResults(manyProbeResults(n, lt.now()))
	if got := len(probeResultMessages(stream)); got != 1 {
		t.Fatalf("delivered %d messages before the stream died, want 1", got)
	}
	if left := l.probeBuffer.Drain(); len(left) != maxResultsPerMessage {
		t.Fatalf("buffered %d, want %d", len(left), maxResultsPerMessage)
	}
	if left := l.buffer.Drain(); len(left) != 0 {
		t.Fatalf("probe results leaked into the check ring: %d", len(left))
	}
}

func TestProbeResultsWithInvalidUTF8AreDroppedAndErrorsSanitised(t *testing.T) {
	lt := newLoopTest(t)
	l := lt.newLoop()
	stream := &chunkingStream{}
	l.sender = newStreamSender(stream)
	bad := "\xff"
	res := manyProbeResults(3, lt.now())
	res[0].Label = &bad
	res[0].Value = nil
	res[1].Item = "x\xffy"
	res[2].Value = nil
	res[2].Err = "dial \xff failed"
	l.handleProbeResults(res)
	msgs := probeResultMessages(stream)
	if len(msgs) != 1 || len(msgs[0].GetResults()) != 1 {
		t.Fatalf("sent %v, want only the result whose error is sanitised", msgs)
	}
	if got := msgs[0].GetResults()[0].GetError(); got != "dial � failed" {
		t.Fatalf("error = %q, want it sanitised", got)
	}
}

func manyProbeResults(n int, at time.Time) []probes.Result {
	out := make([]probes.Result, n)
	for i := range out {
		out[i] = probes.Result{TargetHostID: probeTarget, CheckKey: "web", Item: "i" + strconv.Itoa(i),
			Metric: "tcp.up", Value: floatPtr(1), At: at}
	}
	return out
}

// TestProbeRingIsTheChecksWindow: the probe ring keeps the same 15
// minutes as the check ring.
func TestProbeRingIsTheChecksWindow(t *testing.T) {
	lt := newLoopTest(t)
	base := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	now := base
	lt.now = func() time.Time { return now }
	l := lt.newLoop()
	l.handleProbeResults(manyProbeResults(1, base))
	now = base.Add(bufferWindow + time.Second)
	l.handleProbeResults(manyProbeResults(1, now))
	if got := len(l.probeBuffer.Drain()); got != 1 {
		t.Fatalf("probe ring holds %d after the window passed, want 1", got)
	}
}

func (s *chunkingStream) sentMessages() []*agentpb.AgentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*agentpb.AgentMessage(nil), s.sent...)
}
