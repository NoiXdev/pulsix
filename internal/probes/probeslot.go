package probes

import (
	"context"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/probe"
)

// probeTimeout caps a probe below its check's interval: interval − 1 s.
// The probe itself cuts its own timeout_s to that deadline.
func probeTimeout(interval time.Duration) time.Duration {
	if d := interval - time.Second; d > 0 {
		return d
	}
	return interval
}

// probeSlot probes one slot and records the result. A probe cut short by ctx's
// cancellation (shutdown) did not measure the target, so its observations
// are dropped; one that ran into its own deadline is a real result. A
// panic in the probe or the recording (untrusted network input reaches
// net/http, crypto/tls and x/net/icmp) is logged with its stack and
// records nothing; the slot is released and due again on schedule.
func (r *Runner) probeSlot(ctx context.Context, s slot, t target, p Poller) {
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		delete(r.inFlight, s)
		r.mu.Unlock()
		select {
		case r.wake <- struct{}{}:
		default: // a wake-up is already pending
		}
	}()
	r.safely(ctx, "probe", func() { r.probeAndRecord(ctx, s, t, p) }, "org", t.org, "host", s.host, "check", s.key)
}

// runProbe looks the check's secret up and runs the probe, both under
// the probe's deadline; the deferred cancel also runs when the probe
// panics. A slot with a poller (snmp) is polled with it, any other probed
// through the probe function. A secret that cannot be handed over fails
// every metric without probing.
func (r *Runner) runProbe(ctx context.Context, s slot, t target, p Poller) []probe.Observation {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout(t.check.Interval))
	defer cancel()
	secret, failed := r.secretFor(pctx, s, t)
	if failed != nil {
		return failed
	}
	if p == nil {
		return r.probe(pctx, t.check.Plugin, t.check.Params, t.address, secret)
	}
	obs, info := p.Poll(pctx, t.check.Params, t.address, secret)
	if info.InterfacesSkipped {
		r.logSkipped(ctx, s, t)
	}
	return obs
}

// probeAndRecord is probeSlot's body. A slot with agent vantages goes
// through probeVantages instead; every other one is probed and recorded as
// the check's own result.
func (r *Runner) probeAndRecord(ctx context.Context, s slot, t target, p Poller) {
	if t.vantaged() {
		r.probeVantages(ctx, s, t, p)
		return
	}
	obs := r.runProbe(ctx, s, t, p)
	if ctx.Err() != nil || len(obs) == 0 {
		return
	}
	r.record(ctx, s, t, observations(s, obs, r.clock()))
}

// observations stamps a probe's results with the slot and at.
func observations(s slot, obs []probe.Observation, at time.Time) []checks.Observation {
	batch := make([]checks.Observation, len(obs))
	for i, o := range obs {
		batch[i] = checks.Observation{
			HostID: s.host, CheckKey: s.key, Item: o.Item, Metric: o.Metric, At: at, Value: o.Value, Label: o.Label, Err: o.Err,
		}
	}
	return batch
}

// record stores batch as the check's own result, like an agent's.
func (r *Runner) record(ctx context.Context, s slot, t target, batch []checks.Observation) {
	res, err := r.recorder.RecordProbeObservations(orgScope(ctx, t.org), t.org, batch)
	if err != nil {
		if ctx.Err() == nil {
			r.log.ErrorContext(ctx, "probe runner: record", "org", t.org, "host", s.host, "check", s.key, "err", err)
		}
		return
	}
	if n := res.Rejected(); n > 0 {
		r.log.WarnContext(ctx, "probe runner: observations rejected", "org", t.org, "host", s.host, "check", s.key, "rejected", n)
	}
}
