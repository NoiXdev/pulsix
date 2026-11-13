package hosts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
)

// TestHostKindValidation (backlog 3.57): a host is a server or a client.
// Create without a kind makes a server; update without one keeps the
// stored kind; any other value is invalid input and writes nothing.
func TestHostKindValidation(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, err := svc.CreateZone(ctx, org, "fra", "Falkenstein")
	if err != nil {
		t.Fatal(err)
	}

	plain, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "srv", ZoneID: zone.ID})
	if err != nil || plain.Kind != hosts.KindServer {
		t.Fatalf("create without kind = %q, %v; want server", plain.Kind, err)
	}
	client, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "laptop", ZoneID: zone.ID, Kind: hosts.KindClient})
	if err != nil || client.Kind != hosts.KindClient {
		t.Fatalf("create client = %q, %v; want client", client.Kind, err)
	}

	for _, bad := range []hosts.Kind{"laptop", "Server", " client"} {
		if _, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "bad", ZoneID: zone.ID, Kind: bad}); !errors.Is(err, hosts.ErrInvalidInput) {
			t.Errorf("create kind %q = %v, want ErrInvalidInput", bad, err)
		}
		if _, err := svc.UpdateHost(ctx, org, client.ID, client.Revision, hosts.HostInput{Name: "laptop", ZoneID: zone.ID, Kind: bad}); !errors.Is(err, hosts.ErrInvalidInput) {
			t.Errorf("update kind %q = %v, want ErrInvalidInput", bad, err)
		}
	}
	if n := len(st.list); n != 2 {
		t.Fatalf("hosts stored = %d, want 2 (no bad kind written)", n)
	}

	kept, err := svc.UpdateHost(ctx, org, client.ID, client.Revision, hosts.HostInput{Name: "laptop", ZoneID: zone.ID})
	if err != nil || kept.Kind != hosts.KindClient {
		t.Fatalf("update without kind = %q, %v; want client kept", kept.Kind, err)
	}
	changed, err := svc.UpdateHost(ctx, org, client.ID, kept.Revision, hosts.HostInput{Name: "laptop", ZoneID: zone.ID, Kind: hosts.KindServer})
	if err != nil || changed.Kind != hosts.KindServer {
		t.Fatalf("update to server = %q, %v", changed.Kind, err)
	}
}

// fakeDirty records the hosts marked dirty; err fails every mark.
type fakeDirty struct {
	marked []uuid.UUID
	err    error
}

func (f *fakeDirty) MarkHostDirty(_ context.Context, _, hostID uuid.UUID) error {
	f.marked = append(f.marked, hostID)
	return f.err
}

// TestKindChangeMarksTheHostDirty: an update that changes the kind marks
// the host due for evaluation after the write; one that keeps it, or names
// no kind, does not; a failed update marks nothing; a failed mark does not
// fail the committed update.
func TestKindChangeMarksTheHostDirty(t *testing.T) {
	st := newFakeHostStore()
	dirty := &fakeDirty{}
	svc := hosts.NewService(st, time.Now, hosts.WithEvalDirty(dirty))
	org := uuid.Must(uuid.NewV7())
	zone, err := svc.CreateZone(ctx, org, "fra", "Falkenstein")
	if err != nil {
		t.Fatal(err)
	}
	h, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "laptop", ZoneID: zone.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty.marked) != 0 {
		t.Fatalf("create marked %v, want nothing", dirty.marked)
	}
	update := func(k hosts.Kind) error {
		t.Helper()
		cur, err := svc.GetHost(ctx, org, h.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = svc.UpdateHost(ctx, org, h.ID, cur.Revision, hosts.HostInput{Name: "laptop", ZoneID: zone.ID, Kind: k})
		return err
	}

	for _, k := range []hosts.Kind{"", hosts.KindServer} {
		if err := update(k); err != nil {
			t.Fatal(err)
		}
		if len(dirty.marked) != 0 {
			t.Fatalf("update with kind %q marked %v, want nothing", k, dirty.marked)
		}
	}
	if err := update(hosts.KindClient); err != nil {
		t.Fatal(err)
	}
	if len(dirty.marked) != 1 || dirty.marked[0] != h.ID {
		t.Fatalf("server→client marked %v, want the host once", dirty.marked)
	}

	// A stale revision writes nothing and marks nothing.
	if _, err := svc.UpdateHost(ctx, org, h.ID, 999, hosts.HostInput{Name: "laptop", ZoneID: zone.ID, Kind: hosts.KindServer}); !errors.Is(err, hosts.ErrRevisionMismatch) {
		t.Fatalf("stale update = %v", err)
	}
	if len(dirty.marked) != 1 {
		t.Fatalf("stale update marked %v", dirty.marked)
	}

	// The mark runs after the write committed: its failure is logged, and
	// the update still succeeds.
	dirty.err = errors.New("cursor unavailable")
	if err := update(hosts.KindServer); err != nil {
		t.Fatalf("update with a failing mark = %v, want success", err)
	}
	if got, _ := svc.GetHost(ctx, org, h.ID); got.Kind != hosts.KindServer || len(dirty.marked) != 2 {
		t.Fatalf("after a failed mark: kind %q, marks %v", got.Kind, dirty.marked)
	}
}
