package chain_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/chain"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

var ctx = context.Background()

func newID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

type env struct {
	t      *testing.T
	d      *db.DB
	org    uuid.UUID
	store  *chain.SQLStore
	scoped context.Context
	pb     uuid.UUID
	zone   uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	o, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: newID(), Slug: "chain-" + uuid.NewString()[:8], Name: "chain"})
	if err != nil {
		t.Fatal(err)
	}
	inline := "- hosts: all\n  tasks: []\n"
	pb, err := q.CreatePlaybook(ctx, gen.CreatePlaybookParams{ID: newID(), OrgID: o.ID, Inline: &inline, Source: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	zone := newID()
	if _, err := d.Pool().Exec(ctx, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1,$2,'z','z')`, zone, o.ID); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, d: d, org: o.ID, store: chain.NewSQLStore(d), scoped: db.WithOrgScope(ctx, o.ID), pb: pb.ID, zone: zone}
}

func (e *env) template(name string, followUps ...uuid.UUID) uuid.UUID {
	e.t.Helper()
	q := gen.New(e.d.Pool())
	tm, err := q.InsertTemplate(ctx, gen.InsertTemplateParams{
		ID: newID(), OrgID: e.org, Name: name, PlaybookID: db.NullUUID(&e.pb), Scope: []byte(`{}`), ExtraVars: []byte(`{}`),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	for i, f := range followUps {
		if _, err := e.d.Pool().Exec(ctx, `INSERT INTO job_template_follow_ups (org_id, template_id, follow_up_template_id, position) VALUES ($1,$2,$3,$4)`,
			e.org, tm.ID, f, i); err != nil {
			e.t.Fatal(err)
		}
	}
	return tm.ID
}

// job inserts one run job of group with the given status, targeted hosts and
// recap lists.
func (e *env) job(group uuid.UUID, status string, hosts []uuid.UUID, stats map[string][]uuid.UUID) {
	e.t.Helper()
	if hosts == nil {
		hosts = []uuid.UUID{}
	}
	raw, err := json.Marshal(stats)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.d.Pool().Exec(ctx, `INSERT INTO jobs (id, org_id, zone_id, kind, host_ids, status, stats, run_group_id)
		VALUES ($1,$2,$3,'run',$4,$5,$6,$7)`, newID(), e.org, e.zone, hosts, status, raw, group); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) plan(group, tmpl uuid.UUID, override *[]uuid.UUID) {
	e.t.Helper()
	c := jobs.ChainContext{RootTrigger: "manual", Depth: 1, Path: []uuid.UUID{tmpl}}
	if err := e.store.PlanFollowUps(e.scoped, e.org, group, tmpl, override, c); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) rows(group uuid.UUID) []chain.FollowUp {
	e.t.Helper()
	got, err := e.store.ListForGroup(e.scoped, e.org, group)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

func TestPlanTemplateFollowUps(t *testing.T) {
	e := newEnv(t)
	b, c := e.template("b"), e.template("c")
	a := e.template("a", b, c)
	group, ref := newID(), newID()
	cc := jobs.ChainContext{RootTrigger: "schedule", RootRef: &ref, Depth: 3, Path: []uuid.UUID{b, c, a}}
	for range 2 {
		if err := e.store.PlanFollowUps(e.scoped, e.org, group, a, nil, cc); err != nil {
			t.Fatal(err)
		}
	}
	rows := e.rows(group)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for i, want := range []struct {
		id   uuid.UUID
		name string
	}{{b, "b"}, {c, "c"}} {
		r := rows[i]
		if r.TemplateID != want.id || r.TemplateName != want.name || r.Position != i || r.State != chain.StatePending ||
			r.RootTrigger != "schedule" || r.RootRef == nil || *r.RootRef != ref || r.Depth != 3 || !slices.Equal(r.Path, cc.Path) {
			t.Fatalf("row %d = %+v", i, r)
		}
	}
}

func TestPlanOverride(t *testing.T) {
	e := newEnv(t)
	b, c := e.template("b"), e.template("c")
	a := e.template("a", b)
	group := newID()
	e.plan(group, a, &[]uuid.UUID{c})
	rows := e.rows(group)
	if len(rows) != 1 || rows[0].TemplateID != c {
		t.Fatalf("rows = %+v, want only c", rows)
	}
	empty := newID()
	e.plan(empty, a, &[]uuid.UUID{})
	if got := e.rows(empty); len(got) != 0 {
		t.Fatalf("empty override planned %+v", got)
	}
}

func TestPlanIsIdempotentAndNeverResetsARow(t *testing.T) {
	e := newEnv(t)
	b := e.template("b")
	a := e.template("a", b)
	group := newID()
	e.plan(group, a, nil)
	if _, err := e.d.Pool().Exec(ctx, `UPDATE run_follow_ups SET state = 'started', reason = 'kept' WHERE parent_group_id = $1`, group); err != nil {
		t.Fatal(err)
	}
	e.plan(group, a, nil)
	e.plan(group, a, &[]uuid.UUID{b})
	rows := e.rows(group)
	if len(rows) != 1 || rows[0].State != chain.StateStarted || rows[0].Reason != "kept" {
		t.Fatalf("rows after replay = %+v, want the started row untouched", rows)
	}
}

func TestCopyFollowUps(t *testing.T) {
	e := newEnv(t)
	b, c := e.template("b"), e.template("c")
	a := e.template("a", b, c)
	from, to := newID(), newID()
	ref := newID()
	cc := jobs.ChainContext{RootTrigger: "webhook", RootRef: &ref, Depth: 2, Path: []uuid.UUID{b, a}}
	if err := e.store.PlanFollowUps(e.scoped, e.org, from, a, nil, cc); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.Pool().Exec(ctx, `UPDATE run_follow_ups SET state = 'skipped', reason = 'the run failed' WHERE parent_group_id = $1`, from); err != nil {
		t.Fatal(err)
	}
	user := newID()
	for range 2 {
		if err := e.store.CopyFollowUps(e.scoped, e.org, from, to, &user); err != nil {
			t.Fatal(err)
		}
	}
	rows := e.rows(to)
	if len(rows) != 2 {
		t.Fatalf("copied %d rows, want 2", len(rows))
	}
	for i, r := range rows {
		if r.State != chain.StatePending || r.Reason != "" || r.Position != i || r.RootTrigger != "webhook" || *r.RootRef != ref ||
			r.Depth != 2 || !slices.Equal(r.Path, cc.Path) || r.ConfiguredBy == nil || *r.ConfiguredBy != user || r.ID == e.rows(from)[i].ID {
			t.Fatalf("copy %d = %+v", i, r)
		}
	}
}

func TestPlayedHostsIgnoresTargetedButUnplayed(t *testing.T) {
	e := newEnv(t)
	b := e.template("b")
	a := e.template("a", b)
	group, h1, h2 := newID(), newID(), newID()
	e.plan(group, a, nil)
	e.job(group, "succeeded", []uuid.UUID{h1, h2}, map[string][]uuid.UUID{"ok_hosts": {h1}})
	if err := e.store.ResolveGroup(e.scoped, e.org, group, jobs.StatusSucceeded, time.Now()); err != nil {
		t.Fatal(err)
	}
	if r := e.rows(group)[0]; !slices.Equal(r.HostIDs, []uuid.UUID{h1}) {
		t.Fatalf("host_ids = %v, want only %v", r.HostIDs, h1)
	}
}

func TestDismiss(t *testing.T) {
	e := newEnv(t)
	b, c := e.template("b"), e.template("c")
	a := e.template("a", b, c)
	group := newID()
	e.plan(group, a, nil)
	if _, err := e.d.Pool().Exec(ctx, `UPDATE run_follow_ups SET state = 'started' WHERE parent_group_id = $1 AND template_id = $2`, group, c); err != nil {
		t.Fatal(err)
	}
	n, err := e.store.Dismiss(e.scoped, e.org, group, "dismissed by Ada", time.Now())
	if err != nil || n != 1 {
		t.Fatalf("dismiss = %d, %v", n, err)
	}
	rows := e.rows(group)
	if rows[0].State != chain.StateDismissed || rows[0].Reason != "dismissed by Ada" || rows[1].State != chain.StateStarted {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestParentOf(t *testing.T) {
	e := newEnv(t)
	b := e.template("b")
	a := e.template("a", b)
	group, child := newID(), newID()
	if p, err := e.store.ParentOf(e.scoped, e.org, group, b); err != nil || p != nil {
		t.Fatalf("unchained = %+v, %v", p, err)
	}
	e.plan(group, a, nil)
	// The row is found before the starter's batch has named the child group.
	if p, err := e.store.ParentOf(e.scoped, e.org, group, b); err != nil || p == nil || p.ParentGroupID != group || p.TemplateName != "b" || p.ChildFirstJobID != nil {
		t.Fatalf("not yet linked = %+v, %v", p, err)
	}
	if p, err := e.store.ParentOf(e.scoped, e.org, group, a); err != nil || p != nil {
		t.Fatalf("another template = %+v, %v", p, err)
	}
	e.job(child, "queued", nil, nil)
	if _, err := e.d.Pool().Exec(ctx, `UPDATE run_follow_ups SET state = 'started', child_group_id = $1 WHERE parent_group_id = $2`, child, group); err != nil {
		t.Fatal(err)
	}
	p, err := e.store.ParentOf(e.scoped, e.org, group, b)
	if err != nil || p == nil || p.ParentGroupID != group || p.TemplateName != "b" || p.ChildFirstJobID == nil {
		t.Fatalf("parent = %+v, %v", p, err)
	}
}
