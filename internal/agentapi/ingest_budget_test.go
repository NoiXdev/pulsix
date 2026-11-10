// The per-agent check-result ingest budget (backlog 7.111): an agent over
// its budget is paced, never refused, and opening more streams buys no more.
package agentapi

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// refillPerSecond is the budget's sustained rate in observations a second.
const refillPerSecond = float64(checks.MaxTuplesPerHost) / float64(checks.MinInterval/time.Second)

// resultsBatch is one CheckResults message of n distinct observations.
func resultsBatch(n int, at time.Time) *agentpb.AgentMessage {
	rs := make([]*agentpb.CheckResult, n)
	for i := range rs {
		rs[i] = checkResultValue("cpu", "", "m"+strconv.Itoa(i), at, 1)
	}
	return &agentpb.AgentMessage{Body: &agentpb.AgentMessage_CheckResults{
		CheckResults: &agentpb.CheckResults{Results: rs}}}
}

// sendResultsBatch sends one batch and waits for its acknowledgement.
func sendResultsBatch(t *testing.T, stream agentpb.AgentService_AgentStreamClient, n int, at time.Time) {
	t.Helper()
	if err := stream.Send(resultsBatch(n, at)); err != nil {
		t.Fatal(err)
	}
	msg, err := recvWithin(t, stream, recvTimeout)
	if err != nil || !msg.GetAck() {
		t.Fatalf("check results ack = %+v, %v", msg, err)
	}
}

// budgetHarness enrols one agent whose checks store knows its host.
func budgetHarness(t *testing.T) (*harness, agentpb.AgentServiceClient) {
	t.Helper()
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	h.checks.knownHosts = map[uuid.UUID]bool{uuid.MustParse(resp.GetHostId()): true}
	return h, *client
}

// TestCheckResultsWithinBudgetAreStored: an agent reporting the most one
// host may hold (checks.MaxTuplesPerHost observations) every shortest check
// interval, for ever, is stored in full and never waits.
func TestCheckResultsWithinBudgetAreStored(t *testing.T) {
	h, client := budgetHarness(t)
	stream, err := client.AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const rounds = 10
	for range rounds {
		for left := checks.MaxTuplesPerHost; left > 0; {
			n := min(left, checks.MaxObservationsPerBatch)
			sendResultsBatch(t, stream, n, h.clock())
			left -= n
		}
		h.advance(checks.MinInterval)
	}
	if got, want := len(h.checks.observations()), rounds*checks.MaxTuplesPerHost; got != want {
		t.Fatalf("stored observations = %d, want all %d", got, want)
	}
	if slept := h.sleptTotal(); slept != 0 {
		t.Fatalf("a steady state at the budget waited %s, want never", slept)
	}
}

// TestAReplayBeyondTheBurstIsPacedAndStoredInFull: after a reconnect the
// agent replays its disconnect buffer back to back in 200-observation
// chunks. Nothing may be lost: the stream waits for the refill, which takes
// about as long as the excess over the burst needs at the sustained rate,
// and the host is not left looking offline meanwhile.
func TestAReplayBeyondTheBurstIsPacedAndStoredInFull(t *testing.T) {
	h, client := budgetHarness(t)
	stream, err := client.AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const total, chunk = 50_000, 200
	start := h.clock()
	touchesBefore := len(h.store.touches())
	for range total / chunk {
		sendResultsBatch(t, stream, chunk, h.clock())
	}
	if got := len(h.checks.observations()); got != total {
		t.Fatalf("stored = %d, want the whole replay of %d", got, total)
	}
	want := time.Duration(float64(total-2*checks.MaxTuplesPerHost) / refillPerSecond * float64(time.Second))
	if slept := h.sleptTotal(); slept < want-time.Second || slept > want+time.Second {
		t.Fatalf("paced for %s, want about %s", slept, want)
	}
	if elapsed := h.clock().Sub(start); elapsed < want-time.Second {
		t.Fatalf("replay finished after %s of clock, want at least %s", elapsed, want-time.Second)
	}
	// Pacing outlasts a heartbeat interval several times over; last_seen
	// must keep moving or the agent is shown offline (agents.StaleAfter).
	if got := len(h.store.touches()) - touchesBefore; got < int(want/(30*time.Second)) {
		t.Fatalf("last_seen refreshed %d times during %s of pacing, want one per 30 s", got, want)
	}
}

// TestOpeningStreamsDoesNotBuyBudget: the budget belongs to the agent, not
// the stream. Reconnecting for every batch of a flood admits no more than
// the burst plus what the elapsed time refills.
func TestOpeningStreamsDoesNotBuyBudget(t *testing.T) {
	h, client := budgetHarness(t)
	const streams, perStream = 20, 8
	start := h.clock()
	for range streams {
		stream, err := client.AgentStream(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for range perStream {
			sendResultsBatch(t, stream, checks.MaxObservationsPerBatch, h.clock())
		}
		if err := stream.CloseSend(); err != nil {
			t.Fatal(err)
		}
	}
	stored := len(h.checks.observations())
	if want := streams * perStream * checks.MaxObservationsPerBatch; stored != want {
		t.Fatalf("stored = %d, want all %d (paced, not dropped)", stored, want)
	}
	elapsed := h.clock().Sub(start)
	admissible := float64(2*checks.MaxTuplesPerHost) + refillPerSecond*elapsed.Seconds()
	if float64(stored) > admissible+1 {
		t.Fatalf("stored %d observations in %s, more than the burst plus refill allows (%.0f)", stored, elapsed, admissible)
	}
}

// TestStreamsOfOneAgentShareOneBudget: two open streams of an agent draw on
// one bucket.
func TestStreamsOfOneAgentShareOneBudget(t *testing.T) {
	h, client := budgetHarness(t)
	a, err := client.AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 * checks.MaxTuplesPerHost / checks.MaxObservationsPerBatch {
		sendResultsBatch(t, a, checks.MaxObservationsPerBatch, h.clock())
	}
	if slept := h.sleptTotal(); slept != 0 {
		t.Fatalf("the burst waited %s", slept)
	}
	sendResultsBatch(t, b, checks.MaxObservationsPerBatch, h.clock())
	if slept := h.sleptTotal(); slept == 0 {
		t.Fatal("the second stream's batch did not wait although the first drained the shared burst")
	}
}

// TestCancellingAPacedStreamReturnsWithoutStoring: a wait ends with the
// stream's context and the batch it was waiting for is not stored.
func TestCancellingAPacedStreamReturnsWithoutStoring(t *testing.T) {
	h, client := budgetHarness(t)
	entered := make(chan struct{})
	returned := make(chan struct{})
	var once sync.Once
	h.mu.Lock()
	h.sleepHook = func(ctx context.Context, _ time.Duration) error {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		close(returned)
		return ctx.Err()
	}
	h.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := client.AgentStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	burst := 2 * checks.MaxTuplesPerHost / checks.MaxObservationsPerBatch
	for range burst {
		sendResultsBatch(t, stream, checks.MaxObservationsPerBatch, h.clock())
	}
	if err := stream.Send(resultsBatch(checks.MaxObservationsPerBatch, h.clock())); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(recvTimeout):
		t.Fatal("the over-budget batch did not wait")
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(recvTimeout):
		t.Fatal("cancelling the stream did not end the wait")
	}
	if got, want := len(h.checks.observations()), burst*checks.MaxObservationsPerBatch; got != want {
		t.Fatalf("stored = %d, want %d: the cancelled batch must not be stored", got, want)
	}
}

// TestAnOversizedBatchIsRefusedBeforeItIsCharged: a lying agent keeps the
// InvalidArgument it always got and does not move the budget.
func TestAnOversizedBatchIsRefusedBeforeItIsCharged(t *testing.T) {
	h, client := budgetHarness(t)
	stream, err := client.AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(resultsBatch(checks.MaxObservationsPerBatch+1, h.clock())); err != nil {
		t.Fatal(err)
	}
	if _, err := recvWithin(t, stream, recvTimeout); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversized batch = %v, want InvalidArgument", err)
	}
	if got := len(h.checks.observations()); got != 0 {
		t.Fatalf("stored %d observations of a refused batch", got)
	}
	h.svc.streams.mu.Lock()
	defer h.svc.streams.mu.Unlock()
	for serial, b := range h.svc.streams.ingest {
		if b.tokens != ingestBurst {
			t.Fatalf("budget of %s = %.0f after a refused batch, want untouched %d", serial, b.tokens, ingestBurst)
		}
	}
}

// TestThrottledCheckResultsAreLoggedOncePerMinute: pacing is one log line a
// minute per stream, carrying what was paced since the previous one.
func TestThrottledCheckResultsAreLoggedOncePerMinute(t *testing.T) {
	h, client := budgetHarness(t)
	stream, err := client.AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const batch = checks.MaxObservationsPerBatch
	for range 8 { // the burst
		sendResultsBatch(t, stream, batch, h.clock())
	}
	const msg = "throttled by the ingest budget"
	for range 5 { // about 12 s of waiting
		sendResultsBatch(t, stream, batch, h.clock())
	}
	if n := strings.Count(h.logs.String(), msg); n != 1 {
		t.Fatalf("log lines after five paced batches = %d, want 1:\n%s", n, h.logs.String())
	}
	for range 40 { // about 98 s more
		sendResultsBatch(t, stream, batch, h.clock())
	}
	if n := strings.Count(h.logs.String(), msg); n != 2 {
		t.Fatalf("log lines after 110 s of pacing = %d, want 2:\n%s", n, h.logs.String())
	}
}

// TestIngestReservations pins the bucket arithmetic without a stream.
func TestIngestReservations(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	r := NewStreamRegistry()
	if w := r.reserveIngest("s", t0, 2*checks.MaxTuplesPerHost); w != 0 {
		t.Fatalf("a fresh bucket must hold the whole burst, waits %s", w)
	}
	if w, want := r.reserveIngest("s", t0, checks.MaxTuplesPerHost), checks.MinInterval; w != want {
		t.Fatalf("one refill short waits %s, want %s", w, want)
	}
	if w := r.reserveIngest("s", t0, 1); w <= checks.MinInterval {
		t.Fatalf("a second reservation must queue behind the first, waits %s", w)
	}
	if w := r.reserveIngest("other", t0, 1); w != 0 {
		t.Fatalf("another agent's budget is separate, waits %s", w)
	}
	r.refundIngest("s", 1)
	if w := r.reserveIngest("s", t0.Add(-time.Hour), 0); w <= 0 {
		t.Fatalf("a clock stepping backwards must not refill, waits %s", w)
	}
	// A day later the bucket caps at the burst; a swept entry is fresh.
	later := t0.Add(24 * time.Hour)
	if w := r.reserveIngest("s", later, 2*checks.MaxTuplesPerHost); w != 0 {
		t.Fatalf("an idle bucket must hold the burst, waits %s", w)
	}
	if w := r.reserveIngest("s", later, 0); w != 0 {
		t.Fatalf("an empty reservation waits %s", w)
	}
	r.reserveIngest("third", later.Add(checks.MinInterval), 1)
	r.mu.Lock()
	_, swept := r.ingest["other"]
	r.mu.Unlock()
	if swept {
		t.Fatal("a bucket that is full again must be swept")
	}
}

// TestIngestReservationsAreSafeConcurrently: streams of one agent reserve
// from their own goroutines; the debt they take adds up exactly.
func TestIngestReservationsAreSafeConcurrently(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	r := NewStreamRegistry()
	var wg sync.WaitGroup
	waits := make(chan time.Duration, 64)
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			waits <- r.reserveIngest("s", t0, checks.MaxObservationsPerBatch)
		}()
	}
	wg.Wait()
	close(waits)
	var longest time.Duration
	for w := range waits {
		longest = max(longest, w)
	}
	debt := float64(64*checks.MaxObservationsPerBatch - 2*checks.MaxTuplesPerHost)
	want := time.Duration(debt / refillPerSecond * float64(time.Second))
	if longest < want-time.Millisecond || longest > want+time.Millisecond {
		t.Fatalf("longest wait = %s, want %s", longest, want)
	}
}
