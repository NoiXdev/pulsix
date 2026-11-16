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
	"slices"
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
// (checks.SanitizeObservations). The others are results outside what the
// agent was asked to report, each dropped without an error to the agent,
// which may simply be one refresh behind its assignments:
//
//   - Unassigned: the (target, check key) is not assigned to this agent, the
//     target is not a canonical host id, or the slot's plugin is unknown or
//     snmp (never sent to an agent);
//   - ForeignMetric: the slot's plugin does not report the metric;
//   - UnaskedItem: the slot's params do not ask for the item (a plugin
//     without items reports only on "");
//   - WrongKind: a numeric metric carrying a label, or a discrete one a value.
type probeIngest struct {
	Accepted      int
	Unassigned    int
	ForeignMetric int
	UnaskedItem   int
	WrongKind     int
	Malformed     int
	Implausible   int
}

func (p probeIngest) dropped() int {
	return p.Unassigned + p.ForeignMetric + p.UnaskedItem + p.WrongKind + p.Malformed + p.Implausible
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
// is kept only when it fits a slot of the agent's current assignments —
// one read per message — by probeFit; everything else is counted and
// dropped (Review Focus 1: never recorded). The kept results are stored in
// ONE RecordVantage call (one transaction across targets), vantage = the
// agent's host id, so a message is stored whole or not at all and the
// agent's replay after a fault cannot duplicate a part. A store fault is
// the manager's and fails the stream like a check-results store fault.
func (s *Service) recordProbeResults(ctx context.Context, agent agents.AgentIdentity, results *agentpb.ProbeResults) (probeIngest, error) {
	var res probeIngest
	if s.deps.Probes == nil {
		s.logger().WarnContext(ctx, "agent probe results received but no probes store is wired; dropping",
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
	slots := make(map[probeSlot]probes.Assignment, len(as))
	for _, a := range as {
		slots[probeSlot{a.TargetHostID, a.CheckKey}] = a
	}
	kept := make([]checks.Observation, 0, len(valid))
	for _, o := range valid {
		a, assigned := slots[probeSlot{o.HostID, o.CheckKey}]
		if o.HostID == uuid.Nil || !assigned {
			res.Unassigned++
			continue
		}
		if !probeFit(a, o, &res) {
			continue
		}
		kept = append(kept, o)
	}
	if len(kept) > 0 {
		if err := s.deps.Probes.RecordVantage(ctx, agent.OrgID, agent.HostID.String(), kept); err != nil {
			return res, fmt.Errorf("record probe results: %w", err)
		}
		res.Accepted = len(kept)
	}
	s.logProbeIngest(ctx, agent, res)
	return res, nil
}

// probeFit reports whether o fits its assigned slot a, counting the reason
// in res when it does not: the slot's plugin is known and not snmp (snmp
// never goes to an agent, and with interfaces set its items are anything
// the device names); the metric is one the plugin reports, of its kind —
// a value for a numeric metric, a label for a discrete one, an error for
// either; and the item is one the slot asks for ("" for a plugin without
// items, checkplugins.AsksForItem otherwise).
func probeFit(a probes.Assignment, o checks.Observation, res *probeIngest) bool {
	d, ok := checkplugins.Get(a.Plugin)
	if !ok || a.Plugin == "snmp" {
		res.Unassigned++
		return false
	}
	i := slices.IndexFunc(d.Metrics, func(m checkplugins.MetricSpec) bool { return m.Name == o.Metric })
	if i < 0 {
		res.ForeignMetric++
		return false
	}
	switch d.Metrics[i].Kind {
	case checkplugins.Numeric:
		if o.Label != nil {
			res.WrongKind++
			return false
		}
	case checkplugins.Discrete:
		if o.Value != nil {
			res.WrongKind++
			return false
		}
	}
	asked := o.Item == ""
	if d.ItemParam != "" {
		asked = checkplugins.AsksForItem(a.Plugin, a.Params, o.Item)
	}
	if !asked {
		res.UnaskedItem++
		return false
	}
	return true
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
		"dropped_foreign_metric", res.ForeignMetric, "dropped_unasked_item", res.UnaskedItem,
		"dropped_wrong_kind", res.WrongKind, "dropped_malformed", res.Malformed,
		"dropped_implausible_at", res.Implausible)
}

// Compile-time check: the probe store is the production ProbesPort.
var _ ProbesPort = (*probes.SQLStore)(nil)
