package probes

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

func TestPolicyDown(t *testing.T) {
	cases := []struct {
		policy string
		n, d   int
		want   bool
	}{
		{checkplugins.PolicyAny, 3, 0, false},
		{checkplugins.PolicyAny, 3, 1, true},
		{checkplugins.PolicyAny, 1, 1, true},
		{checkplugins.PolicyMajority, 3, 1, false},
		{checkplugins.PolicyMajority, 3, 2, true},
		{checkplugins.PolicyMajority, 4, 2, false},
		{checkplugins.PolicyMajority, 4, 3, true},
		{checkplugins.PolicyMajority, 1, 1, true},
		{checkplugins.PolicyMajority, 2, 1, false},
		{checkplugins.PolicyAll, 3, 2, false},
		{checkplugins.PolicyAll, 3, 3, true},
		{checkplugins.PolicyAll, 1, 1, true},
		{checkplugins.PolicyAll, 1, 0, false},
		{"", 2, 1, true}, // unknown policy reads as any
	}
	for _, c := range cases {
		if got := policyDown(c.policy, c.n, c.d); got != c.want {
			t.Errorf("policyDown(%q, n=%d, d=%d) = %v, want %v", c.policy, c.n, c.d, got, c.want)
		}
	}
}

// round is one vantage's rows at one time: the plugin's up metric (value
// nil when errText is set) and a response time.
func round(vantage string, at time.Time, up *float64, errText string) []VantageRow {
	ms := 12.0
	return []VantageRow{
		{Vantage: vantage, Metric: "tcp.up", At: at, Value: up, Err: errText},
		{Vantage: vantage, Metric: "tcp.response_ms", At: at, Value: &ms},
	}
}

func f(v float64) *float64 { return &v }

func TestCombineVantages(t *testing.T) {
	now := t0
	interval := time.Minute
	fresh := now.Add(-time.Minute)
	agentA, agentB, agentC := "0190a000-0000-7000-8000-00000000000a", "0190a000-0000-7000-8000-00000000000b",
		"0190a000-0000-7000-8000-00000000000c"
	order := []string{VantageManager, agentA, agentB, agentC}
	up, down := f(1), f(0)
	cases := []struct {
		name    string
		policy  string
		rows    [][]VantageRow
		n, d    int
		rep     string
		isDown  bool
		repDown bool
	}{
		{"all up any", checkplugins.PolicyAny,
			[][]VantageRow{round(VantageManager, fresh, up, ""), round(agentA, fresh, up, "")}, 2, 0, VantageManager, false, false},
		{"one down any", checkplugins.PolicyAny,
			[][]VantageRow{round(VantageManager, fresh, up, ""), round(agentA, fresh, down, "")}, 2, 1, agentA, true, true},
		{"one down majority of three", checkplugins.PolicyMajority,
			[][]VantageRow{round(VantageManager, fresh, up, ""), round(agentA, fresh, down, ""), round(agentB, fresh, up, "")},
			3, 1, VantageManager, false, false},
		{"two down majority of three", checkplugins.PolicyMajority,
			[][]VantageRow{round(VantageManager, fresh, up, ""), round(agentA, fresh, down, ""), round(agentB, fresh, down, "")},
			3, 2, agentA, true, true},
		{"two down all of three", checkplugins.PolicyAll,
			[][]VantageRow{round(VantageManager, fresh, up, ""), round(agentA, fresh, down, ""), round(agentB, fresh, down, "")},
			3, 2, VantageManager, false, false},
		{"all down all", checkplugins.PolicyAll,
			[][]VantageRow{round(VantageManager, fresh, down, ""), round(agentA, fresh, down, "")}, 2, 2, VantageManager, true, true},
		{"no result does not count", checkplugins.PolicyAll,
			[][]VantageRow{round(VantageManager, fresh, nil, "timeout"), round(agentA, fresh, down, "")}, 1, 1, agentA, true, true},
		{"no result only", checkplugins.PolicyAny,
			[][]VantageRow{round(VantageManager, fresh, nil, "timeout")}, 0, 0, "", false, false},
		{"odd up value is no result", checkplugins.PolicyAny,
			[][]VantageRow{round(agentA, fresh, f(0.5), ""), round(agentB, fresh, up, "")}, 1, 0, agentB, false, false},
		{"nothing", checkplugins.PolicyAny, nil, 0, 0, "", false, false},
		{"unexpected vantage ignored", checkplugins.PolicyAny,
			[][]VantageRow{round("0190a000-0000-7000-8000-0000000000ff", fresh, down, ""), round(agentA, fresh, up, "")},
			1, 0, agentA, false, false},
		{"stale vantage ignored", checkplugins.PolicyAny,
			[][]VantageRow{round(VantageManager, now.Add(-151*time.Second), down, ""), round(agentA, fresh, up, "")},
			1, 0, agentA, false, false},
		{"freshness boundary is fresh", checkplugins.PolicyAny,
			[][]VantageRow{round(agentB, now.Add(-150*time.Second), down, ""), round(agentA, fresh, up, "")},
			2, 1, agentB, true, true},
		{"representative order follows order, not rows", checkplugins.PolicyAny,
			[][]VantageRow{round(agentC, fresh, down, ""), round(agentB, fresh, down, ""), round(VantageManager, fresh, up, "")},
			3, 2, agentB, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rows []VantageRow
			for _, r := range c.rows {
				rows = append(rows, r...)
			}
			v := combineVantages("tcp", c.policy, order, rows, now, interval)
			if v.n != c.n || v.down != c.d || v.rep != c.rep || v.isDown != c.isDown {
				t.Fatalf("verdict n=%d d=%d rep=%q down=%v, want n=%d d=%d rep=%q down=%v",
					v.n, v.down, v.rep, v.isDown, c.n, c.d, c.rep, c.isDown)
			}
			if c.rep == "" {
				if len(v.rows) != 0 {
					t.Fatalf("rows = %+v without a representative", v.rows)
				}
				return
			}
			if len(v.rows) != 2 {
				t.Fatalf("rows = %+v, want the representative's round", v.rows)
			}
			for _, r := range v.rows {
				if r.Vantage != c.rep {
					t.Fatalf("row of %q in %q's round", r.Vantage, c.rep)
				}
				if r.Metric == "tcp.up" && (r.Value == nil || (*r.Value == 0) != c.repDown) {
					t.Fatalf("representative's up = %v, want down=%v", r.Value, c.repDown)
				}
			}
		})
	}
}

// TestCombineLatestRoundOnly: a vantage's rows from an older round (an
// item its newest round no longer reports) do not mix into its result.
func TestCombineLatestRoundOnly(t *testing.T) {
	now := t0
	older, newer := now.Add(-50*time.Second), now.Add(-10*time.Second)
	rows := []VantageRow{
		{Vantage: VantageManager, Metric: "tcp.up", At: newer, Value: f(1)},
		{Vantage: VantageManager, Item: "gone", Metric: "tcp.response_ms", At: older, Value: f(3)},
		{Vantage: VantageManager, Metric: "tcp.response_ms", At: newer, Value: f(4)},
	}
	v := combineVantages("tcp", checkplugins.PolicyAny, []string{VantageManager}, rows, now, time.Minute)
	got := make([]string, len(v.rows))
	for i, r := range v.rows {
		got[i] = fmt.Sprintf("%s/%s", r.Item, r.Metric)
	}
	if !slices.Equal(got, []string{"/tcp.up", "/tcp.response_ms"}) {
		t.Fatalf("rows = %v, want the newest round only", got)
	}
}

// TestCombineSilentAgentStopsCounting is Review Focus 4: an agent that
// stops reporting counts until 2 × interval + 30 s have passed, then no
// longer; with no other vantage the check is unknown (n = 0), not up.
func TestCombineSilentAgentStopsCounting(t *testing.T) {
	agent := "0190a000-0000-7000-8000-00000000000a"
	last := t0
	rows := round(agent, last, f(1), "")
	order := []string{agent}
	if v := combineVantages("tcp", checkplugins.PolicyAny, order, rows, last.Add(150*time.Second), time.Minute); v.n != 1 {
		t.Fatalf("at 2×interval+30s n = %d, want 1", v.n)
	}
	v := combineVantages("tcp", checkplugins.PolicyAny, order, rows, last.Add(150*time.Second+time.Nanosecond), time.Minute)
	if v.n != 0 || v.rep != "" || v.isDown {
		t.Fatalf("past the window verdict = %+v, want no vantage", v)
	}
}
