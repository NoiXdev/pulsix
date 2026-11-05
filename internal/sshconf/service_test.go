package sshconf_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/sshconf"
)

var ctx = context.Background()

type fakeStore struct {
	bastions map[uuid.UUID]sshconf.Bastion
	zones    map[uuid.UUID]zoneRow
	hostSSH  map[uuid.UUID]sshconf.HostSSH

	putIfRevision *int64
	putErr        error
}

const fakeHostRevision = 7

type zoneRow struct {
	org     uuid.UUID
	bastion *uuid.UUID
}

func newFakeStore() *fakeStore {
	return &fakeStore{bastions: map[uuid.UUID]sshconf.Bastion{}, zones: map[uuid.UUID]zoneRow{}, hostSSH: map[uuid.UUID]sshconf.HostSSH{}}
}

func (f *fakeStore) CreateBastion(_ context.Context, b sshconf.Bastion) (sshconf.Bastion, error) {
	for _, x := range f.bastions {
		if x.OrgID == b.OrgID && x.Name == b.Name {
			return sshconf.Bastion{}, sshconf.ErrNameTaken
		}
	}
	f.bastions[b.ID] = b
	return b, nil
}

func (f *fakeStore) GetBastion(_ context.Context, orgID, id uuid.UUID) (sshconf.Bastion, error) {
	b, ok := f.bastions[id]
	if !ok || b.OrgID != orgID {
		return sshconf.Bastion{}, sshconf.ErrNotFound
	}
	return b, nil
}

func (f *fakeStore) ListBastions(_ context.Context, orgID uuid.UUID) ([]sshconf.Bastion, error) {
	var out []sshconf.Bastion
	for _, b := range f.bastions {
		if b.OrgID == orgID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateBastion(_ context.Context, b sshconf.Bastion) (sshconf.Bastion, error) {
	if cur, ok := f.bastions[b.ID]; !ok || cur.OrgID != b.OrgID {
		return sshconf.Bastion{}, sshconf.ErrNotFound
	}
	f.bastions[b.ID] = b
	return b, nil
}

func (f *fakeStore) DeleteBastion(_ context.Context, orgID, id uuid.UUID) error {
	b, ok := f.bastions[id]
	if !ok || b.OrgID != orgID {
		return sshconf.ErrNotFound
	}
	delete(f.bastions, id)
	return nil
}

func (f *fakeStore) SetZoneBastion(_ context.Context, orgID, zoneID uuid.UUID, bastionID *uuid.UUID) error {
	z, ok := f.zones[zoneID]
	if !ok || z.org != orgID {
		return sshconf.ErrNotFound
	}
	z.bastion = bastionID
	f.zones[zoneID] = z
	return nil
}

func (f *fakeStore) ZoneBastions(_ context.Context, orgID uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	out := map[uuid.UUID]uuid.UUID{}
	for id, z := range f.zones {
		if z.org == orgID && z.bastion != nil {
			out[id] = *z.bastion
		}
	}
	return out, nil
}

func (f *fakeStore) GetHostSSH(_ context.Context, orgID, hostID uuid.UUID) (sshconf.HostSSH, error) {
	h, ok := f.hostSSH[hostID]
	if !ok || h.OrgID != orgID {
		return sshconf.HostSSH{}, sshconf.ErrNotFound
	}
	return h, nil
}

// PutHostSSH records the If-Match revision it was handed and answers with a
// fixed host revision, standing in for the real store's bump.
func (f *fakeStore) PutHostSSH(_ context.Context, h sshconf.HostSSH, ifRevision *int64) (sshconf.HostSSH, error) {
	f.putIfRevision = ifRevision
	if f.putErr != nil {
		return sshconf.HostSSH{}, f.putErr
	}
	f.hostSSH[h.HostID] = h
	h.HostRevision = fakeHostRevision
	return h, nil
}

func (f *fakeStore) ListHostSSH(_ context.Context, orgID uuid.UUID) ([]sshconf.HostSSH, error) {
	var out []sshconf.HostSSH
	for _, h := range f.hostSSH {
		if h.OrgID == orgID {
			out = append(out, h)
		}
	}
	return out, nil
}

// WithChainLock runs fn directly against f: unit tests exercise one
// goroutine at a time, so there is no concurrent call to serialize against.
// The integration test in concurrency_test.go covers the real lock.
func (f *fakeStore) WithChainLock(ctx context.Context, orgID uuid.UUID, fn func(sshconf.Store) error) error {
	return fn(f)
}

type fakeCreds map[uuid.UUID]credentials.Credential

func (f fakeCreds) Get(_ context.Context, orgID, id uuid.UUID) (credentials.Credential, error) {
	c, ok := f[id]
	if !ok || c.OrgID != orgID {
		return credentials.Credential{}, credentials.ErrNotFound
	}
	return c, nil
}

type fakeHosts map[uuid.UUID]hosts.Host

func (f fakeHosts) GetHost(_ context.Context, orgID, id uuid.UUID) (hosts.Host, error) {
	h, ok := f[id]
	if !ok || h.OrgID != orgID {
		return hosts.Host{}, hosts.ErrNotFound
	}
	return h, nil
}

type fixture struct {
	svc     *sshconf.Service
	store   *fakeStore
	org     uuid.UUID
	sshKey  uuid.UUID
	gitKey  uuid.UUID
	host    uuid.UUID
	zone    uuid.UUID
	foreign uuid.UUID // an SSH key of another organization
}

func newFixture() fixture {
	f := fixture{store: newFakeStore(), org: uuid.Must(uuid.NewV7())}
	f.sshKey, f.gitKey, f.host, f.zone, f.foreign = uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	creds := fakeCreds{
		f.sshKey:  {ID: f.sshKey, OrgID: f.org, Kind: credentials.KindSSHKey},
		f.gitKey:  {ID: f.gitKey, OrgID: f.org, Kind: credentials.KindGitKey},
		f.foreign: {ID: f.foreign, OrgID: uuid.Must(uuid.NewV7()), Kind: credentials.KindSSHKey},
	}
	hostMap := fakeHosts{f.host: {ID: f.host, OrgID: f.org, Name: "web-01", Managed: true}}
	f.store.zones[f.zone] = zoneRow{org: f.org}
	f.svc = sshconf.NewService(f.store, creds, hostMap)
	return f
}

func TestCreateBastionValidates(t *testing.T) {
	f := newFixture()
	unknown := uuid.Must(uuid.NewV7())
	ok := sshconf.BastionInput{Name: "bastion-fra", Address: "bastion.fra.example", Username: "ops", CredentialID: &f.sshKey}
	cases := []struct {
		name   string
		modify func(*sshconf.BastionInput)
		msg    string
	}{
		{"bad name", func(in *sshconf.BastionInput) { in.Name = "Bad Name" }, "name"},
		{"empty address", func(in *sshconf.BastionInput) { in.Address = "" }, "address"},
		{"address with space", func(in *sshconf.BastionInput) { in.Address = "a b" }, "address"},
		{"port too high", func(in *sshconf.BastionInput) { in.Port = 70000 }, "port"},
		{"no username", func(in *sshconf.BastionInput) { in.Username = "" }, "username"},
		{"unknown credential", func(in *sshconf.BastionInput) { in.CredentialID = &unknown }, "credential"},
		{"credential of another org", func(in *sshconf.BastionInput) { in.CredentialID = &f.foreign }, "credential"},
		{"git credential", func(in *sshconf.BastionInput) { in.CredentialID = &f.gitKey }, "SSH key or SSH password"},
		{"unknown via", func(in *sshconf.BastionInput) { in.ViaBastionID = &unknown }, "does not exist"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := ok
			c.modify(&in)
			_, err := f.svc.CreateBastion(ctx, f.org, in)
			if !errors.Is(err, sshconf.ErrInvalidInput) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("err = %v, want ErrInvalidInput mentioning %q", err, c.msg)
			}
		})
	}
	b, err := f.svc.CreateBastion(ctx, f.org, ok)
	if err != nil {
		t.Fatalf("valid bastion: %v", err)
	}
	if b.Port != sshconf.DefaultPort {
		t.Fatalf("port defaulted to %d", b.Port)
	}
	ipv6 := ok
	ipv6.Name, ipv6.Address = "v6", "2001:db8::1"
	if _, err := f.svc.CreateBastion(ctx, f.org, ipv6); err != nil {
		t.Fatalf("IPv6 address: %v", err)
	}
}

func TestUpdateBastionRejectsCycles(t *testing.T) {
	f := newFixture()
	a, err := f.svc.CreateBastion(ctx, f.org, sshconf.BastionInput{Name: "a", Address: "a.example", Username: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.svc.CreateBastion(ctx, f.org, sshconf.BastionInput{Name: "b", Address: "b.example", Username: "ops", ViaBastionID: &a.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.UpdateBastion(ctx, f.org, a.ID, sshconf.BastionInput{Name: "a", Address: "a.example", Username: "ops", ViaBastionID: &b.ID})
	if !errors.Is(err, sshconf.ErrInvalidInput) || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle: %v", err)
	}
	_, err = f.svc.UpdateBastion(ctx, f.org, a.ID, sshconf.BastionInput{Name: "a", Address: "a.example", Username: "ops", ViaBastionID: &a.ID})
	if !errors.Is(err, sshconf.ErrInvalidInput) {
		t.Fatalf("self reference: %v", err)
	}
	renamed, err := f.svc.UpdateBastion(ctx, f.org, a.ID, sshconf.BastionInput{Name: "a2", Address: "a.example", Port: 2222, Username: "ops"})
	if err != nil || renamed.Name != "a2" || renamed.Port != 2222 {
		t.Fatalf("update = %+v, %v", renamed, err)
	}
	if _, err := f.svc.UpdateBastion(ctx, f.org, uuid.Must(uuid.NewV7()), sshconf.BastionInput{Name: "x", Address: "x", Username: "ops"}); !errors.Is(err, sshconf.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	// Not-found must win even when the input is also invalid: the target's
	// existence is checked before field validation.
	if _, err := f.svc.UpdateBastion(ctx, f.org, uuid.Must(uuid.NewV7()), sshconf.BastionInput{Name: "Not Valid!", Address: "x", Username: "ops"}); !errors.Is(err, sshconf.ErrNotFound) {
		t.Fatalf("unknown with invalid input: %v", err)
	}
	if list, err := f.svc.ListBastions(ctx, f.org); err != nil || len(list) != 2 {
		t.Fatalf("ListBastions = %v, %v", list, err)
	}
	if got, err := f.svc.GetBastion(ctx, f.org, b.ID); err != nil || got.Name != "b" {
		t.Fatalf("GetBastion = %+v, %v", got, err)
	}
	if err := f.svc.DeleteBastion(ctx, f.org, b.ID); err != nil {
		t.Fatal(err)
	}
}

func TestZoneBastion(t *testing.T) {
	f := newFixture()
	b, err := f.svc.CreateBastion(ctx, f.org, sshconf.BastionInput{Name: "a", Address: "a.example", Username: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetZoneBastion(ctx, f.org, f.zone, &b.ID); err != nil {
		t.Fatal(err)
	}
	if m, err := f.svc.ZoneBastions(ctx, f.org); err != nil || m[f.zone] != b.ID {
		t.Fatalf("ZoneBastions = %v, %v", m, err)
	}
	unknown := uuid.Must(uuid.NewV7())
	if err := f.svc.SetZoneBastion(ctx, f.org, f.zone, &unknown); !errors.Is(err, sshconf.ErrInvalidInput) {
		t.Fatalf("unknown bastion: %v", err)
	}
	if err := f.svc.SetZoneBastion(ctx, f.org, unknown, nil); !errors.Is(err, sshconf.ErrNotFound) {
		t.Fatalf("unknown zone: %v", err)
	}
	if err := f.svc.SetZoneBastion(ctx, f.org, f.zone, nil); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.svc.ZoneBastions(ctx, f.org); len(m) != 0 {
		t.Fatalf("cleared zone default still listed: %v", m)
	}
}

func TestHostSSH(t *testing.T) {
	f := newFixture()
	def, err := f.svc.HostSSH(ctx, f.org, f.host)
	if err != nil {
		t.Fatal(err)
	}
	if def.Port != sshconf.DefaultPort || def.BastionMode != sshconf.ModeInherit || def.HostID != f.host {
		t.Fatalf("defaults = %+v", def)
	}
	if _, err := f.svc.HostSSH(ctx, f.org, uuid.Must(uuid.NewV7())); !errors.Is(err, sshconf.ErrNotFound) {
		t.Fatalf("unknown host: %v", err)
	}
	b, err := f.svc.CreateBastion(ctx, f.org, sshconf.BastionInput{Name: "a", Address: "a.example", Username: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	unknown := uuid.Must(uuid.NewV7())
	cases := []struct {
		name string
		in   sshconf.HostSSHInput
		msg  string
	}{
		{"explicit without bastion", sshconf.HostSSHInput{BastionMode: sshconf.ModeExplicit}, "explicit"},
		{"inherit with bastion", sshconf.HostSSHInput{BastionMode: sshconf.ModeInherit, BastionID: &b.ID}, "explicit"},
		{"unknown mode", sshconf.HostSSHInput{BastionMode: "sometimes"}, "mode"},
		{"unknown bastion", sshconf.HostSSHInput{BastionMode: sshconf.ModeExplicit, BastionID: &unknown}, "does not exist"},
		{"git credential", sshconf.HostSSHInput{CredentialID: &f.gitKey}, "SSH key or SSH password"},
		{"bad username", sshconf.HostSSHInput{Username: "has space"}, "username"},
		{"bad port", sshconf.HostSSHInput{Port: -1}, "port"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := f.svc.SetHostSSH(ctx, f.org, f.host, c.in); !errors.Is(err, sshconf.ErrInvalidInput) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("err = %v, want ErrInvalidInput mentioning %q", err, c.msg)
			}
		})
	}
	got, err := f.svc.SetHostSSH(ctx, f.org, f.host, sshconf.HostSSHInput{Username: "deploy", CredentialID: &f.sshKey, BastionMode: sshconf.ModeExplicit, BastionID: &b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != sshconf.DefaultPort || got.OrgID != f.org || got.HostID != f.host {
		t.Fatalf("stored = %+v", got)
	}
	none, err := f.svc.SetHostSSH(ctx, f.org, f.host, sshconf.HostSSHInput{})
	if err != nil || none.BastionMode != sshconf.ModeInherit {
		t.Fatalf("empty mode must mean inherit: %+v, %v", none, err)
	}
	if _, err := f.svc.SetHostSSH(ctx, f.org, unknown, sshconf.HostSSHInput{}); !errors.Is(err, sshconf.ErrNotFound) {
		t.Fatalf("unknown host: %v", err)
	}
	if list, err := f.svc.ListHostSSH(ctx, f.org); err != nil || len(list) != 1 {
		t.Fatalf("ListHostSSH = %v, %v", list, err)
	}
}

// TestSetHostSSHPassesTheRevisionThrough: the caller's If-Match revision
// reaches the store untouched, the host's new revision comes back, and a
// stale one is the store's ErrRevisionMismatch (backlog 7.79).
func TestSetHostSSHPassesTheRevisionThrough(t *testing.T) {
	f := newFixture()
	got, err := f.svc.SetHostSSH(ctx, f.org, f.host, sshconf.HostSSHInput{})
	if err != nil || got.HostRevision != fakeHostRevision || f.store.putIfRevision != nil {
		t.Fatalf("without If-Match: %+v, %v, store saw %v", got, err, f.store.putIfRevision)
	}
	rev := int64(5)
	if _, err := f.svc.SetHostSSH(ctx, f.org, f.host, sshconf.HostSSHInput{IfRevision: &rev}); err != nil || f.store.putIfRevision == nil || *f.store.putIfRevision != 5 {
		t.Fatalf("with If-Match: %v, store saw %v", err, f.store.putIfRevision)
	}
	f.store.putErr = sshconf.ErrRevisionMismatch
	if _, err := f.svc.SetHostSSH(ctx, f.org, f.host, sshconf.HostSSHInput{IfRevision: &rev}); !errors.Is(err, sshconf.ErrRevisionMismatch) {
		t.Fatalf("stale: %v, want ErrRevisionMismatch", err)
	}
}

// TestUpdateBastionRejectsBreakingAnotherChain pins that a jump host update
// is checked against every chain of the organization, not only its own: B
// may go via C (B's chain is 4 hops), but A goes via B and would then be 5.
func TestUpdateBastionRejectsBreakingAnotherChain(t *testing.T) {
	f := newFixture()
	mk := func(name string, via *uuid.UUID) sshconf.Bastion {
		t.Helper()
		b, err := f.svc.CreateBastion(ctx, f.org, sshconf.BastionInput{Name: name, Address: name + ".example", Username: "ops", ViaBastionID: via})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	e := mk("e", nil)
	d := mk("d", &e.ID)
	c := mk("c", &d.ID)
	b := mk("b", nil)
	mk("a", &b.ID)

	_, err := f.svc.UpdateBastion(ctx, f.org, b.ID, sshconf.BastionInput{Name: "b", Address: "b.example", Username: "ops", ViaBastionID: &c.ID})
	if !errors.Is(err, sshconf.ErrInvalidInput) || !strings.Contains(err.Error(), "jump host a") || !strings.Contains(err.Error(), "4 hops") {
		t.Fatalf("an update that pushes another chain past the limit: %v", err)
	}
	if got := f.store.bastions[b.ID]; got.ViaBastionID != nil {
		t.Fatalf("the rejected update was stored: %+v", got)
	}
	if _, err := f.svc.UpdateBastion(ctx, f.org, b.ID, sshconf.BastionInput{Name: "b", Address: "b.example", Username: "ops", ViaBastionID: &d.ID}); err != nil {
		t.Fatalf("A's chain stays at 4 hops via D: %v", err)
	}
}
