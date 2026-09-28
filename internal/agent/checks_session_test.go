// Tests for how the loop and stream session wire the check scheduler in:
// the heartbeat carrying the scheduler's version, a pushed CheckConfig
// reaching Apply, a fired check's results reaching the stream, the
// config-interval ticker's own re-assert, and the disconnect buffer's
// replay-before-live-results contract at the Loop level (the ring itself
// is pinned exhaustively in internal/agent/checks; this file pins the
// wiring around it).
package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

func floatPtr(v float64) *float64 { return &v }

// heartbeats returns every heartbeat message sent so far, in order.
func heartbeats(stream *fakeStream) []*agentpb.Heartbeat {
	var out []*agentpb.Heartbeat
	for _, m := range stream.sentMessages() {
		if hb := m.GetHeartbeat(); hb != nil {
			out = append(out, hb)
		}
	}
	return out
}

// checkResultMessages returns every CheckResults message sent so far.
func checkResultMessages(stream *fakeStream) []*agentpb.CheckResults {
	var out []*agentpb.CheckResults
	for _, m := range stream.sentMessages() {
		if cr := m.GetCheckResults(); cr != nil {
			out = append(out, cr)
		}
	}
	return out
}

func TestLoopHeartbeatReportsSchedulerVersion(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	// The immediate, connect-time heartbeat reports no configuration yet.
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	if v := heartbeats(stream)[0].GetConfigVersion(); v != "" {
		t.Errorf("initial config_version = %q, want empty", v)
	}

	// A CheckConfig push (independent of Ack, per the proto's own
	// fallback shape) is applied to the scheduler; the version rides the
	// very next heartbeat this session sends.
	stream.pushManagerMessage(&agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{
		Version: "cfg-v1",
	}})
	waitFor(t, 2*time.Second, func() bool { return l.scheduler.Version() == "cfg-v1" })

	_, tickers := lt.ticks.recorded()
	if len(tickers) < 1 {
		t.Fatal("no heartbeat ticker recorded")
	}
	before := len(heartbeats(stream))
	tickers[0].c <- time.Now()
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > before })
	if v := heartbeats(stream)[len(heartbeats(stream))-1].GetConfigVersion(); v != "cfg-v1" {
		t.Errorf("config_version after applying a push = %q, want cfg-v1", v)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

// TestLoopConfigIntervalTickerResendsHeartbeat covers the one arrangement
// the config-interval ticker exists for: a configuration interval SHORTER
// than the heartbeat interval, where the extra tick genuinely reaches the
// manager sooner than the next heartbeat would.
func TestLoopConfigIntervalTickerResendsHeartbeat(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(stream.sentMessages()) > 0 })

	intervals, tickers := lt.ticks.recorded()
	if len(intervals) != 3 {
		t.Fatalf("tickers = %d, want 3 (heartbeat, facts, config)", len(intervals))
	}
	if intervals[2] != lt.cfg.ConfigInterval {
		t.Fatalf("third ticker interval = %v, want ConfigInterval %v", intervals[2], lt.cfg.ConfigInterval)
	}
	before := len(heartbeats(stream))
	tickers[2].c <- time.Now() // the config-interval ticker, NOT the heartbeat one
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > before })

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

func TestLoopAppliedCheckFiresAndResultsReachTheStream(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(stream.sentMessages()) > 0 })

	// Push a real "cpu" check (the loop wires checks.Registry()'s real
	// plugins — cpu.Collect works against the real host on every OS this
	// suite runs on, the same trust boundary internal/agent/facts' own
	// gopsutil calls already cross in production, never faked here).
	stream.pushManagerMessage(&agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{
		Version: "cfg-v1",
		Checks: []*agentpb.CheckSpec{
			{Key: "cpu", Plugin: "cpu", ParamsJson: "{}", IntervalSeconds: 30},
		},
	}})

	// A fourth ticker appears once Apply spawns the check's own timer.
	waitFor(t, 2*time.Second, func() bool { _, tk := lt.ticks.recorded(); return len(tk) == 4 })
	_, tickers := lt.ticks.recorded()
	tickers[3].c <- time.Now()

	waitFor(t, 2*time.Second, func() bool { return len(checkResultMessages(stream)) > 0 })
	results := checkResultMessages(stream)[0].GetResults()
	if len(results) == 0 {
		t.Fatal("no check results in the CheckResults message")
	}
	for _, r := range results {
		if r.GetCheckKey() != "cpu" {
			t.Errorf("check_key = %q, want cpu", r.GetCheckKey())
		}
		if !strings.HasPrefix(r.GetMetric(), "cpu.") {
			t.Errorf("metric = %q, want a cpu.* metric", r.GetMetric())
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

// TestLoopHandleResultsBuffersWhenDisconnectedAndReplaysInOrder is a
// white-box test of the Loop's own routing contract (handleResults,
// activateSender, deactivateSender): with no active sender, results land
// in the disconnect buffer; activating a sender replays the buffer FIRST,
// preserving its original timestamps, before any newly-produced result
// reaches the same stream; deactivating sends new results back to the
// buffer. The ring's own eviction/counting behaviour is pinned
// exhaustively in internal/agent/checks' own buffer_test.go — this pins
// only the session-transition wiring around it.
func TestLoopHandleResultsBuffersWhenDisconnectedAndReplaysInOrder(t *testing.T) {
	lt := newLoopTest(t)
	base := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	lt.now = func() time.Time { return base }
	l := lt.newLoop()

	// Disconnected: no sender has ever been activated.
	buffered := checks.Result{CheckKey: "cpu", Metric: "cpu.used_pct", Value: floatPtr(11), At: base}
	l.handleResults([]checks.Result{buffered})

	stream := newFakeStream()
	sender := newStreamSender(stream)
	if err := l.activateSender(sender); err != nil {
		t.Fatalf("activateSender: %v", err)
	}

	msgs := checkResultMessages(stream)
	if len(msgs) != 1 {
		t.Fatalf("messages sent by activation = %d, want exactly 1 (the replayed batch)", len(msgs))
	}
	replayed := msgs[0].GetResults()
	if len(replayed) != 1 || replayed[0].GetCheckKey() != "cpu" {
		t.Fatalf("replayed = %+v, want the one buffered cpu result", replayed)
	}
	if !replayed[0].GetAt().AsTime().Equal(base) {
		t.Errorf("replayed At = %v, want the original timestamp %v (never arrival time)", replayed[0].GetAt().AsTime(), base)
	}

	// Now connected: a new result goes straight to the stream, not the
	// buffer — the message count grows immediately.
	live := checks.Result{CheckKey: "cpu", Metric: "cpu.used_pct", Value: floatPtr(22), At: base.Add(time.Second)}
	l.handleResults([]checks.Result{live})
	if got := len(checkResultMessages(stream)); got != 2 {
		t.Fatalf("messages after a live result = %d, want 2", got)
	}

	// Deactivating (as session teardown does via defer) sends the NEXT
	// result back to the buffer instead of this now-defunct stream.
	l.deactivateSender(sender)
	stranded := checks.Result{CheckKey: "cpu", Metric: "cpu.used_pct", Value: floatPtr(33), At: base.Add(2 * time.Second)}
	l.handleResults([]checks.Result{stranded})
	if got := len(checkResultMessages(stream)); got != 2 {
		t.Fatalf("messages after deactivation = %d, want still 2 (the new result must be buffered, not sent)", got)
	}

	// The stranded result is exactly what the NEXT reconnect replays.
	stream2 := newFakeStream()
	sender2 := newStreamSender(stream2)
	if err := l.activateSender(sender2); err != nil {
		t.Fatalf("activateSender (second stream): %v", err)
	}
	replay2 := checkResultMessages(stream2)
	if len(replay2) != 1 || len(replay2[0].GetResults()) != 1 {
		t.Fatalf("second replay = %+v, want exactly the stranded result", replay2)
	}
	got := replay2[0].GetResults()[0]
	if got.GetValue() != 33 {
		t.Errorf("replayed value = %v, want 33 (the stranded result)", got.GetValue())
	}
	if !got.GetAt().AsTime().Equal(stranded.At) {
		t.Errorf("replayed At = %v, want the stranded result's original timestamp %v", got.GetAt().AsTime(), stranded.At)
	}
}

// TestLoopDecodeCheckConfigSkipsUnparsableParamsAndDropsTheVersion
// replaces TestLoopDecodeCheckConfigSkipsUnparsableParamsButKeepsVersion,
// which pinned the opposite and was wrong. Skipping the undecodable check
// is right — most of the set beats none of it. Keeping the version was
// not: it covers the FULL set, so reporting it back makes
// processHeartbeat believe the agent converged and never push again,
// which is permanent divergence. A fully decodable push still carries its
// version through, so this is not "never report anything".
func TestLoopDecodeCheckConfigSkipsUnparsableParamsAndDropsTheVersion(t *testing.T) {
	lt := newLoopTest(t)
	l := lt.newLoop()

	cfg := l.decodeCheckConfig(&agentpb.CheckConfig{
		Version: "cfg-v2",
		Checks: []*agentpb.CheckSpec{
			{Key: "good", Plugin: "cpu", ParamsJson: "{}", IntervalSeconds: 30},
			{Key: "bad", Plugin: "disk", ParamsJson: "not-json", IntervalSeconds: 60},
		},
	})
	if cfg.Version != "" {
		t.Errorf("Version = %q, want \"\" for a partly decodable push", cfg.Version)
	}
	if len(cfg.Checks) != 1 || cfg.Checks[0].Key != "good" {
		t.Fatalf("Checks = %+v, want only the decodable one", cfg.Checks)
	}
	if !strings.Contains(lt.logOutput(), "unparsable params") {
		t.Errorf("the skipped check must be logged:\n%s", lt.logOutput())
	}

	whole := l.decodeCheckConfig(&agentpb.CheckConfig{
		Version: "cfg-v2",
		Checks:  []*agentpb.CheckSpec{{Key: "good", Plugin: "cpu", ParamsJson: "{}", IntervalSeconds: 30}},
	})
	if whole.Version != "cfg-v2" {
		t.Errorf("Version of a fully decodable push = %q, want cfg-v2", whole.Version)
	}
}

func TestLoopDecodeCheckConfigDecodesNestedParams(t *testing.T) {
	lt := newLoopTest(t)
	l := lt.newLoop()

	cfg := l.decodeCheckConfig(&agentpb.CheckConfig{
		Version: "cfg-v3",
		Checks: []*agentpb.CheckSpec{
			{Key: "disk", Plugin: "disk", ParamsJson: `{"mounts":["/data","/"]}`, IntervalSeconds: 45},
		},
	})
	if len(cfg.Checks) != 1 {
		t.Fatalf("Checks = %+v, want 1", cfg.Checks)
	}
	spec := cfg.Checks[0]
	if spec.Interval != 45*time.Second {
		t.Errorf("Interval = %v, want 45s", spec.Interval)
	}
	mounts, ok := spec.Params["mounts"].([]any)
	if !ok || len(mounts) != 2 || mounts[0] != "/data" || mounts[1] != "/" {
		t.Errorf("Params[mounts] = %#v, want [/data /]", spec.Params["mounts"])
	}
}

// TestStreamSenderEncodesErrorResultsOnTheWireErrorArm pins the
// coordinator's named review scenario at the wire boundary: a batch
// mixing one good result and one errored result (exactly what a disk
// check with one unreadable mount produces) renders as a single
// CheckResults message whose good entry carries the "value" oneof arm and
// whose errored entry carries the "error" arm — never silently dropped,
// never coalesced into the good entry.
func TestStreamSenderEncodesErrorResultsOnTheWireErrorArm(t *testing.T) {
	stream := newFakeStream()
	sender := newStreamSender(stream)
	at := time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)
	batch := []checks.Result{
		{CheckKey: "disk", Item: "/", Metric: "disk.used_pct", Value: floatPtr(12), At: at},
		{CheckKey: "disk", Item: "/broken", Metric: "disk.used_pct", Err: "permission denied", At: at},
	}
	if err := sender.sendResults(batch); err != nil {
		t.Fatalf("sendResults: %v", err)
	}

	msgs := checkResultMessages(stream)
	if len(msgs) != 1 {
		t.Fatalf("messages sent = %d, want 1", len(msgs))
	}
	results := msgs[0].GetResults()
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (one good, one errored)", len(results))
	}
	var good, bad *agentpb.CheckResult
	for _, r := range results {
		switch r.GetItem() {
		case "/":
			good = r
		case "/broken":
			bad = r
		}
	}
	if good == nil {
		t.Fatal("no result for the good mount")
	}
	if _, ok := good.GetObservation().(*agentpb.CheckResult_Value); !ok {
		t.Errorf("good result's oneof arm = %T, want CheckResult_Value", good.GetObservation())
	}
	if good.GetValue() != 12 || good.GetError() != "" {
		t.Errorf("good result = value %v error %q, want 12 and no error", good.GetValue(), good.GetError())
	}

	if bad == nil {
		t.Fatal("no result for the broken mount")
	}
	if _, ok := bad.GetObservation().(*agentpb.CheckResult_Error); !ok {
		t.Errorf("broken result's oneof arm = %T, want CheckResult_Error", bad.GetObservation())
	}
	if bad.GetError() != "permission denied" || bad.GetValue() != 0 {
		t.Errorf("broken result = value %v error %q, want no value and the error text", bad.GetValue(), bad.GetError())
	}
}

// chunkingStream is a Stream that records what it was sent and starts
// refusing after failAfter successful sends — enough to drive
// handleResults' chunking without a whole session around it.
type chunkingStream struct {
	mu        sync.Mutex
	sent      []*agentpb.AgentMessage
	failAfter int // 0 means never fail
}

func (s *chunkingStream) Send(m *agentpb.AgentMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAfter > 0 && len(s.sent) >= s.failAfter {
		return errors.New("stream is dead")
	}
	s.sent = append(s.sent, m)
	return nil
}

func (s *chunkingStream) Recv() (*agentpb.ManagerMessage, error) { select {} }
func (s *chunkingStream) CloseSend() error                       { return nil }
func (s *chunkingStream) Context() context.Context               { return context.Background() }

func (s *chunkingStream) batches() [][]*agentpb.CheckResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]*agentpb.CheckResult
	for _, m := range s.sent {
		if cr := m.GetCheckResults(); cr != nil {
			out = append(out, cr.GetResults())
		}
	}
	return out
}

// manyResults builds n distinct results, numbered through Value so a test
// can tell exactly which ones were delivered and which were buffered.
func manyResults(n int, at time.Time) []checks.Result {
	out := make([]checks.Result, n)
	for i := range out {
		out[i] = checks.Result{CheckKey: "disk", Item: "/m" + strconv.Itoa(i), Metric: "disk.used_pct",
			Value: floatPtr(float64(i)), At: at}
	}
	return out
}

// TestHandleResultsChunksToTheIngestContract: a single Collect batch
// larger than maxResultsPerMessage is split into messages of at most that
// size. The manager refuses a CheckResults message over
// checks.MaxObservationsPerBatch outright (internal/checks), so an
// unsplit batch from a host with a few hundred filesystems would not be
// merely large — it would be refused, and the refusal ends the stream, so
// the agent would reconnect and send the identical batch for ever. The
// replay path already chunked; the live path must too.
func TestHandleResultsChunksToTheIngestContract(t *testing.T) {
	lt := newLoopTest(t)
	l := lt.newLoop()
	stream := &chunkingStream{}
	l.sender = newStreamSender(stream)

	n := 2*maxResultsPerMessage + 1
	l.handleResults(manyResults(n, lt.now()))

	batches := stream.batches()
	if len(batches) != 3 {
		t.Fatalf("messages = %d, want 3 for %d results at %d per message", len(batches), n, maxResultsPerMessage)
	}
	total := 0
	for i, b := range batches {
		if len(b) > maxResultsPerMessage {
			t.Errorf("message %d carries %d results, over the %d cap", i, len(b), maxResultsPerMessage)
		}
		total += len(b)
	}
	if total != n {
		t.Fatalf("delivered %d of %d results", total, n)
	}
	if got := len(l.buffer.Drain()); got != 0 {
		t.Fatalf("buffered %d results after a fully successful send, want 0", got)
	}
}

// TestHandleResultsBuffersOnlyTheUndeliveredRemainder: when the stream
// dies partway through a chunked batch, the chunks already delivered must
// not also land in the buffer — replaying them on reconnect would write
// the same samples twice.
func TestHandleResultsBuffersOnlyTheUndeliveredRemainder(t *testing.T) {
	lt := newLoopTest(t)
	l := lt.newLoop()
	stream := &chunkingStream{failAfter: 1}
	l.sender = newStreamSender(stream)

	n := 3 * maxResultsPerMessage
	l.handleResults(manyResults(n, lt.now()))

	if got := len(stream.batches()); got != 1 {
		t.Fatalf("delivered messages = %d, want 1 before the stream died", got)
	}
	buffered := l.buffer.Drain()
	if len(buffered) != n-maxResultsPerMessage {
		t.Fatalf("buffered %d results, want the %d undelivered ones only", len(buffered), n-maxResultsPerMessage)
	}
	if buffered[0].Item != "/m"+strconv.Itoa(maxResultsPerMessage) {
		t.Fatalf("buffer starts at %q, want the first result after the delivered chunk", buffered[0].Item)
	}
}

// TestActivateSenderReturnsOnlyTheUnreplayedRemainder: the reconnect
// replay chunks too, and a stream that dies partway through it must put
// back only what it did not manage to deliver — the same no-duplicate
// rule handleResults follows, on the path that actually drains the
// 15-minute ring.
func TestActivateSenderReturnsOnlyTheUnreplayedRemainder(t *testing.T) {
	lt := newLoopTest(t)
	l := lt.newLoop()
	n := 3 * maxResultsPerMessage
	l.buffer.Add(manyResults(n, lt.now()))

	stream := &chunkingStream{failAfter: 2}
	if err := l.activateSender(newStreamSender(stream)); err == nil {
		t.Fatal("a replay that hits a dead stream must fail the activation")
	}
	if l.sender != nil {
		t.Fatal("a failed activation must not install the sender")
	}
	if got := len(stream.batches()); got != 2 {
		t.Fatalf("replayed messages = %d, want 2 before the stream died", got)
	}
	left := l.buffer.Drain()
	if len(left) != n-2*maxResultsPerMessage {
		t.Fatalf("buffer holds %d results, want the %d unreplayed ones only", len(left), n-2*maxResultsPerMessage)
	}
	if left[0].Item != "/m"+strconv.Itoa(2*maxResultsPerMessage) {
		t.Fatalf("buffer starts at %q, want the first result after the replayed chunks", left[0].Item)
	}
}

// TestLoopConfigTickerAbsentWhenTheHeartbeatIsAtLeastAsFrequent: at the
// production defaults the configuration interval (60 s) is LONGER than
// the heartbeat interval (30 s), so the heartbeat has already carried the
// configuration version by the time this ticker fires. Firing it anyway
// made one heartbeat in three pure duplicate — and, since the manager
// resolves the whole organization's definitions, groups and selectors on
// every heartbeat it receives, one organization resolve in three pure
// waste. The session now opens the ticker only when it can actually beat
// the heartbeat to it.
func TestLoopConfigTickerAbsentWhenTheHeartbeatIsAtLeastAsFrequent(t *testing.T) {
	for _, tc := range []struct {
		name            string
		heartbeat, conf time.Duration
		wantTickers     int
	}{
		{"production defaults: config longer", 30 * time.Second, 60 * time.Second, 2},
		{"equal cadences: still redundant", 30 * time.Second, 30 * time.Second, 2},
		{"config shorter: the one case it helps", 60 * time.Second, 30 * time.Second, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lt := newLoopTest(t)
			lt.cfg.HeartbeatInterval = tc.heartbeat
			lt.cfg.ConfigInterval = tc.conf
			lt.withIdentity()
			stream := newFakeStream()
			lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}

			l := lt.newLoop()
			cancel, done := runLoop(t, l)
			waitFor(t, 2*time.Second, func() bool { return len(stream.sentMessages()) > 0 })

			intervals, _ := lt.ticks.recorded()
			if len(intervals) != tc.wantTickers {
				t.Fatalf("tickers = %v, want %d", intervals, tc.wantTickers)
			}
			if tc.wantTickers == 3 && intervals[2] != tc.conf {
				t.Fatalf("third ticker interval = %v, want ConfigInterval %v", intervals[2], tc.conf)
			}

			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run = %v, want nil", err)
			}
		})
	}
}

// TestLoopUndecodableSpecNeverClaimsTheFullVersion: a CheckSpec whose
// params_json will not decode is skipped rather than losing the whole
// push — but the version the manager stamped covers the FULL set, so
// adopting it would have the next heartbeat report convergence on a set
// the agent is not running, and processHeartbeat would never push again.
// Permanent divergence, not self-healing. The partial set runs; the
// version does not follow it.
func TestLoopUndecodableSpecNeverClaimsTheFullVersion(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(stream.sentMessages()) > 0 })

	stream.pushManagerMessage(&agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{
		Version: "cfg-partial",
		Checks: []*agentpb.CheckSpec{
			{Key: "cpu", Plugin: "cpu", ParamsJson: "{}", IntervalSeconds: 30},
			{Key: "broken", Plugin: "disk", ParamsJson: "{not json", IntervalSeconds: 60},
		},
	}})
	// The runnable half is installed — a fourth ticker appears for it.
	waitFor(t, 2*time.Second, func() bool { _, tk := lt.ticks.recorded(); return len(tk) == 4 })

	if got := l.scheduler.Version(); got != "" {
		t.Fatalf("version after a partly decodable push = %q, want \"\": reporting %q would tell the "+
			"manager the agent runs a set it does not", got, "cfg-partial")
	}

	// A repaired push converges normally.
	stream.pushManagerMessage(&agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{
		Version: "cfg-whole",
		Checks: []*agentpb.CheckSpec{
			{Key: "cpu", Plugin: "cpu", ParamsJson: "{}", IntervalSeconds: 30},
		},
	}})
	waitFor(t, 2*time.Second, func() bool { return l.scheduler.Version() == "cfg-whole" })

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if !strings.Contains(lt.logOutput(), "unparsable params") {
		t.Errorf("the skipped check must be logged; log was: %s", lt.logOutput())
	}
}

// TestLoopUnknownPluginIsLoggedAndUnclaimed: the scheduler skips a check
// naming a plugin this build does not have, and used to do it silently —
// nothing in the log, and the full version adopted anyway. The loop now
// says which keys it cannot run, and the version does not follow.
func TestLoopUnknownPluginIsLoggedAndUnclaimed(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(stream.sentMessages()) > 0 })

	stream.pushManagerMessage(&agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{
		Version: "cfg-future",
		Checks: []*agentpb.CheckSpec{
			{Key: "cpu", Plugin: "cpu", ParamsJson: "{}", IntervalSeconds: 30},
			{Key: "quantum", Plugin: "quantum", ParamsJson: "{}", IntervalSeconds: 60},
		},
	}})
	waitFor(t, 2*time.Second, func() bool { _, tk := lt.ticks.recorded(); return len(tk) == 5 })

	if got := l.scheduler.Version(); got != "" {
		t.Errorf("version after a push naming an unknown plugin = %q, want \"\"", got)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if !strings.Contains(lt.logOutput(), "quantum") {
		t.Errorf("the unrunnable check's key must reach the log; log was: %s", lt.logOutput())
	}
}
