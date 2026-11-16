package probes_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/probes"
)

func seedStoreOrg(t *testing.T, d *db.DB, slug string) (org uuid.UUID, newHost func(name string) uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	q := gen.New(d.Pool())
	o, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	z, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: o.ID, Slug: "z", Name: "z"})
	if err != nil {
		t.Fatal(err)
	}
	return o.ID, func(name string) uuid.UUID {
		t.Helper()
		h, err := q.CreateHost(ctx, gen.CreateHostParams{
			ID: uuid.Must(uuid.NewV7()), OrgID: o.ID, ZoneID: z.ID, Name: name,
			Address: "127.0.0.1", Monitored: true, Facts: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return h.ID
	}
}

func asg(org, agent, target uuid.UUID, key string, interval time.Duration, params map[string]any) probes.Assignment {
	return probes.Assignment{
		OrgID: org, AgentHostID: agent, TargetHostID: target, CheckKey: key, Plugin: "tcp",
		Params: params, Interval: interval, Address: "10.0.0.1",
	}
}

func TestReplaceOrgAssignmentsDiffs(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	store := probes.NewSQLStore(d)
	org, host := seedStoreOrg(t, d, "pa-diff")
	otherOrg, otherHost := seedStoreOrg(t, d, "pa-diff-other")
	a1, a2, a3, target := host("a1"), host("a2"), host("a3"), host("target")
	oa, ot := otherHost("oa"), otherHost("ot")
	scoped := db.WithOrgScope(ctx, org)

	if err := store.ReplaceOrgAssignments(db.WithOrgScope(ctx, otherOrg), otherOrg,
		[]probes.Assignment{asg(otherOrg, oa, ot, "k", time.Minute, map[string]any{"port": float64(1)})}); err != nil {
		t.Fatal(err)
	}

	first := []probes.Assignment{
		asg(org, a1, target, "k", time.Minute, map[string]any{"port": float64(80)}),
		asg(org, a2, target, "k", time.Minute, map[string]any{"port": float64(80)}),
	}
	if err := store.ReplaceOrgAssignments(scoped, org, first); err != nil {
		t.Fatal(err)
	}
	var assignedAt time.Time
	if err := d.Pool().QueryRow(ctx, `SELECT assigned_at FROM probe_assignments WHERE agent_host_id = $1`, a1).Scan(&assignedAt); err != nil {
		t.Fatal(err)
	}

	// a1 unchanged, a2 changed, a3 new; a1's row must not be rewritten.
	second := []probes.Assignment{
		first[0],
		asg(org, a2, target, "k", 2*time.Minute, map[string]any{"port": float64(443)}),
		asg(org, a3, target, "k", time.Minute, map[string]any{"port": float64(80)}),
	}
	if err := store.ReplaceOrgAssignments(scoped, org, second); err != nil {
		t.Fatal(err)
	}
	var again time.Time
	if err := d.Pool().QueryRow(ctx, `SELECT assigned_at FROM probe_assignments WHERE agent_host_id = $1`, a1).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if !again.Equal(assignedAt) {
		t.Errorf("unchanged assignment rewritten: assigned_at %v -> %v", assignedAt, again)
	}
	got, err := store.AgentAssignments(scoped, org, a2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []probes.Assignment{second[1]}) {
		t.Errorf("a2 = %+v, want %+v", got, second[1])
	}
	agents, err := store.SlotAgents(scoped, org, target, "k")
	if err != nil {
		t.Fatal(err)
	}
	want := []uuid.UUID{a1, a2, a3}
	slices.SortFunc(want, func(x, y uuid.UUID) int { return slices.Compare(x[:], y[:]) })
	if !reflect.DeepEqual(agents, want) {
		t.Errorf("SlotAgents = %v, want %v", agents, want)
	}

	// Gone: a1 and a3 leave.
	if err := store.ReplaceOrgAssignments(scoped, org, second[1:2]); err != nil {
		t.Fatal(err)
	}
	agents, err = store.SlotAgents(scoped, org, target, "k")
	if err != nil || !reflect.DeepEqual(agents, []uuid.UUID{a2}) {
		t.Fatalf("after delete SlotAgents = %v, %v", agents, err)
	}

	// The other organization is untouched, and an empty set clears only this one.
	if err := store.ReplaceOrgAssignments(scoped, org, nil); err != nil {
		t.Fatal(err)
	}
	other, err := store.AgentAssignments(db.WithOrgScope(ctx, otherOrg), otherOrg, oa)
	if err != nil || len(other) != 1 {
		t.Fatalf("other org assignments = %v, %v; want 1", other, err)
	}
	// Each organization's scope hides the other's rows.
	hidden, err := store.AgentAssignments(scoped, org, oa)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("cross-org read = %v, %v; want none", hidden, err)
	}
}

func TestReplaceOrgAssignmentsRejectsForeignHost(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	store := probes.NewSQLStore(d)
	org, host := seedStoreOrg(t, d, "pa-fk")
	_, otherHost := seedStoreOrg(t, d, "pa-fk-other")
	err := store.ReplaceOrgAssignments(db.WithOrgScope(ctx, org), org,
		[]probes.Assignment{asg(org, host("a"), otherHost("t"), "k", time.Minute, nil)})
	if err == nil {
		t.Fatal("an assignment naming another organization's host was accepted")
	}
}

func TestRecordVantageStoresCurrentAndSamples(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	store := probes.NewSQLStore(d)
	org, host := seedStoreOrg(t, d, "pv-record")
	target, agent := host("target"), host("agent")
	scoped := db.WithOrgScope(ctx, org)
	vantage := agent.String()
	at := time.Now().UTC().Truncate(time.Millisecond)
	f := func(v float64) *float64 { return &v }
	s := func(v string) *string { return &v }

	obs := []checks.Observation{
		{HostID: target, CheckKey: "site", Metric: "tcp.up", At: at, Value: f(1)},
		{HostID: target, CheckKey: "site", Metric: "tcp.ms", At: at, Value: f(12.5)},
		{HostID: target, CheckKey: "site", Item: "x", Metric: "tcp.state", At: at, Label: s("open")},
	}
	if err := store.RecordVantage(scoped, org, vantage, obs); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordVantage(scoped, org, "manager", obs[:1]); err != nil {
		t.Fatal(err)
	}
	rows, err := store.VantageCurrent(scoped, org, target, "site")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("VantageCurrent = %d rows, want 4: %+v", len(rows), rows)
	}
	if rows[3].Vantage != "manager" || rows[0].Vantage != vantage {
		t.Errorf("rows are not ordered by vantage, item, metric: %+v", rows)
	}

	// A later round replaces the value; an error replaces it and adds no sample.
	later := at.Add(time.Minute)
	if err := store.RecordVantage(scoped, org, vantage, []checks.Observation{
		{HostID: target, CheckKey: "site", Metric: "tcp.up", At: later, Value: f(0)},
		{HostID: target, CheckKey: "site", Metric: "tcp.ms", At: later, Err: "timeout"},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err = store.VantageCurrent(scoped, org, target, "site")
	if err != nil {
		t.Fatal(err)
	}
	var up, ms *probes.VantageRow
	for i, r := range rows {
		switch {
		case r.Vantage == vantage && r.Metric == "tcp.up":
			up = &rows[i]
		case r.Vantage == vantage && r.Metric == "tcp.ms":
			ms = &rows[i]
		}
	}
	if up == nil || up.Value == nil || *up.Value != 0 || !up.At.Equal(later) || up.Err != "" {
		t.Errorf("tcp.up = %+v", up)
	}
	if ms == nil || ms.Value != nil || ms.Err != "timeout" {
		t.Errorf("tcp.ms = %+v, want the error only", ms)
	}
	var samples, errSamples int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE at = $2 AND metric = 'tcp.ms') FROM probe_samples WHERE host_id = $1`, target, later).Scan(&samples, &errSamples); err != nil {
		t.Fatal(err)
	}
	if samples != 5 || errSamples != 0 {
		t.Errorf("samples = %d (error-round tcp.ms samples %d), want 5 and 0", samples, errSamples)
	}
	// Nothing leaked into the checks' own tables.
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM check_current) + (SELECT count(*) FROM check_samples)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("check tables hold %d rows, want 0 (%v)", n, err)
	}
}

func TestRecordVantageRefusesBadVantage(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	store := probes.NewSQLStore(d)
	org, host := seedStoreOrg(t, d, "pv-bad")
	target := host("t")
	v := 1.0
	err := store.RecordVantage(db.WithOrgScope(ctx, org), org, "Not-A-Vantage",
		[]checks.Observation{{HostID: target, CheckKey: "k", Metric: "m", At: time.Now(), Value: &v}})
	if err == nil {
		t.Fatal("a malformed vantage was stored")
	}
}

// TestRecordVantageReportsAGoneTarget: a target host that no longer
// exists (deleted after the caller read its assignments; never created
// here, which fails the same foreign key) is ErrTargetGone, and nothing of
// the call is stored.
func TestRecordVantageReportsAGoneTarget(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	store := probes.NewSQLStore(d)
	org, host := seedStoreOrg(t, d, "pv-gone")
	target, agent := host("t"), host("a")
	gone := uuid.Must(uuid.NewV7())
	v := 1.0
	err := store.RecordVantage(db.WithOrgScope(ctx, org), org, agent.String(), []checks.Observation{
		{HostID: target, CheckKey: "k", Metric: "tcp.up", At: time.Now(), Value: &v},
		{HostID: gone, CheckKey: "k", Metric: "tcp.up", At: time.Now(), Value: &v},
	})
	if !errors.Is(err, probes.ErrTargetGone) {
		t.Fatalf("err = %v, want ErrTargetGone", err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM probe_current) + (SELECT count(*) FROM probe_samples)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("probe tables hold %d rows, want 0 (%v)", n, err)
	}
}

func TestDeleteVantagesKeepsTheListedOnes(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	store := probes.NewSQLStore(d)
	org, host := seedStoreOrg(t, d, "pv-del")
	target, a, b := host("t"), host("a"), host("b")
	scoped := db.WithOrgScope(ctx, org)
	v := 1.0
	for _, vt := range []string{"manager", a.String(), b.String()} {
		for _, key := range []string{"k", "other"} {
			if err := store.RecordVantage(scoped, org, vt,
				[]checks.Observation{{HostID: target, CheckKey: key, Metric: "m", At: time.Now(), Value: &v}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.DeleteVantages(scoped, org, target, "k", []string{"manager", b.String()}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.VantageCurrent(scoped, org, target, "k")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r.Vantage)
	}
	if want := []string{"manager", b.String()}; !reflect.DeepEqual(got, want) && !reflect.DeepEqual(got, []string{b.String(), "manager"}) {
		t.Errorf("remaining vantages = %v, want %v", got, want)
	}
	other, err := store.VantageCurrent(scoped, org, target, "other")
	if err != nil || len(other) != 3 {
		t.Errorf("another key's rows = %d, %v; want 3 untouched", len(other), err)
	}
	var samples int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM probe_samples WHERE check_key = 'k'`).Scan(&samples); err != nil || samples != 3 {
		t.Errorf("samples = %d, %v; want all 3 kept", samples, err)
	}
	// An empty keep list deletes every vantage of the slot.
	if err := store.DeleteVantages(scoped, org, target, "k", nil); err != nil {
		t.Fatal(err)
	}
	rows, err = store.VantageCurrent(scoped, org, target, "k")
	if err != nil || len(rows) != 0 {
		t.Errorf("after empty keep: %d rows, %v", len(rows), err)
	}
}

func TestVantageCurrentIsOrgScoped(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	store := probes.NewSQLStore(d)
	org, host := seedStoreOrg(t, d, "pv-rls")
	otherOrg, _ := seedStoreOrg(t, d, "pv-rls-other")
	target := host("t")
	v := 1.0
	if err := store.RecordVantage(db.WithOrgScope(ctx, org), org, "manager",
		[]checks.Observation{{HostID: target, CheckKey: "k", Metric: "m", At: time.Now(), Value: &v}}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.VantageCurrent(db.WithOrgScope(ctx, otherOrg), otherOrg, target, "k")
	if err != nil || len(rows) != 0 {
		t.Fatalf("other org read %d rows, %v", len(rows), err)
	}
	// Even asking for this org's id under the other org's scope sees nothing.
	rows, err = store.VantageCurrent(db.WithOrgScope(ctx, otherOrg), org, target, "k")
	if err != nil || len(rows) != 0 {
		t.Fatalf("scope mismatch read %d rows, %v", len(rows), err)
	}
}

func TestReplaceOrgAssignmentsComparesParamsCanonically(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	store := probes.NewSQLStore(d)
	org, host := seedStoreOrg(t, d, "pa-canon")
	agent, target := host("a"), host("t")
	scoped := db.WithOrgScope(ctx, org)
	typed := func() []probes.Assignment {
		return []probes.Assignment{asg(org, agent, target, "k", time.Minute,
			map[string]any{"port": 443, "names": []string{"a", "b"}, "tls": true})}
	}
	if err := store.ReplaceOrgAssignments(scoped, org, typed()); err != nil {
		t.Fatal(err)
	}
	var first time.Time
	if err := d.Pool().QueryRow(ctx, `SELECT assigned_at FROM probe_assignments`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceOrgAssignments(scoped, org, typed()); err != nil {
		t.Fatal(err)
	}
	var second time.Time
	if err := d.Pool().QueryRow(ctx, `SELECT assigned_at FROM probe_assignments`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if !first.Equal(second) {
		t.Errorf("identical typed params were rewritten: %v -> %v", first, second)
	}
}

func TestReplaceOrgAssignmentsRefusesDuplicates(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	store := probes.NewSQLStore(d)
	org, host := seedStoreOrg(t, d, "pa-dup")
	agent, target := host("a"), host("t")
	a := asg(org, agent, target, "k", time.Minute, nil)
	err := store.ReplaceOrgAssignments(db.WithOrgScope(ctx, org), org, []probes.Assignment{a, a})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v, want a duplicate assignment error", err)
	}
}

// TestVantageSeriesBucketsOneVantage: the per-vantage series reads one
// vantage's numeric samples of one (check, item, metric) in [from, to),
// averaged per bucket, and nothing of another vantage, metric, or an
// error round.
func TestVantageSeriesBucketsOneVantage(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	store := probes.NewSQLStore(d)
	org, host := seedStoreOrg(t, d, "pv-series")
	target, agent := host("target"), host("agent")
	scoped := db.WithOrgScope(ctx, org)
	to := time.Now().UTC().Truncate(time.Hour).Add(time.Hour)
	from := to.Add(-time.Hour)
	f := func(v float64) *float64 { return &v }
	rec := func(vantage string, at time.Time, metric string, v *float64, errText string) {
		t.Helper()
		if err := store.RecordVantage(scoped, org, vantage, []checks.Observation{
			{HostID: target, CheckKey: "site", Metric: metric, At: at, Value: v, Err: errText},
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec(agent.String(), from.Add(time.Minute), "tcp.response_ms", f(10), "")
	rec(agent.String(), from.Add(2*time.Minute), "tcp.response_ms", f(20), "")
	rec(agent.String(), from.Add(40*time.Minute), "tcp.response_ms", f(30), "")
	rec(agent.String(), from.Add(41*time.Minute), "tcp.response_ms", nil, "timeout")
	rec(agent.String(), from.Add(-time.Minute), "tcp.response_ms", f(99), "") // before the window
	rec(agent.String(), from.Add(time.Minute), "tcp.up", f(1), "")
	rec("manager", from.Add(time.Minute), "tcp.response_ms", f(500), "")

	points, err := store.VantageSeries(scoped, org, target, "site", agent.String(), "", "tcp.response_ms", from, to, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Value != 15 || points[1].Value != 30 || !points[0].At.Equal(from) {
		t.Fatalf("points = %+v, want 15 at %s and 30", points, from)
	}
	points, err = store.VantageSeries(db.WithOrgScope(ctx, uuid.New()), org, target, "site", agent.String(), "", "tcp.response_ms", from, to, 2)
	if err != nil || len(points) != 0 {
		t.Fatalf("another org's scope read %+v, %v", points, err)
	}
}
