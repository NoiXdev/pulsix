package inventory_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// countingBox counts the ciphertexts a renderer opens.
type countingBox struct {
	inventory.Sealer
	opened int
}

func (c *countingBox) OpenAAD(sealed, aad []byte) ([]byte, error) {
	c.opened++
	return c.Sealer.OpenAAD(sealed, aad)
}

// scopeFixture is an organization whose secrets sit in nested groups, a
// host in several groups, a host in no group and an unmanaged member, so
// every way a scope narrows the rendered document is exercised.
type scopeFixture struct {
	fixture
	prod, web, db, lab         uuid.UUID
	web02, db01, lab01, solo01 uuid.UUID
	sealedTotal                int
}

func newScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	sf := scopeFixture{fixture: newFixture()}
	f := sf.fixture
	for name, id := range map[string]*uuid.UUID{"web-02": &sf.web02, "db-01": &sf.db01, "lab-01": &sf.lab01, "solo-01": &sf.solo01} {
		*id = uuid.Must(uuid.NewV7())
		f.store.hosts[*id] = hosts.Host{ID: *id, OrgID: f.org, Name: name, Managed: true}
	}
	withSchema := func(name string, parent *uuid.UUID, fields []schema.Field, vars map[string]any) uuid.UUID {
		g := f.group(t, name, parent)
		v, _, err := f.svc.SetSchema(ctx, f.org, g.ID, g.Revision, schema.Schema{Fields: fields})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.SetVars(ctx, f.org, g.ID, v.Revision, vars); err != nil {
			t.Fatal(err)
		}
		return g.ID
	}
	assign := func(group, host uuid.UUID, vars map[string]any) {
		if _, err := f.svc.Assign(ctx, f.org, group, host, vars, nil); err != nil {
			t.Fatal(err)
		}
	}
	sf.prod = withSchema("prod", nil, []schema.Field{
		{Name: "region", Type: schema.TypeString},
		{Name: "prod_token", Type: schema.TypeSecret},
	}, map[string]any{"region": "eu", "prod_token": "prod-group-secret"})
	sf.web = withSchema("web", &sf.prod, []schema.Field{
		{Name: "db_password", Type: schema.TypeSecret},
		{Name: "vhosts", Type: schema.TypeRepeater, Scope: schema.ScopeHost, Fields: []schema.Field{
			{Name: "domain", Type: schema.TypeString},
			{Name: "password", Type: schema.TypeSecret},
		}},
	}, map[string]any{"db_password": "web-group-secret"})
	sf.db = withSchema("db", &sf.prod, []schema.Field{
		{Name: "db_root", Type: schema.TypeSecret},
		{Name: "replica_pw", Type: schema.TypeSecret, Scope: schema.ScopeHost},
	}, map[string]any{"db_root": "db-group-secret"})
	sf.lab = withSchema("lab", nil, []schema.Field{
		{Name: "lab_key", Type: schema.TypeSecret},
		{Name: "lab_host_pw", Type: schema.TypeSecret, Scope: schema.ScopeHost},
	}, map[string]any{"lab_key": "lab-group-secret"})
	assign(sf.web, f.web01, map[string]any{"vhosts": []any{map[string]any{"domain": "a.io", "password": "web01-web-secret"}}})
	assign(sf.web, sf.web02, map[string]any{"vhosts": []any{map[string]any{"domain": "b.io", "password": "web02-web-secret"}}})
	assign(sf.db, f.web01, map[string]any{"replica_pw": "web01-db-secret"})
	assign(sf.db, sf.db01, map[string]any{"replica_pw": "db01-db-secret"})
	assign(sf.lab, sf.lab01, map[string]any{"lab_host_pw": "lab01-lab-secret"})
	// An unmanaged member, written straight into the store as in
	// rendererFixture: it is never rendered, only warned about.
	f.store.members[memberKey{sf.lab, f.switch01}] = inventory.Membership{OrgID: f.org, GroupID: sf.lab, HostID: f.switch01, Revision: 1}
	sf.sealedTotal = 9
	return sf
}

func (sf scopeFixture) renderer(box inventory.Sealer) *inventory.Renderer {
	return inventory.NewRenderer(sf.store, sf.store.hosts, fakeSSH{}, fakeCreds{}, box)
}

// TestSnapshotOpensOnlyWhatTheScopeCarries pins that equivalence: a
// snapshot that opens only the rows render.Carries names renders exactly
// the document, connections and secrets of one that opens every row of the
// organization, for every shape of scope. A row Carries missed would lose
// its secrets silently (schema.Resolve drops a still-sealed value), so
// this is the guard on the helper.
func TestSnapshotOpensOnlyWhatTheScopeCarries(t *testing.T) {
	sf := newScopeFixture(t)
	ids := func(v ...uuid.UUID) []uuid.UUID { return v }
	scopes := map[string]render.Scope{
		"whole org":              {},
		"root group":             {GroupID: &sf.prod},
		"child group":            {GroupID: &sf.web},
		"sibling group":          {GroupID: &sf.db},
		"other root":             {GroupID: &sf.lab},
		"host in several groups": {HostIDs: ids(sf.web01)},
		"one host":               {HostIDs: ids(sf.db01)},
		"hosts across trees":     {HostIDs: ids(sf.web02, sf.lab01)},
		"group and host":         {GroupID: &sf.prod, HostIDs: ids(sf.web01, sf.lab01)},
		"ungrouped host":         {HostIDs: ids(sf.solo01)},
		"unmanaged host":         {HostIDs: ids(sf.switch01)},
	}
	selective := sf.renderer(testBox())
	every := inventory.OpeningEveryRow(sf.renderer(testBox()))
	for name, scope := range scopes {
		t.Run(name, func(t *testing.T) {
			got, err := selective.Snapshot(ctx, sf.org, scope)
			if err != nil {
				t.Fatal(err)
			}
			want, err := every.Snapshot(ctx, sf.org, scope)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Doc, want.Doc) {
				t.Errorf("doc = %v\nwant %v", got.Doc, want.Doc)
			}
			if !reflect.DeepEqual(got.Connections, want.Connections) {
				t.Errorf("connections = %v\nwant %v", got.Connections, want.Connections)
			}
			if !reflect.DeepEqual(got.Warnings, want.Warnings) {
				t.Errorf("warnings = %v\nwant %v", got.Warnings, want.Warnings)
			}
			// The fake store lists memberships in map order, so the
			// order of Secrets varies between any two snapshots.
			gs, ws := slices.Sorted(slices.Values(got.Secrets)), slices.Sorted(slices.Values(want.Secrets))
			if !slices.Equal(gs, ws) {
				t.Errorf("secrets = %q, want %q", gs, ws)
			}
		})
	}
}

func TestSnapshotNeverOpensAnOutOfScopeSecret(t *testing.T) {
	sf := newScopeFixture(t)
	cases := []struct {
		name    string
		scope   render.Scope
		opened  int
		secrets []string
	}{
		{"one host of another tree", render.Scope{HostIDs: []uuid.UUID{sf.lab01}}, 2,
			[]string{"lab-group-secret", "lab01-lab-secret"}},
		// web-01's own memberships and every group above them, but not
		// the memberships of web-02 and db-01 in the same groups.
		{"host in several groups", render.Scope{HostIDs: []uuid.UUID{sf.web01}}, 5,
			[]string{"db-group-secret", "prod-group-secret", "web-group-secret", "web01-db-secret", "web01-web-secret"}},
		{"ungrouped host", render.Scope{HostIDs: []uuid.UUID{sf.solo01}}, 0, nil},
		{"whole org", render.Scope{}, sf.sealedTotal,
			[]string{"db-group-secret", "db01-db-secret", "lab-group-secret", "lab01-lab-secret", "prod-group-secret",
				"web-group-secret", "web01-db-secret", "web01-web-secret", "web02-web-secret"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			box := &countingBox{Sealer: testBox()}
			snap, err := sf.renderer(box).Snapshot(ctx, sf.org, c.scope)
			if err != nil {
				t.Fatal(err)
			}
			if box.opened != c.opened {
				t.Errorf("opened %d ciphertexts, want %d", box.opened, c.opened)
			}
			if got := slices.Sorted(slices.Values(snap.Secrets)); !slices.Equal(got, c.secrets) {
				t.Errorf("secrets = %q, want %q", got, c.secrets)
			}
		})
	}

	box := &countingBox{Sealer: testBox()}
	if _, err := sf.renderer(box).Preview(ctx, sf.org, render.Scope{}); err != nil || box.opened != 0 {
		t.Fatalf("a preview opened %d ciphertexts: %v", box.opened, err)
	}
}

// TestSnapshotStillFailsOnAnOutOfScopeStray keeps backlog 2.22's decision:
// the stray check runs over the whole organization, so a sealed value left
// under a field that is no longer secret fails a snapshot of a scope that
// never carries it.
func TestSnapshotStillFailsOnAnOutOfScopeStray(t *testing.T) {
	sf := newScopeFixture(t)
	cur := sf.store.groups[sf.db]
	if _, _, err := sf.svc.SetSchema(ctx, sf.org, sf.db, cur.Revision, schema.Schema{Fields: []schema.Field{
		{Name: "db_root", Type: schema.TypeString},
		{Name: "replica_pw", Type: schema.TypeSecret, Scope: schema.ScopeHost},
	}}); err != nil {
		t.Fatal(err)
	}
	box := &countingBox{Sealer: testBox()}
	_, err := sf.renderer(box).Snapshot(ctx, sf.org, render.Scope{HostIDs: []uuid.UUID{sf.lab01}})
	if !errors.Is(err, inventory.ErrUnrenderable) || !strings.Contains(err.Error(), "inventory group db") || !strings.Contains(err.Error(), "db_root") {
		t.Fatalf("snapshot with an out-of-scope stray: %v", err)
	}
	if box.opened != 0 {
		t.Fatalf("a failing snapshot opened %d ciphertexts", box.opened)
	}
}
