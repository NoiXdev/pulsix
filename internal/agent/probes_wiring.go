// The probe runner's wiring (backlog 3.13a): the manager assigns this
// agent synthetic probes of other hosts as a ProbeConfig on a heartbeat's
// acknowledgement; internal/agent/probes runs them, and their results go
// back as ProbeResults through the same sender, chunking and 15-minute
// disconnect ring as check results — in a ring of their own, replayed
// after the check results' on a reconnect, and capped by size as well as
// by age (backlog 3.78).
package agent

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/probes"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probeconfig"
)

// withProbeRegistry replaces the probes the runner runs (tests: fakes, so
// no test dials anything).
func withProbeRegistry(reg probe.Registry) LoopOption {
	return func(l *Loop) { l.probeRegistry = reg }
}

// withProbeTicker replaces the probe runner's ticker factory only, so the
// session's ticker indices stay what every cadence test counts on.
func withProbeTicker(fn tickerFactory) LoopOption {
	return func(l *Loop) { l.probeTicker = fn }
}

// newProbeRunner builds the runner on the loop's clock, logger and ticker
// factory (or the one a test set apart). The registry is
// probes.DefaultRegistry unless a test replaced it, and the dial guard
// probe.NewGuard(nil, nil), to which agent.yaml's deny list is added
// (applyProbePolicy).
func (l *Loop) newProbeRunner() *probes.Runner {
	tf := l.ticker
	if l.probeTicker != nil {
		tf = l.probeTicker
	}
	return probes.NewRunner(probes.Deps{
		Registry: l.probeRegistry,
		Now:      l.now,
		Ticker:   func(d time.Duration) probes.Ticker { return tf(d) },
		Log:      l.log,
	})
}

// maxProbeBufferBytes caps the probe ring at an estimated 4 MiB
// (probeResultCost), about 30 000 results. 200 slots of five metrics at a
// 60 s interval fill the 15-minute window with 15 000, so at ordinary
// intervals the window rules; at the shortest interval (10 s, 90 000
// results a window, tens of MB) the ring keeps the newest few minutes.
// The agent is a monitored host first: a long outage must not grow it by
// the size of everything it probes for others.
const maxProbeBufferBytes = 4 << 20

// probeResultOverhead is a buffered result's own memory: the struct (120
// bytes on 64-bit) and the 8-byte box of its value.
const probeResultOverhead = 128

// probeResultCost estimates what one buffered result holds: its overhead
// plus the text of its item, metric, label and error — the error and label
// may quote what a target answered. The check key is the slot spec's own
// string, shared by every result of the slot, and is not counted.
func probeResultCost(r probes.Result) int {
	n := probeResultOverhead + len(r.Item) + len(r.Metric) + len(r.Err)
	if r.Label != nil {
		n += len(*r.Label)
	}
	return n
}

// newProbeBuffer is the probe results' disconnect ring: the check ring's
// window, each result stamped by its round's At, and capped at
// maxProbeBufferBytes (oldest rounds dropped first, whole).
func newProbeBuffer(now func() time.Time) *checks.Ring[probes.Result] {
	return checks.NewCappedRing(bufferWindow, maxProbeBufferBytes, probeResultCost, now,
		func(r probes.Result) time.Time { return r.At })
}

// probeEvictWarnEvery is the least time between two warnings about probe
// results the ring dropped while disconnected: at its cap the ring evicts
// on almost every round (20 a second with 200 slots at 10 s), and one line
// each would fill the host's journal, where the agent logs.
const probeEvictWarnEvery = time.Minute

// probeEvictWarner rate-limits that warning and sums what each line
// covers. The probe sink runs on the runner's goroutines, so it guards
// itself.
type probeEvictWarner struct {
	mu      sync.Mutex
	last    time.Time
	pending checks.Evicted // dropped since the last line that went out
}

// note adds e and reports whether a line is due now and what it covers:
// the first drop at once, then at most one line per probeEvictWarnEvery.
func (w *probeEvictWarner) note(now time.Time, e checks.Evicted) (covers checks.Evicted, due bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending.Aged += e.Aged
	w.pending.OverCap += e.OverCap
	if w.pending == (checks.Evicted{}) || (!w.last.IsZero() && now.Sub(w.last) < probeEvictWarnEvery) {
		return checks.Evicted{}, false
	}
	covers, w.pending, w.last = w.pending, checks.Evicted{}, now
	return covers, true
}

// flush takes what is pending without waiting for the period.
func (w *probeEvictWarner) flush() checks.Evicted {
	w.mu.Lock()
	defer w.mu.Unlock()
	covers := w.pending
	w.pending = checks.Evicted{}
	return covers
}

// logProbeEvictions reports what the probe ring dropped, why (aged past
// the window, or over the size cap) and in which situation (what), and
// nothing when it dropped nothing.
func (l *Loop) logProbeEvictions(what string, e checks.Evicted, attrs ...any) {
	if e.Aged+e.OverCap == 0 {
		return
	}
	l.log.Warn(what, append([]any{"dropped", e.Aged + e.OverCap, "aged_out", e.Aged,
		"over_cap", e.OverCap, "cap_bytes", maxProbeBufferBytes}, attrs...)...)
}

// applyProbeConfig installs a pushed ProbeConfig. One that cannot be read
// (a target id that is not a canonical UUID) leaves the running set and
// its version alone, so the heartbeat keeps reporting the old version and
// the manager sends its set again. One that arrives while agent.yaml
// turns probing off (a heartbeat sent before the file changed) is refused.
func (l *Loop) applyProbeConfig(pc *agentpb.ProbeConfig) {
	specs, err := probeconfig.FromProto(pc)
	if err != nil {
		l.log.Warn("probe configuration push could not be read; the running probes are kept "+
			"and this configuration is not reported as applied",
			"version", pc.GetVersion(), "err", err)
		return
	}
	if !l.probes.Apply(specs) {
		l.log.Warn("probe configuration refused: probing for other hosts is off on this host",
			"version", pc.GetVersion(), "probes", len(specs))
	}
}

// handleProbeResults is the probe runner's sink, handleResults' twin: one
// round's results to the current session's sender, or to the probe ring
// when no stream is up (or the send just failed and no newer stream is
// live), by the same routeResults. It runs on the runner's probe
// goroutines.
func (l *Loop) handleProbeResults(results []probes.Result) {
	results = l.validUTF8ProbeResults(l.redactProbeResults(results))
	if len(results) == 0 {
		return
	}
	l.status.probeResults(results)
	routeResults(l, "probe results", results, (*streamSender).sendProbeResultsChunked, func(rest []probes.Result) {
		if covers, due := l.probeEvictWarn.note(l.now(), l.probeBuffer.Put(rest)); due {
			l.logProbeEvictions("probe result buffer overflowed while disconnected; oldest samples dropped",
				covers, "every", probeEvictWarnEvery)
		}
	})
}

// replayProbeResults drains the probe ring over sender — activateSender's
// second half, under routeMu like the first. On a failure only what did
// not go out goes back (unsentAfter), and what the put-back evicts is
// logged.
func (l *Loop) replayProbeResults(sender *streamSender) error {
	// What the rate-limited warning has not reported yet is reported now:
	// the outage is over, and its count must not wait for the next one.
	l.logProbeEvictions("probe results dropped while disconnected; oldest samples dropped",
		l.probeEvictWarn.flush())
	pending := l.probeBuffer.Drain()
	sent, err := sender.sendProbeResultsChunked(pending)
	if err == nil {
		return nil
	}
	l.logProbeEvictions("probe results dropped from the buffer while a reconnect replay failed; samples dropped",
		l.probeBuffer.Put(unsentAfter(l.log, "probe results", pending, sent, err)), "replayed", sent, "err", err)
	return err
}

// sendProbeResultsChunked sends results as ProbeResults messages of at
// most maxResultsPerMessage each — the manager holds probe results to the
// same batch bound as check results — and reports how many went out.
func (s *streamSender) sendProbeResultsChunked(results []probes.Result) (int, error) {
	return sendChunked(results, func(chunk []probes.Result) error {
		return s.send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_ProbeResults{
			ProbeResults: &agentpb.ProbeResults{Results: toProbeResultProtos(chunk)},
		}})
	})
}

func toProbeResultProtos(results []probes.Result) []*agentpb.ProbeResult {
	out := make([]*agentpb.ProbeResult, 0, len(results))
	for _, r := range results {
		pr := &agentpb.ProbeResult{
			TargetHostId: r.TargetHostID.String(),
			CheckKey:     r.CheckKey,
			Item:         r.Item,
			Metric:       r.Metric,
			At:           timestamppb.New(r.At),
		}
		switch {
		case r.Value != nil:
			pr.Observation = &agentpb.ProbeResult_Value{Value: *r.Value}
		case r.Label != nil:
			pr.Observation = &agentpb.ProbeResult_Label{Label: *r.Label}
		case r.Err != "":
			// A probe's error may quote what a target sent; gRPC cannot
			// encode text that is not UTF-8.
			pr.Observation = &agentpb.ProbeResult_Error{Error: strings.ToValidUTF8(r.Err, "�")}
		}
		out = append(out, pr)
	}
	return out
}

// validUTF8ProbeResults is validUTF8Results for probe results: a result
// whose check key, item, metric or label is not valid UTF-8 (a label or
// item can carry what a target answered) is dropped before it is buffered
// or sent, and logged at most once per invalidUTF8WarnEvery; an error
// message is sanitised where it is converted instead.
func (l *Loop) validUTF8ProbeResults(results []probes.Result) []probes.Result {
	valid, copied := results, false
	for i, r := range results {
		if utf8.ValidString(r.CheckKey) && utf8.ValidString(r.Item) && utf8.ValidString(r.Metric) &&
			(r.Label == nil || utf8.ValidString(*r.Label)) {
			if copied {
				valid = append(valid, r)
			}
			continue
		}
		if !copied {
			// First invalid result: keep a copy of what was valid so far,
			// never writing into the caller's slice.
			valid = append(make([]probes.Result, 0, len(results)-1), results[:i]...)
			copied = true
		}
		if covers, due := l.utf8Warn.note(l.now()); due {
			l.log.Warn("probe results dropped: a result carries text that is not valid UTF-8",
				"dropped", covers, "target_host_id", r.TargetHostID, "check_key", r.CheckKey, "metric", r.Metric)
		}
	}
	return valid
}
