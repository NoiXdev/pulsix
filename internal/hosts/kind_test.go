package hosts_test

import (
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
