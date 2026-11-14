package agents_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/db"
)

// TestHeartbeatStoresLocalChecks walks one agent through the heartbeat
// shapes against the real store: checks and problems read back as sent
// (params as an object, numbers as JSON numbers); zero checks is known and
// empty; an old agent's heartbeat (no secrets state) writes NULL to both
// columns; Touch keeps the report; a re-registration starts unknown.
func TestHeartbeatStoresLocalChecks(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-local-checks")
	h := createHost(t, d, org, zone, "db-1")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v1", "9501")); err != nil {
		t.Fatal(err)
	}
	scoped := db.WithOrgScope(ctx, org)
	read := func() agents.LocalChecksReport {
		t.Helper()
		r, err := svc.LocalChecksByHost(scoped, org, h.ID)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	columnsNull := func() (checksNull, problemsNull bool) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx,
			`SELECT local_checks IS NULL, local_check_problems IS NULL FROM host_agents WHERE host_id = $1`, h.ID,
		).Scan(&checksNull, &problemsNull); err != nil {
			t.Fatal(err)
		}
		return checksNull, problemsNull
	}

	if r := read(); r.Known || r.Checks != nil {
		t.Fatalf("a registered agent that never reported = %+v, want unknown", r)
	}
	if checksNull, problemsNull := columnsNull(); !checksNull || !problemsNull {
		t.Fatal("a fresh registration stored local checks; want NULL")
	}

	report := agents.HeartbeatReport{Version: "v2", SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{
		{Key: "local-pg", Plugin: "postgres", ParamsJSON: `{"databases":["app","ü"],"secret":"pg_main"}`, IntervalSeconds: 300},
		{Key: "local-cpu", Plugin: "cpu", ParamsJSON: `{}`, IntervalSeconds: 10},
		{Key: "local-script", Plugin: "script", ParamsJSON: `{"commands":["backup_age"],"format":"json","timeout_seconds":30}`, IntervalSeconds: 60},
	}, LocalCheckProblems: []string{`line 9: check "local-x": unknown plugin "nope"`, "line 12: more than 32 local checks"}}
	if err := svc.Heartbeat(ctx, "9501", report); err != nil {
		t.Fatal(err)
	}
	want := agents.LocalChecksReport{Known: true, Checks: []agents.LocalCheck{
		{Key: "local-cpu", Plugin: "cpu", Params: map[string]any{}, Interval: 10 * time.Second},
		{Key: "local-pg", Plugin: "postgres", Params: map[string]any{"databases": []any{"app", "ü"}, "secret": "pg_main"}, Interval: 5 * time.Minute},
		{Key: "local-script", Plugin: "script", Params: map[string]any{"commands": []any{"backup_age"}, "format": "json", "timeout_seconds": float64(30)}, Interval: time.Minute},
	}, Problems: report.LocalCheckProblems}
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatalf("read back %+v\nwant         %+v", got, want)
	}
	listed, err := svc.List(scoped, org)
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0].LocalChecks, want) {
		t.Fatalf("List = %+v, %v; want the same local checks", listed, err)
	}
	var stored string
	if err := d.Pool().QueryRow(ctx, `SELECT local_checks->0->>'interval_seconds' || ' ' || jsonb_typeof(local_checks->0->'params')
		FROM host_agents WHERE host_id = $1`, h.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "10 object" {
		t.Fatalf("stored first entry = %q, want interval 10 and params an object", stored)
	}

	if err := svc.Touch(ctx, "9501", "v2"); err != nil {
		t.Fatal(err)
	}
	if r := read(); !r.Known || len(r.Checks) != 3 {
		t.Fatalf("after Touch = %+v, want the report kept", r)
	}

	if err := svc.Heartbeat(ctx, "9501", agents.HeartbeatReport{Version: "v2", SecretsState: "absent"}); err != nil {
		t.Fatal(err)
	}
	if r := read(); !r.Known || r.Checks == nil || len(r.Checks) != 0 || r.Problems == nil || len(r.Problems) != 0 {
		t.Fatalf("zero local checks = %#v, want known with empty lists", r)
	}
	if checksNull, problemsNull := columnsNull(); checksNull || problemsNull {
		t.Fatal("zero local checks stored NULL; want '[]' and '{}'")
	}

	// Downgraded to a build older than agent.yaml: unknown again.
	if err := svc.Heartbeat(ctx, "9501", report); err != nil {
		t.Fatal(err)
	}
	if err := svc.Heartbeat(ctx, "9501", agents.HeartbeatReport{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if r := read(); r.Known || r.Checks != nil || r.Problems != nil {
		t.Fatalf("old agent = %+v, want unknown", r)
	}
	if checksNull, problemsNull := columnsNull(); !checksNull || !problemsNull {
		t.Fatalf("old agent stored checks NULL=%v, problems NULL=%v; want both NULL", checksNull, problemsNull)
	}

	// A new registration describes a new agent: nothing reported yet.
	if err := svc.Heartbeat(ctx, "9501", report); err != nil {
		t.Fatal(err)
	}
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v2", "9502")); err != nil {
		t.Fatal(err)
	}
	if r := read(); r.Known {
		t.Fatalf("after re-registration = %+v, want unknown", r)
	}
}

// TestLocalChecksByHostsReadsSeveralHosts: one read for the resolver,
// keyed by host; a host without an agent is absent (unknown), and another
// organization's host is not read.
func TestLocalChecksByHostsReadsSeveralHosts(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-local-by-hosts")
	a := createHost(t, d, org, zone, "a")
	b := createHost(t, d, org, zone, "b")
	none := createHost(t, d, org, zone, "none")
	for i, h := range []uuid.UUID{a.ID, b.ID} {
		serial := []string{"9601", "9602"}[i]
		if err := svc.Register(ctx, registrationOf(org, h, "agent-"+serial, "v1", serial)); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Heartbeat(ctx, "9601", agents.HeartbeatReport{Version: "v1", SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{
		{Key: "local-cpu", Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 60},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Heartbeat(ctx, "9602", agents.HeartbeatReport{Version: "v1", SecretsState: "ok", LocalCheckProblems: []string{"line 3: x"}}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.LocalChecksByHosts(db.WithOrgScope(ctx, org), org, []uuid.UUID{a.ID, b.ID, none.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("by hosts = %+v, want two entries", got)
	}
	if r := got[a.ID]; !r.Known || len(r.Checks) != 1 || r.Checks[0].Key != "local-cpu" || len(r.Problems) != 0 {
		t.Fatalf("host a = %+v", r)
	}
	if r := got[b.ID]; !r.Known || len(r.Checks) != 0 || !slices.Equal(r.Problems, []string{"line 3: x"}) {
		t.Fatalf("host b = %+v", r)
	}
	if got[none.ID].Known {
		t.Fatal("a host without an agent reads as known")
	}

	other := uuid.Must(uuid.NewV7())
	if got, err := svc.LocalChecksByHosts(db.WithOrgScope(ctx, other), other, []uuid.UUID{a.ID}); err != nil || len(got) != 0 {
		t.Fatalf("another organization read %+v, %v; want nothing", got, err)
	}
	if got, err := svc.LocalChecksByHosts(db.WithOrgScope(ctx, org), org, nil); err != nil || len(got) != 0 {
		t.Fatalf("no hosts = %+v, %v; want an empty map", got, err)
	}
}
