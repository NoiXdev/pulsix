// The agent loop's tests: enrollment (token, pin, key reuse), the stream
// session (heartbeat cadence, facts at start + interval, ack consumption),
// backoff, reconnect-without-re-enroll and renewal — driven through the
// fakes with injected clocks and tickers.
package agent

import (
	"context"
	"crypto/ecdsa"
	"encoding/pem"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// enrollResponder is the scripted manager: signs the presented CSR with
// the harness CA (whose pin must match the loop's token) and returns the
// enrollment response, or the scripted error/response.
func enrollResponder(caInst *ca.CA, resp *agentpb.EnrollResponse, err error) func(*agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
	return func(req *agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
		if resp != nil || err != nil {
			return resp, err
		}
		return signAgentCSRFor(caInst, []byte(req.GetCsrPem()), testAgentName)
	}
}

// loopTest wires a Loop over the fakes: a data dir (empty by default), a
// token once withToken is called, fake tickers, an injected clock and a
// recorded sleep.
type loopTest struct {
	t      *testing.T
	dir    string
	cfg    Config
	dialer *fakeDialer
	ticks  *fakeTickers
	facts  Collector
	now    func() time.Time

	mu     sync.Mutex
	sleeps []time.Duration
	logs   strings.Builder
	// randVal is what the injected jitter source answers. It defaults to
	// 0, which subtracts no jitter at all, so every existing cadence
	// assertion still names the ladder's own rung; setJitter dials it up
	// for the test that pins the jitter itself.
	randVal float64
}

func (lt *loopTest) setJitter(v float64) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.randVal = v
}

func (lt *loopTest) jitter() float64 {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.randVal
}

func newLoopTest(t *testing.T) *loopTest {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		ManagerAddr:       "manager.example:8444",
		DataDir:           dir,
		AgentName:         testAgentName,
		Version:           "v0.3a-test",
		HeartbeatInterval: time.Minute,
		FactsInterval:     time.Hour,
		// SHORTER than the heartbeat interval on purpose: that is the
		// only arrangement in which the session opens a config-interval
		// ticker at all (see streamSession), and it is what every
		// ticker-index assertion in these tests counts on. The
		// production defaults are the other way round (30 s heartbeat,
		// 60 s config), which is exactly the case
		// TestLoopConfigTickerAbsentWhenTheHeartbeatIsAtLeastAsFrequent
		// covers.
		ConfigInterval: 30 * time.Second,
		CertPath:       filepath.Join(dir, certFileName),
		KeyPath:        filepath.Join(dir, keyFileName),
		MetaPath:       filepath.Join(dir, metaFileName),
	}
	return &loopTest{
		t: t, dir: dir, cfg: cfg, dialer: &fakeDialer{},
		ticks: &fakeTickers{}, facts: &fakeCollector{}, now: time.Now,
	}
}

// withToken renders an enrollment token whose CA pin matches the harness
// CA the loop's fakes sign certificates with.
func (lt *loopTest) withToken(caInst *ca.CA) {
	lt.t.Helper()
	_, caCert := caInst.Pool()
	tok := enrolltoken.Token{ID: testTokenID, Secret: []byte(strings.Repeat("s", 32)), CAPin: enrolltoken.Pin(caCert.Raw)}
	lt.cfg.EnrollToken = enrolltoken.AgentString(tok)
}

// newLoop builds the loop with the test's injections.
func (lt *loopTest) newLoop() *Loop {
	return NewLoop(lt.cfg, lt.dialer, lt.facts, lt.options()...)
}

func (lt *loopTest) options() []LoopOption {
	return []LoopOption{
		WithNow(func() time.Time { return lt.now() }),
		WithSleep(lt.recordSleep),
		WithLogger(slog.New(slog.NewTextHandler(&lt.logs, nil))),
		WithTicker(lt.ticks.factory()),
		WithRand(lt.jitter),
	}
}

func (lt *loopTest) recordSleep(ctx context.Context, d time.Duration) error {
	lt.mu.Lock()
	lt.sleeps = append(lt.sleeps, d)
	lt.mu.Unlock()
	return ctx.Err()
}

func (lt *loopTest) recordedSleeps() []time.Duration {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return append([]time.Duration(nil), lt.sleeps...)
}

func (lt *loopTest) logOutput() string { return lt.logs.String() }

// runLoop runs the loop until cancel, returning its outcome.
func runLoop(t *testing.T, l *Loop) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	return cancel, done
}

// waitFor polls cond until it holds or the timeout passes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// waitForTickers blocks until the session has handed out at least n
// tickers, and returns what it recorded.
//
// Waiting on a SENT MESSAGE is waiting for the wrong thing: a session
// pushes facts and its first heartbeat BEFORE it builds any ticker, so
// the first send says nothing about whether the tickers exist yet. On an
// idle machine the gap is microseconds and the assertion that followed
// happened to win the race; under the load of a full coverage-gate run it
// does not, and the test fails with "ticker intervals = []" or panics
// indexing the slice. Found in the gate, reproduced by delaying the
// session at exactly that point (backlog 3.29).
func (lt *loopTest) waitForTickers(n int) ([]time.Duration, []*fakeTicker) {
	lt.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		intervals, tickers := lt.ticks.recorded()
		if len(tickers) >= n {
			return intervals, tickers
		}
		if !time.Now().Before(deadline) {
			lt.t.Fatalf("the session handed out %d tickers in 2s, want at least %d", len(tickers), n)
			return nil, nil
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// testTokenID is a fixed enrollment token id the tests render tokens with.
var testTokenID = uuid.Must(uuid.NewV7())

func TestLoopNoIdentityNoToken(t *testing.T) {
	lt := newLoopTest(t)
	l := lt.newLoop()
	if err := l.Run(context.Background()); !errors.Is(err, ErrNoEnrollment) {
		t.Fatalf("Run = %v, want ErrNoEnrollment", err)
	}
	if !strings.Contains(lt.logOutput(), "create a new enrollment token") {
		t.Errorf("the remediation must reach the log:\n%s", lt.logOutput())
	}
	if lt.dialer.enrollDials != 0 || lt.dialer.agentDials != 0 {
		t.Errorf("dials = %d enroll / %d agent, want none", lt.dialer.enrollDials, lt.dialer.agentDials)
	}
}

func TestLoopEnrollsAndStreams(t *testing.T) {
	lt := newLoopTest(t)
	caInst := newTestCA(t)
	lt.withToken(caInst)
	lt.dialer.enrollConn = &fakeEnrollConn{respond: enrollResponder(caInst, nil, nil)}
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	collector := &fakeCollector{facts: map[string]string{"os": "linux"}}
	lt.facts = collector

	l := lt.newLoop()
	cancel, done := runLoop(t, l)

	// The enrollment happens first; then the stream session opens and the
	// facts land at start.
	waitFor(t, 2*time.Second, func() bool { return len(stream.sentMessages()) > 0 })

	if lt.dialer.enrollDials != 1 {
		t.Fatalf("enroll dials = %d, want 1", lt.dialer.enrollDials)
	}
	// The enrollment was pinned to the token's CA fingerprint.
	_, caCert := caInst.Pool()
	if lt.dialer.enrollTokens[0].CAPin != enrolltoken.Pin(caCert.Raw) {
		t.Errorf("pinned CA = %x, want the harness CA's fingerprint", lt.dialer.enrollTokens[0].CAPin)
	}
	reqs := lt.dialer.enrollConn.recorded()
	if len(reqs) != 1 {
		t.Fatalf("enroll requests = %d, want 1", len(reqs))
	}
	if reqs[0].GetToken() != lt.cfg.EnrollToken {
		t.Error("the enroll request must carry the token verbatim")
	}
	if reqs[0].GetName() != testAgentName {
		t.Errorf("enroll name = %q, want %q", reqs[0].GetName(), testAgentName)
	}
	if reqs[0].GetVersion() != "v0.3a-test" {
		t.Errorf("enroll version = %q, want v0.3a-test", reqs[0].GetVersion())
	}
	// The identity persisted: a restart would reconnect, not re-enroll.
	id, err := LoadIdentity(lt.dir)
	if err != nil || id == nil {
		t.Fatalf("identity after enrollment = %v, %v; want a saved identity", id, err)
	}
	if !id.Valid(lt.now()) {
		t.Error("the enrolled identity must be valid")
	}
	if id.HostID() != "host-1" {
		t.Errorf("host id = %q, want host-1", id.HostID())
	}

	// Facts at start: one collect, one facts message.
	if got := collector.collectCalls(); got != 1 {
		t.Fatalf("facts collects = %d, want 1", got)
	}
	var factsMsg *agentpb.AgentMessage
	for _, m := range stream.sentMessages() {
		if m.GetFacts() != nil {
			factsMsg = m
		}
	}
	if factsMsg == nil {
		t.Fatal("no facts message sent")
	}
	if got := factsMsg.GetFacts().GetValues()["os"]; got != "linux" {
		t.Errorf("facts os = %q, want linux", got)
	}

	// Heartbeat cadence: the first ticker is the heartbeat's, the second
	// the facts', the third the config re-assert's.
	intervals, tickers := lt.waitForTickers(3)
	if len(intervals) != 3 || intervals[0] != lt.cfg.HeartbeatInterval || intervals[1] != lt.cfg.FactsInterval ||
		intervals[2] != lt.cfg.ConfigInterval {
		t.Fatalf("ticker intervals = %v, want [%v %v %v]", intervals, lt.cfg.HeartbeatInterval, lt.cfg.FactsInterval, lt.cfg.ConfigInterval)
	}
	tickers[0].c <- time.Now()
	waitFor(t, 2*time.Second, func() bool {
		for _, m := range stream.sentMessages() {
			if m.GetHeartbeat() != nil {
				return true
			}
		}
		return false
	})
	var hb *agentpb.AgentMessage
	for _, m := range stream.sentMessages() {
		if m.GetHeartbeat() != nil {
			hb = m
		}
	}
	if hb.GetHeartbeat().GetVersion() != "v0.3a-test" {
		t.Errorf("heartbeat version = %q, want v0.3a-test", hb.GetHeartbeat().GetVersion())
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if !stream.closeSendCalled() {
		t.Error("the stream must be closed on shutdown")
	}
	if len(lt.recordedSleeps()) != 0 {
		t.Errorf("sleeps = %v, want none in a healthy session", lt.recordedSleeps())
	}
}

func TestLoopReconnectSkipsEnroll(t *testing.T) {
	lt := newLoopTest(t)
	caInst := newTestCA(t)
	lt.withToken(caInst)
	lt.dialer.enrollConn = &fakeEnrollConn{respond: enrollResponder(caInst, nil, nil)}
	stream1 := newFakeStream()
	stream2 := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream1, stream2}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)

	waitFor(t, 2*time.Second, func() bool { return len(stream1.sentMessages()) > 0 })
	// The manager ends the first stream; the loop reconnects — with the
	// valid identity, so enrollment must NOT run again.
	stream1.closeRecv()
	waitFor(t, 2*time.Second, func() bool { return len(stream2.sentMessages()) > 0 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if lt.dialer.enrollDials != 1 {
		t.Errorf("enroll dials = %d, want 1 (a reconnect never re-enrolls)", lt.dialer.enrollDials)
	}
	if lt.dialer.agentDials != 2 {
		t.Errorf("agent dials = %d, want 2 (one per session)", lt.dialer.agentDials)
	}
}

func TestLoopBackoffProgression(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{newFakeStream()}}

	// The dialer's error fires once per dial (see fakeDialer.DialAgent),
	// so the loop walks the backoff ladder one rung per iteration.
	lt.dialer.agentDialErr = errors.New("manager unreachable")
	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(lt.recordedSleeps()) >= 4 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	sleeps := lt.recordedSleeps()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, want := range want {
		if sleeps[i] != want {
			t.Fatalf("sleep %d = %v, want %v (all: %v)", i, sleeps[i], want, sleeps)
		}
	}
}

func TestLoopBackoffCap(t *testing.T) {
	for _, tt := range []struct {
		shift int
		want  time.Duration
	}{{0, time.Second}, {5, 32 * time.Second}, {6, maxBackoff}, {10, maxBackoff}} {
		if got := backoff(tt.shift); got != tt.want {
			t.Errorf("backoff(%d) = %v, want %v", tt.shift, got, tt.want)
		}
	}
}

func TestLoopStreamErrorReconnects(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	stream1 := newFakeStream()
	stream2 := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream1, stream2}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(stream1.sentMessages()) > 0 })
	// The manager kills the stream with a transport error.
	stream1.failRecv(status.Error(codes.Unavailable, "transport closing"))
	waitFor(t, 2*time.Second, func() bool { return len(stream2.sentMessages()) > 0 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if len(lt.recordedSleeps()) != 1 {
		t.Fatalf("sleeps = %v, want exactly the one reconnect backoff", lt.recordedSleeps())
	}
	if got := lt.recordedSleeps()[0]; got != time.Second {
		t.Errorf("first backoff = %v, want 1s", got)
	}
	if lt.dialer.enrollDials != 0 {
		t.Errorf("enroll dials = %d, want 0", lt.dialer.enrollDials)
	}
}

func TestLoopRenewsAtConnectWhenDue(t *testing.T) {
	lt := newLoopTest(t)
	id := lt.withIdentity()
	leaf := id.Leaf()
	lifetime := leaf.NotAfter.Sub(leaf.NotBefore)
	// The clock sits in the renewal window: less than a third of the
	// lifetime is left.
	lt.now = func() time.Time { return leaf.NotBefore.Add(2 * lifetime / 3).Add(time.Hour) }

	renewedCSR, err := MakeCSR(id.Signer().(*ecdsa.PrivateKey), testAgentName)
	if err != nil {
		t.Fatal(err)
	}
	caInst := newTestCA(t)
	renewedPEM, renewedCrt, err := caInst.SignAgentCSR(renewedCSR, testOrgID, testAgentName)
	if err != nil {
		t.Fatal(err)
	}
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}, renewResp: &agentpb.EnrollResponse{CertPem: string(renewedPEM)}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(stream.sentMessages()) > 0 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if len(lt.dialer.agentConn.renewed()) != 1 {
		t.Fatalf("renews = %d, want 1", len(lt.dialer.agentConn.renewed()))
	}
	if lt.dialer.enrollDials != 0 {
		t.Errorf("enroll dials = %d, want 0", lt.dialer.enrollDials)
	}
	// The renewal authenticated with the OLD certificate, the stream
	// reconnected with the NEW one.
	if len(lt.dialer.agentLeaves) != 2 {
		t.Fatalf("agent dials = %d, want 2 (renew, then stream)", len(lt.dialer.agentLeaves))
	}
	if lt.dialer.agentLeaves[0].SerialNumber.Cmp(leaf.SerialNumber) != 0 {
		t.Errorf("the renewal must present the old certificate, got serial %v", lt.dialer.agentLeaves[0].SerialNumber)
	}
	if lt.dialer.agentLeaves[1].SerialNumber.Cmp(renewedCrt.SerialNumber) != 0 {
		t.Error("the post-renewal dial must present the renewed certificate")
	}
}

func TestLoopExpiredIdentityWithoutToken(t *testing.T) {
	lt := newLoopTest(t)
	// An identity whose certificate expired long ago, and no token.
	lt.withExpiredIdentity()
	l := lt.newLoop()
	if err := l.Run(context.Background()); !errors.Is(err, ErrNoEnrollment) {
		t.Fatalf("Run = %v, want ErrNoEnrollment", err)
	}
	if !strings.Contains(lt.logOutput(), "create a new enrollment token") {
		t.Errorf("the remediation must reach the log:\n%s", lt.logOutput())
	}
}

func TestLoopEnrollRefused(t *testing.T) {
	lt := newLoopTest(t)
	caInst := newTestCA(t)
	lt.withToken(caInst)
	lt.dialer.enrollConn = &fakeEnrollConn{respond: enrollResponder(caInst,
		nil, status.Error(codes.Unauthenticated, "enrollment refused"))}
	l := lt.newLoop()
	err := l.Run(context.Background())
	if !errors.Is(err, ErrEnrollRefused) {
		t.Fatalf("Run = %v, want ErrEnrollRefused", err)
	}
	if !strings.Contains(err.Error(), "enrollment refused") {
		t.Errorf("the refusal must carry the manager's answer: %v", err)
	}
	if !strings.Contains(lt.logOutput(), "create a new enrollment token") {
		t.Errorf("the remediation must reach the log:\n%s", lt.logOutput())
	}
}

func TestLoopEnrollUnreachableRetries(t *testing.T) {
	lt := newLoopTest(t)
	caInst := newTestCA(t)
	lt.withToken(caInst)
	lt.dialer.enrollConn = &fakeEnrollConn{respond: enrollResponder(caInst,
		nil, status.Error(codes.Unavailable, "connection refused"))}
	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	// Manager downtime is NOT an error exit: the loop backs off and
	// retries the enrollment until it succeeds.
	waitFor(t, 2*time.Second, func() bool {
		return len(lt.recordedSleeps()) >= 2 && len(lt.dialer.enrollConn.recorded()) >= 2
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil (shutdown, not failure)", err)
	}
}

func TestLoopNoSecretsInLogs(t *testing.T) {
	lt := newLoopTest(t)
	caInst := newTestCA(t)
	lt.withToken(caInst)
	lt.dialer.enrollConn = &fakeEnrollConn{respond: enrollResponder(caInst, nil, nil)}
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(stream.sentMessages()) > 0 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	out := lt.logOutput()
	if out == "" {
		t.Fatal("the loop must log at all")
	}
	// The enrollment token, the certificate material and the CA PEM must
	// never reach the log; the host id (a uuid) and the agent name may.
	for _, s := range []string{lt.cfg.EnrollToken, "pxa1", "-----BEGIN", "PRIVATE KEY"} {
		if strings.Contains(out, s) {
			t.Fatalf("log leaks %q:\n%s", s, out)
		}
	}
	if !strings.Contains(out, "host-1") || !strings.Contains(out, testAgentName) {
		t.Errorf("the log must carry the host id and agent name:\n%s", out)
	}
}

func TestLoopFactsPushErrorIsAdvisory(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	collector := &fakeCollector{err: errors.New("cpu probe failed")}
	lt.facts = collector
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	// A failed collection still pushes what it has (an empty map here) —
	// one probe hiccup must not stall the heartbeat loop.
	waitFor(t, 2*time.Second, func() bool { return len(stream.sentMessages()) > 0 })
	if collector.collectCalls() != 1 {
		t.Fatalf("facts collects = %d, want 1", collector.collectCalls())
	}
	factsMsg := stream.sentMessages()[0]
	if factsMsg.GetFacts() == nil {
		t.Fatal("the initial push must be a facts message")
	}
	intervals, tickers := lt.waitForTickers(3)
	if len(intervals) != 3 {
		t.Fatalf("ticker intervals = %v, want 3", intervals)
	}
	tickers[1].c <- time.Now() // the facts interval tick
	waitFor(t, 2*time.Second, func() bool { return collector.collectCalls() == 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if !strings.Contains(lt.logOutput(), "cpu probe failed") {
		t.Errorf("the collection error is advisory and must be logged:\n%s", lt.logOutput())
	}
}

func TestLoopRenewalDueDuringStreamEndsSession(t *testing.T) {
	lt := newLoopTest(t)
	id := lt.withIdentity()
	leaf := id.Leaf()

	// Start valid, but move the clock into the renewal window after the
	// stream is up: the next heartbeat tick must end the session (the
	// outer loop then renews and reconnects).
	lifetime := leaf.NotAfter.Sub(leaf.NotBefore)
	base := leaf.NotBefore.Add(time.Hour)
	lt.now = func() time.Time { return base }

	// Renewal response signed with the SAME key.
	renewedCSR, err := MakeCSR(id.Signer().(*ecdsa.PrivateKey), testAgentName)
	if err != nil {
		t.Fatal(err)
	}
	caInst := newTestCA(t)
	renewedPEM, renewedCrt, err := caInst.SignAgentCSR(renewedCSR, testOrgID, testAgentName)
	if err != nil {
		t.Fatal(err)
	}
	stream1 := newFakeStream()
	stream2 := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream1, stream2}, renewResp: &agentpb.EnrollResponse{CertPem: string(renewedPEM)}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(stream1.sentMessages()) > 0 })

	// Time passes into the renewal window; the heartbeat tick fires.
	lt.now = func() time.Time { return leaf.NotBefore.Add(2 * lifetime / 3).Add(time.Hour) }
	intervals, tickers := lt.waitForTickers(3)
	if len(intervals) != 3 {
		t.Fatalf("ticker intervals = %v, want 3", intervals)
	}
	tickers[0].c <- time.Now()
	// The loop renews and reconnects with the renewed certificate.
	waitFor(t, 2*time.Second, func() bool { return len(stream2.sentMessages()) > 0 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if len(lt.dialer.agentConn.renewed()) != 1 {
		t.Fatalf("renews = %d, want 1", len(lt.dialer.agentConn.renewed()))
	}
	if lt.dialer.enrollDials != 0 {
		t.Errorf("enroll dials = %d, want 0", lt.dialer.enrollDials)
	}
	if len(lt.dialer.agentLeaves) != 3 {
		t.Fatalf("agent dials = %d, want 3 (stream, renew, stream)", len(lt.dialer.agentLeaves))
	}
	if lt.dialer.agentLeaves[2].SerialNumber.Cmp(renewedCrt.SerialNumber) != 0 {
		t.Error("the post-renewal session must present the renewed certificate")
	}
}

// withIdentity persists a fresh enrollment into the loop test's data dir
// and returns the identity.
func (lt *loopTest) withIdentity() *Identity {
	lt.t.Helper()
	key, csrPEM := newTestKey(lt.t)
	certPEM, caPEM := issueAgentCert(lt.t, csrPEM)
	if err := SaveEnrollment(lt.dir, key, certPEM, caPEM, "host-1"); err != nil {
		lt.t.Fatalf("SaveEnrollment: %v", err)
	}
	id, err := LoadIdentity(lt.dir)
	if err != nil || id == nil {
		lt.t.Fatalf("LoadIdentity = %v, %v", id, err)
	}
	return id
}

// withExpiredIdentity persists an identity whose certificate expired a
// month ago (the CA clock is shifted back for the issuance).
func (lt *loopTest) withExpiredIdentity() {
	lt.t.Helper()
	caInst := expiredCA(lt.t, -100*24*time.Hour) // issued 100 days ago
	key, _ := newTestKey(lt.t)
	csrPEM, err := MakeCSR(key, testAgentName)
	if err != nil {
		lt.t.Fatal(err)
	}
	certPEM, _, err := caInst.SignAgentCSR(csrPEM, testOrgID, testAgentName)
	if err != nil {
		lt.t.Fatal(err)
	}
	_, caCert := caInst.Pool()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
	if err := SaveEnrollment(lt.dir, key, certPEM, caPEM, "host-1"); err != nil {
		lt.t.Fatal(err)
	}
}

// TestJitteredBackoffSubtractsUpToHalfTheRung pins the jitter's shape at
// both edges: with no jitter drawn the delay is exactly the ladder's own
// rung, with the maximum drawn it is exactly half of it, and nothing in
// between escapes [rung/2, rung]. Subtracting rather than adding is the
// property that matters — the 60 s cap stays a cap, and the first retry
// still happens within a second.
func TestJitteredBackoffSubtractsUpToHalfTheRung(t *testing.T) {
	for _, tt := range []struct {
		shift int
		draw  float64
		want  time.Duration
	}{
		{0, 0, time.Second},
		{0, 1, 500 * time.Millisecond},
		{0, 0.5, 750 * time.Millisecond},
		{3, 0, 8 * time.Second},
		{3, 1, 4 * time.Second},
		{6, 0, maxBackoff},
		{6, 1, maxBackoff / 2},
		{10, 0.25, maxBackoff - maxBackoff/8},
	} {
		l := &Loop{rand: func() float64 { return tt.draw }}
		if got := l.jitteredBackoff(tt.shift); got != tt.want {
			t.Errorf("jitteredBackoff(%d) with draw %v = %v, want %v", tt.shift, tt.draw, got, tt.want)
		}
	}
}

// TestEveryBackoffPathIsJittered covers all three places the loop backs
// off, because TestLoopReconnectBackoffIsJittered below covers only one of
// them: replacing l.jitteredBackoff with the bare ladder at the
// certificate-renewal site alone, leaving the other two jittered, passed
// the whole package. All three share one backoffShift and all three sit on
// the same fleet-wide-fault path — a manager restart fails renewals,
// streams and enrolments at the same instant — so a fix that reaches two
// of them is not the fix.
func TestEveryBackoffPathIsJittered(t *testing.T) {
	// The maximum draw: exactly half of the first rung, 500ms, wherever
	// the jitter is actually applied.
	const wantFirst = 500 * time.Millisecond

	t.Run("certificate renewal", func(t *testing.T) {
		lt := newLoopTest(t)
		id := lt.withIdentity()
		leaf := id.Leaf()
		lifetime := leaf.NotAfter.Sub(leaf.NotBefore)
		lt.now = func() time.Time { return leaf.NotBefore.Add(2 * lifetime / 3).Add(time.Hour) }
		lt.setJitter(1)
		lt.dialer.agentConn = &fakeAgentConn{renewErr: status.Error(codes.Unavailable, "manager restarting")}

		l := lt.newLoop()
		cancel, done := runLoop(t, l)
		waitFor(t, 2*time.Second, func() bool { return len(lt.recordedSleeps()) >= 1 })
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if got := lt.recordedSleeps()[0]; got != wantFirst {
			t.Fatalf("first renewal backoff = %v, want %v (the jittered rung, not the bare ladder)", got, wantFirst)
		}
	})

	t.Run("stream reconnect", func(t *testing.T) {
		lt := newLoopTest(t)
		lt.withIdentity()
		lt.setJitter(1)
		lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{newFakeStream()}}
		lt.dialer.agentDialErr = errors.New("manager unreachable")

		l := lt.newLoop()
		cancel, done := runLoop(t, l)
		waitFor(t, 2*time.Second, func() bool { return len(lt.recordedSleeps()) >= 1 })
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if got := lt.recordedSleeps()[0]; got != wantFirst {
			t.Fatalf("first reconnect backoff = %v, want %v", got, wantFirst)
		}
	})

	t.Run("unreachable enrollment", func(t *testing.T) {
		lt := newLoopTest(t)
		caInst := newTestCA(t)
		lt.withToken(caInst)
		lt.setJitter(1)
		lt.dialer.enrollConn = &fakeEnrollConn{respond: enrollResponder(caInst, nil, nil)}
		lt.dialer.enrollDialErr = errors.New("connection refused")

		l := lt.newLoop()
		cancel, done := runLoop(t, l)
		waitFor(t, 2*time.Second, func() bool { return len(lt.recordedSleeps()) >= 1 })
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if got := lt.recordedSleeps()[0]; got != wantFirst {
			t.Fatalf("first enrollment backoff = %v, want %v", got, wantFirst)
		}
	})
}

// TestLoopReconnectBackoffIsJittered proves the loop sleeps the JITTERED
// delay, not the bare ladder. Without it a fault that cuts every stream
// at once — a manager restart, a database failover — has the whole fleet
// reconnect in lockstep, rung by rung, into whatever is already failing;
// with it the same rung is spread across half its own width.
func TestLoopReconnectBackoffIsJittered(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	lt.setJitter(1) // the maximum draw: exactly half of each rung
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{newFakeStream()}}
	lt.dialer.agentDialErr = errors.New("manager unreachable")

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(lt.recordedSleeps()) >= 3 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	sleeps := lt.recordedSleeps()
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}
	for i, w := range want {
		if sleeps[i] != w {
			t.Fatalf("sleep %d = %v, want %v (the jittered rung, not the bare ladder) — all: %v", i, sleeps[i], w, sleeps[:len(want)])
		}
	}
}
