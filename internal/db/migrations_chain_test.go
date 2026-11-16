package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// pgCode returns the SQLSTATE of a Postgres error, or "".
func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func seedChainZone(t *testing.T, d *db.DB, org uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(context.Background(),
		`INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'z', 'z')`, id, org); err != nil {
		t.Fatal(err)
	}
	return id
}

// insertRunJob inserts a kind='run' job of a group with the given targeted
// hosts and raw stats JSON.
func insertRunJob(t *testing.T, d *db.DB, org, zone, group uuid.UUID, hosts []uuid.UUID, stats string) {
	t.Helper()
	if _, err := d.Pool().Exec(context.Background(), `
		INSERT INTO jobs (id, org_id, zone_id, kind, host_ids, run_group_id, stats, status)
		VALUES (gen_random_uuid(), $1, $2, 'run', $3, $4, $5::jsonb, 'succeeded')`,
		org, zone, hosts, group, stats); err != nil {
		t.Fatal(err)
	}
}

func TestJobChainingMigration(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	orgA, orgB := seedOrg(t, d), seedOrg(t, d)
	a, b := seedJobTemplate(t, d, orgA, "a"), seedJobTemplate(t, d, orgA, "b")
	other := seedJobTemplate(t, d, orgB, "other")

	t.Run("a template cannot follow itself", func(t *testing.T) {
		err := q.InsertTemplateFollowUp(ctx, gen.InsertTemplateFollowUpParams{OrgID: orgA, TemplateID: a, FollowUpTemplateID: a})
		if pgCode(err) != "23514" {
			t.Fatalf("self edge: got %v, want check violation", err)
		}
	})

	t.Run("a follow-up of another organization is refused", func(t *testing.T) {
		err := q.InsertTemplateFollowUp(ctx, gen.InsertTemplateFollowUpParams{OrgID: orgA, TemplateID: a, FollowUpTemplateID: other})
		if pgCode(err) != "23503" {
			t.Fatalf("cross-org edge: got %v, want foreign key violation", err)
		}
	})

	if err := q.InsertTemplateFollowUp(ctx, gen.InsertTemplateFollowUpParams{OrgID: orgA, TemplateID: a, FollowUpTemplateID: b, Position: 1}); err != nil {
		t.Fatal(err)
	}

	t.Run("edge queries", func(t *testing.T) {
		got, err := q.ListTemplateFollowUps(ctx, gen.ListTemplateFollowUpsParams{OrgID: orgA, TemplateID: a})
		if err != nil || len(got) != 1 || got[0].FollowUpTemplateID != b || got[0].FollowUpName != "b" || got[0].Condition != "success" {
			t.Fatalf("ListTemplateFollowUps = %+v, %v", got, err)
		}
		edges, err := q.ListOrgFollowUpEdges(ctx, orgA)
		if err != nil || len(edges) != 1 || edges[0].TemplateID != a || edges[0].FollowUpTemplateID != b {
			t.Fatalf("ListOrgFollowUpEdges = %+v, %v", edges, err)
		}
		names, err := q.ListFollowUpReferrers(ctx, gen.ListFollowUpReferrersParams{OrgID: orgA, FollowUpTemplateID: b})
		if err != nil || !slices.Equal(names, []string{"a"}) {
			t.Fatalf("ListFollowUpReferrers = %v, %v", names, err)
		}
	})

	t.Run("deleting a follow-up template is refused", func(t *testing.T) {
		_, err := d.Pool().Exec(ctx, `DELETE FROM job_templates WHERE org_id = $1 AND id = $2`, orgA, b)
		if pgCode(err) != "23503" {
			t.Fatalf("delete follow-up: got %v, want foreign key violation", err)
		}
	})

	t.Run("deleting the owning template cascades its edges", func(t *testing.T) {
		if _, err := d.Pool().Exec(ctx, `DELETE FROM job_templates WHERE org_id = $1 AND id = $2`, orgA, a); err != nil {
			t.Fatal(err)
		}
		edges, err := q.ListOrgFollowUpEdges(ctx, orgA)
		if err != nil || len(edges) != 0 {
			t.Fatalf("edges after delete = %+v, %v", edges, err)
		}
	})

	t.Run("jobs accept the chain trigger", func(t *testing.T) {
		zone := seedChainZone(t, d, orgA)
		if _, err := d.Pool().Exec(ctx, `
			INSERT INTO jobs (id, org_id, zone_id, kind, run_group_id, trigger, trigger_ref)
			VALUES (gen_random_uuid(), $1, $2, 'run', gen_random_uuid(), 'chain', gen_random_uuid())`, orgA, zone); err != nil {
			t.Fatalf("trigger chain: %v", err)
		}
		_, err := d.Pool().Exec(ctx, `
			INSERT INTO jobs (id, org_id, zone_id, kind, run_group_id, trigger)
			VALUES (gen_random_uuid(), $1, $2, 'run', gen_random_uuid(), 'bogus')`, orgA, zone)
		if pgCode(err) != "23514" {
			t.Fatalf("trigger bogus: got %v, want check violation", err)
		}
	})

	t.Run("run_follow_ups state and root_trigger are constrained", func(t *testing.T) {
		for col, stmt := range map[string]string{
			"state": `INSERT INTO run_follow_ups (id, org_id, parent_group_id, template_id, state, root_trigger, path)
			          VALUES (gen_random_uuid(), $1, gen_random_uuid(), gen_random_uuid(), 'bogus', 'manual', '{}')`,
			"root_trigger": `INSERT INTO run_follow_ups (id, org_id, parent_group_id, template_id, root_trigger, path)
			          VALUES (gen_random_uuid(), $1, gen_random_uuid(), gen_random_uuid(), 'bogus', '{}')`,
		} {
			if _, err := d.Pool().Exec(ctx, stmt, orgA); pgCode(err) != "23514" {
				t.Errorf("%s: got %v, want check violation", col, err)
			}
		}
	})

	t.Run("InsertRunFollowUp ignores a duplicate and RLS hides other organizations", func(t *testing.T) {
		group, tpl := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		insert := func(org uuid.UUID) int64 {
			n, err := q.InsertRunFollowUp(ctx, gen.InsertRunFollowUpParams{
				ID: uuid.Must(uuid.NewV7()), OrgID: org, ParentGroupID: group, TemplateID: tpl,
				Condition: "success", RootTrigger: "manual", Depth: 1, Path: []uuid.UUID{tpl},
			})
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		if n := insert(orgA); n != 1 {
			t.Fatalf("first insert affected %d rows", n)
		}
		if n := insert(orgA); n != 0 {
			t.Fatalf("duplicate insert affected %d rows, want 0 (no-op)", n)
		}
		var count int
		if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM run_follow_ups WHERE parent_group_id = $1`, group).Scan(&count); err != nil || count != 1 {
			t.Fatalf("rows = %d, %v", count, err)
		}
		var seenByB, seenByA int
		if err := d.WithOrg(ctx, orgB, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM run_follow_ups`).Scan(&seenByB)
		}); err != nil {
			t.Fatal(err)
		}
		if err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM run_follow_ups`).Scan(&seenByA)
		}); err != nil {
			t.Fatal(err)
		}
		if seenByB != 0 || seenByA == 0 {
			t.Fatalf("RLS: org B sees %d rows, org A sees %d", seenByB, seenByA)
		}
	})
}

func TestPlayedHosts(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	org := seedOrg(t, d)
	zone := seedChainZone(t, d, org)
	h1, h2 := uuid.MustParse("00000000-0000-7000-8000-000000000001"), uuid.MustParse("00000000-0000-7000-8000-000000000002")
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	type m = map[string]any

	cases := []struct {
		name  string
		hosts []uuid.UUID
		stats string
		want  []uuid.UUID
	}{
		{"ok host", []uuid.UUID{h1, h2}, js(m{"ok_hosts": []string{h1.String()}, "skipped_hosts": []string{}}), []uuid.UUID{h1}},
		{"failed host removed", []uuid.UUID{h1, h2},
			js(m{"ok_hosts": []string{h1.String(), h2.String()}, "failed_hosts": []string{h2.String()}}), []uuid.UUID{h1}},
		{"unreachable host removed", []uuid.UUID{h1, h2},
			js(m{"ok_hosts": []string{h1.String()}, "changed_hosts": []string{h2.String()}, "unreachable_hosts": []string{h2.String()}}), []uuid.UUID{h1}},
		{"changed and skipped count", []uuid.UUID{h1, h2},
			js(m{"changed_hosts": []string{h1.String()}, "skipped_hosts": []string{h2.String()}}), []uuid.UUID{h1, h2}},
		{"bare host name ignored", []uuid.UUID{h1}, js(m{"ok_hosts": []string{"localhost", h1.String()}}), []uuid.UUID{h1}},
		{"null list", []uuid.UUID{h1}, `{"ok_hosts": null}`, nil},
		{"untargeted host ignored", []uuid.UUID{h1}, js(m{"ok_hosts": []string{h1.String(), h2.String()}}), []uuid.UUID{h1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			group := uuid.Must(uuid.NewV7())
			insertRunJob(t, d, org, zone, group, tc.hosts, tc.stats)
			got, err := q.PlayedHosts(ctx, gen.PlayedHostsParams{OrgID: org, RunGroupID: group})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) && (len(got) != 0 || len(tc.want) != 0) {
				t.Fatalf("PlayedHosts = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFollowUpLifecycleQueries drives the state-changing queries once, so a
// wrong WHERE clause or column fails here rather than in a later task.
func TestFollowUpLifecycleQueries(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	org := seedOrg(t, d)
	zone := seedChainZone(t, d, org)
	a, b, c := seedJobTemplate(t, d, org, "a"), seedJobTemplate(t, d, org, "b"), seedJobTemplate(t, d, org, "c")
	for i, f := range []uuid.UUID{b, c} {
		if err := q.InsertTemplateFollowUp(ctx, gen.InsertTemplateFollowUpParams{OrgID: org, TemplateID: a, FollowUpTemplateID: f, Position: int32(i)}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	group := uuid.Must(uuid.NewV7())
	n, err := q.PlanTemplateFollowUps(ctx, gen.PlanTemplateFollowUpsParams{
		OrgID: org, TemplateID: a, ParentGroupID: group, RootTrigger: "manual", Depth: 1, Path: []uuid.UUID{a},
	})
	if err != nil || n != 2 {
		t.Fatalf("Plan = %d, %v", n, err)
	}
	if n, _ := q.PlanTemplateFollowUps(ctx, gen.PlanTemplateFollowUpsParams{
		OrgID: org, TemplateID: a, ParentGroupID: group, RootTrigger: "manual", Depth: 1, Path: []uuid.UUID{a},
	}); n != 0 {
		t.Fatalf("second Plan inserted %d rows", n)
	}
	rows, err := q.ListRunFollowUps(ctx, gen.ListRunFollowUpsParams{OrgID: org, ParentGroupID: group})
	if err != nil || len(rows) != 2 || rows[0].TemplateID != b || rows[0].State != "pending" {
		t.Fatalf("ListRunFollowUps = %+v, %v", rows, err)
	}

	// Stale: pending, old enough, the group has a running job -> not listed; terminal -> listed.
	if _, err := d.Pool().Exec(ctx, `UPDATE run_follow_ups SET created_at = $1`, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `INSERT INTO jobs (id, org_id, zone_id, kind, run_group_id, status)
		VALUES (gen_random_uuid(), $1, $2, 'run', $3, 'running')`, org, zone, group); err != nil {
		t.Fatal(err)
	}
	stale, err := q.ListStalePendingFollowUps(ctx, gen.ListStalePendingFollowUpsParams{Before: now, Lim: 10})
	if err != nil || len(stale) != 0 {
		t.Fatalf("stale with a running job = %+v, %v", stale, err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE jobs SET status = 'failed' WHERE run_group_id = $1`, group); err != nil {
		t.Fatal(err)
	}
	if stale, err = q.ListStalePendingFollowUps(ctx, gen.ListStalePendingFollowUpsParams{Before: now, Lim: 10}); err != nil || len(stale) != 2 {
		t.Fatalf("stale after finish = %+v, %v", stale, err)
	}
	out, err := q.RunGroupOutcome(ctx, gen.RunGroupOutcomeParams{OrgID: org, RunGroupID: group})
	if err != nil || out.Jobs != 1 || out.FailedOrError != 1 || out.Cancelled != 0 {
		t.Fatalf("RunGroupOutcome = %+v, %v", out, err)
	}

	// Dismiss one group's rows, resolve another's to due.
	if n, err := q.DismissPendingFollowUps(ctx, gen.DismissPendingFollowUpsParams{OrgID: org, ParentGroupID: group, Reason: "dismissed by Ann", Now: now}); err != nil || n != 2 {
		t.Fatalf("Dismiss = %d, %v", n, err)
	}
	g2 := uuid.Must(uuid.NewV7())
	if n, err := q.CopyRunFollowUps(ctx, gen.CopyRunFollowUpsParams{OrgID: org, FromGroup: group, ToGroup: g2}); err != nil || n != 2 {
		t.Fatalf("Copy = %d, %v", n, err)
	}
	h := uuid.Must(uuid.NewV7())
	if n, err := q.ResolvePendingFollowUps(ctx, gen.ResolvePendingFollowUpsParams{
		OrgID: org, ParentGroupID: g2, State: "due", HostIds: []uuid.UUID{h}, Now: now,
	}); err != nil || n != 2 {
		t.Fatalf("Resolve = %d, %v", n, err)
	}
	// Lock, then everything else, on the raw pool (bypass policy).
	due, err := q.LockDueFollowUps(ctx, gen.LockDueFollowUpsParams{Now: now.Add(-time.Second), Lim: 10})
	if err != nil || len(due) != 0 {
		t.Fatalf("not yet due = %+v, %v", due, err)
	}
	if due, err = q.LockDueFollowUps(ctx, gen.LockDueFollowUpsParams{Now: now, Lim: 10}); err != nil || len(due) != 2 || due[0].HostIds[0] != h {
		t.Fatalf("due = %+v, %v", due, err)
	}
	first, second := due[0], due[1]
	child := uuid.Must(uuid.NewV7())
	if n, err := q.MarkFollowUpStarted(ctx, gen.MarkFollowUpStartedParams{OrgID: org, ID: first.ID, ChildGroupID: pgtype.UUID{Bytes: child, Valid: true}, Now: now}); err != nil || n != 1 {
		t.Fatalf("Started = %d, %v", n, err)
	}
	if got, err := q.GetFollowUpOfChainedRun(ctx, gen.GetFollowUpOfChainedRunParams{OrgID: org, ParentGroupID: g2, TemplateID: first.TemplateID}); err != nil || got.ID != first.ID || got.State != "started" {
		t.Fatalf("GetFollowUpOfChainedRun = %+v, %v", got, err)
	}
	if n, err := q.MarkFollowUpWaiting(ctx, gen.MarkFollowUpWaitingParams{OrgID: org, ID: second.ID, Reason: "waiting for the active run of c", NextAttemptAt: now.Add(5 * time.Second)}); err != nil || n != 1 {
		t.Fatalf("Waiting = %d, %v", n, err)
	}
	attempts, err := q.BumpFollowUpAttempt(ctx, gen.BumpFollowUpAttemptParams{OrgID: org, ID: second.ID, Error: "boom", NextAttemptAt: now.Add(10 * time.Second)})
	if err != nil || attempts != 1 {
		t.Fatalf("Bump = %d, %v", attempts, err)
	}
	if n, err := q.MarkFollowUpSkipped(ctx, gen.MarkFollowUpSkippedParams{OrgID: org, ID: second.ID, Reason: "the template was deleted"}); err != nil || n != 1 {
		t.Fatalf("Skipped = %d, %v", n, err)
	}
	if n, _ := q.MarkFollowUpSkipped(ctx, gen.MarkFollowUpSkippedParams{OrgID: org, ID: first.ID, Reason: "x"}); n != 0 {
		t.Fatalf("a started row was skipped (%d)", n)
	}
}
