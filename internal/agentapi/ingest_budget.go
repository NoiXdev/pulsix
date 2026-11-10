package agentapi

import (
	"time"

	"github.com/noixdev/pulsix/internal/checks"
)

// The check-result ingest budget of one agent stream (backlog 7.111).
//
// checks.MaxObservationsPerBatch bounds one message, but nothing bounded how
// many messages one stream may send: RecordObservations costs two statements
// per observation inside one transaction on the shared pool, so an agent
// looping on maximum batches turned its single certificate into a denial of
// service against every tenant. The budget counts observations, not
// messages, because the cost is per observation.
//
// A host cannot legitimately report more than checks.MaxTuplesPerHost
// distinct tuples (backlog 7.57), and nothing reports a tuple faster than the
// shortest check interval (checks.MinInterval), so that is the refill. The
// burst is twice it, which absorbs a reconnect's replay of the disconnect
// buffer (15 minutes of results, chunked, internal/agent/session.go) arriving
// together with the live results without ever throttling an agent that
// behaves.
const (
	ingestRefill      = checks.MaxTuplesPerHost
	ingestRefillEvery = checks.MinInterval
	ingestBurst       = 2 * checks.MaxTuplesPerHost

	// ingestDropLogEvery is the least time between two log lines about
	// dropped batches on one stream. Pulsix has no metrics endpoint yet
	// (backlog 3.27), so the line carries the counts a counter would.
	ingestDropLogEvery = time.Minute
)

// ingestBudget is a token bucket over observations, owned by one
// receiveMessages loop and so used from one goroutine only. Time is passed
// in, never read: the service's injected clock drives it.
type ingestBudget struct {
	tokens float64
	last   time.Time

	// What was dropped since the last log line, and when that was.
	droppedBatches, droppedObservations int
	lastDropLog                         time.Time
	loggedDrop                          bool
}

// newIngestBudget returns a full bucket, as of now.
func newIngestBudget(now time.Time) *ingestBudget {
	return &ingestBudget{tokens: ingestBurst, last: now}
}

// allow takes n observations from the bucket as of now and reports whether
// it held them. A batch is all or nothing: a refused one takes nothing, so a
// flood of large batches cannot starve the small ones behind it more than
// its own size. A clock that steps backwards refills nothing.
func (b *ingestBudget) allow(now time.Time, n int) bool {
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(ingestBurst, b.tokens+float64(ingestRefill)*float64(elapsed)/float64(ingestRefillEvery))
		b.last = now
	}
	if float64(n) > b.tokens {
		return false
	}
	b.tokens -= float64(n)
	return true
}

// drop records one refused batch of n observations and reports whether this
// one is due a log line (at most one per ingestDropLogEvery), and if so how
// many batches and observations it stands for since the previous line.
func (b *ingestBudget) drop(now time.Time, n int) (logIt bool, batches, observations int) {
	b.droppedBatches++
	b.droppedObservations += n
	if b.loggedDrop && now.Sub(b.lastDropLog) < ingestDropLogEvery {
		return false, 0, 0
	}
	batches, observations = b.droppedBatches, b.droppedObservations
	b.droppedBatches, b.droppedObservations = 0, 0
	b.lastDropLog, b.loggedDrop = now, true
	return true, batches, observations
}
