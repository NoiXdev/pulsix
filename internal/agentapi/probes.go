// Probe vantage points (backlog 3.13a), the manager side of the agent
// protocol: a heartbeat records whether the agent can probe other hosts,
// a capable agent's heartbeat is answered with its probe assignments when
// its reported version differs (the way CheckConfig converges), and the
// results it sends back are stored as its own vantage — only for slots it
// is assigned and only metrics of the slot's plugin.
package agentapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
	"github.com/noixdev/pulsix/internal/probeconfig"
	"github.com/noixdev/pulsix/internal/probes"
)

func (s *Service) logger() *slog.Logger {
	if s.deps.Log != nil {
		return s.deps.Log
	}
	return slog.Default()
}

// recordProbesSupported hands the heartbeat's probes_supported flag to the
// registry, which writes only a change. A failed write is logged, never
// fatal: the flag decides assignments the probe runner makes, and the next
// heartbeat writes it again.
func (s *Service) recordProbesSupported(ctx context.Context, agent agents.AgentIdentity, supported bool) {
	if err := s.deps.Agents.SetProbesSupported(ctx, agent.HostID, supported); err != nil {
		s.logger().WarnContext(ctx, "record agent probes_supported; the next heartbeat writes it again",
			"serial", agent.Serial, "org_id", agent.OrgID, "host_id", agent.HostID, "err", err)
	}
}

// probeConfigFor reads the agent's probe assignments and returns them as a
// ProbeConfig when their version differs from reported, nil when it is the
// same. A read or encoding fault is logged and answered with nil — the ack
// still goes out and the next heartbeat retries, the posture of a failed
// resolve in processHeartbeat.
func (s *Service) probeConfigFor(ctx context.Context, agent agents.AgentIdentity, reported string) *agentpb.ProbeConfig {
	as, err := s.deps.Probes.AgentAssignments(ctx, agent.OrgID, agent.HostID)
	if err != nil {
		s.logger().ErrorContext(ctx, "read agent probe assignments for heartbeat; the next heartbeat retries",
			"serial", agent.Serial, "org_id", agent.OrgID, "host_id", agent.HostID, "err", err)
		return nil
	}
	specs, err := probeSpecsOf(as)
	if err != nil {
		s.logger().ErrorContext(ctx, "encode agent probe assignments; the next heartbeat retries",
			"serial", agent.Serial, "org_id", agent.OrgID, "host_id", agent.HostID, "err", err)
		return nil
	}
	if probeconfig.Version(specs) == reported {
		return nil
	}
	return probeconfig.ToProto(specs)
}

// probeSpecsOf renders assignments as probe specs: params in the canonical
// JSON check specs use (checkplugins.CanonicalParams), the interval in
// whole seconds.
func probeSpecsOf(as []probes.Assignment) ([]probeconfig.Spec, error) {
	specs := make([]probeconfig.Spec, len(as))
	for i, a := range as {
		params, err := checkplugins.CanonicalParams(a.Params)
		if err != nil {
			return nil, fmt.Errorf("encode params of %s/%s: %w", a.TargetHostID, a.CheckKey, err)
		}
		specs[i] = probeconfig.Spec{
			TargetHostID: a.TargetHostID, CheckKey: a.CheckKey, Plugin: a.Plugin, ParamsJSON: string(params),
			IntervalSeconds: int32(a.Interval / time.Second), Address: a.Address,
		}
	}
	return specs, nil
}

// probeIngest is what one ProbeResults message did. Malformed and
// Implausible are the check-results contract's own counts
// (checks.SanitizeObservations); Unassigned counts results for a (target,
// check key) not assigned to this agent — or a target that is not a
// canonical host id — and ForeignMetric results whose metric the slot's
// plugin does not report. Both are ordinary for an agent one refresh
// behind its assignments and are dropped without an error to the agent.
type probeIngest struct {
	Accepted      int
	Unassigned    int
	ForeignMetric int
	Malformed     int
	Implausible   int
}

func (p probeIngest) dropped() int {
	return p.Unassigned + p.ForeignMetric + p.Malformed + p.Implausible
}

type probeSlot struct {
	target uuid.UUID
	key    string
}

// recordProbeResults stores one ProbeResults message as the agent's
// vantage. The check-results contract applies first and whole
// (checks.SanitizeObservations): a batch over its bounds is refused with
// errCheckResultsRejected, which the stream answers InvalidArgument, and
// malformed or implausible results are counted and left out. What remains
// is kept only when it names a slot of the agent's current assignments —
// one read per message — and a metric of that slot's plugin; everything
// else is counted and dropped (Review Focus 1: never recorded). The kept
// results are stored per target host, vantage = the agent's host id. A
// store fault is the manager's and fails the stream like a check-results
// store fault; the agent replays the batch after reconnecting.
func (s *Service) recordProbeResults(ctx context.Context, agent agents.AgentIdentity, results *agentpb.ProbeResults) (probeIngest, error) {
	var res probeIngest
	if s.deps.Probes == nil {
		slog.WarnContext(ctx, "agent probe results received but no probes store is wired; dropping",
			"serial", agent.Serial, "host_id", agent.HostID)
		return res, nil
	}
	obs := probeObservations(results.GetResults())
	valid, ing, err := checks.SanitizeObservations(obs, s.deps.Clock())
	if err != nil {
		if errors.Is(err, checks.ErrInvalidInput) {
			return res, fmt.Errorf("%w: %v", errCheckResultsRejected, err)
		}
		return res, fmt.Errorf("sanitize probe results: %w", err)
	}
	res.Malformed, res.Implausible = ing.Malformed, ing.Implausible
	if len(valid) == 0 {
		s.logProbeIngest(ctx, agent, res)
		return res, nil
	}
	as, err := s.deps.Probes.AgentAssignments(ctx, agent.OrgID, agent.HostID)
	if err != nil {
		return res, fmt.Errorf("read agent probe assignments: %w", err)
	}
	metrics := make(map[probeSlot]map[string]bool, len(as))
	for _, a := range as {
		metrics[probeSlot{a.TargetHostID, a.CheckKey}] = pluginMetrics(a.Plugin)
	}
	byTarget := map[uuid.UUID][]checks.Observation{}
	var targets []uuid.UUID
	for _, o := range valid {
		known, assigned := metrics[probeSlot{o.HostID, o.CheckKey}]
		switch {
		case o.HostID == uuid.Nil || !assigned:
			res.Unassigned++
			continue
		case !known[o.Metric]:
			res.ForeignMetric++
			continue
		}
		if _, seen := byTarget[o.HostID]; !seen {
			targets = append(targets, o.HostID)
		}
		byTarget[o.HostID] = append(byTarget[o.HostID], o)
	}
	vantage := agent.HostID.String()
	for _, target := range targets {
		if err := s.deps.Probes.RecordVantage(ctx, agent.OrgID, vantage, byTarget[target]); err != nil {
			return res, fmt.Errorf("record probe results: %w", err)
		}
		res.Accepted += len(byTarget[target])
	}
	s.logProbeIngest(ctx, agent, res)
	return res, nil
}

// probeObservations maps wire results to observations keyed by their
// target host. A target that is not a canonical host id maps to uuid.Nil,
// which no assignment names.
func probeObservations(items []*agentpb.ProbeResult) []checks.Observation {
	obs := make([]checks.Observation, 0, len(items))
	for _, r := range items {
		target, err := uuid.Parse(r.GetTargetHostId())
		if err != nil || target.String() != r.GetTargetHostId() {
			target = uuid.Nil
		}
		o := checks.Observation{
			HostID: target, CheckKey: r.GetCheckKey(), Item: r.GetItem(), Metric: r.GetMetric(),
			At: r.GetAt().AsTime(),
		}
		switch v := r.GetObservation().(type) {
		case *agentpb.ProbeResult_Value:
			val := v.Value
			o.Value = &val
		case *agentpb.ProbeResult_Label:
			lbl := v.Label
			o.Label = &lbl
		case *agentpb.ProbeResult_Error:
			o.Err = v.Error
		}
		obs = append(obs, o)
	}
	return obs
}

// pluginMetrics is the set of metric names plugin reports; empty for a
// plugin the manager does not know.
func pluginMetrics(plugin string) map[string]bool {
	d, ok := checkplugins.Get(plugin)
	if !ok {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(d.Metrics))
	for _, m := range d.Metrics {
		out[m.Name] = true
	}
	return out
}

// logProbeIngest logs what a message dropped, counts only. A malformed or
// implausible result is a broken agent build or a skewed clock and is
// warned about, as for check results; a result outside the assignment is
// ordinary right after a refresh and logged at debug.
func (s *Service) logProbeIngest(ctx context.Context, agent agents.AgentIdentity, res probeIngest) {
	if res.dropped() == 0 {
		return
	}
	level := slog.LevelDebug
	if res.Malformed > 0 || res.Implausible > 0 {
		level = slog.LevelWarn
	}
	msg := "agent probe results partially dropped"
	if res.Accepted == 0 {
		msg = "agent probe results dropped in full"
	}
	s.logger().Log(ctx, level, msg,
		"serial", agent.Serial, "org_id", agent.OrgID, "host_id", agent.HostID,
		"accepted", res.Accepted, "dropped_unassigned", res.Unassigned,
		"dropped_foreign_metric", res.ForeignMetric, "dropped_malformed", res.Malformed,
		"dropped_implausible_at", res.Implausible)
}

// Compile-time check: the probe store is the production ProbesPort.
var _ ProbesPort = (*probes.SQLStore)(nil)
