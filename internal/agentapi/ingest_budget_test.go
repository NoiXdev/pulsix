// The per-stream check-result ingest budget (backlog 7.111).
package agentapi

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// resultsBatch is one CheckResults message of n distinct observations.
func resultsBatch(n int, at time.Time) *agentpb.AgentMessage {
	rs := make([]*agentpb.CheckResult, n)
	for i := range rs {
		rs[i] = checkResultValue("cpu", "", "m"+strconv.Itoa(i), at, 1)
	}
	return &agentpb.AgentMessage{Body: &agentpb.AgentMessage_CheckResults{
		CheckResults: &agentpb.CheckResults{Results: rs}}}
}

// sendResultsBatch sends one batch and waits for its acknowledgement: a
// dropped batch is acknowledged like a stored one (the agent never waits
// for the ack and never resends on it, so what matters is that the stream
// stays open and nothing errors).
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

// TestCheckResultsWithinBudgetAreStored: an agent reporting the most one
// host may hold (checks.MaxTuplesPerHost observations) every shortest check
// interval, for ever, is never throttled.
func TestCheckResultsWithinBudgetAreStored(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	h.checks.knownHosts = map[uuid.UUID]bool{uuid.MustParse(resp.GetHostId()): true}
	stream, err := (*client).AgentStream(t.Context())
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
}

// TestCheckResultsOverBudgetAreDropped: batches back to back drain the
// burst (2 * MaxTuplesPerHost); after that the store sees nothing more
// until the clock advances, the stream stays open and every batch is still
// acknowledged.
func TestCheckResultsOverBudgetAreDropped(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	h.checks.knownHosts = map[uuid.UUID]bool{uuid.MustParse(resp.GetHostId()): true}
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const batch = checks.MaxObservationsPerBatch
	burstBatches := 2 * checks.MaxTuplesPerHost / batch // 8
	for range burstBatches + 5 {
		sendResultsBatch(t, stream, batch, h.clock())
	}
	if got, want := len(h.checks.observations()), burstBatches*batch; got != want {
		t.Fatalf("stored after the burst = %d, want exactly the burst %d", got, want)
	}

	// Ten seconds refill MaxTuplesPerHost observations: 192 left over plus
	// 4096 is room for four more batches, not five.
	h.advance(checks.MinInterval)
	for range 5 {
		sendResultsBatch(t, stream, batch, h.clock())
	}
	if got, want := len(h.checks.observations()), (burstBatches+4)*batch; got != want {
		t.Fatalf("stored after one refill = %d, want %d", got, want)
	}
}

// TestCheckResultsBudgetBelongsToTheStream: a second stream of the agent
// has its own budget, so draining one does not starve the other.
func TestCheckResultsBudgetBelongsToTheStream(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	h.checks.knownHosts = map[uuid.UUID]bool{uuid.MustParse(resp.GetHostId()): true}
	first, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for range 12 {
		sendResultsBatch(t, first, checks.MaxObservationsPerBatch, h.clock())
	}
	before := len(h.checks.observations())
	second, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sendResultsBatch(t, second, checks.MaxObservationsPerBatch, h.clock())
	if got := len(h.checks.observations()); got != before+checks.MaxObservationsPerBatch {
		t.Fatalf("stored after the second stream's batch = %d, want %d", got, before+checks.MaxObservationsPerBatch)
	}
}

// TestDroppedCheckResultsAreLoggedOncePerMinute: a flood is one log line a
// minute per stream, carrying what was dropped since the previous one.
func TestDroppedCheckResultsAreLoggedOncePerMinute(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	h.checks.knownHosts = map[uuid.UUID]bool{uuid.MustParse(resp.GetHostId()): true}
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const batch = checks.MaxObservationsPerBatch
	for range 8 { // the burst
		sendResultsBatch(t, stream, batch, h.clock())
	}
	const msg = "agent check results over the ingest budget"
	for range 4 { // four drops at one instant: one line
		sendResultsBatch(t, stream, batch, h.clock())
	}
	if n := strings.Count(h.logs.String(), msg); n != 1 {
		t.Fatalf("log lines after four drops at once = %d, want 1:\n%s", n, h.logs.String())
	}
	h.advance(30 * time.Second) // refilled, so the burst drains again, then drops
	for range 8 {
		sendResultsBatch(t, stream, batch, h.clock())
	}
	if n := strings.Count(h.logs.String(), msg); n != 1 {
		t.Fatalf("log lines 30 s later = %d, want still 1:\n%s", n, h.logs.String())
	}
	h.advance(time.Minute)
	for range 12 {
		sendResultsBatch(t, stream, batch, h.clock())
	}
	if n := strings.Count(h.logs.String(), msg); n != 2 {
		t.Fatalf("log lines a minute on = %d, want 2:\n%s", n, h.logs.String())
	}
}

// TestIngestBudget pins the bucket arithmetic without a stream.
func TestIngestBudget(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	b := newIngestBudget(t0)
	if !b.allow(t0, 2*checks.MaxTuplesPerHost) {
		t.Fatal("a fresh bucket must hold the whole burst")
	}
	if b.allow(t0, 1) {
		t.Fatal("an emptied bucket admitted one more observation")
	}
	if b.allow(t0.Add(-time.Hour), 1) {
		t.Fatal("a clock stepping backwards must not refill the bucket")
	}
	if !b.allow(t0.Add(checks.MinInterval), checks.MaxTuplesPerHost) {
		t.Fatal("one interval must refill MaxTuplesPerHost")
	}
	if b.allow(t0.Add(checks.MinInterval), 1) {
		t.Fatal("more than the refill was admitted")
	}
	if !b.allow(t0.Add(24*time.Hour), 2*checks.MaxTuplesPerHost) || b.allow(t0.Add(24*time.Hour), 1) {
		t.Fatal("a long idle must cap at the burst, not accumulate")
	}
	if !b.allow(t0.Add(24*time.Hour), 0) {
		t.Fatal("an empty batch costs nothing")
	}
}
