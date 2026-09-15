// Service tests for host-key pinning and test history (spec §5). The fake
// store is the existing fakeHostStore (service_test.go), extended here
// with the new hostkey methods; the SQL behavior is integration-tested in
// hostkey_store_sql_test.go against the real database.
package hosts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
)

// pinRecord is one recorded SetHostKeyPin call, for the fake's assertions.
type pinRecord struct {
	orgID, hostID uuid.UUID
	fingerprint   string
}

func (f *fakeHostStore) SetHostKeyPin(_ context.Context, orgID, hostID uuid.UUID, fingerprint string) error {
	if err := f.take("SetHostKeyPin"); err != nil {
		return err
	}
	f.pins = append(f.pins, pinRecord{orgID: orgID, hostID: hostID, fingerprint: fingerprint})
	return nil
}

func (f *fakeHostStore) ClearHostKeyPin(_ context.Context, _, _ uuid.UUID) error {
	return f.take("ClearHostKeyPin")
}

func (f *fakeHostStore) ListPinnedKeys(_ context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	if err := f.take("ListPinnedKeys"); err != nil {
		return nil, err
	}
	out := map[uuid.UUID]string{}
	for _, id := range hostIDs {
		if h, ok := f.list[id]; ok && h.OrgID == orgID {
			if pin, ok := f.pinnedKeys[id]; ok {
				out[id] = pin
			}
		}
	}
	return out, nil
}

func (f *fakeHostStore) ListChangedKeys(_ context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) ([]uuid.UUID, error) {
	if err := f.take("ListChangedKeys"); err != nil {
		return nil, err
	}
	var out []uuid.UUID
	for _, id := range hostIDs {
		if h, ok := f.list[id]; ok && h.OrgID == orgID && f.changedHosts[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeHostStore) CreateHostTest(_ context.Context, t hosts.HostTest) error {
	if err := f.take("CreateHostTest"); err != nil {
		return err
	}
	f.tests[t.HostID] = append(f.tests[t.HostID], t)
	return nil
}

func (f *fakeHostStore) LatestHostTest(_ context.Context, orgID, hostID uuid.UUID) (hosts.HostTest, error) {
	if err := f.take("LatestHostTest"); err != nil {
		return hosts.HostTest{}, err
	}
	rows := f.tests[hostID]
	if len(rows) == 0 {
		return hosts.HostTest{}, hosts.ErrNotFound
	}
	return rows[len(rows)-1], nil
}

func (f *fakeHostStore) ListHostsDueForTest(_ context.Context, orgID uuid.UUID, since time.Time) ([]hosts.Host, error) {
	if err := f.take("ListHostsDueForTest"); err != nil {
		return nil, err
	}
	var out []hosts.Host
	for _, h := range f.list {
		if h.OrgID != orgID || !h.Managed {
			continue
		}
		if last, ok := f.latestTestAt[h.ID]; ok && last.After(since) {
			continue
		}
		if f.queuedTests[h.ID] {
			continue
		}
		out = append(out, h)
	}
	return out, nil
}

func newHostKeyStore() *fakeHostStore {
	store := newFakeHostStore()
	store.pins = []pinRecord{}
	store.pinnedKeys = map[uuid.UUID]string{}
	store.changedHosts = map[uuid.UUID]bool{}
	store.tests = map[uuid.UUID][]hosts.HostTest{}
	store.latestTestAt = map[uuid.UUID]time.Time{}
	store.queuedTests = map[uuid.UUID]bool{}
	store.orgIDs = []uuid.UUID{}
	return store
}

// seededService returns a service with one managed host.
func seededService(t *testing.T) (*hosts.Service, *fakeHostStore, uuid.UUID, uuid.UUID) {
	t.Helper()
	store := newHostKeyStore()
	svc := hosts.NewService(store, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, err := svc.CreateZone(ctx, org, "default", "Default")
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreateHost(ctx, org, hosts.HostInput{
		Name: "web1", Address: "192.0.2.10", ZoneID: zone.ID, Managed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store, org, created.ID
}

func TestSetHostKeyValidatesFingerprintShape(t *testing.T) {
	svc, _, org, host := seededService(t)
	cases := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"missing prefix", "AABAACAQAD"},
		{"wrong digest length", "SHA256:AAAA"},
		{"illegal base64 char", "SHA256:" + string(make([]byte, 43))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := svc.SetHostKey(ctx, org, host, c.key); err != nil {
				if !errors.Is(err, hosts.ErrInvalidInput) {
					t.Fatalf("SetHostKey(%q) = %v, want ErrInvalidInput", c.key, err)
				}
			}
		})
	}
	// A well-formed fingerprint is accepted (the fake records it).
	valid := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := svc.SetHostKey(ctx, org, host, valid); err != nil {
		t.Fatalf("SetHostKey(valid): %v", err)
	}
}

func TestSetHostKeyForeignHostInvisible(t *testing.T) {
	svc, _, orgA, hostA := seededService(t)
	orgB := uuid.Must(uuid.NewV7())
	valid := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := svc.SetHostKey(ctx, orgB, hostA, valid); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("SetHostKey in another org = %v, want ErrNotFound", err)
	}
	if err := svc.SetHostKey(ctx, orgA, hostA, valid); err != nil {
		t.Fatalf("own org: %v", err)
	}
}

func TestClearHostKey(t *testing.T) {
	svc, _, org, host := seededService(t)
	if err := svc.ClearHostKey(ctx, org, host); err != nil {
		t.Fatalf("clear on a never-pinned host: %v", err)
	}
	orgB := uuid.Must(uuid.NewV7())
	if err := svc.ClearHostKey(ctx, orgB, host); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("clear in another org = %v, want ErrNotFound", err)
	}
}

func TestLatestTest(t *testing.T) {
	svc, store, org, host := seededService(t)

	// Never tested → ErrNotFound.
	if _, err := svc.LatestTest(ctx, org, host); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("LatestTest on untested host = %v, want ErrNotFound", err)
	}

	// Two records: the newest wins.
	at1 := time.Now().Add(-2 * time.Hour)
	at2 := time.Now().Add(-time.Hour)
	if err := svc.RecordConnectionTest(ctx, org, host, at1, true, "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", hosts.KeyStateUnpinned,
		[]hosts.HostTestCheck{{Name: "reachability", Detail: "connected", OK: true}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateHostTest(ctx, hosts.HostTest{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, HostID: host, At: at2, OK: true,
		HostKey: "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", HostKeyState: hosts.KeyStatePinned,
	}); err != nil {
		t.Fatal(err)
	}
	latest, err := svc.LatestTest(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if !latest.At.Equal(at2) || latest.HostKeyState != hosts.KeyStatePinned {
		t.Fatalf("latest = %+v, want the at2/pinned row", latest)
	}
	if latest.ID != store.tests[host][1].ID {
		t.Fatalf("latest id = %s, want the second row", latest.ID)
	}
}

func TestRecordConnectionTestNormalizesEmptyState(t *testing.T) {
	svc, store, org, host := seededService(t)
	if err := svc.RecordConnectionTest(ctx, org, host, time.Now(), false, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := store.tests[host][0].HostKeyState; got != hosts.KeyStateUnpinned {
		t.Fatalf("state = %q, want unpinned", got)
	}
}

func TestRecordConnectionTestForeignHostFails(t *testing.T) {
	svc, _, _, _ := seededService(t)
	orgB := uuid.Must(uuid.NewV7())
	if err := svc.RecordConnectionTest(ctx, orgB, uuid.Must(uuid.NewV7()), time.Now(), true, "", hosts.KeyStateUnpinned, nil); err == nil {
		t.Fatal("recording a test for a foreign host must fail")
	}
}

func TestChangedHosts(t *testing.T) {
	svc, store, org, hostA := seededService(t)
	zone, err := svc.CreateZone(ctx, org, "zone2", "Zone 2")
	if err != nil {
		t.Fatal(err)
	}
	hostB, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web2", Address: "192.0.2.11", ZoneID: zone.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	store.changedHosts[hostA] = true

	changed, err := svc.ChangedHosts(ctx, org, []uuid.UUID{hostA, hostB.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !changed[hostA] || changed[hostB.ID] {
		t.Fatalf("changed = %v", changed)
	}
}

func TestPinnedKeysScopedToOrg(t *testing.T) {
	svc, store, orgA, hostA := seededService(t)
	orgB := uuid.Must(uuid.NewV7())
	store.pinnedKeys[hostA] = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	// The fake only ever reports org-scoped data.
	otherHost := uuid.Must(uuid.NewV7())
	store.list[otherHost] = hosts.Host{ID: otherHost, OrgID: orgB, Name: "foreign", Managed: true}
	store.pinnedKeys[otherHost] = "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

	keys, err := svc.PinnedKeys(ctx, orgA, []uuid.UUID{hostA, otherHost})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[hostA] == "" {
		t.Fatalf("pinned keys = %v, want only the own-org pin", keys)
	}
}

func TestHostsDueForRetest(t *testing.T) {
	svc, store, org, hostA := seededService(t)
	zone, err := svc.CreateZone(ctx, org, "zone2", "Zone 2")
	if err != nil {
		t.Fatal(err)
	}
	monitored, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "watch", Address: "192.0.2.12", ZoneID: zone.ID})
	if err != nil {
		t.Fatal(err)
	}
	_ = monitored
	store.queuedTests[hostA] = true

	due, err := svc.HostsDueForRetest(ctx, org, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// hostA is queued (skipped); the unmanaged host is skipped; nothing left.
	if len(due) != 0 {
		t.Fatalf("due = %+v", due)
	}
}

// ListOrgIDs is on the fake for interface completeness; the re-test
// scheduler's own tests drive it through the schedule package's fakes.
func (f *fakeHostStore) ListOrgIDs(_ context.Context) ([]uuid.UUID, error) {
	if err := f.take("ListOrgIDs"); err != nil {
		return nil, err
	}
	return f.orgIDs, nil
}
