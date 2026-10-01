package rules_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestWorse is the full 4x4 table the brief calls out by name: every pair
// of states, both orderings, against the single ordering an operator ever
// sees on a fleet card. unknown ranking below warn (never above) is the one
// row in this table someone is likely to get backwards, which is exactly
// why every pair is pinned rather than a representative sample.
func TestWorse(t *testing.T) {
	// worst-to-best, per the design's own ordering.
	order := []rules.State{rules.StateCrit, rules.StateWarn, rules.StateUnknown, rules.StateOK}
	for i, a := range order {
		for j, b := range order {
			want := a
			if j < i {
				want = b
			}
			t.Run(string(a)+"_vs_"+string(b), func(t *testing.T) {
				if got := rules.Worse(a, b); got != want {
					t.Fatalf("Worse(%s, %s) = %s, want %s", a, b, got, want)
				}
			})
		}
	}
}

// TestStateRank pins the exported ordering Worse itself is built on and
// that internal/api's fleet listing now sorts by directly (backlog 3.31):
// every pair of states, both directions, against the single worst-to-best
// order the design specifies — the same full-table treatment TestWorse
// above gives Worse, so the exported surface is pinned at least as well as
// the function it replaces a duplicate of.
func TestStateRank(t *testing.T) {
	// worst-to-best, per the design's own ordering.
	order := []rules.State{rules.StateCrit, rules.StateWarn, rules.StateUnknown, rules.StateOK}
	for i, a := range order {
		for j, b := range order {
			switch {
			case i == j:
				if rules.StateRank(a) != rules.StateRank(b) {
					t.Errorf("StateRank(%s) != StateRank(%s), want equal (same state)", a, b)
				}
			case i < j:
				if rules.StateRank(a) <= rules.StateRank(b) {
					t.Errorf("StateRank(%s) <= StateRank(%s), want %s to outrank %s", a, b, a, b)
				}
			default:
				if rules.StateRank(a) >= rules.StateRank(b) {
					t.Errorf("StateRank(%s) >= StateRank(%s), want %s to outrank %s", a, b, b, a)
				}
			}
		}
	}
}

// seedZonedHost is seedHost (store_sql_test.go) plus the zone id, which
// FleetFilter.ZoneID needs and seedHost itself discards.
func seedZonedHost(t *testing.T, d *db.DB, org uuid.UUID, name string) (zoneID, hostID uuid.UUID) {
	t.Helper()
	q := gen.New(d.Pool())
	zone, err := q.CreateZone(context.Background(), gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: name, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	host, err := q.CreateHost(context.Background(), gen.CreateHostParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: name, Facts: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return zone.ID, host.ID
}

// insertTag writes one host_tags row directly, the same bypass-the-service
// shape hosts.tags_store_sql_test.go's own insertRawTag uses.
func insertTag(t *testing.T, d *db.DB, org, host uuid.UUID, key, value string) {
	t.Helper()
	q := gen.New(d.Pool())
	if err := q.InsertHostTag(context.Background(), gen.InsertHostTagParams{
		HostID: host, OrgID: org, Key: key, Value: value, Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
}

// stateRow is the check_states fixture insertCheckState writes; every field
// left at its zero value produces a plain "ok, nothing pending" row.
type stateRow struct {
	tuple              rules.Tuple
	state              rules.State
	since, evaluatedAt time.Time
	ruleID             *uuid.UUID
	lastValue          *float64
	lastLabel          *string
	candidateState     *string
	candidateSince     *time.Time
	flapping           bool
}

// insertCheckState writes one check_states row directly through the pool
// (bypassing row-level security, like TestRuleStoreDecodeErrors' own raw
// inserts): Task 7 ships no writer for this table at all — EvalStore
// (Task 9) is what will — so a store test exercising the read side has no
// path to a fixture row except writing one itself.
func insertCheckState(t *testing.T, d *db.DB, org, host uuid.UUID, r stateRow) {
	t.Helper()
	_, err := d.Pool().Exec(context.Background(), `
		INSERT INTO check_states (
			org_id, host_id, check_key, item, metric, state, since, rule_id,
			last_value, last_label, candidate_state, candidate_since, flapping, evaluated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		org, host, r.tuple.CheckKey, r.tuple.Item, r.tuple.Metric, string(r.state), r.since, r.ruleID,
		r.lastValue, r.lastLabel, r.candidateState, r.candidateSince, r.flapping, r.evaluatedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
}

// insertStateChange writes one check_state_changes row directly, for the
// same reason insertCheckState does.
func insertStateChange(t *testing.T, d *db.DB, org, host uuid.UUID, at time.Time, tuple rules.Tuple, from, to rules.State) {
	t.Helper()
	_, err := d.Pool().Exec(context.Background(), `
		INSERT INTO check_state_changes (at, org_id, host_id, check_key, item, metric, from_state, to_state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		at, org, host, tuple.CheckKey, tuple.Item, tuple.Metric, string(from), string(to),
	)
	if err != nil {
		t.Fatal(err)
	}
}

// insertHostState writes one check_host_states row directly, for the same
// reason insertCheckState does.
func insertHostState(t *testing.T, d *db.DB, org, host uuid.UUID, state rules.State, crit, warn, unknown int, since, evaluatedAt time.Time) {
	t.Helper()
	_, err := d.Pool().Exec(context.Background(), `
		INSERT INTO check_host_states (org_id, host_id, state, crit_count, warn_count, unknown_count, since, evaluated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		org, host, string(state), crit, warn, unknown, since, evaluatedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func ptr[T any](v T) *T { return &v }

// TestStateStoreStatesForHost round-trips a plain row and a fully-populated
// one (rule, last value, a ripening candidate, flapping) through
// StatesForHost, and pins that a host in another organization (even one
// queried under the right org_id but the wrong host_id combination) sees
// nothing.
func TestStateStoreStatesForHost(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "states-for-host")
	_, host := seedZonedHost(t, d, org, "h1")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	// check_states.rule_id carries a real composite FK into check_rules
	// (org_id, id) — a state referencing a rule that does not exist would
	// be rejected the same way it is here, so the fixture needs a genuine
	// row, not a bare random id.
	r := hostRule(org, host)
	if err := st.Create(scoped, r); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	insertCheckState(t, d, org, host, stateRow{
		tuple: rules.Tuple{CheckKey: "cpu", Item: "", Metric: "cpu.used_pct"},
		state: rules.StateOK, since: now, evaluatedAt: now,
	})
	insertCheckState(t, d, org, host, stateRow{
		tuple: rules.Tuple{CheckKey: "disk", Item: "sda1", Metric: "disk.used_pct"},
		state: rules.StateCrit, since: now.Add(-time.Hour), evaluatedAt: now,
		ruleID: &r.ID, lastValue: ptr(97.5), candidateState: ptr("crit"),
		candidateSince: ptr(now.Add(-5 * time.Minute)), flapping: true,
	})

	got, err := st.StatesForHost(scoped, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	// ListCheckStates orders by (check_key, item, metric); "cpu" < "disk".
	cpu, disk := got[0], got[1]
	if cpu.Tuple.CheckKey != "cpu" || cpu.State != rules.StateOK {
		t.Fatalf("cpu row = %+v", cpu)
	}
	if cpu.RuleID != nil || cpu.LastValue != nil || cpu.Candidate != "" || cpu.CandidateSince != nil || cpu.Flapping {
		t.Fatalf("plain row must have every optional field at its zero value: %+v", cpu)
	}
	if disk.Tuple.CheckKey != "disk" || disk.Tuple.Item != "sda1" || disk.State != rules.StateCrit {
		t.Fatalf("disk row = %+v", disk)
	}
	if disk.RuleID == nil || *disk.RuleID != r.ID {
		t.Fatalf("disk.RuleID = %v, want %v", disk.RuleID, r.ID)
	}
	if disk.LastValue == nil || *disk.LastValue != 97.5 {
		t.Fatalf("disk.LastValue = %v, want 97.5", disk.LastValue)
	}
	if disk.Candidate != rules.StateCrit {
		t.Fatalf("disk.Candidate = %v, want crit", disk.Candidate)
	}
	if disk.CandidateSince == nil || !disk.CandidateSince.Equal(now.Add(-5*time.Minute)) {
		t.Fatalf("disk.CandidateSince = %v, want %v", disk.CandidateSince, now.Add(-5*time.Minute))
	}
	if !disk.Flapping {
		t.Fatal("disk.Flapping = false, want true")
	}

	// A host that does not exist under this org (queried through the same
	// org scope but a foreign host id) sees nothing rather than an error.
	ghost := uuid.Must(uuid.NewV7())
	empty, err := st.StatesForHost(scoped, org, ghost)
	if err != nil || len(empty) != 0 {
		t.Fatalf("StatesForHost for an unknown host = %+v, %v, want empty, nil", empty, err)
	}
}

// TestStateStoreStatesForHostOrgIsolation pins that a host's states are
// invisible under any other organization's scope, even the app-level WHERE
// org_id filtering alone (independent of what RLS additionally enforces —
// see TestStateStoreRLS below for that half).
func TestStateStoreStatesForHostOrgIsolation(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "states-isolation-a")
	orgB := seedOrg(t, d, "states-isolation-b")
	_, hostA := seedZonedHost(t, d, orgA, "h1")
	st := rules.NewSQLStore(d)

	now := time.Now().UTC().Truncate(time.Microsecond)
	insertCheckState(t, d, orgA, hostA, stateRow{
		tuple: rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}, state: rules.StateOK, since: now, evaluatedAt: now,
	})

	got, err := st.StatesForHost(db.WithOrgScope(context.Background(), orgB), orgB, hostA)
	if err != nil || len(got) != 0 {
		t.Fatalf("orgB reading orgA's host = %+v, %v, want empty, nil", got, err)
	}
}

// TestStateStoreChangeCounts seeds a history straddling the 30-minute
// boundary ChangeCounts' own callers use for flapping: two changes well
// inside the window, one exactly at the boundary (inclusive), one just
// outside it (excluded), and a second tuple with no changes at all in the
// window (absent from the result, not a zero-count entry).
func TestStateStoreChangeCounts(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "change-counts")
	_, host := seedZonedHost(t, d, org, "h1")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-30 * time.Minute)
	flapping := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	quiet := rules.Tuple{CheckKey: "disk", Item: "sda1", Metric: "disk.used_pct"}

	insertStateChange(t, d, org, host, now.Add(-5*time.Minute), flapping, rules.StateOK, rules.StateWarn)
	insertStateChange(t, d, org, host, now.Add(-20*time.Minute), flapping, rules.StateWarn, rules.StateOK)
	insertStateChange(t, d, org, host, since, flapping, rules.StateOK, rules.StateWarn)                   // exactly at the boundary: counted
	insertStateChange(t, d, org, host, since.Add(-time.Second), flapping, rules.StateWarn, rules.StateOK) // just outside: excluded
	// A change on `quiet` well before the window: it exists in history but
	// must not appear in this call's result at all.
	insertStateChange(t, d, org, host, since.Add(-time.Hour), quiet, rules.StateOK, rules.StateCrit)

	counts, err := st.ChangeCounts(scoped, org, host, since)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := counts[flapping], 3; got != want {
		t.Fatalf("counts[flapping] = %d, want %d (map: %+v)", got, want, counts)
	}
	if _, present := counts[quiet]; present {
		t.Fatalf("quiet tuple must be absent from the map, not a zero entry: %+v", counts)
	}
}

// TestStateStoreChangeCountsOrgIsolation pins the same org-scoped WHERE
// filtering TestStateStoreStatesForHostOrgIsolation pins for StatesForHost.
func TestStateStoreChangeCountsOrgIsolation(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "change-counts-isolation-a")
	orgB := seedOrg(t, d, "change-counts-isolation-b")
	_, hostA := seedZonedHost(t, d, orgA, "h1")
	st := rules.NewSQLStore(d)

	now := time.Now().UTC()
	tuple := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	insertStateChange(t, d, orgA, hostA, now, tuple, rules.StateOK, rules.StateWarn)

	counts, err := st.ChangeCounts(db.WithOrgScope(context.Background(), orgB), orgB, hostA, now.Add(-time.Hour))
	if err != nil || len(counts) != 0 {
		t.Fatalf("orgB reading orgA's changes = %+v, %v, want empty, nil", counts, err)
	}
}

// TestStateStoreHostStates exercises HostStates under no filter and under
// each of FleetFilter's three fields in turn, against three hosts: h1 in
// zone1 tagged role=web, h2 in zone2 tagged role=web, h3 in zone1 untagged.
// A monitoring group "web" selects role=web, matching h1 and h2.
func TestStateStoreHostStates(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "host-states")
	zone1, h1 := seedZonedHost(t, d, org, "h1")
	zone2, h2 := seedZonedHost(t, d, org, "h2")
	_, h3 := seedZonedHost(t, d, org, "h3")
	insertTag(t, d, org, h1, "role", "web")
	insertTag(t, d, org, h2, "role", "web")
	insertTag(t, d, org, h3, "env", "staging")
	webGroup := seedGroup(t, d, org, "web") // seedGroup (store_sql_test.go) sets selector "role="+name = "role=web"

	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	now := time.Now().UTC().Truncate(time.Microsecond)
	insertHostState(t, d, org, h1, rules.StateCrit, 1, 0, 0, now, now)
	insertHostState(t, d, org, h2, rules.StateWarn, 0, 1, 0, now, now)
	insertHostState(t, d, org, h3, rules.StateOK, 0, 0, 0, now, now)

	byHost := func(states []rules.HostState) map[uuid.UUID]rules.HostState {
		m := make(map[uuid.UUID]rules.HostState, len(states))
		for _, s := range states {
			m[s.HostID] = s
		}
		return m
	}

	t.Run("no filter returns every host", func(t *testing.T) {
		got, err := st.HostStates(scoped, org, rules.FleetFilter{})
		if err != nil {
			t.Fatal(err)
		}
		m := byHost(got)
		if len(m) != 3 || m[h1].State != rules.StateCrit || m[h2].State != rules.StateWarn || m[h3].State != rules.StateOK {
			t.Fatalf("got = %+v", got)
		}
		if m[h1].Crit != 1 {
			t.Fatalf("h1.Crit = %d, want 1", m[h1].Crit)
		}
	})

	t.Run("zone filter", func(t *testing.T) {
		got, err := st.HostStates(scoped, org, rules.FleetFilter{ZoneID: &zone1})
		if err != nil {
			t.Fatal(err)
		}
		m := byHost(got)
		if len(m) != 1 {
			t.Fatalf("got = %+v, want only h1 (zone1)", got)
		}
		if _, ok := m[h1]; !ok {
			t.Fatalf("got = %+v, want h1", got)
		}
		got2, err := st.HostStates(scoped, org, rules.FleetFilter{ZoneID: &zone2})
		if err != nil || len(got2) != 1 || got2[0].HostID != h2 {
			t.Fatalf("zone2 filter = %+v, %v, want only h2", got2, err)
		}
	})

	t.Run("tag filter, exact key=value", func(t *testing.T) {
		got, err := st.HostStates(scoped, org, rules.FleetFilter{Tag: "role=web"})
		if err != nil {
			t.Fatal(err)
		}
		m := byHost(got)
		if len(m) != 2 || !mapHasKeys(m, h1, h2) {
			t.Fatalf("got = %+v, want h1 and h2", got)
		}
	})

	t.Run("tag filter, bare key existence", func(t *testing.T) {
		got, err := st.HostStates(scoped, org, rules.FleetFilter{Tag: "env"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].HostID != h3 {
			t.Fatalf("got = %+v, want only h3", got)
		}
	})

	t.Run("group filter", func(t *testing.T) {
		got, err := st.HostStates(scoped, org, rules.FleetFilter{GroupID: &webGroup})
		if err != nil {
			t.Fatal(err)
		}
		m := byHost(got)
		if len(m) != 2 || !mapHasKeys(m, h1, h2) {
			t.Fatalf("got = %+v, want h1 and h2 (the group's own selector matches both)", got)
		}
	})

	t.Run("group filter naming a nonexistent group matches nothing, no error", func(t *testing.T) {
		got, err := st.HostStates(scoped, org, rules.FleetFilter{GroupID: ptr(uuid.Must(uuid.NewV7()))})
		if err != nil || len(got) != 0 {
			t.Fatalf("got = %+v, %v, want empty, nil", got, err)
		}
	})
}

func mapHasKeys(m map[uuid.UUID]rules.HostState, ids ...uuid.UUID) bool {
	for _, id := range ids {
		if _, ok := m[id]; !ok {
			return false
		}
	}
	return true
}

// TestStateStoreHostStatesOrgIsolation pins that a host_states row never
// crosses into another organization's HostStates result, under no filter
// and under a filter that would otherwise match it (its own group's
// selector matches its own tag, but that group id means nothing in the
// other org).
func TestStateStoreHostStatesOrgIsolation(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "host-states-isolation-a")
	orgB := seedOrg(t, d, "host-states-isolation-b")
	_, hostA := seedZonedHost(t, d, orgA, "h1")
	insertTag(t, d, orgA, hostA, "role", "web")
	groupA := seedGroup(t, d, orgA, "web")
	now := time.Now().UTC().Truncate(time.Microsecond)
	insertHostState(t, d, orgA, hostA, rules.StateCrit, 1, 0, 0, now, now)

	st := rules.NewSQLStore(d)
	scopedB := db.WithOrgScope(context.Background(), orgB)

	got, err := st.HostStates(scopedB, orgB, rules.FleetFilter{})
	if err != nil || len(got) != 0 {
		t.Fatalf("orgB unfiltered = %+v, %v, want empty, nil", got, err)
	}
	gotTag, err := st.HostStates(scopedB, orgB, rules.FleetFilter{Tag: "role=web"})
	if err != nil || len(gotTag) != 0 {
		t.Fatalf("orgB tag filter = %+v, %v, want empty, nil", gotTag, err)
	}
	gotGroup, err := st.HostStates(scopedB, orgB, rules.FleetFilter{GroupID: &groupA})
	if err != nil || len(gotGroup) != 0 {
		t.Fatalf("orgB filtered by orgA's group id = %+v, %v, want empty, nil (the group is not orgB's)", gotGroup, err)
	}
}

// TestStateStoreRuleDeleteSetsNull is review focus 7, pinned end to end
// against the real database: a check_states row referencing a rule that is
// then deleted must survive the delete, with rule_id cleared to NULL — not
// be deleted itself by an over-eager CASCADE. A cascade here would let a
// tuple's crit silently vanish, reappearing as if it had never been
// anything but healthy, instead of standing until the next tick
// re-evaluates it.
func TestStateStoreRuleDeleteSetsNull(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rule-delete-set-null")
	_, host := seedZonedHost(t, d, org, "h1")
	ruleStore := rules.NewSQLStore(d)
	stateStore := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	r := hostRule(org, host)
	if err := ruleStore.Create(scoped, r); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	tuple := rules.Tuple{CheckKey: r.CheckKey, Metric: r.Metric}
	insertCheckState(t, d, org, host, stateRow{
		tuple: tuple, state: rules.StateCrit, since: now, evaluatedAt: now, ruleID: &r.ID,
	})

	before, err := stateStore.StatesForHost(scoped, org, host)
	if err != nil || len(before) != 1 || before[0].RuleID == nil || *before[0].RuleID != r.ID {
		t.Fatalf("before delete: %+v, %v", before, err)
	}

	if err := ruleStore.Delete(scoped, org, r.ID); err != nil {
		t.Fatal(err)
	}

	after, err := stateStore.StatesForHost(scoped, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("the state row must survive the rule's deletion: %+v", after)
	}
	if after[0].State != rules.StateCrit {
		t.Fatalf("State = %v, want crit unchanged (only rule_id clears)", after[0].State)
	}
	if after[0].RuleID != nil {
		t.Fatalf("RuleID = %v, want nil after the rule was deleted", after[0].RuleID)
	}
}

// TestStateStoreRLS mirrors TestRulesAreIsolatedPerOrgByRLS: it exercises
// the org_isolation policy directly, via a raw query over d.WithOrg's
// transaction, for all three tables this migration adds. testdb.Open never
// sets pulsix.org_id on its own connections, so a plain SQLStore-based test
// only ever exercises org_isolation_bypass — never org_isolation's own
// USING clause, which is what this test would fail to catch if it were
// ever replaced with USING (true).
func TestStateStoreRLS(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "states-rls-a")
	orgB := seedOrg(t, d, "states-rls-b")
	_, hostA := seedZonedHost(t, d, orgA, "h1")
	_, hostB := seedZonedHost(t, d, orgB, "h1")

	now := time.Now().UTC().Truncate(time.Microsecond)
	tuple := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	insertCheckState(t, d, orgA, hostA, stateRow{tuple: tuple, state: rules.StateOK, since: now, evaluatedAt: now})
	insertCheckState(t, d, orgB, hostB, stateRow{tuple: tuple, state: rules.StateOK, since: now, evaluatedAt: now})
	insertStateChange(t, d, orgA, hostA, now, tuple, rules.StateOK, rules.StateWarn)
	insertStateChange(t, d, orgB, hostB, now, tuple, rules.StateOK, rules.StateWarn)
	insertHostState(t, d, orgA, hostA, rules.StateOK, 0, 0, 0, now, now)
	insertHostState(t, d, orgB, hostB, rules.StateOK, 0, 0, 0, now, now)

	assertOnlyOrgAVisible := func(t *testing.T, table string) {
		t.Helper()
		var seen []uuid.UUID
		err := d.WithOrg(context.Background(), orgA, func(tx pgx.Tx) error {
			rows, err := tx.Query(context.Background(), "SELECT org_id FROM "+table)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					return err
				}
				seen = append(seen, id)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != orgA {
			t.Fatalf("RLS leak on %s: saw %v, want only [%s]", table, seen, orgA)
		}
	}

	t.Run("check_states", func(t *testing.T) { assertOnlyOrgAVisible(t, "check_states") })
	t.Run("check_state_changes", func(t *testing.T) { assertOnlyOrgAVisible(t, "check_state_changes") })
	t.Run("check_host_states", func(t *testing.T) { assertOnlyOrgAVisible(t, "check_host_states") })
}

// TestStateStoreGenericErrorsSurface mirrors
// TestRuleStoreGenericErrorsSurface: a store failure that is neither "no
// rows" nor a constraint violation (a context already canceled before the
// statement runs) must surface as a plain wrapped error from every
// StateStore method, including HostStates under a filter that exercises
// groupSelector's own query.
func TestStateStoreGenericErrorsSurface(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "states-generic-errors")
	_, host := seedZonedHost(t, d, org, "h1")
	group := seedGroup(t, d, org, "g1")
	st := rules.NewSQLStore(d)

	canceled, cancel := context.WithCancel(db.WithOrgScope(context.Background(), org))
	cancel()

	if _, err := st.StatesForHost(canceled, org, host); err == nil {
		t.Fatal("StatesForHost on a canceled context must fail")
	}
	if _, err := st.ChangeCounts(canceled, org, host, time.Now()); err == nil {
		t.Fatal("ChangeCounts on a canceled context must fail")
	}
	if _, err := st.HostStates(canceled, org, rules.FleetFilter{}); err == nil {
		t.Fatal("HostStates on a canceled context must fail")
	}
	if _, err := st.HostStates(canceled, org, rules.FleetFilter{GroupID: &group}); err == nil {
		t.Fatal("HostStates with a group filter on a canceled context must fail (groupSelector's own query)")
	}
}
