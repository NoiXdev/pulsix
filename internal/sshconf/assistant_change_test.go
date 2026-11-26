package sshconf_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/sshconf"
)

type recordingWatcher struct{ changes []assistantchange.Change }

func (w *recordingWatcher) Changed(_ context.Context, _ uuid.UUID, c assistantchange.Change) error {
	w.changes = append(w.changes, c)
	return nil
}

func (w *recordingWatcher) take() []assistantchange.Change {
	out := w.changes
	w.changes = nil
	return out
}

// TestAnAssistantsRouteChangeIsHandedOn (decision D7): an assistant's
// change of a host's SSH user, port, credential or jump host, and of a
// zone's default jump host, is handed on with what moved and the hosts it
// reaches; an unchanged resend and a person's change are not.
func TestAnAssistantsRouteChangeIsHandedOn(t *testing.T) {
	org, zone, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	key := uuid.Must(uuid.NewV7())
	web, db, off := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store := newFakeStore()
	store.zones[zone] = zoneRow{org: org}
	store.zones[other] = zoneRow{org: org}
	hs := fakeHosts{
		web: {ID: web, OrgID: org, ZoneID: zone, Name: "web-01", Managed: true},
		db:  {ID: db, OrgID: org, ZoneID: other, Name: "db-01", Managed: true},
		off: {ID: off, OrgID: org, ZoneID: zone, Name: "off-01"},
	}
	svc := sshconf.NewService(store, fakeCreds{key: {ID: key, OrgID: org, Name: "deploy-key", Kind: credentials.KindSSHKey}}, hs)
	w := &recordingWatcher{}
	svc.SetChangeWatcher(w)
	assistant := assistantchange.Actor{UserID: uuid.Must(uuid.NewV7()), Assistant: true}
	b, err := svc.CreateBastion(ctx, org, sshconf.BastionInput{Name: "jump", Address: "10.0.0.2", Port: 22, Username: "ops"})
	if err != nil {
		t.Fatal(err)
	}

	in := sshconf.HostSSHInput{Username: "deploy", Port: 2222, CredentialID: &key, BastionMode: sshconf.ModeExplicit, BastionID: &b.ID}
	if _, err := svc.SetHostSSHBy(ctx, org, web, in, assistant); err != nil {
		t.Fatal(err)
	}
	got := w.take()
	if len(got) != 1 || got[0].Subject != assistantchange.SubjectHost || got[0].ID != web || got[0].Name != "web-01" ||
		got[0].Reach == nil || len(got[0].Reach.Hosts) != 1 || got[0].Reach.Hosts[0] != web {
		t.Fatalf("host SSH change = %+v", got)
	}
	names := []string{}
	for _, f := range got[0].Fields {
		names = append(names, f.Name)
	}
	if len(names) != 4 || names[0] != "ssh_user" || names[1] != "ssh_port" || names[2] != "ssh_credential" || names[3] != "jump_host" ||
		got[0].Fields[2].New != "deploy-key" || got[0].Fields[3].New != "explicit jump" {
		t.Fatalf("fields = %+v", got[0].Fields)
	}
	if _, err := svc.SetHostSSHBy(ctx, org, web, in, assistant); err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 0 {
		t.Fatalf("an unchanged resend handed on %+v", got)
	}
	in.Port = 22
	if _, err := svc.SetHostSSH(ctx, org, web, in); err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 0 {
		t.Fatalf("a person's change handed on %+v", got)
	}

	if err := svc.SetZoneBastionBy(ctx, org, zone, &b.ID, assistant); err != nil {
		t.Fatal(err)
	}
	got = w.take()
	if len(got) != 1 || got[0].Subject != assistantchange.SubjectZone || got[0].ID != zone || got[0].Reach == nil ||
		len(got[0].Reach.Hosts) != 1 || got[0].Reach.Hosts[0] != web || got[0].Fields[0].New != "jump" {
		t.Fatalf("zone jump host change = %+v", got)
	}
	if err := svc.SetZoneBastionBy(ctx, org, zone, &b.ID, assistant); err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 0 {
		t.Fatalf("an unchanged zone jump host handed on %+v", got)
	}
}

// TestAnAssistantsPasswordTestNeedsAPinnedKey (controller's ruling N1): an
// assistant's connection test is refused while the host's credential is a
// password and its key is not pinned; a pinned key, a key credential and
// no credential pass.
func TestAnAssistantsPasswordTestNeedsAPinnedKey(t *testing.T) {
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	pw, key := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := sshconf.NewService(store, fakeCreds{
		pw:  {ID: pw, OrgID: org, Name: "root-pw", Kind: credentials.KindSSHPassword},
		key: {ID: key, OrgID: org, Name: "deploy-key", Kind: credentials.KindSSHKey},
	}, fakeHosts{host: {ID: host, OrgID: org, Name: "web-01", Managed: true}})
	if err := svc.CheckAssistantConnectionTest(ctx, org, host); err != nil {
		t.Fatalf("no credential: %v", err)
	}
	store.hostSSH[host] = sshconf.HostSSH{HostID: host, OrgID: org, Port: 22, CredentialID: &pw, BastionMode: sshconf.ModeInherit}
	if err := svc.CheckAssistantConnectionTest(ctx, org, host); !errors.Is(err, sshconf.ErrAssistantPasswordTest) {
		t.Fatalf("password, unpinned: %v", err)
	}
	pinned := store.hostSSH[host]
	pinned.KeyPinned = true
	store.hostSSH[host] = pinned
	if err := svc.CheckAssistantConnectionTest(ctx, org, host); err != nil {
		t.Fatalf("password, pinned: %v", err)
	}
	store.hostSSH[host] = sshconf.HostSSH{HostID: host, OrgID: org, Port: 22, CredentialID: &key, BastionMode: sshconf.ModeInherit}
	if err := svc.CheckAssistantConnectionTest(ctx, org, host); err != nil {
		t.Fatalf("key, unpinned: %v", err)
	}
}
