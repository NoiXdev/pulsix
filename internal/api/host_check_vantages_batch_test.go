package api_test

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/probes"
)

// countingViewStore counts the per-vantage reads the host page makes.
type countingViewStore struct {
	probes.ViewStore
	reads *atomic.Int64
}

func (c countingViewStore) HostVantageCurrent(ctx context.Context, org, host uuid.UUID, keys []string) (map[string][]probes.VantageRow, error) {
	c.reads.Add(1)
	return c.ViewStore.HostVantageCurrent(ctx, org, host, keys)
}

func (c countingViewStore) HostSlotAgents(ctx context.Context, org, host uuid.UUID, keys []string) (map[string][]uuid.UUID, error) {
	c.reads.Add(1)
	return c.ViewStore.HostSlotAgents(ctx, org, host, keys)
}

// manyVantagesFixture is web-1 with 20 tcp checks v00..v19 probed from
// the group of role=db, each with its own mix (backlog 3.79): even checks
// are assigned db-1, every third cold-1; db-1 is up on checks divisible
// by four and down on the other even ones; the manager has no round on
// v03, v07, …; every fifth check keeps rows of a vantage no longer
// expected. want is the response's vantages per check key, built from
// that mix independently of the view.
func manyVantagesFixture(t *testing.T, f checksFixture) map[string]wantCheckVantages {
	t.Helper()
	group := f.createGroup(t, map[string]any{"name": "probes", "selector": "role=db"})
	policies := []string{"any", "majority", "all"}
	ctx := db.WithOrgScope(context.Background(), f.orgID)
	store := probes.NewSQLStore(f.s.DB)
	at := time.Now().UTC().Truncate(time.Second)
	var assignments []probes.Assignment
	type round struct {
		vantage string
		key     string
		up      float64
	}
	var rounds []round
	want := map[string]wantCheckVantages{}
	for i := range 20 {
		key := fmt.Sprintf("v%02d", i)
		policy := policies[i%3]
		f.createCheck(t, f.operatorC, map[string]any{
			"key": key, "plugin": "tcp", "interval_seconds": 60, "binding_kind": "host", "host_id": f.web.String(),
			"params": map[string]any{"port": 80, "vantage_groups": []string{group.ID}, "fail_policy": policy},
		})
		w := wantCheckVantages{policy: policy}
		managerStatus := "none"
		if i%4 != 3 {
			rounds = append(rounds, round{probes.VantageManager, key, 1})
			managerStatus = "up"
		}
		w.vantages = append(w.vantages, wantVantage{"manager", "", "manager", managerStatus})
		// Agents in combining order: by host name, cold-1 before db-1.
		if i%3 == 0 {
			assignments = append(assignments, probes.Assignment{
				OrgID: f.orgID, AgentHostID: f.cold, TargetHostID: f.web, CheckKey: key,
				Plugin: "tcp", Params: map[string]any{"port": 80}, Interval: time.Minute, Address: "10.0.0.1",
			})
			rounds = append(rounds, round{f.cold.String(), key, 1})
			w.vantages = append(w.vantages, wantVantage{f.cold.String(), "cold-1", "agent", "up"})
		}
		if i%2 == 0 {
			assignments = append(assignments, probes.Assignment{
				OrgID: f.orgID, AgentHostID: f.dbHost, TargetHostID: f.web, CheckKey: key,
				Plugin: "tcp", Params: map[string]any{"port": 80}, Interval: time.Minute, Address: "10.0.0.1",
			})
			up, status := 0.0, "down"
			if i%4 == 0 {
				up, status = 1, "up"
			}
			rounds = append(rounds, round{f.dbHost.String(), key, up})
			w.vantages = append(w.vantages, wantVantage{f.dbHost.String(), "db-1", "agent", status})
		} else if i%5 == 0 {
			// db-1 is not assigned this check: its rows are left out.
			rounds = append(rounds, round{f.dbHost.String(), key, 1})
		}
		want[key] = w
	}
	if err := store.ReplaceOrgAssignments(ctx, f.orgID, assignments); err != nil {
		t.Fatal(err)
	}
	for _, r := range rounds {
		if err := store.RecordVantage(ctx, f.orgID, r.vantage, []checks.Observation{
			{HostID: f.web, CheckKey: r.key, Metric: "tcp.up", At: at, Value: floatPtr(r.up)},
			{HostID: f.web, CheckKey: r.key, Metric: "tcp.connect_ms", At: at, Value: floatPtr(7)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return want
}

// vantageChecksBody is the host checks response's vantage fields.
type vantageChecksBody struct {
	Checks []struct {
		Key        string `json:"key"`
		FailPolicy string `json:"fail_policy"`
		Vantages   *[]struct {
			Vantage      string     `json:"vantage"`
			Name         string     `json:"name"`
			Kind         string     `json:"kind"`
			Status       string     `json:"status"`
			At           *time.Time `json:"at"`
			Observations []struct {
				Metric string `json:"metric"`
			} `json:"observations"`
		} `json:"vantages"`
	} `json:"checks"`
}

func vantageChecks(t *testing.T, f checksFixture) vantageChecksBody {
	t.Helper()
	resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/checks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("host checks = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out vantageChecksBody
	decodeInto(t, resp, &out)
	return out
}

type wantVantage struct{ vantage, name, kind, status string }

type wantCheckVantages struct {
	policy   string
	vantages []wantVantage
}

// TestHostChecksVantagesBatched is backlog 3.79 (2): a host with 20
// vantage checks answers exactly what the mix of assignments and rounds
// says, and the vantage view reads the per-vantage store a bounded number
// of times for the whole host, not twice per check.
func TestHostChecksVantagesBatched(t *testing.T) {
	var reads atomic.Int64
	f := newChecksFixture(t, "host-check-vantages-batched", apitest.WithVantageStoreWrapper(func(s probes.ViewStore) probes.ViewStore {
		return countingViewStore{ViewStore: s, reads: &reads}
	}))
	want := manyVantagesFixture(t, f)

	reads.Store(0)
	body := vantageChecks(t, f)
	if n := reads.Load(); n > 2 {
		t.Errorf("per-vantage store reads for one host checks request = %d, want at most 2", n)
	}
	got := map[string]wantCheckVantages{}
	for _, c := range body.Checks {
		if c.Vantages == nil {
			continue
		}
		g := wantCheckVantages{policy: c.FailPolicy}
		for _, v := range *c.Vantages {
			g.vantages = append(g.vantages, wantVantage{v.Vantage, v.Name, v.Kind, v.Status})
			if v.Status != "none" && (v.At == nil || len(v.Observations) != 2) {
				t.Errorf("check %s vantage %s: at %v observations %+v, want its round", c.Key, v.Vantage, v.At, v.Observations)
			}
		}
		got[c.Key] = g
	}
	if len(got) != len(want) {
		t.Fatalf("checks with vantages = %d, want %d", len(got), len(want))
	}
	for key, w := range want {
		g := got[key]
		if g.policy != w.policy || fmt.Sprint(g.vantages) != fmt.Sprint(w.vantages) {
			t.Errorf("check %s = %+v, want %+v", key, g, w)
		}
	}

	// The cost of the request, for the report (not asserted).
	const n = 20
	start := time.Now()
	for range n {
		vantageChecks(t, f)
	}
	t.Logf("host checks with 20 vantage checks: %v per request, %d store reads per request",
		time.Since(start)/n, reads.Load()/(n+1))
}
