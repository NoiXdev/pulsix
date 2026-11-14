package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// storedLocalCheck is one entry of host_agents.local_checks (migration
// 00095): params as a JSON object, the interval in whole seconds.
type storedLocalCheck struct {
	Key             string          `json:"key"`
	Plugin          string          `json:"plugin"`
	Params          json.RawMessage `json:"params"`
	IntervalSeconds int64           `json:"interval_seconds"`
}

// localChecksColumns renders a report for the two columns: nil for both
// when it is not Known (NULL, unknown), otherwise a JSON array (possibly
// empty, never null) and a problems array (possibly empty, never nil).
func localChecksColumns(r LocalChecksReport) ([]byte, []string, error) {
	if !r.Known {
		return nil, nil, nil
	}
	entries := make([]storedLocalCheck, len(r.Checks))
	for i, c := range r.Checks {
		params, err := checkplugins.CanonicalParams(c.Params)
		if err != nil {
			return nil, nil, fmt.Errorf("encode params of local check %q: %w", c.Key, err)
		}
		entries[i] = storedLocalCheck{Key: c.Key, Plugin: c.Plugin, Params: params, IntervalSeconds: int64(c.Interval / time.Second)}
	}
	checks, err := json.Marshal(entries)
	if err != nil {
		return nil, nil, fmt.Errorf("encode local checks: %w", err)
	}
	problems := r.Problems
	if problems == nil {
		problems = []string{}
	}
	return checks, problems, nil
}

// localChecksFromRow reads the two columns back: a NULL local_checks is
// "unknown" (Heartbeat writes both or neither), and a known report's
// lists are never nil — the GUI tells "none" from "unknown" by that.
func localChecksFromRow(checks []byte, problems []string) (LocalChecksReport, error) {
	if checks == nil {
		return LocalChecksReport{}, nil
	}
	var entries []storedLocalCheck
	if err := json.Unmarshal(checks, &entries); err != nil {
		return LocalChecksReport{}, fmt.Errorf("decode local checks: %w", err)
	}
	out := LocalChecksReport{Known: true, Checks: make([]LocalCheck, len(entries)), Problems: problems}
	for i, e := range entries {
		params := map[string]any{}
		if len(e.Params) > 0 {
			if err := json.Unmarshal(e.Params, &params); err != nil {
				return LocalChecksReport{}, fmt.Errorf("decode params of local check %q: %w", e.Key, err)
			}
		}
		if params == nil {
			params = map[string]any{}
		}
		out.Checks[i] = LocalCheck{Key: e.Key, Plugin: e.Plugin, Params: params, Interval: time.Duration(e.IntervalSeconds) * time.Second}
	}
	if out.Problems == nil {
		out.Problems = []string{}
	}
	return out, nil
}

// LocalChecksByHosts reads the listed hosts' agents' local checks reports
// in one query, under the caller's organization scope.
func (s *SQLStore) LocalChecksByHosts(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]LocalChecksReport, error) {
	rows, err := s.q.ListAgentLocalChecks(ctx, gen.ListAgentLocalChecksParams{OrgID: orgID, HostIds: hostIDs})
	if err != nil {
		return nil, fmt.Errorf("list agent local checks: %w", err)
	}
	out := make(map[uuid.UUID]LocalChecksReport, len(rows))
	for _, r := range rows {
		report, err := localChecksFromRow(r.LocalChecks, r.LocalCheckProblems)
		if err != nil {
			return nil, fmt.Errorf("host %s: %w", r.HostID, err)
		}
		out[r.HostID] = report
	}
	return out, nil
}
