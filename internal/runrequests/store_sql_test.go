package runrequests_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/runrequests"
)

type sqlFixture struct {
	d        *db.DB
	org      uuid.UUID
	user     uuid.UUID
	template uuid.UUID
	ctx      context.Context
}

func newSQLFixture(t *testing.T, d *db.DB, slug string) sqlFixture {
	t.Helper()
	ctx := context.Background()
	q := gen.New(d.Pool())
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	user := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: user, Email: slug + "@example.test", DisplayName: slug}); err != nil {
		t.Fatal(err)
	}
	playbook, template := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(ctx, `INSERT INTO playbooks (id, org_id, inline, source) VALUES ($1, $2, '- hosts: all', 'editor')`, playbook, org.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `INSERT INTO job_templates (id, org_id, name, playbook_id, scope) VALUES ($1, $2, 'deploy', $3, '{}')`, template, org.ID, playbook); err != nil {
		t.Fatal(err)
	}
	return sqlFixture{d: d, org: org.ID, user: user, template: template, ctx: db.WithOrgScope(ctx, org.ID)}
}

func TestSQLStoreRoundTripDecideRevertAndRunGroup(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-a")
	store := runrequests.NewSQLStore(d)
	tok := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)
	r := runrequests.Request{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindRun, TemplateID: f.template, TemplateRevision: 3,
		Reason: "set up", RequestedByUser: f.user, RequestedByToken: &tok, Status: runrequests.StatusPending,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := store.Insert(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(f.ctx, f.org, r.ID)
	if err != nil || got.TemplateRevision != 3 || got.RequestedByToken == nil || *got.RequestedByToken != tok || got.Status != runrequests.StatusPending || !got.ExpiresAt.Equal(r.ExpiresAt) {
		t.Fatalf("Get = %+v %v", got, err)
	}
	ok, err := store.Decide(f.ctx, f.org, r.ID, runrequests.StatusApproved, f.user, now, "fp-1")
	if err != nil || !ok {
		t.Fatalf("Decide = %v %v", ok, err)
	}
	if ok, _ := store.Decide(f.ctx, f.org, r.ID, runrequests.StatusRejected, f.user, now, ""); ok {
		t.Fatal("a decided request was decided again")
	}
	// An approval takes the request's own id as its run group and keeps
	// the approved fingerprint.
	byGroup, err := store.ByRunGroup(f.ctx, f.org, r.ID)
	if err != nil || byGroup.ID != r.ID || byGroup.ApprovedFingerprint != "fp-1" || byGroup.RunGroupID == nil || *byGroup.RunGroupID != r.ID {
		t.Fatalf("ByRunGroup = %+v %v", byGroup, err)
	}
	if err := store.RevertToPending(f.ctx, f.org, r.ID); err != nil {
		t.Fatal(err)
	}
	// The run group stays (jobs may point at it), the fingerprint goes.
	got, _ = store.Get(f.ctx, f.org, r.ID)
	if got.Status != runrequests.StatusPending || got.DecidedBy != nil || got.ApprovedFingerprint != "" || got.RunGroupID == nil {
		t.Fatalf("after revert = %+v", got)
	}
	list, err := store.List(f.ctx, f.org)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v %v", list, err)
	}
}

func TestSQLStoreIsolatesOrganizations(t *testing.T) {
	d := testdb.Open(t)
	a := newSQLFixture(t, d, "runreq-iso-a")
	b := newSQLFixture(t, d, "runreq-iso-b")
	store := runrequests.NewSQLStore(d)
	r := runrequests.Request{ID: uuid.Must(uuid.NewV7()), OrgID: a.org, Kind: runrequests.KindRun, TemplateID: a.template,
		Reason: "x", RequestedByUser: a.user, Status: runrequests.StatusPending, ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now()}
	if err := store.Insert(a.ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(b.ctx, a.org, r.ID); !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("org a's request read in org b's scope = %v, want ErrNotFound", err)
	}
	if list, err := store.List(b.ctx, b.org); err != nil || len(list) != 0 {
		t.Fatalf("org b's list = %+v %v", list, err)
	}
}
