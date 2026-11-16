package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/probes"
)

// Vantage points on the host page (backlog 3.13a, spec
// 2026-11-16-probe-vantages-design.md, "API and SPA"): a synthetic check
// with vantage groups lists its expected vantages with their state, and a
// graph may read one vantage's series instead of the check's combined one.

// VantageReader reads checks' vantage points: probes.View.
type VantageReader interface {
	// HostVantages returns, keyed by check key, the fail policy and the
	// expected vantages of every check of resolved that names vantage
	// groups.
	HostVantages(ctx context.Context, org, host uuid.UUID, resolved []checks.Resolved) (map[string]probes.CheckVantages, error)
	// Series is one vantage's series of (key, item, metric); it answers
	// probes.ErrInvalidInput for a vantage that is neither "manager" nor a
	// canonical host id.
	Series(ctx context.Context, org, host uuid.UUID, key, vantage, item, metric string, from, to time.Time, buckets int) (checks.Series, error)
}

type hostCheckVantageValueDTO struct {
	Item   string   `json:"item" doc:"The item this value is for; empty for a whole-check metric"`
	Metric string   `json:"metric"`
	Value  *float64 `json:"value,omitempty" doc:"Set for a numeric metric"`
	Label  string   `json:"label,omitempty" doc:"Set for a discrete metric"`
	Error  string   `json:"error,omitempty" doc:"Set when the probe failed; value and label are then both absent"`
}

type hostCheckVantageDTO struct {
	Vantage string `json:"vantage" doc:"manager, or the probing agent's host id"`
	Name    string `json:"name" doc:"The probing agent's host name; empty for the Pulsix server itself"`
	Kind    string `json:"kind" enum:"manager,agent"`
	// Status is read exactly as the combined verdict reads the vantage: a
	// round older than twice the check's interval plus 30 s is none.
	Status       string                     `json:"status" enum:"up,down,none" doc:"The vantage's latest round: down when the check's up metric is 0, up when it is 1, none for an error, no up metric, a round older than twice the interval plus 30 seconds, or no round yet"`
	At           *time.Time                 `json:"at,omitempty" doc:"When the vantage's latest round was taken; absent when it has none"`
	Observations []hostCheckVantageValueDTO `json:"observations" doc:"The latest round's values"`
}

// hostCheckVantages fills dto's vantage fields from the check's entry in
// vantages; a check without one (no vantage groups) keeps both fields
// empty, so they are left out of the response altogether.
func hostCheckVantages(dto *hostCheckDTO, vantages map[string]probes.CheckVantages) {
	cv, ok := vantages[dto.Key]
	if !ok {
		return
	}
	list := make([]hostCheckVantageDTO, len(cv.Vantages))
	for i, v := range cv.Vantages {
		kind := "manager"
		if v.Agent {
			kind = "agent"
		}
		out := hostCheckVantageDTO{
			Vantage: v.Vantage, Name: v.Name, Kind: kind, Status: v.Status, At: v.At,
			Observations: make([]hostCheckVantageValueDTO, len(v.Observations)),
		}
		for j, o := range v.Observations {
			val := hostCheckVantageValueDTO{Item: o.Item, Metric: o.Metric, Value: o.Value, Error: o.Err}
			if o.Value != nil {
				if problem := checks.ValueProblem(*o.Value); problem != "" {
					val.Value, val.Error = nil, problem
				}
			}
			if o.Label != nil {
				val.Label = *o.Label
			}
			out.Observations[j] = val
		}
		list[i] = out
	}
	dto.Vantages = &list
	dto.FailPolicy = cv.Policy
}

// hostVantages reads the vantages of host's resolved synthetic checks,
// mapping a failure to a logged 500 in the vantage reader's own name.
func hostVantages(ctx context.Context, deps Deps, orgID, hostID uuid.UUID, resolved []checks.Resolved) (map[string]probes.CheckVantages, error) {
	vantages, err := deps.Vantages.HostVantages(ctx, orgID, hostID, resolved)
	if err != nil {
		if ctx.Err() == nil {
			slog.ErrorContext(ctx, "host checks: vantages", "org_id", orgID, "host_id", hostID, "err", err)
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	return vantages, nil
}

// vantageSeries is the metrics route's read of one vantage's series.
func vantageSeries(ctx context.Context, deps Deps, orgID, hostID uuid.UUID, key, vantage, item, metric string, from, to time.Time, buckets int) (checks.Series, error) {
	series, err := deps.Vantages.Series(ctx, orgID, hostID, key, vantage, item, metric, from, to, buckets)
	switch {
	case err == nil:
		return series, nil
	case errors.Is(err, probes.ErrInvalidInput):
		return checks.Series{}, huma.Error400BadRequest(err.Error())
	default:
		if ctx.Err() == nil {
			slog.ErrorContext(ctx, "host metrics: vantage series", "org_id", orgID, "host_id", hostID, "err", err)
		}
		return checks.Series{}, huma.Error500InternalServerError("internal error")
	}
}
