package sshconf_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/sshconf"
)

type sqlFixture struct {
	store *sshconf.SQLStore
	creds *credentials.SQLStore
	hosts *hosts.SQLStore
	org   uuid.UUID
	other uuid.UUID
	zone  uuid.UUID
	host  uuid.UUID
	key   uuid.UUID
}

func newSQLFixture(t *testing.T) sqlFixture {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	f := sqlFixture{store: sshconf.NewSQLStore(d), creds: credentials.NewSQLStore(d), hosts: hosts.NewSQLStore(d)}
	for _, slug := range []string{"ssh-org", "ssh-other"} {
		o, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
		if err != nil {
			t.Fatal(err)
		}
		if f.org == uuid.Nil {
			f.org = o.ID
		} else {
			f.other = o.ID
		}
	}
	f.zone = uuid.Must(uuid.NewV7())
	if err := f.hosts.CreateZone(ctx, hosts.Zone{ID: f.zone, OrgID: f.org, Slug: "fra", Name: "fra"}); err != nil {
		t.Fatal(err)
	}
	f.host = uuid.Must(uuid.NewV7())
	if err := f.hosts.CreateHost(ctx, hosts.Host{ID: f.host, OrgID: f.org, ZoneID: f.zone, Name: "web-01", Managed: true, Facts: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	c, err := f.creds.Create(ctx, credentials.Credential{ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Name: "key", Kind: credentials.KindSSHKey}, []byte("s"))
	if err != nil {
		t.Fatal(err)
	}
	f.key = c.ID
	return f
}

func TestSQLStoreBastions(t *testing.T) {
	f := newSQLFixture(t)
	a, err := f.store.CreateBastion(ctx, sshconf.Bastion{ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Name: "a", Address: "a.example", Port: 22, Username: "ops", CredentialID: &f.key})
	if err != nil {
		t.Fatalf("CreateBastion: %v", err)
	}
	if _, err := f.store.CreateBastion(ctx, sshconf.Bastion{ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Name: "a", Address: "x", Port: 22, Username: "ops"}); !errors.Is(err, sshconf.ErrNameTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	// A credential of another organization is refused by the composite key.
	if _, err := f.store.CreateBastion(ctx, sshconf.Bastion{ID: uuid.Must(uuid.NewV7()), OrgID: f.other, Name: "x", Address: "x", Port: 22, Username: "ops", CredentialID: &f.key}); !errors.Is(err, sshconf.ErrInvalidInput) {
		t.Fatalf("cross-org credential: %v", err)
	}
	b, err := f.store.CreateBastion(ctx, sshconf.Bastion{ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Name: "b", Address: "b.example", Port: 2222, Username: "ops", ViaBastionID: &a.ID})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.store.GetBastion(ctx, f.org, b.ID)
	if err != nil || got.ViaBastionID == nil || *got.ViaBastionID != a.ID || got.Port != 2222 {
		t.Fatalf("GetBastion = %+v, %v", got, err)
	}
	if _, err := f.store.GetBastion(ctx, f.other, b.ID); !errors.Is(err, sshconf.ErrNotFound) {
		t.Fatalf("other org: %v", err)
	}
	got.Address = "b2.example"
	if upd, err := f.store.UpdateBastion(ctx, got); err != nil || upd.Address != "b2.example" {
		t.Fatalf("UpdateBastion = %+v, %v", upd, err)
	}
	if list, err := f.store.ListBastions(ctx, f.org); err != nil || len(list) != 2 || list[0].Name != "a" {
		t.Fatalf("ListBastions = %v, %v", list, err)
	}

	// References block deletes: b is reached via a, and the credential is used by a.
	if err := f.store.DeleteBastion(ctx, f.org, a.ID); !errors.Is(err, sshconf.ErrInUse) {
		t.Fatalf("delete referenced bastion: %v", err)
	}
	if err := f.creds.Delete(ctx, f.org, f.key); !errors.Is(err, credentials.ErrInUse) {
		t.Fatalf("delete referenced credential: %v", err)
	}
	usage, err := f.creds.Usage(ctx, f.org)
	if err != nil || usage[f.key].Bastions != 1 || usage[f.key].Hosts != 0 {
		t.Fatalf("Usage = %v, %v", usage, err)
	}

	// Zone default.
	if err := f.store.SetZoneBastion(ctx, f.org, f.zone, &b.ID); err != nil {
		t.Fatal(err)
	}
	if z, err := f.hosts.GetZone(ctx, f.org, f.zone); err != nil || z.BastionID == nil || *z.BastionID != b.ID {
		t.Fatalf("zone default = %+v, %v", z, err)
	}
	if m, err := f.store.ZoneBastions(ctx, f.org); err != nil || m[f.zone] != b.ID {
		t.Fatalf("ZoneBastions = %v, %v", m, err)
	}
	if err := f.store.DeleteBastion(ctx, f.org, b.ID); !errors.Is(err, sshconf.ErrInUse) {
		t.Fatalf("delete zone default: %v", err)
	}
	if err := f.store.SetZoneBastion(ctx, f.other, f.zone, nil); !errors.Is(err, sshconf.ErrNotFound) {
		t.Fatalf("zone of another org: %v", err)
	}
	if err := f.store.SetZoneBastion(ctx, f.org, f.zone, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteBastion(ctx, f.org, b.ID); err != nil {
		t.Fatalf("delete unreferenced: %v", err)
	}
	if err := f.store.DeleteBastion(ctx, f.org, b.ID); !errors.Is(err, sshconf.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestSQLStoreHostSSH(t *testing.T) {
	f := newSQLFixture(t)
	if _, err := f.store.GetHostSSH(ctx, f.org, f.host); !errors.Is(err, sshconf.ErrNotFound) {
		t.Fatalf("unset: %v", err)
	}
	h := sshconf.HostSSH{HostID: f.host, OrgID: f.org, Username: "deploy", Port: 2200, CredentialID: &f.key, BastionMode: sshconf.ModeNone}
	if _, err := f.store.PutHostSSH(ctx, h, nil); err != nil {
		t.Fatalf("PutHostSSH: %v", err)
	}
	h.Port = 22
	if _, err := f.store.PutHostSSH(ctx, h, nil); err != nil {
		t.Fatalf("second PutHostSSH: %v", err)
	}
	got, err := f.store.GetHostSSH(ctx, f.org, f.host)
	if err != nil || got.Port != 22 || got.BastionMode != sshconf.ModeNone || got.CredentialID == nil {
		t.Fatalf("GetHostSSH = %+v, %v", got, err)
	}
	if list, err := f.store.ListHostSSH(ctx, f.org); err != nil || len(list) != 1 {
		t.Fatalf("ListHostSSH = %v, %v", list, err)
	}
	if usage, _ := f.creds.Usage(ctx, f.org); usage[f.key].Hosts != 1 {
		t.Fatalf("host usage = %v", usage)
	}
	// Deleting the host removes its settings.
	if err := f.hosts.DeleteHost(ctx, f.org, f.host); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.store.ListHostSSH(ctx, f.org); len(list) != 0 {
		t.Fatalf("settings survived their host: %v", list)
	}
}

// hostRevision reads the fixture host's current revision.
func (f sqlFixture) hostRevision(t *testing.T) int64 {
	t.Helper()
	h, err := f.hosts.GetHost(ctx, f.org, f.host)
	if err != nil {
		t.Fatal(err)
	}
	return h.Revision
}

// Backlog 7.79: an SSH settings write moves the host's revision, so a host
// dialog opened before it answers 412 instead of overwriting it.
func TestPutHostSSHBumpsTheHostRevision(t *testing.T) {
	f := newSQLFixture(t)
	before := f.hostRevision(t)
	h := sshconf.HostSSH{HostID: f.host, OrgID: f.org, Username: "deploy", Port: 2200, BastionMode: sshconf.ModeNone}

	got, err := f.store.PutHostSSH(ctx, h, nil)
	if err != nil {
		t.Fatalf("PutHostSSH: %v", err)
	}
	if got.HostRevision != before+1 || f.hostRevision(t) != before+1 {
		t.Fatalf("revision: returned %d, stored %d, want %d", got.HostRevision, f.hostRevision(t), before+1)
	}

	// The current revision, when sent, is accepted and spent.
	cur := before + 1
	if got, err := f.store.PutHostSSH(ctx, h, &cur); err != nil || got.HostRevision != cur+1 {
		t.Fatalf("with the current revision: %+v, %v", got, err)
	}
}

func TestPutHostSSHRefusesAStaleRevision(t *testing.T) {
	f := newSQLFixture(t)
	h := sshconf.HostSSH{HostID: f.host, OrgID: f.org, Username: "deploy", Port: 2200, BastionMode: sshconf.ModeNone}
	if _, err := f.store.PutHostSSH(ctx, h, nil); err != nil {
		t.Fatal(err)
	}
	before := f.hostRevision(t)

	stale := before - 1
	h.Port = 2300
	if _, err := f.store.PutHostSSH(ctx, h, &stale); !errors.Is(err, sshconf.ErrRevisionMismatch) {
		t.Fatalf("stale revision = %v, want ErrRevisionMismatch", err)
	}
	got, err := f.store.GetHostSSH(ctx, f.org, f.host)
	if err != nil || got.Port != 2200 {
		t.Fatalf("settings after a refused write = %+v, %v; want port 2200 untouched", got, err)
	}
	if f.hostRevision(t) != before {
		t.Fatalf("revision moved on a refused write: %d, want %d", f.hostRevision(t), before)
	}

	unknown := uuid.Must(uuid.NewV7())
	if _, err := f.store.PutHostSSH(ctx, sshconf.HostSSH{HostID: unknown, OrgID: f.org, Port: 22, BastionMode: sshconf.ModeInherit}, &stale); !errors.Is(err, sshconf.ErrNotFound) {
		t.Fatalf("unknown host = %v, want ErrNotFound", err)
	}
}
