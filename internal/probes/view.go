package probes

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/checks"
)

// The host page's view of a check's vantage points (backlog 3.13a, spec
// "API and SPA"): per check with vantage groups, every expected vantage —
// the manager while it probes, then the slot's assigned agents by host
// name, then host id, the combining order — with its state as combining
// reads it, and the per-vantage series behind its graph. Rows of a
// vantage that is no longer expected (left until the runner's next
// refresh prunes them) are never shown.

// The states of a vantage as the host page shows them.
const (
	StatusUp   = "up"
	StatusDown = "down"
	StatusNone = "none"
)

// ErrInvalidInput is a series request the view refuses: a vantage that is
// neither "manager" nor a canonical host id, no buckets, or an empty
// window.
var ErrInvalidInput = errors.New("invalid input")

// ViewStore is the read side of the per-vantage tables: SQLStore.
type ViewStore interface {
	// HostVantageCurrent is the current rows of each of keys' checks on
	// host, by check key, each ordered by vantage, item and metric.
	HostVantageCurrent(ctx context.Context, org, host uuid.UUID, keys []string) (map[string][]VantageRow, error)
	// HostSlotAgents is the agents assigned to each of keys' slots on
	// host, by check key.
	HostSlotAgents(ctx context.Context, org, host uuid.UUID, keys []string) (map[string][]uuid.UUID, error)
	VantageSeries(ctx context.Context, org, host uuid.UUID, key, vantage, item, metric string, from, to time.Time, buckets int) ([]checks.Point, error)
}

// VantageView is one expected vantage of a check.
type VantageView struct {
	// Vantage is VantageManager or the agent's host id.
	Vantage string
	// Name is the agent's host name; empty for the manager.
	Name string
	// Agent is false for the manager.
	Agent bool
	// Status is StatusUp, StatusDown or StatusNone, as combining reads the
	// vantage (a stale round is none).
	Status string
	// At is the vantage's latest round; nil when it has no rows.
	At *time.Time
	// Observations are the latest round's rows.
	Observations []VantageRow
}

// CheckVantages is one check's fail policy and its expected vantages in
// combining order.
type CheckVantages struct {
	Policy   string
	Vantages []VantageView
}

// View reads checks' vantages for the host page.
type View struct {
	store    ViewStore
	hosts    Hosts
	clock    func() time.Time
	describe func(string) (checkplugins.Descriptor, bool)
}

// NewView builds the view on store, naming agents through hosts.
func NewView(store ViewStore, hosts Hosts, clock func() time.Time) *View {
	return &View{store: store, hosts: hosts, clock: clock, describe: checkplugins.Get}
}

// slotView is one vantage check while HostVantages reads it.
type slotView struct {
	check  checks.Resolved
	policy string
	order  []string
	agents []uuid.UUID
	rows   []VantageRow
}

// HostVantages returns, keyed by check key, the vantages of every check of
// resolved (a host's resolved synthetic checks) that names vantage groups.
// A check probed only from the manager (a secret, an http custom
// request) has none, as the runner never gives it agent vantages. The
// rows and the assigned agents of all those checks are read in one query
// each, and agents are named with one host read for all checks (backlog
// 3.79): three reads per host whatever its number of vantage checks.
func (v *View) HostVantages(ctx context.Context, org, host uuid.UUID, resolved []checks.Resolved) (map[string]CheckVantages, error) {
	var slots []slotView
	var keys []string
	for _, c := range resolved {
		vant := checkplugins.VantageOf(c.Params)
		if len(vant.Groups) == 0 || v.managerOnly(c) {
			continue
		}
		s := slotView{check: c, policy: vant.Policy}
		if vant.Manager {
			s.order = append(s.order, VantageManager)
		}
		slots = append(slots, s)
		keys = append(keys, c.Key)
	}
	if len(slots) == 0 {
		return map[string]CheckVantages{}, nil
	}
	rows, err := v.store.HostVantageCurrent(ctx, org, host, keys)
	if err != nil {
		return nil, fmt.Errorf("read vantages: %w", err)
	}
	assigned, err := v.store.HostSlotAgents(ctx, org, host, keys)
	if err != nil {
		return nil, fmt.Errorf("read vantage agents: %w", err)
	}
	var agentIDs []uuid.UUID
	for i := range slots {
		s := &slots[i]
		s.rows, s.agents = rows[s.check.Key], assigned[s.check.Key]
		for _, a := range s.agents {
			if !slices.Contains(agentIDs, a) {
				agentIDs = append(agentIDs, a)
			}
		}
	}
	names := map[uuid.UUID]string{}
	if len(agentIDs) > 0 {
		hs, err := v.hosts.ListHostsByIDs(ctx, org, agentIDs)
		if err != nil {
			return nil, fmt.Errorf("name vantage agents: %w", err)
		}
		for _, h := range hs {
			names[h.ID] = h.Name
		}
	}
	now := v.clock()
	out := make(map[string]CheckVantages, len(slots))
	for _, s := range slots {
		agents := slices.Clone(s.agents)
		slices.SortFunc(agents, func(a, b uuid.UUID) int {
			return cmp.Or(cmp.Compare(names[a], names[b]), bytes.Compare(a[:], b[:]))
		})
		agentNames := make(map[string]string, len(agents))
		for _, a := range agents {
			s.order = append(s.order, a.String())
			agentNames[a.String()] = names[a]
		}
		latest, states := classifyVantages(s.check.Plugin, s.order, s.rows, now, s.check.Interval)
		cv := CheckVantages{Policy: s.policy, Vantages: make([]VantageView, len(s.order))}
		for i, vant := range s.order {
			view := VantageView{Vantage: vant, Agent: vant != VantageManager, Status: statusOf(states[vant])}
			if view.Agent {
				view.Name = agentNames[vant]
			}
			if at, ok := latest[vant]; ok {
				view.At = &at
				view.Observations = latestRound(vant, at, s.rows)
			}
			cv.Vantages[i] = view
		}
		out[s.check.Key] = cv
	}
	return out, nil
}

// managerOnly reports whether c's check is probed only from the manager
// (the runner's managerOnly rule).
func (v *View) managerOnly(c checks.Resolved) bool {
	d, ok := v.describe(c.Plugin)
	return ok && d.ProbedOnlyFromManager(c.Params)
}

func statusOf(s vantageState) string {
	switch s {
	case stateUp:
		return StatusUp
	case stateDown:
		return StatusDown
	case stateNone:
	}
	return StatusNone
}

// IsVantage reports whether s names a vantage: VantageManager or a host id
// in canonical (lower-case, hyphenated) form, the spelling the
// per-vantage tables store.
func IsVantage(s string) bool {
	if s == VantageManager {
		return true
	}
	id, err := uuid.Parse(s)
	return err == nil && id.String() == s
}

// Series returns metric's per-vantage samples for host's (key, item) in
// [from, to), downsampled into at most buckets points, like the check's
// own series (checks.Service.SeriesForHost).
func (v *View) Series(ctx context.Context, org, host uuid.UUID, key, vantage, item, metric string, from, to time.Time, buckets int) (checks.Series, error) {
	switch {
	case !IsVantage(vantage):
		return checks.Series{}, fmt.Errorf("%w: vantage must be manager or a host id", ErrInvalidInput)
	case buckets < 1:
		return checks.Series{}, fmt.Errorf("%w: buckets must be at least 1", ErrInvalidInput)
	case !to.After(from):
		return checks.Series{}, fmt.Errorf("%w: to must be after from", ErrInvalidInput)
	}
	points, err := v.store.VantageSeries(ctx, org, host, key, vantage, item, metric, from, to, buckets)
	if err != nil {
		return checks.Series{}, err
	}
	return checks.Series{Metric: metric, Points: points}, nil
}
