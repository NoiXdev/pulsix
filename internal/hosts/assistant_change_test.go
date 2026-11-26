package hosts_test

import (
	"context"
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
	if _, err := svc.UpdateHostBy(ctx, org, h.ID, h.Revision, hosts.HostInput{Name: "web-01", Address: "10.0.0.2", ZoneID: zone.ID, Managed: true}, assistant); err != nil {
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
	if got := w.take(); len(got) != 1 || got[0].Fields[0].Name != "managed" {
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
