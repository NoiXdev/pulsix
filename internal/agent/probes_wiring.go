// The probe runner's wiring (backlog 3.13a): the manager assigns this
// agent synthetic probes of other hosts as a ProbeConfig on a heartbeat's
// acknowledgement; internal/agent/probes runs them, and their results go
// back as ProbeResults through the same sender, chunking and 15-minute
// disconnect ring as check results — in a ring of their own, replayed
// after the check results' on a reconnect.
package agent

import (
	"strings"
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
// probes.DefaultRegistry and the dial guard probe.NewGuard(nil, nil)
// unless a test replaced the registry.
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

// newProbeBuffer is the probe results' disconnect ring: the check ring's
// window, each result stamped by its round's At.
func newProbeBuffer(now func() time.Time) *checks.Ring[probes.Result] {
	return checks.NewRing(bufferWindow, now, func(r probes.Result) time.Time { return r.At })
}

// applyProbeConfig installs a pushed ProbeConfig. One that cannot be read
// (a target id that is not a canonical UUID) leaves the running set and
// its version alone, so the heartbeat keeps reporting the old version and
// the manager sends its set again.
func (l *Loop) applyProbeConfig(pc *agentpb.ProbeConfig) {
	specs, err := probeconfig.FromProto(pc)
	if err != nil {
		l.log.Warn("probe configuration push could not be read; the running probes are kept "+
			"and this configuration is not reported as applied",
			"version", pc.GetVersion(), "err", err)
		return
	}
	l.probes.Apply(specs)
}

// handleProbeResults is the probe runner's sink, handleResults' twin: one
// round's results to the current session's sender, or to the probe ring
// when no stream is up (or the send just failed and no newer stream is
// live), by the same routeResults. It runs on the runner's probe
// goroutines.
func (l *Loop) handleProbeResults(results []probes.Result) {
	results = l.validUTF8ProbeResults(results)
	if len(results) == 0 {
		return
	}
	routeResults(l, "probe results", results, (*streamSender).sendProbeResultsChunked, func(rest []probes.Result) {
		if dropped := l.probeBuffer.Add(rest); dropped > 0 {
			l.log.Warn("probe result buffer overflowed while disconnected; oldest samples dropped",
				"dropped", dropped)
		}
	})
}

// replayProbeResults drains the probe ring over sender — activateSender's
// second half, under routeMu like the first. On a failure only what did
// not go out goes back (unsentAfter), and what the put-back evicts is
// logged.
func (l *Loop) replayProbeResults(sender *streamSender) error {
	pending := l.probeBuffer.Drain()
	sent, err := sender.sendProbeResultsChunked(pending)
	if err == nil {
		return nil
	}
	if dropped := l.probeBuffer.Add(unsentAfter(l.log, "probe results", pending, sent, err)); dropped > 0 {
		l.log.Warn("probe results aged out of the buffer while a reconnect replay failed; samples dropped",
			"dropped", dropped, "replayed", sent, "err", err)
	}
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
