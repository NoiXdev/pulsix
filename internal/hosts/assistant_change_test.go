package hosts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/hosts"
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

// TestAnAssistantsManagedHostIsHandedOn (decision D7): a managed host an
// assistant creates, or makes managed, falls into every run scope that
// selects it, so it is handed to the watcher with the host as its reach; an
// unmanaged host, a host already managed and a person's write are not.
func TestAnAssistantsManagedHostIsHandedOn(t *testing.T) {
	svc := hosts.NewService(newFakeHostStore(), time.Now)
	w := &recordingWatcher{}
	svc.SetChangeWatcher(w)
	org := uuid.Must(uuid.NewV7())
	zone, err := svc.CreateZone(ctx, org, "fra", "Falkenstein")
	if err != nil {
		t.Fatal(err)
	}
	assistant := assistantchange.Actor{UserID: uuid.Must(uuid.NewV7()), Assistant: true}
	person := assistantchange.Actor{UserID: uuid.Must(uuid.NewV7())}

	h, err := svc.CreateHostBy(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID, Managed: true}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	got := w.take()
	if len(got) != 1 || got[0].Subject != assistantchange.SubjectHost || got[0].ID != h.ID || got[0].Name != "web-01" ||
		got[0].Reach == nil || len(got[0].Reach.Hosts) != 1 || got[0].Reach.Hosts[0] != h.ID {
		t.Fatalf("created = %+v", got)
	}
	// The picture shows where and how runs reach it (final review N4).
	want := []string{"host", "address", "zone", "ssh_user", "ssh_port", "ssh_credential", "jump_host"}
	if len(got[0].Fields) != len(want) {
		t.Fatalf("created fields = %+v", got[0].Fields)
	}
	for i, name := range want {
		if got[0].Fields[i].Name != name {
			t.Fatalf("created fields = %+v, want %v", got[0].Fields, want)
		}
	}
	if got[0].Fields[1].New != "web-01" || got[0].Fields[2].New != "fra" || got[0].Fields[4].New != 22 || got[0].Fields[6].New != "inherit" {
		t.Fatalf("created route = %+v", got[0].Fields)
	}
	if _, err := svc.UpdateHostBy(ctx, org, h.ID, h.Revision, hosts.HostInput{Name: "web-01", ZoneID: zone.ID, Managed: true, Monitored: true}, assistant); err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 0 {
		t.Fatalf("an update of a managed host handed on %+v", got)
	}

	off, err := svc.CreateHostBy(ctx, org, hosts.HostInput{Name: "web-02", ZoneID: zone.ID}, assistant)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 0 {
		t.Fatalf("an unmanaged host handed on %+v", got)
	}
	if off, err = svc.UpdateHostBy(ctx, org, off.ID, off.Revision, hosts.HostInput{Name: "web-02", ZoneID: zone.ID, Managed: true}, assistant); err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 1 || got[0].Fields[0].Name != "managed" || len(got[0].Fields) != 7 {
		t.Fatalf("made managed by an update = %+v", got)
	}
	no, yes := false, true
	if _, err := svc.SetCapabilities(ctx, org, off.ID, &no, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetCapabilitiesBy(ctx, org, off.ID, &yes, nil, assistant); err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 1 || got[0].ID != off.ID {
		t.Fatalf("made managed by the capabilities = %+v", got)
	}
	if _, err := svc.CreateHostBy(ctx, org, hosts.HostInput{Name: "web-03", ZoneID: zone.ID, Managed: true}, person); err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 0 {
		t.Fatalf("a person's host handed on %+v", got)
	}
}

// TestAnAssistantsHostRouteChangeIsHandedOn (decision D7): an assistant
// moving a managed host to another zone, or changing its jump host, hands
// the host on; a resend of the same jump host, a rename and a person's
// zone change do not.
func TestAnAssistantsHostRouteChangeIsHandedOn(t *testing.T) {
	svc := hosts.NewService(newFakeHostStore(), time.Now)
	w := &recordingWatcher{}
	svc.SetChangeWatcher(w)
	org := uuid.Must(uuid.NewV7())
	fra, err := svc.CreateZone(ctx, org, "fra", "Falkenstein")
	if err != nil {
		t.Fatal(err)
	}
	nbg, err := svc.CreateZone(ctx, org, "nbg", "Nuremberg")
	if err != nil {
		t.Fatal(err)
	}
	assistant := assistantchange.Actor{UserID: uuid.Must(uuid.NewV7()), Assistant: true}
	h, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", Address: "10.0.0.1", ZoneID: fra.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	if h, err = svc.UpdateHostBy(ctx, org, h.ID, h.Revision, hosts.HostInput{Name: "web-01", Address: "10.0.0.1", ZoneID: nbg.ID, Managed: true}, assistant); err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 1 || len(got[0].Fields) != 1 || got[0].Fields[0].Name != "zone" || got[0].Fields[0].Old != "fra" || got[0].Fields[0].New != "nbg" {
		t.Fatalf("zone change = %+v", got)
	}
	bastion := uuid.Must(uuid.NewV7())
	jump := &hosts.Jump{Mode: "explicit", BastionID: &bastion}
	if h, err = svc.UpdateHostBy(ctx, org, h.ID, h.Revision, hosts.HostInput{Name: "web-01", Address: "10.0.0.1", ZoneID: nbg.ID, Managed: true, Jump: jump}, assistant); err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 1 || got[0].Fields[0].Name != "jump_host" || got[0].Fields[0].Old != "inherit" {
		t.Fatalf("jump host change = %+v", got)
	}
	if h, err = svc.UpdateHostBy(ctx, org, h.ID, h.Revision, hosts.HostInput{Name: "web-01", Address: "10.0.0.1", ZoneID: nbg.ID, Managed: true, Monitored: true, Jump: jump}, assistant); err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 0 {
		t.Fatalf("an update with the same zone and jump host handed on %+v", got)
	}
	// A managed host with an address may be renamed: inventory_hostname
	// changes, so it is handed on (controller's ruling N1).
	if h, err = svc.UpdateHostBy(ctx, org, h.ID, h.Revision, hosts.HostInput{Name: "web-1", Address: "10.0.0.1", ZoneID: nbg.ID, Managed: true, Jump: jump}, assistant); err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 1 || got[0].Fields[0].Name != "name" || got[0].Fields[0].Old != "web-01" || got[0].Fields[0].New != "web-1" {
		t.Fatalf("a rename = %+v", got)
	}
	if _, err = svc.UpdateHost(ctx, org, h.ID, h.Revision, hosts.HostInput{Name: "web-1", Address: "10.0.0.1", ZoneID: fra.ID, Managed: true}); err != nil {
		t.Fatal(err)
	}
	if got := w.take(); len(got) != 0 {
		t.Fatalf("a person's zone change handed on %+v", got)
	}
}

// TestAnAssistantCannotMoveAManagedHostsConnection (controller's ruling
// N1): an assistant may not change where a managed host is reached — its
// address, or its name while it has no address — whether or not a watcher
// is wired; an unmanaged host and a person may.
func TestAnAssistantCannotMoveAManagedHostsConnection(t *testing.T) {
	svc := hosts.NewService(newFakeHostStore(), time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, err := svc.CreateZone(ctx, org, "fra", "Falkenstein")
	if err != nil {
		t.Fatal(err)
	}
	assistant := assistantchange.Actor{UserID: uuid.Must(uuid.NewV7()), Assistant: true}
	withAddr, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", Address: "10.0.0.1", ZoneID: zone.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	noAddr, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-02", ZoneID: zone.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	off, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-03", Address: "10.0.0.3", ZoneID: zone.ID})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		h  hosts.Host
		in hosts.HostInput
	}{
		"another address":         {withAddr, hosts.HostInput{Name: "web-01", Address: "10.0.0.9", ZoneID: zone.ID, Managed: true}},
		"address cleared":         {withAddr, hosts.HostInput{Name: "web-01", ZoneID: zone.ID, Managed: true}},
		"an address set":          {noAddr, hosts.HostInput{Name: "web-02", Address: "10.0.0.2", ZoneID: zone.ID, Managed: true}},
		"renamed without address": {noAddr, hosts.HostInput{Name: "web-22", ZoneID: zone.ID, Managed: true}},
	} {
		if _, err := svc.UpdateHostBy(ctx, org, tc.h.ID, tc.h.Revision, tc.in, assistant); !errors.Is(err, hosts.ErrAssistantConnection) {
			t.Errorf("%s: err = %v, want ErrAssistantConnection", name, err)
		}
	}
	if _, err := svc.UpdateHostBy(ctx, org, off.ID, off.Revision, hosts.HostInput{Name: "web-03", Address: "10.0.0.33", ZoneID: zone.ID}, assistant); err != nil {
		t.Errorf("an unmanaged host's address: %v", err)
	}
	if _, err := svc.UpdateHost(ctx, org, withAddr.ID, withAddr.Revision, hosts.HostInput{Name: "web-01", Address: "10.0.0.9", ZoneID: zone.ID, Managed: true}); err != nil {
		t.Errorf("a person's address change: %v", err)
	}
}
