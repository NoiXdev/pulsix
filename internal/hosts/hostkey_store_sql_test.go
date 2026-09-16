// SQL integration tests for the host-key pin and connection-test store
// methods (hostkeys.sql queries), against the real database. The org
// isolation of host_connection_tests is proven in internal/db
// (TestHostConnectionTestsAreIsolatedPerOrg); these tests cover the query
// behavior the service and the queue depend on.
package hosts_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
)

func TestSQLStoreSetHostKeyPinCreatesRowWithDefaults(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostkey-pin-org")
	zone := uuid.Must(uuid.NewV7())
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zone, OrgID: org, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	hostID := uuid.Must(uuid.NewV7())
	if _, err := q.CreateHost(ctx, gen.CreateHostParams{ID: hostID, OrgID: org, ZoneID: zone, Name: "web1", Managed: true, Facts: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	// No host_ssh row: pinning creates one with the defaults and the key.
	fingerprint := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := store.SetHostKeyPin(ctx, org, hostID, fingerprint, ""); err != nil {
		t.Fatalf("SetHostKeyPin: %v", err)
	}
	sshRow, err := q.GetHostSSH(ctx, gen.GetHostSSHParams{OrgID: org, HostID: hostID})
	if err != nil {
		t.Fatalf("host_ssh row missing after pin: %v", err)
	}
	if sshRow.Port != 22 || sshRow.Username != "" || sshRow.BastionMode != "inherit" || sshRow.SshHostKey == nil || *sshRow.SshHostKey != fingerprint {
		t.Fatalf("created host_ssh row = %+v", sshRow)
	}
}

func TestSQLStoreSetHostKeyPinKeepsExistingSettings(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostkey-pin-keep-org")
	zone := uuid.Must(uuid.NewV7())
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zone, OrgID: org, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	hostID := uuid.Must(uuid.NewV7())
	if _, err := q.CreateHost(ctx, gen.CreateHostParams{ID: hostID, OrgID: org, ZoneID: zone, Name: "web1", Managed: true, Facts: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	// Existing SSH settings survive the pin.
	if _, err := q.UpsertHostSSH(ctx, gen.UpsertHostSSHParams{
		HostID: hostID, OrgID: org, Username: "deploy", Port: 2222, BastionMode: "inherit",
	}); err != nil {
		t.Fatal(err)
	}
	fingerprint := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := store.SetHostKeyPin(ctx, org, hostID, fingerprint, ""); err != nil {
		t.Fatal(err)
	}
	sshRow, err := q.GetHostSSH(ctx, gen.GetHostSSHParams{OrgID: org, HostID: hostID})
	if err != nil {
		t.Fatal(err)
	}
	if sshRow.Username != "deploy" || sshRow.Port != 2222 {
		t.Fatalf("pin clobbered the SSH settings: %+v", sshRow)
	}
	if sshRow.SshHostKey == nil || *sshRow.SshHostKey != fingerprint {
		t.Fatalf("pin missing: %+v", sshRow)
	}
}

func TestSQLStoreSetHostKeyPinForeignHostFails(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	orgA := seedOrg(t, q, "hostkey-pin-fk-a")
	orgB := seedOrg(t, q, "hostkey-pin-fk-b")
	zone := uuid.Must(uuid.NewV7())
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zone, OrgID: orgA, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	hostID := uuid.Must(uuid.NewV7())
	if _, err := q.CreateHost(ctx, gen.CreateHostParams{ID: hostID, OrgID: orgA, ZoneID: zone, Name: "web1", Managed: true, Facts: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHostKeyPin(ctx, orgB, hostID, "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ""); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("pinning a foreign host = %v, want ErrNotFound", err)
	}
	if err := store.SetHostKeyPin(ctx, orgA, uuid.Must(uuid.NewV7()), "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ""); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("pinning an unknown host = %v, want ErrNotFound", err)
	}
}

func TestSQLStoreClearAndListPins(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostkey-clear-org")
	zone := uuid.Must(uuid.NewV7())
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zone, OrgID: org, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	hostA, hostB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, h := range []struct {
		id   uuid.UUID
		name string
	}{{hostA, "a"}, {hostB, "b"}} {
		if _, err := q.CreateHost(ctx, gen.CreateHostParams{ID: h.id, OrgID: org, ZoneID: zone, Name: h.name, Managed: true, Facts: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	pinA := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	pinB := "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	if err := store.SetHostKeyPin(ctx, org, hostA, pinA, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHostKeyPin(ctx, org, hostB, pinB, ""); err != nil {
		t.Fatal(err)
	}
	keys, err := store.ListPinnedKeys(ctx, org, []uuid.UUID{hostA, hostB})
	if err != nil || len(keys) != 2 || keys[hostA].Fingerprint != pinA || keys[hostB].Fingerprint != pinB {
		t.Fatalf("ListPinnedKeys = %v, %v", keys, err)
	}
	// Other orgs see none.
	if keys, err := store.ListPinnedKeys(ctx, seedOrg(t, q, "hostkey-clear-org-2"), []uuid.UUID{hostA}); err != nil || len(keys) != 0 {
		t.Fatalf("cross-org pins = %v, %v", keys, err)
	}
	// Clear one; it disappears, the other stays.
	if err := store.ClearHostKeyPin(ctx, org, hostA); err != nil {
		t.Fatal(err)
	}
	if keys, err := store.ListPinnedKeys(ctx, org, []uuid.UUID{hostA, hostB}); err != nil || len(keys) != 1 || keys[hostB].Fingerprint != pinB {
		t.Fatalf("after clear: %v, %v", keys, err)
	}
	// Clearing a host without a host_ssh row is a no-op.
	if err := store.ClearHostKeyPin(ctx, org, uuid.Must(uuid.NewV7())); err != nil {
		t.Fatalf("clear on host without ssh settings: %v", err)
	}
}

func TestSQLStoreConnectionTestHistory(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostkey-history-org")
	zone := uuid.Must(uuid.NewV7())
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zone, OrgID: org, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	hostID := uuid.Must(uuid.NewV7())
	if _, err := q.CreateHost(ctx, gen.CreateHostParams{ID: hostID, OrgID: org, ZoneID: zone, Name: "web1", Managed: true, Facts: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	// No rows yet.
	if _, err := store.LatestHostTest(ctx, org, hostID); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("LatestHostTest on empty history = %v, want ErrNotFound", err)
	}
	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-time.Hour)
	first := hosts.HostTest{ID: uuid.Must(uuid.NewV7()), OrgID: org, HostID: hostID, At: older, OK: true,
		HostKey: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", HostKeyState: hosts.KeyStateUnpinned}
	second := hosts.HostTest{ID: uuid.Must(uuid.NewV7()), OrgID: org, HostID: hostID, At: newer, OK: false,
		HostKey: "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", HostKeyState: hosts.KeyStateChanged,
		Checks: []hosts.HostTestCheck{{Name: "reachability", Detail: "connected", OK: true}, {Name: "sudo", Detail: "ok", OK: true}}}
	if err := store.CreateHostTest(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateHostTest(ctx, second); err != nil {
		t.Fatal(err)
	}
	latest, err := store.LatestHostTest(ctx, org, hostID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != second.ID || latest.OK || latest.HostKeyState != hosts.KeyStateChanged {
		t.Fatalf("latest = %+v, want the newest row", latest)
	}
	if len(latest.Checks) != 2 || latest.Checks[0].Name != "reachability" {
		t.Fatalf("checks round trip = %+v", latest.Checks)
	}
	// A different org's host is invisible.
	if _, err := store.LatestHostTest(ctx, seedOrg(t, q, "hostkey-history-org-2"), hostID); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("cross-org latest = %v, want ErrNotFound", err)
	}
}

func TestSQLStoreListHostKeyChanged(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostkey-changed-org")
	zone := uuid.Must(uuid.NewV7())
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zone, OrgID: org, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	hostA, hostB, hostC, hostD := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	names := map[uuid.UUID]string{hostA: "a", hostB: "b", hostC: "c", hostD: "d"}
	for id, name := range names {
		if _, err := q.CreateHost(ctx, gen.CreateHostParams{ID: id, OrgID: org, ZoneID: zone, Name: name, Managed: true, Facts: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	row := func(host uuid.UUID, state, key string) hosts.HostTest {
		return hosts.HostTest{ID: uuid.Must(uuid.NewV7()), OrgID: org, HostID: host, At: now, OK: false, HostKey: key, HostKeyState: state}
	}
	keyA := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	keyC := "SHA256:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	// hostA: latest state changed → listed.
	if err := store.CreateHostTest(ctx, hosts.HostTest{ID: uuid.Must(uuid.NewV7()), OrgID: org, HostID: hostA, At: now.Add(-2 * time.Hour), OK: true, HostKeyState: hosts.KeyStatePinned, HostKey: keyA}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateHostTest(ctx, row(hostA, hosts.KeyStateChanged, keyC)); err != nil {
		t.Fatal(err)
	}
	// hostB: never tested → not listed.
	// hostC: an older changed row, but the LATEST is pinned → not listed.
	if err := store.CreateHostTest(ctx, row(hostC, hosts.KeyStateChanged, keyC)); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateHostTest(ctx, hosts.HostTest{ID: uuid.Must(uuid.NewV7()), OrgID: org, HostID: hostC, At: now.Add(time.Hour), OK: true, HostKeyState: hosts.KeyStatePinned, HostKey: keyC}); err != nil {
		t.Fatal(err)
	}
	// hostD: latest is unpinned with a DIFFERENT key than the stored pin —
	// the pin comparison also blocks it (defense in depth).
	pinD := "SHA256:DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
	if err := store.SetHostKeyPin(ctx, org, hostD, pinD, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateHostTest(ctx, row(hostD, hosts.KeyStateUnpinned, keyC)); err != nil {
		t.Fatal(err)
	}

	changed, err := store.ListChangedKeys(ctx, org, []uuid.UUID{hostA, hostB, hostC, hostD})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 2 {
		t.Fatalf("changed = %v, want exactly hostA and hostD", changed)
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range changed {
		seen[id] = true
	}
	if !seen[hostA] || !seen[hostD] {
		t.Fatalf("changed = %v, want hostA and hostD", changed)
	}
}

func TestSQLStoreListHostsDueForTest(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostkey-due-org")
	zone := uuid.Must(uuid.NewV7())
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zone, OrgID: org, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	hostA, hostB, hostC, hostD := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rows := []struct {
		id      uuid.UUID
		name    string
		managed bool
	}{
		{hostA, "a", true}, {hostB, "b", true}, {hostC, "c", true}, {hostD, "d", false},
	}
	for _, r := range rows {
		if _, err := q.CreateHost(ctx, gen.CreateHostParams{ID: r.id, OrgID: org, ZoneID: zone, Name: r.name, Managed: r.managed, Facts: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	// hostA: tested 2 h ago (within the window) → not due.
	if err := store.CreateHostTest(ctx, hosts.HostTest{ID: uuid.Must(uuid.NewV7()), OrgID: org, HostID: hostA, At: now.Add(-1 * time.Hour), OK: true, HostKeyState: hosts.KeyStateUnpinned}); err != nil {
		t.Fatal(err)
	}
	// hostB: tested 25 h ago (outside the window) → due.
	if err := store.CreateHostTest(ctx, hosts.HostTest{ID: uuid.Must(uuid.NewV7()), OrgID: org, HostID: hostB, At: now.Add(-25 * time.Hour), OK: true, HostKeyState: hosts.KeyStateUnpinned}); err != nil {
		t.Fatal(err)
	}
	// hostC: an active queued connection_test job → not due.
	if _, err := q.CreateJob(ctx, gen.CreateJobParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: "connection_test", Status: "queued", RunGroupID: uuid.Must(uuid.NewV7()), HostIds: []uuid.UUID{hostC}, Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	due, err := store.ListHostsDueForTest(ctx, org, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != hostB {
		t.Fatalf("due = %+v, want exactly hostB", due)
	}
	// A queued job for hostB (e.g. re-enqueued between passes) removes it.
	if _, err := q.CreateJob(ctx, gen.CreateJobParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: "connection_test", Status: "queued", RunGroupID: uuid.Must(uuid.NewV7()), HostIds: []uuid.UUID{hostB}, Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	due, err = store.ListHostsDueForTest(ctx, org, now.Add(-24*time.Hour))
	if err != nil || len(due) != 0 {
		t.Fatalf("due after re-queue = %+v, %v — no duplicates allowed", due, err)
	}
}

// TestSQLStorePinPublicKeyAndBackfill: the pin stores the full key with
// the fingerprint; a fingerprint-only pin is completed only by the key the
// pin names, never overwritten, and clearing the pin clears both.
func TestSQLStorePinPublicKeyAndBackfill(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hostkey-pubkey-org")
	zone := uuid.Must(uuid.NewV7())
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zone, OrgID: org, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	hostID := uuid.Must(uuid.NewV7())
	if _, err := q.CreateHost(ctx, gen.CreateHostParams{ID: hostID, OrgID: org, ZoneID: zone, Name: "web1", Managed: true, Facts: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	pinFP := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := store.SetHostKeyPin(ctx, org, hostID, pinFP, ""); err != nil {
		t.Fatal(err)
	}
	read := func() hosts.HostKeyPin {
		t.Helper()
		keys, err := store.ListPinnedKeys(ctx, org, []uuid.UUID{hostID})
		if err != nil {
			t.Fatal(err)
		}
		return keys[hostID]
	}
	// A key for another fingerprint does not complete the pin.
	if err := store.BackfillHostKeyPin(ctx, org, hostID, "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", "ssh-ed25519 OTHER"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.PublicKey != "" {
		t.Fatalf("a foreign fingerprint filled the pin: %+v", got)
	}
	// The pin's own fingerprint does, once.
	if err := store.BackfillHostKeyPin(ctx, org, hostID, pinFP, "ssh-ed25519 PINNED"); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillHostKeyPin(ctx, org, hostID, pinFP, "ssh-ed25519 LATER"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.Fingerprint != pinFP || got.PublicKey != "ssh-ed25519 PINNED" {
		t.Fatalf("after backfill: %+v", got)
	}
	// Re-pinning stores both; clearing clears both.
	if err := store.SetHostKeyPin(ctx, org, hostID, "SHA256:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", "ssh-ed25519 NEW"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.PublicKey != "ssh-ed25519 NEW" {
		t.Fatalf("after re-pin: %+v", got)
	}
	if err := store.ClearHostKeyPin(ctx, org, hostID); err != nil {
		t.Fatal(err)
	}
	var fp, pub *string
	if err := d.Pool().QueryRow(ctx, `SELECT ssh_host_key, ssh_host_public_key FROM host_ssh WHERE host_id = $1`, hostID).Scan(&fp, &pub); err != nil {
		t.Fatal(err)
	}
	if fp != nil || pub != nil {
		t.Fatalf("cleared pin still has fingerprint %v / key %v", fp, pub)
	}
	// The test row keeps the observed key.
	if err := store.CreateHostTest(ctx, hosts.HostTest{ID: uuid.Must(uuid.NewV7()), OrgID: org, HostID: hostID, At: time.Now(), OK: true,
		HostKey: pinFP, HostPublicKey: "ssh-ed25519 SEEN", HostKeyState: hosts.KeyStatePinned}); err != nil {
		t.Fatal(err)
	}
	if latest, err := store.LatestHostTest(ctx, org, hostID); err != nil || latest.HostPublicKey != "ssh-ed25519 SEEN" {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
}
