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

// chainRows is an approved request of template x whose run started the
// follow-up b, whose run (group gB, a chain job) started c; neither
// follow-up row names its child group yet, as before the starter's batch
// commits (review B1). A loose row belongs to no request's chain.
type chainRows struct {
	f        sqlFixture
	store    *runrequests.SQLStore
	r        runrequests.Request
	b, c, gB uuid.UUID
	loose, h uuid.UUID
}

func newChainRows(t *testing.T, slug string, bindings func(chainRows) *runrequests.FollowUpBindings) chainRows {
	t.Helper()
	d := testdb.Open(t)
	c := chainRows{f: newSQLFixture(t, d, slug), store: runrequests.NewSQLStore(d),
		b: uuid.Must(uuid.NewV7()), c: uuid.Must(uuid.NewV7()), gB: uuid.Must(uuid.NewV7()), loose: uuid.Must(uuid.NewV7()), h: uuid.Must(uuid.NewV7())}
	f := c.f
	now := time.Now().UTC().Truncate(time.Microsecond)
	c.r = runrequests.Request{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindRun, TemplateID: f.template, TemplateRevision: 1,
		Reason: "x", RequestedByUser: f.user, Status: runrequests.StatusPending, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := c.store.Insert(f.ctx, c.r); err != nil {
		t.Fatal(err)
	}
	var bound *runrequests.FollowUpBindings
	if bindings != nil {
		bound = bindings(c)
	}
	if ok, err := c.store.Decide(f.ctx, f.org, c.r.ID, runrequests.StatusApproved, f.user, now, "fp", jobs.ApprovedRun{}, bound); err != nil || !ok {
		t.Fatalf("Decide = %v %v", ok, err)
	}
	insert := func(parent, template uuid.UUID, depth int, path []uuid.UUID) {
		t.Helper()
		if _, err := d.Pool().Exec(f.ctx, `INSERT INTO run_follow_ups (id, org_id, parent_group_id, template_id, state, root_trigger, depth, path)
			VALUES ($1, $2, $3, $4, 'due', 'manual', $5, $6)`, uuid.Must(uuid.NewV7()), f.org, parent, template, depth, path); err != nil {
			t.Fatal(err)
		}
	}
	insert(c.r.ID, c.b, 1, []uuid.UUID{f.template})
	insert(c.gB, c.c, 2, []uuid.UUID{f.template, c.b})
	insert(c.loose, c.b, 1, []uuid.UUID{f.template})
	zone := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(f.ctx, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'z', 'z')`, zone, f.org); err != nil {
		t.Fatal(err)
	}
	parent := c.r.ID
	chained := jobs.Job{ID: uuid.Must(uuid.NewV7()), OrgID: f.org, ZoneID: zone, Kind: jobs.KindRun, Status: jobs.StatusSucceeded,
		RunGroupID: c.gB, Trigger: jobs.TriggerChain, TriggerRef: &parent, TemplateID: &c.b, HostIDs: []uuid.UUID{c.h}, CreatedAt: now}
	if err := jobs.NewSQLStore(d).CreateJob(f.ctx, chained, nil); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestSQLStoreFindsAFollowUpsRequest (backlog 2.143, review B1): a
// follow-up is found by its row's unique key (parent group, template),
// whether or not the row names its child group yet, and its chain is
// walked up through the parent groups' chain jobs to the run request
// whose run group started it; a run no request started has none.
func TestSQLStoreFindsAFollowUpsRequest(t *testing.T) {
	c := newChainRows(t, "runreq-fu-origin", nil)
	f := c.f
	for _, tc := range []struct {
		name             string
		parent, template uuid.UUID
		path             []uuid.UUID
	}{
		{"direct follow-up", c.r.ID, c.b, []uuid.UUID{f.template}},
		{"follow-up of a follow-up", c.gB, c.c, []uuid.UUID{f.template, c.b}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := c.store.FollowUpOrigin(f.ctx, f.org, tc.parent, tc.template)
			if err != nil || o.RequestID != c.r.ID || o.TemplateID != tc.template || !slices.Equal(o.Path, tc.path) {
				t.Fatalf("origin = %+v, %v", o, err)
			}
		})
	}
	for _, k := range [][2]uuid.UUID{{c.loose, c.b}, {c.r.ID, uuid.Must(uuid.NewV7())}, {c.gB, c.b}, {uuid.Must(uuid.NewV7()), c.c}} {
		if o, err := c.store.FollowUpOrigin(f.ctx, f.org, k[0], k[1]); !errors.Is(err, runrequests.ErrNotFound) {
			t.Fatalf("origin of %v = %+v, %v; want ErrNotFound", k, o, err)
		}
	}
}

// TestFollowUpClaimIsCheckedBeforeItsRowNamesItsChild is review B1: a
// worker claims a follow-up's job before the starter's batch wrote the
// row's child group; the claim is still checked against the approval,
// for a direct follow-up and for a follow-up of a follow-up, and refused
// once its binding changed.
func TestFollowUpClaimIsCheckedBeforeItsRowNamesItsChild(t *testing.T) {
	host := []runrequests.PictureHost{}
	nodes := map[string]runrequests.PictureFollowUp{}
	c := newChainRows(t, "runreq-fu-claim-early", func(c chainRows) *runrequests.FollowUpBindings {
		host = []runrequests.PictureHost{{ID: c.h.String(), Name: "web-1"}}
		nodes["b"] = runrequests.PictureFollowUp{TemplateID: c.b.String(), Targets: host, VarsDigest: "d-b"}
		nodes["c"] = runrequests.PictureFollowUp{TemplateID: c.c.String(), Targets: host, VarsDigest: "d-c"}
		return &runrequests.FollowUpBindings{Nodes: map[string]runrequests.FollowUpBinding{
			runrequests.FollowUpKey([]uuid.UUID{c.f.template}, c.b):      {Fingerprint: runrequests.FollowUpFingerprint(c.r.ID, nodes["b"]), Within: []uuid.UUID{c.h}},
			runrequests.FollowUpKey([]uuid.UUID{c.f.template, c.b}, c.c): {Fingerprint: runrequests.FollowUpFingerprint(c.r.ID, nodes["c"]), Within: []uuid.UUID{c.h}},
		}}
	})
	pics := &fakePictures{live: map[uuid.UUID]runrequests.PictureFollowUp{c.b: nodes["b"], c.c: nodes["c"]}}
	svc := runrequests.NewService(c.store, pics, &fakeEnqueuer{}, &fakeValidator{}, &fakeSyncer{}, &fakeAutomation{}, time.Now, func() uuid.UUID { return uuid.Must(uuid.NewV7()) })
	parentOf := map[uuid.UUID]uuid.UUID{c.b: c.r.ID, c.c: c.gB}
	for _, tmpl := range []uuid.UUID{c.b, c.c} {
		parent, tmpl := parentOf[tmpl], tmpl
		run := jobs.ApprovedRun{TriggerRef: &parent, TemplateID: &tmpl, HostIDs: []uuid.UUID{c.h}}
		if err := svc.CheckRunClaim(c.f.ctx, c.f.org, uuid.Must(uuid.NewV7()), run); err != nil {
			t.Fatalf("claim of the unchanged follow-up %s = %v, want it let through", tmpl, err)
		}
		changed := pics.live[tmpl]
		changed.VarsDigest += "-changed"
		pics.live[tmpl] = changed
		if err := svc.CheckRunClaim(c.f.ctx, c.f.org, uuid.Must(uuid.NewV7()), run); !errors.Is(err, runrequests.ErrChangedAfterApproval) {
			t.Fatalf("claim of the changed follow-up %s = %v, want ErrChangedAfterApproval", tmpl, err)
		}
	}
}
