package probes

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/hosts"
)

// fakeViewStore is the read side of the per-vantage tables: the current
// rows and the slot agents per (host, check key), plus the series reads it
// was asked for.
type fakeViewStore struct {
	rows    map[string][]VantageRow
	agents  map[string][]uuid.UUID
	err     error
	reads   int
	series  []seriesCall
	points  []checks.Point
	listErr error
}

type seriesCall struct {
	host                       uuid.UUID
	key, vantage, item, metric string
	from, to                   time.Time
	buckets                    int
}

func (f *fakeViewStore) VantageCurrent(_ context.Context, _, _ uuid.UUID, key string) ([]VantageRow, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	return slices.Clone(f.rows[key]), nil
}

func (f *fakeViewStore) SlotAgents(_ context.Context, _, _ uuid.UUID, key string) ([]uuid.UUID, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	return slices.Clone(f.agents[key]), nil
}

func (f *fakeViewStore) VantageSeries(_ context.Context, _, host uuid.UUID, key, vantage, item, metric string, from, to time.Time, buckets int) ([]checks.Point, error) {
	f.series = append(f.series, seriesCall{host, key, vantage, item, metric, from, to, buckets})
	return f.points, f.listErr
}

// fakeHostNames names hosts.
type fakeHostNames struct {
	names map[uuid.UUID]string
	calls int
}

func (f *fakeHostNames) ListHostsByIDs(_ context.Context, _ uuid.UUID, ids []uuid.UUID) ([]hosts.Host, error) {
	f.calls++
	var out []hosts.Host
	for _, id := range ids {
		if name, ok := f.names[id]; ok {
			out = append(out, hosts.Host{ID: id, Name: name})
		}
	}
	return out, nil
}

func TestHostVantages(t *testing.T) {
	org, host := uuid.New(), uuid.New()
	group := uuid.New().String()
	agentA := uuid.MustParse("0190a000-0000-7000-8000-00000000000a")
	agentB := uuid.MustParse("0190a000-0000-7000-8000-00000000000b")
	leftover := "0190a000-0000-7000-8000-0000000000ff"
	now := t0
	fresh, stale := now.Add(-time.Minute), now.Add(-151*time.Second)
	store := &fakeViewStore{
		rows: map[string][]VantageRow{
			"web": slices.Concat(
				round(VantageManager, fresh, f(1), ""),
				round(agentA.String(), fresh, f(0), ""),
				round(agentB.String(), stale, f(1), ""),
				round(leftover, fresh, f(0), ""),
			),
		},
		// SlotAgents answers by id; the view orders by host name.
		agents: map[string][]uuid.UUID{"web": {agentA, agentB}},
	}
	names := &fakeHostNames{names: map[uuid.UUID]string{agentA: "zulu", agentB: "alpha"}}
	view := NewView(store, names, func() time.Time { return now })

	resolved := []checks.Resolved{
		{Key: "plain", Plugin: "tcp", Interval: time.Minute, Params: map[string]any{"port": 80}},
		{Key: "web", Plugin: "tcp", Interval: time.Minute, Params: map[string]any{
			"port": 80, "vantage_groups": []any{group}, "fail_policy": "majority",
		}},
	}
	got, err := view.HostVantages(context.Background(), org, host, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["plain"]; ok {
		t.Fatalf("a check without vantage groups has vantages: %+v", got["plain"])
	}
	web, ok := got["web"]
	if !ok {
		t.Fatalf("no vantages for web: %+v", got)
	}
	if web.Policy != "majority" {
		t.Fatalf("policy = %q, want majority", web.Policy)
	}
	type row struct {
		vantage, name string
		agent         bool
		status        string
		at            time.Time
	}
	want := []row{
		{VantageManager, "", false, StatusUp, fresh},
		{agentB.String(), "alpha", true, StatusNone, stale},
		{agentA.String(), "zulu", true, StatusDown, fresh},
	}
	if len(web.Vantages) != len(want) {
		t.Fatalf("vantages = %+v, want %d (the leftover vantage left out)", web.Vantages, len(want))
	}
	for i, w := range want {
		v := web.Vantages[i]
		if v.Vantage != w.vantage || v.Name != w.name || v.Agent != w.agent || v.Status != w.status ||
			v.At == nil || !v.At.Equal(w.at) {
			t.Fatalf("vantage %d = %+v, want %+v", i, v, w)
		}
		if len(v.Observations) != 2 {
			t.Fatalf("vantage %d observations = %+v, want its latest round", i, v.Observations)
		}
		for _, o := range v.Observations {
			if o.Vantage != w.vantage || !o.At.Equal(w.at) {
				t.Fatalf("vantage %d carries %+v", i, o)
			}
		}
	}
	if names.calls != 1 {
		t.Fatalf("host names read %d times, want once for every check", names.calls)
	}
}

// TestHostVantagesExpectedOnly: the manager is listed only while it probes;
// an expected vantage with no rows yet reads as none without a time; a
// check naming a secret has no agent vantages (the runner's rule) and so
// no vantage block.
func TestHostVantagesExpectedOnly(t *testing.T) {
	org, host := uuid.New(), uuid.New()
	agent := uuid.New()
	now := t0
	store := &fakeViewStore{
		rows:   map[string][]VantageRow{"web": round(VantageManager, now, f(1), "")},
		agents: map[string][]uuid.UUID{"web": {agent}},
	}
	view := NewView(store, &fakeHostNames{names: map[uuid.UUID]string{agent: "edge-1"}}, func() time.Time { return now })
	got, err := view.HostVantages(context.Background(), org, host, []checks.Resolved{
		{Key: "web", Plugin: "tcp", Interval: time.Minute, Params: map[string]any{
			"port": 80, "vantage_manager": false, "vantage_groups": []any{uuid.NewString()},
		}},
		{Key: "sw", Plugin: "snmp", Interval: time.Minute, Params: map[string]any{
			"secret": "community", "vantage_groups": []any{uuid.NewString()},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["sw"]; ok {
		t.Fatalf("a check with a secret has vantages: %+v", got["sw"])
	}
	web := got["web"]
	if web.Policy != "any" {
		t.Fatalf("policy = %q, want the default any", web.Policy)
	}
	if len(web.Vantages) != 1 {
		t.Fatalf("vantages = %+v, want the agent only", web.Vantages)
	}
	v := web.Vantages[0]
	if v.Vantage != agent.String() || v.Name != "edge-1" || v.Status != StatusNone || v.At != nil || len(v.Observations) != 0 {
		t.Fatalf("vantage = %+v, want edge-1 with no result", v)
	}
}

func TestHostVantagesReadError(t *testing.T) {
	boom := errors.New("boom")
	view := NewView(&fakeViewStore{err: boom}, &fakeHostNames{}, func() time.Time { return t0 })
	_, err := view.HostVantages(context.Background(), uuid.New(), uuid.New(), []checks.Resolved{
		{Key: "web", Plugin: "tcp", Interval: time.Minute, Params: map[string]any{"port": 80, "vantage_groups": []any{uuid.NewString()}}},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's", err)
	}
	store := &fakeViewStore{}
	view = NewView(store, &fakeHostNames{}, func() time.Time { return t0 })
	if _, err := view.HostVantages(context.Background(), uuid.New(), uuid.New(), []checks.Resolved{
		{Key: "plain", Plugin: "tcp", Interval: time.Minute, Params: map[string]any{"port": 80}},
	}); err != nil || store.reads != 0 {
		t.Fatalf("checks without groups: err %v, %d reads, want none", err, store.reads)
	}
}

func TestIsVantage(t *testing.T) {
	cases := map[string]bool{
		"manager":                                true,
		"0190a000-0000-7000-8000-00000000000a":   true,
		"0190A000-0000-7000-8000-00000000000A":   false,
		"{0190a000-0000-7000-8000-00000000000a}": false,
		"0190a00000007000800000000000000a":       false,
		"Manager":                                false,
		"":                                       false,
		"combined":                               false,
	}
	for in, want := range cases {
		if got := IsVantage(in); got != want {
			t.Errorf("IsVantage(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestVantageSeries(t *testing.T) {
	org, host := uuid.New(), uuid.New()
	from := t0.Add(-time.Hour)
	store := &fakeViewStore{points: []checks.Point{{At: from, Value: 3}}}
	view := NewView(store, &fakeHostNames{}, func() time.Time { return t0 })
	s, err := view.Series(context.Background(), org, host, "web", "manager", "", "tcp.response_ms", from, t0, 60)
	if err != nil {
		t.Fatal(err)
	}
	if s.Metric != "tcp.response_ms" || len(s.Points) != 1 || s.Points[0].Value != 3 {
		t.Fatalf("series = %+v", s)
	}
	want := seriesCall{host, "web", "manager", "", "tcp.response_ms", from, t0, 60}
	if len(store.series) != 1 || store.series[0] != want {
		t.Fatalf("store asked %+v, want %+v", store.series, want)
	}
	for name, call := range map[string]func() error{
		"bad vantage": func() error {
			_, err := view.Series(context.Background(), org, host, "web", "combined", "", "m", from, t0, 60)
			return err
		},
		"no buckets": func() error {
			_, err := view.Series(context.Background(), org, host, "web", "manager", "", "m", from, t0, 0)
			return err
		},
		"empty window": func() error {
			_, err := view.Series(context.Background(), org, host, "web", "manager", "", "m", t0, t0, 60)
			return err
		},
	} {
		if err := call(); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}
