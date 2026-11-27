package runrequests_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
)

// TestSQLStoreKeepsTheFollowUpBindings (backlog 2.143): an approval
// stores what it binds of its follow-ups with the decision, a revert
// clears it, and a request decided without any reads none.
func TestSQLStoreKeepsTheFollowUpBindings(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-fu-bind")
	store := runrequests.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	r := runrequests.Request{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindRun, TemplateID: f.template, TemplateRevision: 1,
		Reason: "x", RequestedByUser: f.user, Status: runrequests.StatusPending, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := store.Insert(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	h := uuid.Must(uuid.NewV7())
	bindings := &runrequests.FollowUpBindings{Nodes: map[string]runrequests.FollowUpBinding{
		"a/b": {Fingerprint: "fp-b", Within: []uuid.UUID{h}},
	}}
	if ok, err := store.Decide(f.ctx, f.org, r.ID, runrequests.StatusApproved, f.user, now, "fp", jobs.ApprovedRun{}, bindings); err != nil || !ok {
		t.Fatalf("Decide = %v %v", ok, err)
	}
	got, err := store.Get(f.ctx, f.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FollowUpBindings == nil || len(got.FollowUpBindings.Nodes) != 1 || got.FollowUpBindings.Nodes["a/b"].Fingerprint != "fp-b" ||
		!slices.Equal(got.FollowUpBindings.Nodes["a/b"].Within, []uuid.UUID{h}) {
		t.Fatalf("bindings = %+v", got.FollowUpBindings)
	}
	if err := store.RevertToPending(f.ctx, f.org, r.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(f.ctx, f.org, r.ID); got.FollowUpBindings != nil {
		t.Fatalf("bindings after the revert = %+v, want none", got.FollowUpBindings)
	}
	if ok, err := store.Decide(f.ctx, f.org, r.ID, runrequests.StatusApproved, f.user, now, "fp", jobs.ApprovedRun{}, nil); err != nil || !ok {
		t.Fatalf("Decide = %v %v", ok, err)
	}
	if got, _ := store.Get(f.ctx, f.org, r.ID); got.FollowUpBindings != nil {
		t.Fatalf("bindings of a decision without = %+v, want none", got.FollowUpBindings)
	}
}

// TestSQLStoreFindsAFollowUpsRequest (backlog 2.143): from a follow-up's
// run group, through a follow-up of a follow-up, up to the run request
// whose run group started the chain; a run no request started has none.
func TestSQLStoreFindsAFollowUpsRequest(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-fu-origin")
	store := runrequests.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	r := runrequests.Request{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindRun, TemplateID: f.template, TemplateRevision: 1,
		Reason: "x", RequestedByUser: f.user, Status: runrequests.StatusPending, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := store.Insert(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Decide(f.ctx, f.org, r.ID, runrequests.StatusApproved, f.user, now, "fp", jobs.ApprovedRun{}, nil); err != nil || !ok {
		t.Fatalf("Decide = %v %v", ok, err)
	}
	b, c := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	childB, childC, loose, looseChild := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	insert := func(parent, template, child uuid.UUID, depth int, path []uuid.UUID) {
		t.Helper()
		if _, err := d.Pool().Exec(f.ctx, `INSERT INTO run_follow_ups (id, org_id, parent_group_id, template_id, state, child_group_id, root_trigger, depth, path)
			VALUES ($1, $2, $3, $4, 'started', $5, 'manual', $6, $7)`, uuid.Must(uuid.NewV7()), f.org, parent, template, child, depth, path); err != nil {
			t.Fatal(err)
		}
	}
	insert(r.ID, b, childB, 1, []uuid.UUID{f.template})
	insert(childB, c, childC, 2, []uuid.UUID{f.template, b})
	insert(loose, b, looseChild, 1, []uuid.UUID{f.template})

	for _, tc := range []struct {
		name     string
		group    uuid.UUID
		template uuid.UUID
		path     []uuid.UUID
	}{
		{"direct follow-up", childB, b, []uuid.UUID{f.template}},
		{"follow-up of a follow-up", childC, c, []uuid.UUID{f.template, b}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := store.FollowUpOrigin(f.ctx, f.org, tc.group)
			if err != nil || o.RequestID != r.ID || o.TemplateID != tc.template || !slices.Equal(o.Path, tc.path) {
				t.Fatalf("origin = %+v, %v", o, err)
			}
		})
	}
	for _, group := range []uuid.UUID{r.ID, looseChild, uuid.Must(uuid.NewV7())} {
		if o, err := store.FollowUpOrigin(f.ctx, f.org, group); !errors.Is(err, runrequests.ErrNotFound) {
			t.Fatalf("origin of %s = %+v, %v; want ErrNotFound", group, o, err)
		}
	}
}
