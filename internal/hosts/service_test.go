package hosts_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
)

var ctx = context.Background()

// fakeHostStore is an in-memory hosts.Store good enough to drive Service
// without a database, mirroring tenancy's own fakeStore (service_test.go)
// and audit's memStore: every method matches the real SQL store's
// contract (ErrNotFound on a missing or foreign row, ErrNameTaken on a
// duplicate name/slug within an org, never a swallowed error).
type fakeHostStore struct {
	zones map[uuid.UUID]hosts.Zone
	list  map[uuid.UUID]hosts.Host
	// tags holds every tag (both sources) recorded per host, keyed by host
	// id — used by the tag-related fakeHostStore methods in tags_test.go
	// (same package, a different file: Go lets a type's methods live
	// anywhere in the package it is declared in).
	tags map[uuid.UUID][]hosts.Tag

	// failNext, when set, makes the named method's next call return this
	// error instead of doing its normal thing — used to prove a genuine
	// store failure is propagated, never swallowed into ErrNotFound.
	failNext map[string]error

	// lastListLimit records the limit ListHosts was actually called with,
	// for TestListHostsClampsLimit.
	lastListLimit int

	// orgsWithoutZone is returned by OrgsWithoutDefaultZone as-is. The fake
	// has no separate notion of "every organization that exists" (only
	// whichever zones happen to have been created), unlike the real SQL
	// store's query, which scans the organizations table directly — so
	// ReconcileDefaultZones' own tests set this to whatever candidate list
	// they want the reconciliation pass to see.
	orgsWithoutZone []uuid.UUID
	// orgIDs backs ListOrgIDs the same way.
	orgIDs []uuid.UUID

	// Host-key pinning and connection-test state (hostkey_test.go). pins
	// records SetHostKeyPin calls; pinnedKeys/changedHosts/queuedTests are
	// what the fake's reads report; tests holds the recorded test rows.
	pins         []pinRecord
	pinnedKeys   map[uuid.UUID]hosts.HostKeyPin
	changedHosts map[uuid.UUID]bool
	tests        map[uuid.UUID][]hosts.HostTest
	latestTestAt map[uuid.UUID]time.Time
	queuedTests  map[uuid.UUID]bool
}

func newFakeHostStore() *fakeHostStore {
	return &fakeHostStore{
		zones: map[uuid.UUID]hosts.Zone{}, list: map[uuid.UUID]hosts.Host{},
		tags: map[uuid.UUID][]hosts.Tag{}, failNext: map[string]error{},
	}
}

func (f *fakeHostStore) take(method string) error {
	if err, ok := f.failNext[method]; ok {
		delete(f.failNext, method)
		return err
	}
	return nil
}

func (f *fakeHostStore) CreateZone(_ context.Context, z hosts.Zone) error {
	if err := f.take("CreateZone"); err != nil {
		return err
	}
	for _, existing := range f.zones {
		if existing.OrgID == z.OrgID && existing.Slug == z.Slug {
			return hosts.ErrNameTaken
		}
	}
	f.zones[z.ID] = z
	return nil
}

func (f *fakeHostStore) ListZones(_ context.Context, orgID uuid.UUID) ([]hosts.Zone, error) {
	if err := f.take("ListZones"); err != nil {
		return nil, err
	}
	var out []hosts.Zone
	for _, z := range f.zones {
		if z.OrgID == orgID {
			out = append(out, z)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func (f *fakeHostStore) GetZone(_ context.Context, orgID, zoneID uuid.UUID) (hosts.Zone, error) {
	if err := f.take("GetZone"); err != nil {
		return hosts.Zone{}, err
	}
	z, ok := f.zones[zoneID]
	if !ok || z.OrgID != orgID {
		return hosts.Zone{}, hosts.ErrNotFound
	}
	return z, nil
}

func (f *fakeHostStore) CreateHost(_ context.Context, h hosts.Host) error {
	if err := f.take("CreateHost"); err != nil {
		return err
	}
	for _, existing := range f.list {
		if existing.OrgID == h.OrgID && existing.Name == h.Name {
			return hosts.ErrNameTaken
		}
	}
	f.list[h.ID] = h
	return nil
}

func (f *fakeHostStore) GetHost(_ context.Context, orgID, hostID uuid.UUID) (hosts.Host, error) {
	if err := f.take("GetHost"); err != nil {
		return hosts.Host{}, err
	}
	h, ok := f.list[hostID]
	if !ok || h.OrgID != orgID {
		return hosts.Host{}, hosts.ErrNotFound
	}
	return h, nil
}

func (f *fakeHostStore) UpdateHost(_ context.Context, h hosts.Host) (hosts.Host, error) {
	if err := f.take("UpdateHost"); err != nil {
		return hosts.Host{}, err
	}
	existing, ok := f.list[h.ID]
	if !ok || existing.OrgID != h.OrgID {
		return hosts.Host{}, hosts.ErrNotFound
	}
	for id, other := range f.list {
		if id != h.ID && other.OrgID == h.OrgID && other.Name == h.Name {
			return hosts.Host{}, hosts.ErrNameTaken
		}
	}
	// Preserve fields the caller's Host value does not carry (Facts,
	// CreatedAt), exactly like UPDATE hosts SET <mutable columns> leaves
	// facts/created_at untouched in the real schema.
	merged := existing
	merged.ZoneID, merged.Name, merged.Address = h.ZoneID, h.Name, h.Address
	merged.Managed, merged.Monitored = h.Managed, h.Monitored
	f.list[h.ID] = merged
	return merged, nil
}

func (f *fakeHostStore) PatchCapabilities(_ context.Context, orgID, hostID uuid.UUID, managed, monitored *bool) (hosts.Host, error) {
	if err := f.take("PatchCapabilities"); err != nil {
		return hosts.Host{}, err
	}
	h, ok := f.list[hostID]
	if !ok || h.OrgID != orgID {
		return hosts.Host{}, hosts.ErrNotFound
	}
	if managed != nil {
		h.Managed = *managed
	}
	if monitored != nil {
		h.Monitored = *monitored
	}
	f.list[hostID] = h
	return h, nil
}

func (f *fakeHostStore) OrgsWithoutDefaultZone(_ context.Context) ([]uuid.UUID, error) {
	if err := f.take("OrgsWithoutDefaultZone"); err != nil {
		return nil, err
	}
	return f.orgsWithoutZone, nil
}

func (f *fakeHostStore) DeleteHost(_ context.Context, orgID, hostID uuid.UUID) error {
	if err := f.take("DeleteHost"); err != nil {
		return err
	}
	h, ok := f.list[hostID]
	if !ok || h.OrgID != orgID {
		return hosts.ErrNotFound
	}
	delete(f.list, hostID)
	return nil
}

// lastListLimit records the limit ListHosts was actually called with, for
// TestListHostsClampsLimit — the fake's own list logic below always uses
// this recorded value, not the caller's raw one, exercising exactly what
// Service.ListHosts passes down after clamping.
func (f *fakeHostStore) ListHosts(_ context.Context, orgID uuid.UUID, filt hosts.ListFilter, cursor string, limit int) ([]hosts.Host, string, error) {
	if err := f.take("ListHosts"); err != nil {
		return nil, "", err
	}
	f.lastListLimit = limit

	cursorName, cursorID, err := hosts.DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}

	var matched []hosts.Host
	for _, h := range f.list {
		if h.OrgID != orgID {
			continue
		}
		if filt.ZoneID != nil && h.ZoneID != *filt.ZoneID {
			continue
		}
		if filt.Managed != nil && h.Managed != *filt.Managed {
			continue
		}
		if filt.Monitored != nil && h.Monitored != *filt.Monitored {
			continue
		}
		if filt.Query != "" && !strings.Contains(strings.ToLower(h.Name), strings.ToLower(filt.Query)) &&
			!strings.Contains(strings.ToLower(h.Address), strings.ToLower(filt.Query)) {
			continue
		}
		matched = append(matched, h)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].Name != matched[j].Name {
			return matched[i].Name < matched[j].Name
		}
		return matched[i].ID.String() < matched[j].ID.String()
	})

	if cursor != "" {
		start := 0
		for i, h := range matched {
			if h.Name > cursorName || (h.Name == cursorName && h.ID.String() > cursorID.String()) {
				start = i
				break
			}
			start = i + 1
		}
		matched = matched[start:]
	}

	next := ""
	if len(matched) > limit {
		next = hosts.EncodeCursor(matched[limit-1].Name, matched[limit-1].ID)
		matched = matched[:limit]
	}
	return matched, next, nil
}

// ListAllHosts returns every host of orgID sorted by name, unpaginated —
// the fake's counterpart to the real store's ORDER BY name query, used by
// TestListAllHostsSortedAndScoped and by the renderer's own tests.
func (f *fakeHostStore) ListAllHosts(_ context.Context, orgID uuid.UUID) ([]hosts.Host, error) {
	if err := f.take("ListAllHosts"); err != nil {
		return nil, err
	}
	var out []hosts.Host
	for _, h := range f.list {
		if h.OrgID == orgID {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func TestCreateHostValidatesNameAndZone(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, err := svc.CreateZone(ctx, org, "fra", "Falkenstein")
	if err != nil {
		t.Fatal(err)
	}
	otherOrgZone, err := svc.CreateZone(ctx, uuid.Must(uuid.NewV7()), "x", "X")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		in   hosts.HostInput
		want error
	}{
		"ok":           {hosts.HostInput{Name: "web-01.fra", ZoneID: zone.ID, Monitored: true}, nil},
		"upper case":   {hosts.HostInput{Name: "Web-01", ZoneID: zone.ID}, hosts.ErrInvalidInput},
		"foreign zone": {hosts.HostInput{Name: "web-02", ZoneID: otherOrgZone.ID}, hosts.ErrNotFound},
		"duplicate":    {hosts.HostInput{Name: "web-01.fra", ZoneID: zone.ID}, hosts.ErrNameTaken},
	}
	// "duplicate" depends on "ok" having already run and committed
	// web-01.fra, and map iteration order is random, so run "ok" first
	// explicitly before checking the rest.
	if _, err := svc.CreateHost(ctx, org, cases["ok"].in); err != nil {
		t.Fatalf("ok: %v", err)
	}
	for name, c := range cases {
		if name == "ok" {
			continue
		}
		_, err := svc.CreateHost(ctx, org, c.in)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}

func TestCapabilitiesAreIndependent(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "Falkenstein")
	h, _ := svc.CreateHost(ctx, org, hosts.HostInput{Name: "k3s-01", ZoneID: zone.ID, Managed: true})
	on := true
	off := false
	h, err := svc.SetCapabilities(ctx, org, h.ID, nil, &on)
	if err != nil || !h.Managed || !h.Monitored {
		t.Fatalf("%+v %v", h, err)
	}
	h, err = svc.SetCapabilities(ctx, org, h.ID, &off, nil)
	if err != nil || h.Managed || !h.Monitored {
		t.Fatalf("managed off should keep monitored: %+v %v", h, err)
	}
	// Setting monitored off now, mentioning only that flag, must not
	// resurrect managed: nil for managed always means "leave it", never
	// "reset to false".
	h, err = svc.SetCapabilities(ctx, org, h.ID, nil, &off)
	if err != nil || h.Managed || h.Monitored {
		t.Fatalf("both should now be off: %+v %v", h, err)
	}
}

func TestListHostsFilters(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	for _, h := range []hosts.HostInput{
		{Name: "a", ZoneID: zone.ID, Managed: true},
		{Name: "b", ZoneID: zone.ID, Monitored: true},
		{Name: "c", ZoneID: zone.ID, Managed: true, Monitored: true},
	} {
		if _, err := svc.CreateHost(ctx, org, h); err != nil {
			t.Fatal(err)
		}
	}
	on := true
	got, _, err := svc.ListHosts(ctx, org, hosts.ListFilter{Managed: &on}, "", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("managed filter: %d %v", len(got), err)
	}
	got, _, err = svc.ListHosts(ctx, org, hosts.ListFilter{Query: "c"}, "", 10)
	if err != nil || len(got) != 1 || got[0].Name != "c" {
		t.Fatalf("query filter: %+v %v", got, err)
	}
	got, _, err = svc.ListHosts(ctx, org, hosts.ListFilter{ZoneID: &zone.ID}, "", 10)
	if err != nil || len(got) != 3 {
		t.Fatalf("zone filter: %d %v", len(got), err)
	}
	otherZone := uuid.Must(uuid.NewV7())
	got, _, err = svc.ListHosts(ctx, org, hosts.ListFilter{ZoneID: &otherZone}, "", 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("zone filter (no match): %d %v", len(got), err)
	}
}

func TestCreateZoneValidation(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	if _, err := svc.CreateZone(ctx, org, "Not_Valid", "Name"); !errors.Is(err, hosts.ErrInvalidInput) {
		t.Fatalf("bad slug: %v, want ErrInvalidInput", err)
	}
	if _, err := svc.CreateZone(ctx, org, "ok-slug", ""); !errors.Is(err, hosts.ErrInvalidInput) {
		t.Fatalf("empty name: %v, want ErrInvalidInput", err)
	}
	if _, err := svc.CreateZone(ctx, org, "fra", "Falkenstein"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateZone(ctx, org, "fra", "Duplicate"); !errors.Is(err, hosts.ErrNameTaken) {
		t.Fatalf("duplicate slug: %v, want ErrNameTaken", err)
	}
}

func TestEnsureDefaultZoneIsIdempotent(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	if err := svc.EnsureDefaultZone(ctx, org); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := svc.EnsureDefaultZone(ctx, org); err != nil {
		t.Fatalf("second call must be a no-op, not an error: %v", err)
	}
	zones, err := svc.ListZones(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 1 || zones[0].Slug != hosts.DefaultZoneSlug {
		t.Fatalf("zones = %+v, want exactly one default zone", zones)
	}
}

func TestEnsureDefaultZonePropagatesStoreError(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	boom := errors.New("boom")
	st.failNext["CreateZone"] = boom
	if err := svc.EnsureDefaultZone(ctx, org); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped %v", err, boom)
	}
}

func TestUpdateHostValidatesNameAndZone(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	otherOrgZone, _ := svc.CreateZone(ctx, uuid.Must(uuid.NewV7()), "x", "X")
	h, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-02", ZoneID: zone.ID}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.UpdateHost(ctx, org, h.ID, hosts.HostInput{Name: "Bad Name", ZoneID: zone.ID}); !errors.Is(err, hosts.ErrInvalidInput) {
		t.Fatalf("invalid name: %v, want ErrInvalidInput", err)
	}
	if _, err := svc.UpdateHost(ctx, org, h.ID, hosts.HostInput{Name: "web-01b", ZoneID: otherOrgZone.ID}); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("foreign zone: %v, want ErrNotFound", err)
	}
	if _, err := svc.UpdateHost(ctx, org, h.ID, hosts.HostInput{Name: "web-02", ZoneID: zone.ID}); !errors.Is(err, hosts.ErrNameTaken) {
		t.Fatalf("duplicate name: %v, want ErrNameTaken", err)
	}
	if _, err := svc.UpdateHost(ctx, org, uuid.Must(uuid.NewV7()), hosts.HostInput{Name: "web-03", ZoneID: zone.ID}); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("unknown host: %v, want ErrNotFound", err)
	}

	updated, err := svc.UpdateHost(ctx, org, h.ID, hosts.HostInput{Name: "web-01-renamed", Address: "10.0.0.1", ZoneID: zone.ID, Managed: true, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "web-01-renamed" || updated.Address != "10.0.0.1" || !updated.Managed || !updated.Monitored {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestSetCapabilitiesNotFound(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	on := true
	if _, err := svc.SetCapabilities(ctx, org, uuid.Must(uuid.NewV7()), &on, nil); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteHostScopedToOrg(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	zoneA, _ := svc.CreateZone(ctx, orgA, "fra", "F")
	h, err := svc.CreateHost(ctx, orgA, hosts.HostInput{Name: "web-01", ZoneID: zoneA.ID})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteHost(ctx, orgB, h.ID); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("delete from wrong org: %v, want ErrNotFound", err)
	}
	if _, err := svc.GetHost(ctx, orgA, h.ID); err != nil {
		t.Fatalf("host must still exist after the wrong-org delete: %v", err)
	}
	if err := svc.DeleteHost(ctx, orgA, h.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.GetHost(ctx, orgA, h.ID); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("host must be gone: %v, want ErrNotFound", err)
	}
	if err := svc.DeleteHost(ctx, orgA, h.ID); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("deleting again: %v, want ErrNotFound", err)
	}
}

func TestGetHostInvisibleAcrossOrgs(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	zoneA, _ := svc.CreateZone(ctx, orgA, "fra", "F")
	h, err := svc.CreateHost(ctx, orgA, hosts.HostInput{Name: "web-01", ZoneID: zoneA.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetHost(ctx, orgB, h.ID); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("cross-org get: %v, want ErrNotFound", err)
	}
}

func TestListHostsClampsLimit(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	if _, _, err := svc.ListHosts(ctx, org, hosts.ListFilter{}, "", 0); err != nil {
		t.Fatal(err)
	}
	if st.lastListLimit != hosts.DefaultListLimit {
		t.Fatalf("limit 0 -> %d, want default %d", st.lastListLimit, hosts.DefaultListLimit)
	}
	if _, _, err := svc.ListHosts(ctx, org, hosts.ListFilter{}, "", 1000); err != nil {
		t.Fatal(err)
	}
	if st.lastListLimit != hosts.MaxListLimit {
		t.Fatalf("limit 1000 -> %d, want max %d", st.lastListLimit, hosts.MaxListLimit)
	}
	if _, _, err := svc.ListHosts(ctx, org, hosts.ListFilter{}, "", 5); err != nil {
		t.Fatal(err)
	}
	if st.lastListLimit != 5 {
		t.Fatalf("limit 5 -> %d, want 5", st.lastListLimit)
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	if _, _, err := hosts.DecodeCursor("not-a-valid-cursor!!"); !errors.Is(err, hosts.ErrInvalidCursor) {
		t.Fatalf("DecodeCursor(garbage) err = %v, want ErrInvalidCursor", err)
	}
}

func TestEncodeDecodeCursorRoundTrip(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	gotName, gotID, err := hosts.DecodeCursor(hosts.EncodeCursor("web-01.fra", id))
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "web-01.fra" || gotID != id {
		t.Fatalf("round trip = (%q, %s), want (%q, %s)", gotName, gotID, "web-01.fra", id)
	}
}

func TestDecodeCursorEmptyIsFirstPage(t *testing.T) {
	name, id, err := hosts.DecodeCursor("")
	if err != nil || name != "" || id != uuid.Nil {
		t.Fatalf("DecodeCursor(\"\") = (%q, %s, %v), want (\"\", uuid.Nil, nil)", name, id, err)
	}
}

// TestSetCapabilitiesPropagatesPatchStoreError proves a genuine store
// failure from SetCapabilities' single atomic write is never swallowed
// into ErrNotFound. (Previously named TestCreateHostPropagatesGetHostStoreError
// — that name was wrong on two counts: it is SetCapabilities under test,
// not CreateHost, and SetCapabilities no longer calls GetHost at all since
// Task 13 fix round 1, finding 2 replaced its read-modify-write with one
// atomic Store.PatchCapabilities call.)
func TestSetCapabilitiesPropagatesPatchStoreError(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	h, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID})
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	st.failNext["PatchCapabilities"] = boom
	if _, err := svc.SetCapabilities(ctx, org, h.ID, nil, nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v (a genuine store failure must never be swallowed into ErrNotFound)", err, boom)
	}
}

// TestReconcileDefaultZonesCreatesMissingZones proves
// Service.ReconcileDefaultZones gives every organization store.
// OrgsWithoutDefaultZone names its default zone.
func TestReconcileDefaultZonesCreatesMissingZones(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	st.orgsWithoutZone = []uuid.UUID{orgA, orgB}

	repaired, err := svc.ReconcileDefaultZones(ctx)
	if err != nil {
		t.Fatalf("ReconcileDefaultZones: %v", err)
	}
	if repaired != 2 {
		t.Fatalf("repaired = %d, want 2", repaired)
	}
	for _, org := range []uuid.UUID{orgA, orgB} {
		zones, err := svc.ListZones(ctx, org)
		if err != nil || len(zones) != 1 || zones[0].Slug != hosts.DefaultZoneSlug {
			t.Fatalf("org %s zones = %+v, %v", org, zones, err)
		}
	}
}

// TestReconcileDefaultZonesContinuesPastOneFailure proves one organization
// failing to repair does not stop the pass from trying the rest — the
// whole point of running this at startup instead of failing the server
// outright.
func TestReconcileDefaultZonesContinuesPastOneFailure(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	st.orgsWithoutZone = []uuid.UUID{orgA, orgB}
	boom := errors.New("boom")
	// Only the first CreateZone call (orgA's) fails; orgB's must still go
	// through.
	st.failNext["CreateZone"] = boom

	repaired, err := svc.ReconcileDefaultZones(ctx)
	if repaired != 1 {
		t.Fatalf("repaired = %d, want 1 (one org failing must not stop the rest)", repaired)
	}
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want a joined error wrapping %v", err, boom)
	}
	zones, err := svc.ListZones(ctx, orgB)
	if err != nil || len(zones) != 1 || zones[0].Slug != hosts.DefaultZoneSlug {
		t.Fatalf("orgB zones = %+v, %v, want a default zone despite orgA's failure", zones, err)
	}
}

// TestReconcileDefaultZonesPropagatesListError proves a failure listing
// candidate organizations itself (as opposed to repairing one of them) is
// reported, not silently treated as "nothing to repair".
func TestReconcileDefaultZonesPropagatesListError(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	boom := errors.New("boom")
	st.failNext["OrgsWithoutDefaultZone"] = boom
	if _, err := svc.ReconcileDefaultZones(ctx); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped %v", err, boom)
	}
}

// TestListAllHostsSortedAndScoped proves Service.ListAllHosts passes
// through to the store and that the store returns hosts sorted by name,
// scoped to the given organization — the contract the inventory renderer
// (internal/inventory/render) relies on.
func TestListAllHostsSortedAndScoped(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	other := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	otherZone, _ := svc.CreateZone(ctx, other, "fra", "F")
	for _, name := range []string{"c", "a", "b"} {
		if _, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: name, ZoneID: zone.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.CreateHost(ctx, other, hosts.HostInput{Name: "z", ZoneID: otherZone.ID}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ListAllHosts(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "c" {
		t.Fatalf("ListAllHosts = %+v, want [a b c]", got)
	}
}
