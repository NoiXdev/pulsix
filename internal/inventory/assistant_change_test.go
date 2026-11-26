package inventory_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// recordingWatcher records the changes handed to it; err fails them.
type recordingWatcher struct {
	changes []assistantchange.Change
	err     error
}

func (w *recordingWatcher) Changed(_ context.Context, _ uuid.UUID, c assistantchange.Change) error {
	w.changes = append(w.changes, c)
	return w.err
}

// take returns the changes recorded since the last take.
func (w *recordingWatcher) take() []assistantchange.Change {
	out := w.changes
	w.changes = nil
	return out
}

func wantFields(t *testing.T, what string, got []assistantchange.Change, want ...assistantchange.Field) assistantchange.Change {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("%s: handed on %d changes, want 1: %+v", what, len(got), got)
	}
	if !reflect.DeepEqual(got[0].Fields, want) {
		t.Fatalf("%s: fields = %#v, want %#v", what, got[0].Fields, want)
	}
	return got[0]
}

func wantNone(t *testing.T, what string, got []assistantchange.Change) {
	t.Helper()
	if len(got) != 0 {
		t.Fatalf("%s handed on %+v, want nothing", what, got)
	}
}

var setMask = map[string]any{"$secret": "set"}

// TestAssistantGroupChangesAreHandedOn (decision D6): an assistant's
// change of a group's values or schema defaults is handed to the watcher
// with the group, each moved key's old and new value (a secret masked) and
// the actor; a person's change, an accept and a write that moves nothing
// hand on nothing.
func TestAssistantGroupChangesAreHandedOn(t *testing.T) {
	f := newFixture()
	w := &recordingWatcher{}
	f.svc.SetChangeWatcher(w)
	g := f.marksGroup(t)
	wantNone(t, "a person's schema save", w.take())
	g, err := f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "count": 1.0, "pw": "s3cret"}, person)
	if err != nil {
		t.Fatal(err)
	}
	wantNone(t, "a person's values save", w.take())

	keepPW := map[string]any{"$secret": "keep", "from": "pw"}
	g, err = f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "count": 2.0, "greeting": "hi", "pw": keepPW}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	c := wantFields(t, "the assistant's change", w.take(),
		assistantchange.Field{Name: "vars.count", Old: 1.0, New: 2.0},
		assistantchange.Field{Name: "vars.greeting", Old: nil, New: "hi"})
	if c.Subject != assistantchange.SubjectInventoryGroup || c.ID != g.ID || c.Name != "web" || c.UserID != assistant.UserID || c.HostID != nil {
		t.Fatalf("change = %+v", c)
	}

	g, err = f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "count": 2.0, "greeting": "hi", "pw": keepPW}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	wantNone(t, "the assistant's unchanged resend", w.take())

	// A removed key is a change too; a removed secret shows masked.
	g, err = f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "count": 2.0}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	wantFields(t, "the assistant's removal", w.take(),
		assistantchange.Field{Name: "vars.greeting", Old: "hi", New: nil},
		assistantchange.Field{Name: "vars.pw", Old: setMask, New: nil})

	g, err = f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops", "count": 3.0}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	w.take()
	if g, err = f.svc.AcceptVars(ctx, f.org, g.ID, g.Revision, []string{"count"}); err != nil {
		t.Fatal(err)
	}
	wantNone(t, "a person's accept", w.take())

	// A fresh schema per save: the fake store keeps what it is given.
	withDefault := func(d any) schema.Schema {
		sc := marksSchema()
		sc.Fields[0].Default = d
		return sc
	}
	sc := withDefault("hello")
	if g, _, err = f.svc.SetSchemaBy(ctx, f.org, g.ID, g.Revision, sc, assistant); err != nil {
		t.Fatal(err)
	}
	c = wantFields(t, "the assistant's default", w.take(), assistantchange.Field{Name: "default.greeting", Old: nil, New: "hello"},
		assistantchange.Field{Name: "vars.greeting", Old: nil, New: "hello"})
	if c.Reach == nil || c.Reach.Group == nil || *c.Reach.Group != g.ID {
		t.Fatalf("reach = %+v, want the group", c.Reach)
	}
	if g, err = f.svc.AcceptDefaults(ctx, f.org, g.ID, g.Revision, []string{"greeting"}); err != nil {
		t.Fatal(err)
	}
	wantNone(t, "a person's accept of a default", w.take())
	sc = withDefault("bye")
	if g, _, err = f.svc.SetSchemaBy(ctx, f.org, g.ID, g.Revision, sc, person); err != nil {
		t.Fatal(err)
	}
	wantNone(t, "a person's default", w.take())
	sc = withDefault(nil)
	if _, _, err = f.svc.SetSchemaBy(ctx, f.org, g.ID, g.Revision, sc, assistant); err != nil {
		t.Fatal(err)
	}
	wantFields(t, "the assistant's removed default", w.take(), assistantchange.Field{Name: "default.greeting", Old: "bye", New: nil},
		assistantchange.Field{Name: "vars.greeting", Old: "bye", New: nil})
}

// TestAssistantMembershipChangesAreHandedOn (decisions D6, D7): an
// assistant's host joining a group (with or without values), its change
// of a membership's values and its removal of a host are handed on with
// the group, the host and the reach; a person's change and an unchanged
// resend hand on nothing.
func TestAssistantMembershipChangesAreHandedOn(t *testing.T) {
	f := newFixture()
	w := &recordingWatcher{}
	f.svc.SetChangeWatcher(w)
	g := f.marksGroup(t)
	if _, err := f.svc.AssignBy(ctx, f.org, g.ID, f.web01, map[string]any{"note": "x"}, nil, assistant); err != nil {
		t.Fatal(err)
	}
	c := wantFields(t, "a host joining", w.take(), assistantchange.Field{Name: "member", Old: nil, New: "web-01"},
		assistantchange.Field{Name: "vars.note", Old: nil, New: "x"})
	if c.Reach == nil || len(c.Reach.Hosts) != 1 || c.Reach.Hosts[0] != f.web01 || len(c.Reach.FormerGroups) != 0 {
		t.Fatalf("join reach = %+v", c.Reach)
	}
	m, err := f.svc.AssignBy(ctx, f.org, g.ID, f.web01, map[string]any{"note": "y"}, nil, assistant)
	if err != nil {
		t.Fatal(err)
	}
	c = wantFields(t, "the assistant's change", w.take(), assistantchange.Field{Name: "vars.note", Old: "x", New: "y"})
	if c.Subject != assistantchange.SubjectInventoryMembership || c.ID != g.ID || c.Name != "web" || c.HostID == nil || *c.HostID != f.web01 || c.HostName != "web-01" {
		t.Fatalf("change = %+v", c)
	}
	rev := m.Revision
	if _, err = f.svc.AssignBy(ctx, f.org, g.ID, f.web01, map[string]any{"note": "z"}, &rev, person); err != nil {
		t.Fatal(err)
	}
	wantNone(t, "a person's change", w.take())
	if _, err = f.svc.AssignBy(ctx, f.org, g.ID, f.web01, map[string]any{"note": "z"}, nil, assistant); err != nil {
		t.Fatal(err)
	}
	wantNone(t, "the assistant's unchanged resend", w.take())
	if err := f.svc.UnassignBy(ctx, f.org, g.ID, f.web01, assistant); err != nil {
		t.Fatal(err)
	}
	c = wantFields(t, "a host leaving", w.take(), assistantchange.Field{Name: "member", Old: "web-01", New: nil},
		assistantchange.Field{Name: "vars.note", Old: "z", New: nil})
	if c.Reach == nil || len(c.Reach.Hosts) != 1 || len(c.Reach.FormerGroups) != 1 || c.Reach.FormerGroups[0] != g.ID {
		t.Fatalf("leave reach = %+v", c.Reach)
	}
	// A person's join and removal hand on nothing.
	if _, err := f.svc.Assign(ctx, f.org, g.ID, f.web01, map[string]any{"note": "p"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Unassign(ctx, f.org, g.ID, f.web01); err != nil {
		t.Fatal(err)
	}
	wantNone(t, "a person's join and removal", w.take())
}

// TestAssistantSchemaEditsThatChangeResolvedValuesAreHandedOn (final
// review I3): an assistant's schema save that changes what a run receives
// without touching a default — a field made inactive, retyped, removed,
// moved to host scope — is handed on with the values as they resolve
// before and after, members' included; a label change moves nothing.
func TestAssistantSchemaEditsThatChangeResolvedValuesAreHandedOn(t *testing.T) {
	base := func() schema.Schema {
		return schema.Schema{Fields: []schema.Field{
			{Name: "port", Type: schema.TypeInt},
			{Name: "flag", Type: schema.TypeBool},
			{Name: "note", Type: schema.TypeString, Scope: schema.ScopeHost},
		}}
	}
	inactive := false
	cases := map[string]struct {
		edit func(*schema.Schema)
		want []assistantchange.Field
	}{
		"label only": {func(sc *schema.Schema) { sc.Fields[0].Label = "Port" }, nil},
		"inactive": {func(sc *schema.Schema) { sc.Fields[1].Active = &inactive },
			[]assistantchange.Field{{Name: "vars.flag", Old: true, New: nil}}},
		"retyped": {func(sc *schema.Schema) { sc.Fields[0].Type = schema.TypeString },
			[]assistantchange.Field{{Name: "vars.port", Old: 80.0, New: nil}}},
		"removed": {func(sc *schema.Schema) { sc.Fields = sc.Fields[1:] },
			[]assistantchange.Field{{Name: "vars.port", Old: 80.0, New: nil}}},
		"host field removed": {func(sc *schema.Schema) { sc.Fields = sc.Fields[:2] },
			[]assistantchange.Field{{Name: "web-01: vars.note", Old: "n", New: nil}}},
		"moved to host scope": {func(sc *schema.Schema) { sc.Fields[0].Scope = schema.ScopeHost },
			[]assistantchange.Field{{Name: "vars.port", Old: 80.0, New: nil}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			w := &recordingWatcher{}
			f.svc.SetChangeWatcher(w)
			g := f.group(t, "web", nil)
			g, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, base())
			if err != nil {
				t.Fatal(err)
			}
			if g, err = f.svc.SetVars(ctx, f.org, g.ID, g.Revision, map[string]any{"port": 80.0, "flag": true}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.svc.Assign(ctx, f.org, g.ID, f.web01, map[string]any{"note": "n"}, nil); err != nil {
				t.Fatal(err)
			}
			sc := base()
			tc.edit(&sc)
			if _, _, err := f.svc.SetSchemaBy(ctx, f.org, g.ID, g.Revision, sc, assistant); err != nil {
				t.Fatal(err)
			}
			if tc.want == nil {
				wantNone(t, name, w.take())
				return
			}
			wantFields(t, name, w.take(), tc.want...)
		})
	}
}

// TestAssistantGroupMovesAndDeletesAreHandedOn (decision D7): an
// assistant's rename or jump host change of a group reaches its subtree,
// its move reaches the subtree and the groups above it before the move;
// its delete of a group with members reaches them and the group's former
// line; a description, a person's move and rename and an empty group's
// delete hand on nothing.
func TestAssistantGroupMovesAndDeletesAreHandedOn(t *testing.T) {
	f := newFixture()
	w := &recordingWatcher{}
	f.svc.SetChangeWatcher(w)
	a := f.group(t, "a", nil)
	b := f.group(t, "b", nil)
	child := f.group(t, "child", &a.ID)
	if _, err := f.svc.Assign(ctx, f.org, child.ID, f.web01, nil, nil); err != nil {
		t.Fatal(err)
	}
	child, err := f.svc.UpdateGroupBy(ctx, f.org, child.ID, child.Revision, inventory.GroupPatch{Name: "kid", ParentID: &a.ID}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	// A rename changes the group names a run sees (decision D7).
	c := wantFields(t, "a rename", w.take(), assistantchange.Field{Name: "name", Old: "child", New: "kid"})
	if c.Reach == nil || c.Reach.Group == nil || *c.Reach.Group != child.ID || len(c.Reach.FormerGroups) != 0 {
		t.Fatalf("rename reach = %+v", c.Reach)
	}
	// So does a jump host: it changes how a run reaches the members.
	if child, err = f.svc.UpdateGroupBy(ctx, f.org, child.ID, child.Revision, inventory.GroupPatch{Name: "kid", ParentID: &a.ID, BastionID: &f.bastionID}, assistant); err != nil {
		t.Fatal(err)
	}
	wantFields(t, "a jump host", w.take(), assistantchange.Field{Name: "jump_host", Old: nil, New: "bastion"})
	if child, err = f.svc.UpdateGroupBy(ctx, f.org, child.ID, child.Revision, inventory.GroupPatch{Name: "kid", Description: "d", ParentID: &a.ID, BastionID: &f.bastionID}, assistant); err != nil {
		t.Fatal(err)
	}
	wantNone(t, "a description", w.take())
	child, err = f.svc.UpdateGroupBy(ctx, f.org, child.ID, child.Revision, inventory.GroupPatch{Name: "kid", Description: "d", ParentID: &b.ID, BastionID: &f.bastionID}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	c = wantFields(t, "a move", w.take(), assistantchange.Field{Name: "parent", Old: "a", New: "b"})
	if c.Subject != assistantchange.SubjectInventoryGroup || c.Reach == nil || c.Reach.Group == nil || *c.Reach.Group != child.ID ||
		len(c.Reach.FormerGroups) != 1 || c.Reach.FormerGroups[0] != a.ID {
		t.Fatalf("move = %+v reach %+v", c, c.Reach)
	}
	if child, err = f.svc.UpdateGroup(ctx, f.org, child.ID, child.Revision, inventory.GroupPatch{Name: "kid2"}); err != nil {
		t.Fatal(err)
	}
	wantNone(t, "a person's move", w.take())
	if err := f.svc.DeleteGroupBy(ctx, f.org, a.ID, assistant); err != nil {
		t.Fatal(err)
	}
	wantNone(t, "an empty group's delete", w.take())
	if err := f.svc.DeleteGroupBy(ctx, f.org, child.ID, assistant); err != nil {
		t.Fatal(err)
	}
	c = wantFields(t, "a delete", w.take(), assistantchange.Field{Name: "members", Old: []string{"web-01"}, New: nil})
	if c.Reach == nil || len(c.Reach.Hosts) != 1 || c.Reach.Hosts[0] != f.web01 || len(c.Reach.FormerGroups) != 1 || c.Reach.FormerGroups[0] != child.ID {
		t.Fatalf("delete reach = %+v", c.Reach)
	}
}

// A watcher's failure fails the write.
func TestAWatcherFailureFailsTheInventoryWrite(t *testing.T) {
	f := newFixture()
	w := &recordingWatcher{err: errors.New("reviewer down")}
	f.svc.SetChangeWatcher(w)
	g := f.marksGroup(t)
	if _, err := f.svc.SetVarsBy(ctx, f.org, g.ID, g.Revision, map[string]any{"owner": "ops"}, assistant); err == nil {
		t.Fatal("the write succeeded although its pause failed")
	}
}

// TestAnInventoryChangeAndItsPauseAreOneTransaction: the watcher runs
// inside the write's transaction, so its failure leaves the stored values
// as they were.
func TestAnInventoryChangeAndItsPauseAreOneTransaction(t *testing.T) {
	d := testdb.Open(t)
	o, err := gen.New(d.Pool()).CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "inv-pause-tx", Name: "inv-pause-tx"})
	if err != nil {
		t.Fatal(err)
	}
	svc := inventory.NewService(inventory.NewSQLStore(d), fakeHosts{}, fakeBastions{}, testBox())
	w := &recordingWatcher{err: errors.New("reviewer down")}
	svc.SetChangeWatcher(w)
	g, err := svc.CreateGroup(ctx, o.ID, inventory.GroupInput{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	sc := schema.Schema{Fields: []schema.Field{{Name: "port", Type: schema.TypeInt}}}
	if g, _, err = svc.SetSchema(ctx, o.ID, g.ID, g.Revision, sc); err != nil {
		t.Fatal(err)
	}
	if g, err = svc.SetVars(ctx, o.ID, g.ID, g.Revision, map[string]any{"port": 80.0}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetVarsBy(ctx, o.ID, g.ID, g.Revision, map[string]any{"port": 8080.0}, assistant); err == nil {
		t.Fatal("the write succeeded although its pause failed")
	}
	if len(w.changes) != 1 {
		t.Fatalf("watcher saw %+v", w.changes)
	}
	got, err := svc.GetGroup(ctx, o.ID, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != g.Revision || got.Vars["port"] != 80.0 || len(got.AssistantKeys) != 0 {
		t.Fatalf("after a failed pause the group is %+v, want it unchanged", got.Group)
	}
}

// blockingWatcher signals inside once it is handed a change and returns
// when release closes: the write's transaction stays open meanwhile.
type blockingWatcher struct {
	inside  chan struct{}
	release chan struct{}
}

func (w blockingWatcher) Changed(context.Context, uuid.UUID, assistantchange.Change) error {
	close(w.inside)
	<-w.release
	return nil
}

// TestAMembershipChangeHoldsTheRowUntilItsPauseCommits (final review M1):
// an assistant's membership change locks the row from its read until the
// transaction that writes the values and the pause commits, so no other
// writer slips in between. A second transaction asking for the row
// without waiting is refused while the first is open, and gets it after.
func TestAMembershipChangeHoldsTheRowUntilItsPauseCommits(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	o, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "inv-member-lock", Name: "inv-member-lock"})
	if err != nil {
		t.Fatal(err)
	}
	hostStore := hosts.NewSQLStore(d)
	zone, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := hostStore.CreateZone(ctx, hosts.Zone{ID: zone, OrgID: o.ID, Slug: "fra", Name: "fra"}); err != nil {
		t.Fatal(err)
	}
	if err := hostStore.CreateHost(ctx, hosts.Host{ID: host, OrgID: o.ID, ZoneID: zone, Name: "web-01", Managed: true, Facts: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	svc := inventory.NewService(inventory.NewSQLStore(d), hosts.NewService(hostStore, time.Now), fakeBastions{}, testBox())
	g, err := svc.CreateGroup(ctx, o.ID, inventory.GroupInput{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if g, _, err = svc.SetSchema(ctx, o.ID, g.ID, g.Revision, schema.Schema{Fields: []schema.Field{{Name: "note", Type: schema.TypeString, Scope: schema.ScopeHost}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Assign(ctx, o.ID, g.ID, host, map[string]any{"note": "a"}, nil); err != nil {
		t.Fatal(err)
	}
	w := blockingWatcher{inside: make(chan struct{}), release: make(chan struct{})}
	svc.SetChangeWatcher(w)
	done := make(chan error, 1)
	go func() {
		_, err := svc.AssignBy(ctx, o.ID, g.ID, host, map[string]any{"note": "b"}, nil, assistant)
		done <- err
	}()
	<-w.inside
	lockNoWait := func() error {
		tx, err := d.Pool().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // read only
		var one int
		return tx.QueryRow(ctx, `SELECT 1 FROM inventory_memberships WHERE group_id = $1 AND host_id = $2 FOR NO KEY UPDATE NOWAIT`, g.ID, host).Scan(&one)
	}
	var pgErr *pgconn.PgError
	if err := lockNoWait(); !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("while the change is open the row = %v, want lock_not_available", err)
	}
	close(w.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := lockNoWait(); err != nil {
		t.Fatalf("after the commit the row = %v", err)
	}
}
