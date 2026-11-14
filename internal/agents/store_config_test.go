package agents_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agents"
)

// TestConfigCheckIsWrittenOnlyWhenItChanges walks one agent through the
// configuration check the heartbeat records (backlog 3.13c): unknown after
// enrollment, written by the first comparison, left alone — no row write
// at all — while both versions stay the same and the stored check is
// younger than 10 minutes, rewritten once it is older, rewritten at once
// when either version changes, untouched by a plain heartbeat, readable
// through the list and the host read, and unknown again after a
// re-enrollment.
func TestConfigCheckIsWrittenOnlyWhenItChanges(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-config")
	h := createHost(t, d, org, zone, "db-1")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v1", "9401")); err != nil {
		t.Fatal(err)
	}
	type row struct {
		reported, expected *string
		checkedAt          *time.Time
		xmin               int64
	}
	read := func() row {
		t.Helper()
		var r row
		if err := d.Pool().QueryRow(ctx,
			`SELECT config_version, config_expected, config_checked_at, xmin::text::bigint FROM host_agents WHERE host_id = $1`, h.ID,
		).Scan(&r.reported, &r.expected, &r.checkedAt, &r.xmin); err != nil {
			t.Fatal(err)
		}
		return r
	}
	record := func(reported, expected string) {
		t.Helper()
		if err := svc.RecordConfigCheck(ctx, "9401", reported, expected); err != nil {
			t.Fatal(err)
		}
	}
	// age moves the stored check back by d.
	age := func(d0 time.Duration) {
		t.Helper()
		if _, err := d.Pool().Exec(ctx,
			`UPDATE host_agents SET config_checked_at = config_checked_at - make_interval(secs => $2::float8) WHERE host_id = $1`,
			h.ID, d0.Seconds()); err != nil {
			t.Fatal(err)
		}
	}
	is := func(step string, r row, reported, expected string) {
		t.Helper()
		if r.reported == nil || *r.reported != reported || r.expected == nil || *r.expected != expected || r.checkedAt == nil {
			t.Fatalf("%s: config_version = %v, config_expected = %v, config_checked_at = %v; want %q, %q, set",
				step, deref(r.reported), deref(r.expected), r.checkedAt, reported, expected)
		}
	}
	untouched := func(step string, before row) {
		t.Helper()
		if after := read(); after.xmin != before.xmin {
			t.Fatalf("%s: the row was written (xmin %d → %d); want no write", step, before.xmin, after.xmin)
		}
	}
	rewritten := func(step string, before row) row {
		t.Helper()
		after := read()
		if after.xmin == before.xmin || after.checkedAt == nil || !after.checkedAt.After(*before.checkedAt) {
			t.Fatalf("%s: config_checked_at %v → %v (xmin %d → %d); want the check rewritten",
				step, before.checkedAt, after.checkedAt, before.xmin, after.xmin)
		}
		return after
	}

	if r := read(); r.reported != nil || r.expected != nil || r.checkedAt != nil {
		t.Fatalf("enrolled: config columns = %v, %v, %v; want all NULL", r.reported, r.expected, r.checkedAt)
	}
	if a, err := svc.GetByHost(ctx, org, h.ID); err != nil || a.Config.Known {
		t.Fatalf("enrolled: GetByHost config = %+v, %v; want unknown", a.Config, err)
	}

	record("aaa", "bbb")
	first := read()
	is("first comparison", first, "aaa", "bbb")

	record("aaa", "bbb")
	untouched("the same check right after", first)
	age(9 * time.Minute)
	aged := read()
	record("aaa", "bbb")
	untouched("the same check 9 min later", aged)

	age(2 * time.Minute) // 11 min old now
	aged = read()
	record("aaa", "bbb")
	cur := rewritten("the same check 11 min later", aged)
	is("the same check 11 min later", cur, "aaa", "bbb")

	record("bbb", "bbb")
	cur2 := read()
	if cur2.xmin == cur.xmin {
		t.Fatal("a changed reported version was not written")
	}
	is("the agent converged", cur2, "bbb", "bbb")
	record("bbb", "ccc")
	cur3 := read()
	if cur3.xmin == cur2.xmin {
		t.Fatal("a changed expected version was not written")
	}
	is("the configuration moved on", cur3, "bbb", "ccc")

	// The plain heartbeat leaves the check as it is.
	if err := svc.Heartbeat(ctx, "9401", agents.HeartbeatReport{Version: "v1", SecretsState: "ok"}); err != nil {
		t.Fatal(err)
	}
	is("after a heartbeat", read(), "bbb", "ccc")

	// Both reads carry it.
	a, err := svc.GetByHost(ctx, org, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Config.Known || a.Config.Reported != "bbb" || a.Config.Expected != "ccc" || a.Config.CheckedAt.IsZero() || a.Config.Current() {
		t.Fatalf("GetByHost config = %+v; want bbb against ccc, outdated", a.Config)
	}
	list, err := svc.List(ctx, org)
	if err != nil || len(list) != 1 || list[0].Config != a.Config {
		t.Fatalf("List config = %+v, %v; want %+v", list, err, a.Config)
	}

	// An unknown serial writes nothing and is no error: the stream's own
	// identity check is what refuses it.
	if err := svc.RecordConfigCheck(ctx, "no-such-serial", "x", "y"); err != nil {
		t.Fatalf("unknown serial: %v", err)
	}

	// A re-enrollment is a new agent: nothing compared yet.
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v2", "9402")); err != nil {
		t.Fatal(err)
	}
	if r := read(); r.reported != nil || r.expected != nil || r.checkedAt != nil {
		t.Fatalf("after re-enrollment: config columns = %v, %v, %v; want all NULL", deref(r.reported), deref(r.expected), r.checkedAt)
	}
}

// TestConfigCheckAnEmptyReportIsKnown: an agent that has never applied a
// configuration reports "" — a real answer (it runs nothing), stored as
// the empty string, not as unknown.
func TestConfigCheckAnEmptyReportIsKnown(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-config-empty")
	h := createHost(t, d, org, zone, "db-1")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v1", "9411")); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordConfigCheck(ctx, "9411", "", "bbb"); err != nil {
		t.Fatal(err)
	}
	a, err := svc.GetByHost(ctx, org, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := agents.ConfigCheck{Known: true, Reported: "", Expected: "bbb", CheckedAt: a.Config.CheckedAt}
	if a.Config != want || a.Config.CheckedAt.IsZero() || a.Config.Current() {
		t.Fatalf("config = %+v; want an empty report against bbb, outdated", a.Config)
	}
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
