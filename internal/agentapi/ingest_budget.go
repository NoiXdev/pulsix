package agentapi

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checks"
)

// The check-result ingest budget of one agent identity (backlog 7.111).
//
// checks.MaxObservationsPerBatch bounds one message, but nothing bounded how
// many messages an agent may send: RecordObservations costs two statements
// per observation inside one transaction on the shared pool, so an agent
// looping on maximum batches turned its single certificate into a denial of
// service against every tenant. The budget counts observations, not
// messages, because the cost is per observation.
//
// A host cannot legitimately report more than checks.MaxTuplesPerHost
// distinct tuples (backlog 7.57), and nothing reports a tuple faster than the
// shortest check interval (checks.MinInterval), so that is the refill. The
// burst is twice it, which lets a reconnect's first replay chunks and the
// live results arrive together without waiting.
//
// An agent over its budget is PACED, never refused: the stream's receive
// loop waits until the bucket holds the batch, then stores it. Nothing is
// lost, because the agent's disconnect buffer (internal/agent/checks, 15
// minutes, bounded by age; the probe results' ring also by size, backlog
// 3.78) is replayed back to back after a reconnect and a host with many
// tuples legitimately exceeds the burst there; the wait reaches the agent
// as HTTP/2 flow control on its Send. The bucket is kept per agent serial
// in the StreamRegistry, shared by all the streams of that serial, so
// opening another stream (or reconnecting) buys no budget, exactly as
// heartbeatResolveInterval does for resolves.
//
// Check results and probe results each have a bucket of this size
// (backlog 3.78): an agent probing up to probes.MaxAssignmentsPerAgent
// slots for other hosts must not delay its own checks at the budget, nor
// its own checks the probes. 4096 observations per 10 s is 20 a slot at
// 200 slots on the shortest interval, 120 at the usual 60 s. The kind is
// the message type, so an agent that sends no probe results (an older
// one, or one without assignments) is paced exactly as before; one serial
// may store at most twice what one bucket refills.
const (
	ingestRefill      = checks.MaxTuplesPerHost
	ingestRefillEvery = checks.MinInterval
	ingestBurst       = 2 * checks.MaxTuplesPerHost

	// ingestThrottleLogEvery is the least time between two log lines about
	// a throttled stream. Pulsix has no metrics endpoint yet (backlog
	// 3.27), so the line carries the counts a counter would.
	ingestThrottleLogEvery = time.Minute
)

// ingestKind is which of an agent's budgets a results message draws on.
type ingestKind uint8

const (
	ingestChecks ingestKind = iota
	ingestProbes
	// ingestKinds is how many kinds there are.
	ingestKinds
)

func (k ingestKind) String() string {
	if k == ingestProbes {
		return "probe results"
	}
	return "check results"
}

// ingestKey is one bucket: an agent serial's budget for one kind.
type ingestKey struct {
	serial string
	kind   ingestKind
}

// ingestBucket is a token bucket over observations, guarded by the
// registry's mutex. Tokens go negative while a reservation waits, which is
// what keeps concurrent streams of one serial fair: each takes its own debt
// and waits for exactly that. Time is passed in, never read.
type ingestBucket struct {
	tokens float64
	last   time.Time
}

func (b *ingestBucket) refill(now time.Time) {
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(ingestBurst, b.tokens+float64(ingestRefill)*float64(elapsed)/float64(ingestRefillEvery))
		b.last = now
	}
}

// full reports whether, as of now, the bucket is indistinguishable from a
// fresh one and may be forgotten.
func (b *ingestBucket) full(now time.Time) bool {
	b.refill(now)
	return b.tokens >= ingestBurst
}

// reserveIngest takes n observations from serial's bucket of kind as of now and
// returns how long the caller must wait before storing them: zero when the
// bucket held them. A clock stepping backwards refills nothing. Buckets that
// are full again are swept once per ingestRefillEvery, so the map holds at
// most the agents that were throttled or active recently.
func (r *StreamRegistry) reserveIngest(serial string, kind ingestKind, now time.Time, n int) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := ingestKey{serial, kind}
	if now.Sub(r.lastIngestSweep) >= ingestRefillEvery {
		for k, b := range r.ingest {
			if k != key && b.full(now) {
				delete(r.ingest, k)
			}
		}
		r.lastIngestSweep = now
	}
	b, ok := r.ingest[key]
	if !ok {
		b = &ingestBucket{tokens: ingestBurst, last: now}
		r.ingest[key] = b
	}
	b.refill(now)
	b.tokens -= float64(n)
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(math.Ceil(-b.tokens / float64(ingestRefill) * float64(ingestRefillEvery)))
}

// refundIngest returns a reservation that was never used (the wait was
// cancelled).
func (r *StreamRegistry) refundIngest(serial string, kind ingestKind, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.ingest[ingestKey{serial, kind}]; ok {
		b.tokens = min(ingestBurst, b.tokens+float64(n))
	}
}

// ingestPacer is one receive loop's own state for pacing: what to say in
// the log and when this stream last proved liveness.
type ingestPacer struct {
	lastTouch time.Time

	// logs is the throttle line's state per message type, so each type
	// has its own once-a-minute line and counts.
	logs [ingestKinds]throttleLog
}

// throttleLog is one message type's pacing since its last log line.
type throttleLog struct {
	lastLog       time.Time
	logged        bool
	waited        time.Duration
	waitedBatches int
}

func newIngestPacer(now time.Time) *ingestPacer { return &ingestPacer{lastTouch: now} }

// pace charges a results message of kind against the agent's budget for
// that kind and waits
// until the budget holds it. It runs before the batch is stored and outside
// the stream's processing lock, so a revocation never waits on a paced
// stream (shut cancels ctx, which ends the wait). A batch over
// checks.MaxObservationsPerBatch is refused before it is charged, as the
// checks service would refuse it: a lying agent keeps its InvalidArgument
// and loses the stream, instead of reserving an unbounded debt.
//
// While the loop waits it reads nothing, so a heartbeat queued behind the
// batch waits too, and the registry's staleness verdict (agents.OnlineWindow,
// three of the agent's heartbeat intervals) would mark a host offline
// during a long replay. A paced stream is demonstrably alive, so the wait
// is sliced at the default heartbeat interval and last_seen is refreshed
// between slices. The default, not the agent's own interval, on purpose:
// every agent's online window is at least agents.OnlineWindowFloor, three
// default intervals, so a refresh every default interval keeps any agent
// online, and a shorter slice only costs a touch per 30 s of waiting.
func (s *Service) pace(ctx context.Context, agent agents.AgentIdentity, p *ingestPacer, kind ingestKind, n int) error {
	if n > checks.MaxObservationsPerBatch {
		return fmt.Errorf("%w: batch of %d observations exceeds %d",
			errCheckResultsRejected, n, checks.MaxObservationsPerBatch)
	}
	wait := s.streams.reserveIngest(agent.Serial, kind, s.deps.Clock(), n)
	if wait <= 0 {
		return nil
	}
	p.noteThrottled(s, ctx, agent, kind, wait)
	for remaining := wait; remaining > 0; {
		slice := min(remaining, agents.HeartbeatInterval)
		if err := s.deps.Sleep(ctx, slice); err != nil {
			s.streams.refundIngest(agent.Serial, kind, n)
			return err
		}
		remaining -= slice
		if now := s.deps.Clock(); now.Sub(p.lastTouch) >= agents.HeartbeatInterval {
			if err := s.deps.Agents.Touch(ctx, agent.Serial, agent.Version); err != nil {
				slog.WarnContext(ctx, "refresh last_seen of a throttled agent stream",
					"serial", agent.Serial, "err", err)
			}
			p.lastTouch = now
		}
	}
	return nil
}

// noteThrottled counts one paced batch of kind and logs at most once per
// ingestThrottleLogEvery per kind, the first at once, each line naming the
// message type and standing for what of it was paced since its previous
// line.
func (p *ingestPacer) noteThrottled(s *Service, ctx context.Context, agent agents.AgentIdentity, kind ingestKind, wait time.Duration) {
	l := &p.logs[kind]
	l.waitedBatches++
	l.waited += wait
	now := s.deps.Clock()
	if l.logged && now.Sub(l.lastLog) < ingestThrottleLogEvery {
		return
	}
	log := s.deps.Log
	if log == nil {
		log = slog.Default()
	}
	log.WarnContext(ctx, "agent "+kind.String()+" throttled by the ingest budget; delaying, not dropping",
		"serial", agent.Serial, "org_id", agent.OrgID, "host_id", agent.HostID,
		"throttled_batches", l.waitedBatches, "total_wait", l.waited,
		"refill_per_interval", ingestRefill, "interval", ingestRefillEvery, "burst", ingestBurst)
	l.waitedBatches, l.waited = 0, 0
	l.lastLog, l.logged = now, true
}

// sleepCtx waits d or until ctx ends, whichever is first; Deps.Sleep's
// default.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
