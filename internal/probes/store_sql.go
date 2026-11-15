package probes

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// Assignment is one slot an agent probes: the agent's host probes the
// target host's check (CheckKey, Plugin, Params minus the vantage params)
// every Interval, against Address.
type Assignment struct {
	OrgID, AgentHostID, TargetHostID uuid.UUID
	CheckKey, Plugin                 string
	Params                           map[string]any
	Interval                         time.Duration
	Address                          string
}

// VantageRow is one current value of one vantage of a check: the shape of
// checks.Current plus the vantage ("manager" or the agent's host id).
type VantageRow struct {
	Vantage, Item, Metric string
	At                    time.Time
	Value                 *float64
	Label                 *string
	Err                   string
}

// SQLStore keeps probe assignments and per-vantage results (migration
// 00098). Every statement runs inside the organization the caller names
// (db.DB.WithOrg), so the row-level security of the tables applies.
type SQLStore struct {
	d *db.DB
	q *gen.Queries
}

// NewSQLStore builds the store on d.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{d: d, q: gen.New(d)} }

type slotKey struct {
	agent, target uuid.UUID
	key           string
}

func (a Assignment) slot() slotKey { return slotKey{a.AgentHostID, a.TargetHostID, a.CheckKey} }

func encodeParams(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func decodeParams(b []byte) (map[string]any, error) {
	m := map[string]any{}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func toAssignment(r gen.ProbeAssignment) (Assignment, error) {
	params, err := decodeParams(r.Params)
	if err != nil {
		return Assignment{}, fmt.Errorf("decode params of %s/%s: %w", r.TargetHostID, r.CheckKey, err)
	}
	return Assignment{
		OrgID: r.OrgID, AgentHostID: r.AgentHostID, TargetHostID: r.TargetHostID, CheckKey: r.CheckKey,
		Plugin: r.Plugin, Params: params, Interval: time.Duration(r.IntervalSeconds) * time.Second, Address: r.Address,
	}, nil
}

// sameAssignment compares what an update would change: the identifying key
// is equal by construction.
func sameAssignment(a, b Assignment) bool {
	return a.Plugin == b.Plugin && a.Interval == b.Interval && a.Address == b.Address &&
		reflect.DeepEqual(a.Params, b.Params)
}

// ReplaceOrgAssignments makes want the organization's complete set of
// assignments in one transaction: new ones are inserted, changed ones
// updated (unchanged rows are not written), and stored ones that are not in
// want deleted. Other organizations are never touched; every element of
// want must carry org.
func (s *SQLStore) ReplaceOrgAssignments(ctx context.Context, org uuid.UUID, want []Assignment) error {
	return s.d.WithOrg(ctx, org, func(tx pgx.Tx) error {
		q := gen.New(tx)
		rows, err := q.ListProbeAssignmentsForOrg(ctx, org)
		if err != nil {
			return fmt.Errorf("list probe assignments: %w", err)
		}
		have := make(map[slotKey]Assignment, len(rows))
		for _, r := range rows {
			a, err := toAssignment(r)
			if err != nil {
				return err
			}
			have[a.slot()] = a
		}
		seen := make(map[slotKey]bool, len(want))
		for _, a := range want {
			if a.OrgID != org {
				return fmt.Errorf("assignment %s/%s names organization %s, not %s", a.TargetHostID, a.CheckKey, a.OrgID, org)
			}
			seen[a.slot()] = true
			params, err := encodeParams(a.Params)
			if err != nil {
				return fmt.Errorf("encode params of %s/%s: %w", a.TargetHostID, a.CheckKey, err)
			}
			secs := int32(a.Interval / time.Second) //nolint:gosec // a check interval is bounded far below int32
			old, exists := have[a.slot()]
			switch {
			case !exists:
				err = q.InsertProbeAssignment(ctx, gen.InsertProbeAssignmentParams{
					OrgID: org, AgentHostID: a.AgentHostID, TargetHostID: a.TargetHostID, CheckKey: a.CheckKey,
					Plugin: a.Plugin, Params: params, IntervalSeconds: secs, Address: a.Address,
				})
			case !sameAssignment(old, a):
				err = q.UpdateProbeAssignment(ctx, gen.UpdateProbeAssignmentParams{
					OrgID: org, AgentHostID: a.AgentHostID, TargetHostID: a.TargetHostID, CheckKey: a.CheckKey,
					Plugin: a.Plugin, Params: params, IntervalSeconds: secs, Address: a.Address,
				})
			}
			if err != nil {
				return fmt.Errorf("write probe assignment %s/%s: %w", a.TargetHostID, a.CheckKey, err)
			}
		}
		for k := range have {
			if seen[k] {
				continue
			}
			if err := q.DeleteProbeAssignment(ctx, gen.DeleteProbeAssignmentParams{
				OrgID: org, AgentHostID: k.agent, TargetHostID: k.target, CheckKey: k.key,
			}); err != nil {
				return fmt.Errorf("delete probe assignment %s/%s: %w", k.target, k.key, err)
			}
		}
		return nil
	})
}

// AgentAssignments is what one agent probes, ordered by target and key.
func (s *SQLStore) AgentAssignments(ctx context.Context, org, agentHost uuid.UUID) ([]Assignment, error) {
	rows, err := s.q.ListProbeAssignmentsForAgent(ctx, gen.ListProbeAssignmentsForAgentParams{OrgID: org, AgentHostID: agentHost})
	if err != nil {
		return nil, fmt.Errorf("list agent probe assignments: %w", err)
	}
	out := make([]Assignment, len(rows))
	for i, r := range rows {
		if out[i], err = toAssignment(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SlotAgents are the agent hosts currently assigned to the slot (target
// host, check key): the expected agent vantages of that check.
func (s *SQLStore) SlotAgents(ctx context.Context, org, host uuid.UUID, key string) ([]uuid.UUID, error) {
	ids, err := s.q.ListProbeSlotAgents(ctx, gen.ListProbeSlotAgentsParams{OrgID: org, TargetHostID: host, CheckKey: key})
	if err != nil {
		return nil, fmt.Errorf("list slot agents: %w", err)
	}
	return ids, nil
}

// RecordVantage stores obs as the results of one vantage in one
// transaction: every observation upserts its probe_current row, and a
// non-error one also adds a probe_samples row — an error is a current
// value only, like check_current. obs are validated by the caller; the
// target host of each is obs.HostID. A vantage other than "manager" or a
// canonical host id is refused by the table's CHECK.
func (s *SQLStore) RecordVantage(ctx context.Context, org uuid.UUID, vantage string, obs []checks.Observation) error {
	if len(obs) == 0 {
		return nil
	}
	return s.d.WithOrg(ctx, org, func(tx pgx.Tx) error {
		q := gen.New(tx)
		for _, o := range obs {
			var errText *string
			if o.Err != "" {
				e := o.Err
				errText = &e
			}
			if err := q.UpsertProbeCurrent(ctx, gen.UpsertProbeCurrentParams{
				OrgID: org, HostID: o.HostID, CheckKey: o.CheckKey, Vantage: vantage, Item: o.Item, Metric: o.Metric,
				Value: o.Value, Label: o.Label, At: o.At, Error: errText,
			}); err != nil {
				return fmt.Errorf("upsert probe current: %w", err)
			}
			if o.Err != "" {
				continue
			}
			if err := q.InsertProbeSample(ctx, gen.InsertProbeSampleParams{
				At: o.At, OrgID: org, HostID: o.HostID, CheckKey: o.CheckKey, Vantage: vantage, Item: o.Item,
				Metric: o.Metric, Value: o.Value, Label: o.Label,
			}); err != nil {
				return fmt.Errorf("insert probe sample: %w", err)
			}
		}
		return nil
	})
}

// VantageCurrent is every vantage's current rows of one check on one host,
// ordered by vantage, item and metric.
func (s *SQLStore) VantageCurrent(ctx context.Context, org, host uuid.UUID, key string) ([]VantageRow, error) {
	rows, err := s.q.ListProbeCurrent(ctx, gen.ListProbeCurrentParams{OrgID: org, HostID: host, CheckKey: key})
	if err != nil {
		return nil, fmt.Errorf("list probe current: %w", err)
	}
	out := make([]VantageRow, len(rows))
	for i, r := range rows {
		out[i] = VantageRow{Vantage: r.Vantage, Item: r.Item, Metric: r.Metric, At: r.At, Value: r.Value, Label: r.Label}
		if r.Error != nil {
			out[i].Err = *r.Error
		}
	}
	return out, nil
}

// DeleteVantages removes the current rows of every vantage of the slot
// that is not in keep (an empty keep removes them all). Samples stay until
// retention.
func (s *SQLStore) DeleteVantages(ctx context.Context, org, host uuid.UUID, key string, keep []string) error {
	if keep == nil {
		keep = []string{}
	}
	if err := s.q.DeleteProbeCurrentExcept(ctx, gen.DeleteProbeCurrentExceptParams{OrgID: org, HostID: host, CheckKey: key, Keep: keep}); err != nil {
		return fmt.Errorf("delete probe vantages: %w", err)
	}
	return nil
}
