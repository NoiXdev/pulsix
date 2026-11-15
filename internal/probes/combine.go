package probes

import (
	"time"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// VantageManager is the manager's vantage name in the per-vantage tables;
// an agent's is its host id in canonical form.
const VantageManager = "manager"

// errNoVantage is every metric's error when no vantage has a fresh result
// (spec "Combining", step 4: n = 0).
const errNoVantage = "no vantage point has a recent result"

// freshness is how old a vantage's latest round may be and still count:
// 2 × interval + 30 s.
func freshness(interval time.Duration) time.Duration { return 2*interval + 30*time.Second }

// verdict is the combination of a slot's vantages.
type verdict struct {
	// n is the fresh vantages that are up or down; down how many of them
	// are down.
	n, down int
	// isDown is the fail policy's verdict.
	isDown bool
	// rep is the representative vantage ("" when n = 0) and rows its
	// latest round, in the order the store gave them.
	rep  string
	rows []VantageRow
}

// policyDown is the fail policy over n vantages of which d are down:
// any → d ≥ 1, majority → d > n/2, all → d = n (n > 0). An unknown policy
// reads as any.
func policyDown(policy string, n, d int) bool {
	switch policy {
	case checkplugins.PolicyMajority:
		return 2*d > n
	case checkplugins.PolicyAll:
		return n > 0 && d == n
	default:
		return d >= 1
	}
}

// vantageState is one vantage's latest round read as up, down or no
// result.
type vantageState int

const (
	stateNone vantageState = iota
	stateUp
	stateDown
)

// combineVantages applies the spec's Combining steps 2–5 to rows (every
// stored current row of the slot): only the vantages in order count (the
// expected ones, in representative order: manager, then agents by host
// name, then host id); a vantage's latest round is its rows with the
// greatest At, fresh when At is within freshness(interval) of now; a fresh
// round is down when <plugin>.up (item "") is 0, up when it is 1, and no
// result otherwise (an error or no up metric). The representative is the
// first down vantage when the policy says down, else the first up one.
func combineVantages(plugin, policy string, order []string, rows []VantageRow, now time.Time, interval time.Duration) verdict {
	latest := map[string]time.Time{}
	for _, r := range rows {
		if at, ok := latest[r.Vantage]; !ok || r.At.After(at) {
			latest[r.Vantage] = r.At
		}
	}
	upMetric := plugin + ".up"
	window := freshness(interval)
	states := make(map[string]vantageState, len(order))
	var v verdict
	for _, vant := range order {
		at, ok := latest[vant]
		if !ok || now.Sub(at) > window {
			continue
		}
		state := stateNone
		for _, r := range rows {
			if r.Vantage != vant || !r.At.Equal(at) || r.Item != "" || r.Metric != upMetric || r.Err != "" || r.Value == nil {
				continue
			}
			switch *r.Value {
			case 0:
				state = stateDown
			case 1:
				state = stateUp
			}
		}
		states[vant] = state
		switch state {
		case stateDown:
			v.n++
			v.down++
		case stateUp:
			v.n++
		}
	}
	if v.n == 0 {
		return v
	}
	v.isDown = policyDown(policy, v.n, v.down)
	want := stateUp
	if v.isDown {
		want = stateDown
	}
	for _, vant := range order {
		if states[vant] == want {
			v.rep = vant
			break
		}
	}
	at := latest[v.rep]
	for _, r := range rows {
		if r.Vantage == v.rep && r.At.Equal(at) {
			v.rows = append(v.rows, r)
		}
	}
	return v
}
