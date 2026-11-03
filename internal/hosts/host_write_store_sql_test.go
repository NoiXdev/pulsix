package hosts_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
)

func seedBastion(t *testing.T, q *gen.Queries, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := q.CreateBastion(ctx, gen.CreateBastionParams{
		ID: id, OrgID: orgID, Name: name, Address: "10.0.0.9", Port: 22, Username: "ops",
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func manualTags(kv ...string) *[]hosts.Tag {
	out := make([]hosts.Tag, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, hosts.Tag{Key: kv[i], Value: kv[i+1], Source: hosts.TagSourceManual})
	}
	return &out
}

// TestCreateHostWithJumpAndTagsIsAtomic: the host row, its tags and its jump
// host are one write. A jump host from another organization fails the
// foreign key, which reads as "not found", and leaves no host and no tags
// behind.
func TestCreateHostWithJumpAndTagsIsAtomic(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostwrite-atomic-org")
	other := seedOrg(t, q, "hostwrite-atomic-other")
	zone := seedZone(t, store, org, "fra")
	foreignBastion := seedBastion(t, q, other, "edge")

	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-atomic", Facts: map[string]any{}}
	_, err := store.CreateHostWith(ctx, h, hosts.HostExtras{
		Jump: &hosts.Jump{Mode: hosts.JumpModeExplicit, BastionID: &foreignBastion},
		Tags: manualTags("role", "web"),
	})
	if !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("CreateHostWith foreign bastion = %v, want ErrNotFound", err)
	}
	if _, err := store.GetHost(ctx, org, h.ID); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("GetHost after a failed write = %v, want ErrNotFound", err)
	}
	list, _, err := store.ListHosts(ctx, org, hosts.ListFilter{Query: "web-atomic"}, "", 10)
	if err != nil || len(list) != 0 {
		t.Fatalf("ListHosts after a failed write = %+v, %v; want none", list, err)
	}
	tags, err := store.ListHostTags(ctx, org, h.ID)
	if err != nil || len(tags) != 0 {
		t.Fatalf("tags after a failed write = %+v, %v; want none", tags, err)
	}

	// An unknown bastion id fails the same way.
	unknown := uuid.Must(uuid.NewV7())
	_, err = store.CreateHostWith(ctx, h, hosts.HostExtras{Jump: &hosts.Jump{Mode: hosts.JumpModeExplicit, BastionID: &unknown}})
	if !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("CreateHostWith unknown bastion = %v, want ErrNotFound", err)
	}

	// The same write with the org's own bastion lands whole.
	own := seedBastion(t, q, org, "edge")
	created, err := store.CreateHostWith(ctx, h, hosts.HostExtras{
		Jump: &hosts.Jump{Mode: hosts.JumpModeExplicit, BastionID: &own},
		Tags: manualTags("role", "web"),
	})
	if err != nil {
		t.Fatalf("CreateHostWith own bastion: %v", err)
	}
	if created.ID != h.ID || created.Name != "web-atomic" || created.Revision != 1 {
		t.Fatalf("created = %+v, want the stored row at revision 1", created)
	}
	row, err := q.GetHostSSH(ctx, gen.GetHostSSHParams{OrgID: org, HostID: h.ID})
	if err != nil || row.BastionMode != "explicit" || db.UUIDPtr(row.BastionID) == nil || *db.UUIDPtr(row.BastionID) != own {
		t.Fatalf("host_ssh = %+v, %v", row, err)
	}
	if row.Port != 22 || row.Username != "" {
		t.Fatalf("a new host_ssh row keeps the defaults, got %+v", row)
	}
	tags, err = store.ListHostTags(ctx, org, h.ID)
	if err != nil || len(tags) != 1 || tags[0].Key != "role" || tags[0].Value != "web" {
		t.Fatalf("tags = %+v, %v", tags, err)
	}
}

// TestCreateHostWithoutExtrasWritesNoSSHRow: no jump host means no host_ssh
// row at all, which reads as the defaults (inherit).
func TestCreateHostWithoutExtrasWritesNoSSHRow(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostwrite-plain-org")
	zone := seedZone(t, store, org, "fra")
	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-plain"}
	if _, err := store.CreateHostWith(ctx, h, hosts.HostExtras{}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetHostSSH(ctx, gen.GetHostSSHParams{OrgID: org, HostID: h.ID}); err == nil {
		t.Fatal("a host written without a jump host must have no host_ssh row")
	}
}

// TestUpdateHostJumpKeepsSSHUser: the jump host write touches only
// bastion_mode and bastion_id; the user, port and credential stay.
func TestUpdateHostJumpKeepsSSHUser(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostwrite-keepuser-org")
	zone := seedZone(t, store, org, "fra")
	bastion := seedBastion(t, q, org, "edge")
	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-01"}
	if err := store.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertHostSSH(ctx, gen.UpsertHostSSHParams{
		HostID: h.ID, OrgID: org, Username: "deploy", Port: 2222,
		BastionMode: "explicit", BastionID: db.NullUUID(&bastion),
	}); err != nil {
		t.Fatal(err)
	}

	updated, err := store.UpdateHostWith(ctx, hosts.Host{ID: h.ID, OrgID: org, ZoneID: zone.ID, Name: "web-01", Revision: 1},
		hosts.HostExtras{Jump: &hosts.Jump{Mode: hosts.JumpModeNone}})
	if err != nil {
		t.Fatalf("UpdateHostWith: %v", err)
	}
	if updated.Revision != 2 {
		t.Fatalf("revision = %d, want 2", updated.Revision)
	}
	row, err := q.GetHostSSH(ctx, gen.GetHostSSHParams{OrgID: org, HostID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	if row.Username != "deploy" || row.Port != 2222 {
		t.Fatalf("user and port must survive a jump host write, got %+v", row)
	}
	if row.BastionMode != "none" || row.BastionID.Valid {
		t.Fatalf("bastion = %s/%v, want none and no id", row.BastionMode, row.BastionID)
	}
}

// TestUpdateHostNilJumpAndTagsLeavesThem: an update that names neither
// leaves both exactly as they were.
func TestUpdateHostNilJumpAndTagsLeavesThem(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostwrite-nil-org")
	zone := seedZone(t, store, org, "fra")
	bastion := seedBastion(t, q, org, "edge")
	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-01"}
	if _, err := store.CreateHostWith(ctx, h, hosts.HostExtras{
		Jump: &hosts.Jump{Mode: hosts.JumpModeExplicit, BastionID: &bastion},
		Tags: manualTags("role", "web", "env", "prod"),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.UpdateHostWith(ctx, hosts.Host{ID: h.ID, OrgID: org, ZoneID: zone.ID, Name: "web-01-renamed", Revision: 1},
		hosts.HostExtras{}); err != nil {
		t.Fatalf("UpdateHostWith: %v", err)
	}
	row, err := q.GetHostSSH(ctx, gen.GetHostSSHParams{OrgID: org, HostID: h.ID})
	if err != nil || row.BastionMode != "explicit" || db.UUIDPtr(row.BastionID) == nil || *db.UUIDPtr(row.BastionID) != bastion {
		t.Fatalf("host_ssh = %+v, %v; want the explicit jump host untouched", row, err)
	}
	tags, err := store.ListHostTags(ctx, org, h.ID)
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags = %+v, %v; want both untouched", tags, err)
	}

	// An empty, non-nil tag list does clear the manual tags.
	if _, err := store.UpdateHostWith(ctx, hosts.Host{ID: h.ID, OrgID: org, ZoneID: zone.ID, Name: "web-01-renamed", Revision: 2},
		hosts.HostExtras{Tags: manualTags()}); err != nil {
		t.Fatal(err)
	}
	if tags, err := store.ListHostTags(ctx, org, h.ID); err != nil || len(tags) != 0 {
		t.Fatalf("tags = %+v, %v; want none after an empty list", tags, err)
	}
}

// TestUpdateHostForeignBastionRollsBackTheRow: a failing jump host write
// also undoes the row change and the tags of the same update.
func TestUpdateHostForeignBastionRollsBackTheRow(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostwrite-rollback-org")
	other := seedOrg(t, q, "hostwrite-rollback-other")
	zone := seedZone(t, store, org, "fra")
	foreign := seedBastion(t, q, other, "edge")
	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-01"}
	if _, err := store.CreateHostWith(ctx, h, hosts.HostExtras{Tags: manualTags("role", "web")}); err != nil {
		t.Fatal(err)
	}

	_, err := store.UpdateHostWith(ctx, hosts.Host{ID: h.ID, OrgID: org, ZoneID: zone.ID, Name: "web-01-renamed", Revision: 1},
		hosts.HostExtras{Jump: &hosts.Jump{Mode: hosts.JumpModeExplicit, BastionID: &foreign}, Tags: manualTags("role", "db")})
	if !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("UpdateHostWith foreign bastion = %v, want ErrNotFound", err)
	}
	got, err := store.GetHost(ctx, org, h.ID)
	if err != nil || got.Name != "web-01" || got.Revision != 1 {
		t.Fatalf("host after a failed update = %+v, %v; want it unchanged", got, err)
	}
	tags, err := store.ListHostTags(ctx, org, h.ID)
	if err != nil || len(tags) != 1 || tags[0].Value != "web" {
		t.Fatalf("tags after a failed update = %+v, %v; want role=web", tags, err)
	}
}

// TestUpdateHostWithStaleRevisionWritesNothing: a stale revision fails
// before the jump host and tags are written.
func TestUpdateHostWithStaleRevisionWritesNothing(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostwrite-stale-org")
	zone := seedZone(t, store, org, "fra")
	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-01"}
	if err := store.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	_, err := store.UpdateHostWith(ctx, hosts.Host{ID: h.ID, OrgID: org, ZoneID: zone.ID, Name: "web-01", Revision: 7},
		hosts.HostExtras{Jump: &hosts.Jump{Mode: hosts.JumpModeNone}, Tags: manualTags("role", "web")})
	if !errors.Is(err, hosts.ErrRevisionMismatch) {
		t.Fatalf("stale revision = %v, want ErrRevisionMismatch", err)
	}
	if _, err := q.GetHostSSH(ctx, gen.GetHostSSHParams{OrgID: org, HostID: h.ID}); err == nil {
		t.Fatal("a stale update must not write the jump host")
	}
	if tags, err := store.ListHostTags(ctx, org, h.ID); err != nil || len(tags) != 0 {
		t.Fatalf("a stale update must not write tags, got %+v, %v", tags, err)
	}
}
