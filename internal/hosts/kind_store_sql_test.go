package hosts_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
)

// TestHostKindRoundTrip (backlog 3.57): a host is a server unless it is
// created as a client; an update without a kind keeps the stored one, an
// update with one changes it under the revision, and a stale revision
// changes nothing.
func TestHostKindRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostkind-roundtrip-org")
	zone := seedZone(t, store, org, "fra")

	server, err := store.CreateHostWith(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "srv", Facts: map[string]any{}}, hosts.HostExtras{})
	if err != nil {
		t.Fatal(err)
	}
	if server.Kind != hosts.KindServer {
		t.Fatalf("created without a kind = %q, want server", server.Kind)
	}

	client, err := store.CreateHostWith(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "laptop", Kind: hosts.KindClient, Facts: map[string]any{}}, hosts.HostExtras{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetHost(ctx, org, client.ID); err != nil || got.Kind != hosts.KindClient {
		t.Fatalf("GetHost = %q, %v; want client", got.Kind, err)
	}

	// No kind: kept.
	up := client
	up.Kind = ""
	up.Address = "10.0.0.5"
	got, err := store.UpdateHostWith(ctx, up, hosts.HostExtras{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != hosts.KindClient || got.Revision != client.Revision+1 {
		t.Fatalf("update without kind = %q rev %d, want client rev %d", got.Kind, got.Revision, client.Revision+1)
	}

	// A kind: changed.
	up = got
	up.Kind = hosts.KindServer
	got, err = store.UpdateHostWith(ctx, up, hosts.HostExtras{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != hosts.KindServer {
		t.Fatalf("update to server = %q", got.Kind)
	}

	// A stale revision changes nothing.
	stale := got
	stale.Revision = got.Revision - 1
	stale.Kind = hosts.KindClient
	if _, err := store.UpdateHostWith(ctx, stale, hosts.HostExtras{}); !errors.Is(err, hosts.ErrRevisionMismatch) {
		t.Fatalf("stale update = %v, want ErrRevisionMismatch", err)
	}
	if after, _ := store.GetHost(ctx, org, client.ID); after.Kind != hosts.KindServer {
		t.Fatalf("kind after a stale update = %q, want server", after.Kind)
	}

	// Lists carry the kind too.
	list, _, err := store.ListHosts(ctx, org, hosts.ListFilter{}, "", 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListHosts = %+v, %v", list, err)
	}
	for _, h := range list {
		if h.Kind != hosts.KindServer {
			t.Fatalf("listed %s kind %q, want server", h.Name, h.Kind)
		}
	}
}

// TestHostKindCheckConstraint: the database refuses a kind it does not
// know, whoever writes it.
func TestHostKindCheckConstraint(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostkind-check-org")
	zone := seedZone(t, store, org, "fra")
	h, err := store.CreateHostWith(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "srv", Facts: map[string]any{}}, hosts.HostExtras{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE hosts SET kind = 'laptop' WHERE id = $1`, h.ID); err == nil {
		t.Fatal("kind 'laptop' was accepted, want a CHECK violation")
	}
	bad := h
	bad.Kind = hosts.Kind("laptop")
	if _, err := store.UpdateHostWith(ctx, bad, hosts.HostExtras{}); err == nil {
		t.Fatal("store update with kind 'laptop' succeeded, want an error")
	}
}
