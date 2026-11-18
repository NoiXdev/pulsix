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

// TestHeartbeatWithANULStores: PostgreSQL refuses a NUL in jsonb
// (\u0000) and in text. A heartbeat carrying a check whose params hold one,
// and a problem holding one, must still store — the check dropped, the
// problem's NUL replaced — or no heartbeat of that agent would ever land.
func TestHeartbeatWithANULStores(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-local-nul")
	h := createHost(t, d, org, zone, "nul-1")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-n", "v1", "9701")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Heartbeat(ctx, "9701", agents.HeartbeatReport{Version: "v2", SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{
		{Key: "local-cpu", Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 60},
		{Key: "local-d", Plugin: "disk", ParamsJSON: `{"mounts":["/data\u0000"]}`, IntervalSeconds: 60},
		{Key: "local-e", Plugin: "disk", ParamsJSON: `{"mounts\u0000":["/data"]}`, IntervalSeconds: 60},
	}, LocalCheckProblems: []string{"line 4: a\x00b"}}); err != nil {
		t.Fatalf("heartbeat with a NUL: %v", err)
	}
	r, err := svc.LocalChecksByHost(db.WithOrgScope(ctx, org), org, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Known || len(r.Checks) != 1 || r.Checks[0].Key != "local-cpu" || !slices.Equal(r.Problems, []string{
		"line 4: a b",
		"local-d: ignored by the server (the params contain a NUL character)",
		"local-e: ignored by the server (the params contain a NUL character)",
	}) {
		t.Fatalf("stored %+v, want local-cpu only and the problem without its NUL, then the dropped checks", r)
	}
}

// TestRenewalKeepsTheAgentReports (backlog 3.5a, final review finding 3):
// a certificate renewal is the same agent on a new serial, so what it last
// reported about agent.yaml stays true — the local checks keep resolving
// (a NULL would leave their tuples unmeasured and close their open alerts
// until the next heartbeat) and the secrets report stays with them, the
// four columns known or unknown together. A re-enrollment may be another
// agent: there all four start over.
func TestRenewalKeepsTheAgentReports(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-local-renew")
	h := createHost(t, d, org, zone, "renew-1")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-r", "v1", "9801")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Heartbeat(ctx, "9801", agents.HeartbeatReport{Version: "v1", SecretsState: "ok", SecretNames: []string{"pg_main"},
		LocalChecks:        []agents.LocalCheckInput{{Key: "local-cpu", Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 60}},
		LocalCheckProblems: []string{"line 9: unknown plugin"},
	}); err != nil {
		t.Fatal(err)
	}
	scoped := db.WithOrgScope(ctx, org)
	reports := func() (agents.LocalChecksReport, agents.SecretsReport) {
		t.Helper()
		local, err := svc.LocalChecksByHost(scoped, org, h.ID)
		if err != nil {
			t.Fatal(err)
		}
		secrets, err := svc.SecretsByHost(scoped, org, h.ID)
		if err != nil {
			t.Fatal(err)
		}
		return local, secrets
	}

	if err := svc.Renew(ctx, registrationOf(org, h.ID, "agent-r", "v1", "9802"), "9801"); err != nil {
		t.Fatal(err)
	}
	local, secrets := reports()
	if !local.Known || len(local.Checks) != 1 || local.Checks[0].Key != "local-cpu" || !slices.Equal(local.Problems, []string{"line 9: unknown plugin"}) {
		t.Fatalf("local checks after a renewal = %+v, want the report kept", local)
	}
	if !secrets.Known || secrets.State != "ok" || !slices.Equal(secrets.Names, []string{"pg_main"}) {
		t.Fatalf("secrets after a renewal = %+v, want the report kept", secrets)
	}

	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-r", "v2", "9803")); err != nil {
		t.Fatal(err)
	}
	if local, secrets := reports(); local.Known || secrets.Known {
		t.Fatalf("after a re-enrollment: local %+v, secrets %+v; want both unknown", local, secrets)
	}
}

// TestHeartbeatLocalChecksChangeSequence walks the report through changed,
// identical, changed, zero checks and unknown on the real store, so the
// IS DISTINCT FROM guards of RecordAgentHeartbeat (backlog 3.64) never keep
// a stale report or skip a needed write.
func TestHeartbeatLocalChecksChangeSequence(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-local-checks-seq")
	h := createHost(t, d, org, zone, "db-seq")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v1", "9601")); err != nil {
		t.Fatal(err)
	}
	cpu := func(interval int32) agents.LocalCheckInput {
		return agents.LocalCheckInput{Key: "local-cpu", Plugin: "cpu", ParamsJSON: `{}`, IntervalSeconds: interval}
	}
	steps := []struct {
		name     string
		report   agents.HeartbeatReport
		known    bool
		interval time.Duration // of the single check, 0 for none
		problems []string
	}{
		{"changed", agents.HeartbeatReport{Version: "v1", SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{cpu(10)}, LocalCheckProblems: []string{"p1"}}, true, 10 * time.Second, []string{"p1"}},
		{"identical", agents.HeartbeatReport{Version: "v1", SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{cpu(10)}, LocalCheckProblems: []string{"p1"}}, true, 10 * time.Second, []string{"p1"}},
		{"changed again", agents.HeartbeatReport{Version: "v1", SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{cpu(30)}, LocalCheckProblems: []string{"p2", "p3"}}, true, 30 * time.Second, []string{"p2", "p3"}},
		{"zero checks", agents.HeartbeatReport{Version: "v1", SecretsState: "ok"}, true, 0, []string{}},
		{"changed after zero", agents.HeartbeatReport{Version: "v1", SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{cpu(60)}}, true, 60 * time.Second, []string{}},
		{"unknown", agents.HeartbeatReport{Version: "v1"}, false, 0, nil},
		{"known again", agents.HeartbeatReport{Version: "v1", SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{cpu(10)}}, true, 10 * time.Second, []string{}},
	}
	for _, st := range steps {
		if err := svc.Heartbeat(ctx, "9601", st.report); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		got, err := svc.LocalChecksByHost(db.WithOrgScope(ctx, org), org, h.ID)
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if got.Known != st.known {
			t.Fatalf("%s: Known = %v, want %v", st.name, got.Known, st.known)
		}
		if st.interval == 0 {
			if len(got.Checks) != 0 {
				t.Fatalf("%s: checks = %+v, want none", st.name, got.Checks)
			}
		} else if len(got.Checks) != 1 || got.Checks[0].Interval != st.interval {
			t.Fatalf("%s: checks = %+v, want one with interval %v", st.name, got.Checks, st.interval)
		}
		if !slices.Equal(got.Problems, st.problems) || (st.problems == nil) != (got.Problems == nil) {
			t.Fatalf("%s: problems = %#v, want %#v", st.name, got.Problems, st.problems)
		}
	}
}
