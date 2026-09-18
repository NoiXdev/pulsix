package workers_test

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/workers"
)

func seedOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

func seedZone(t *testing.T, q *gen.Queries, orgID uuid.UUID, slug string) uuid.UUID {
	t.Helper()
	zone, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return zone.ID
}

// TestWorkerRoundTrip follows the credentials store test: register a worker,
// find it again by certificate serial with every field intact, touch it,
// then delete and confirm the serial no longer resolves.
func TestWorkerRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := workers.NewSQLStore(d)
	svc := workers.NewService(store)
	org := seedOrg(t, q, "worker-org")
	zone := seedZone(t, q, org, "default")
	serial := new(big.Int).SetBytes([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08})

	w, err := svc.Register(ctx, org, workers.RegisterInput{
		ZoneID: zone, Name: "fra-1", CertSerial: serial, Version: "0.2.0",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if w.LastSeen != nil {
		t.Fatalf("a fresh worker must not have last_seen: %+v", w)
	}

	got, err := svc.GetBySerial(ctx, serial)
	if err != nil {
		t.Fatalf("GetBySerial: %v", err)
	}
	if got.ID != w.ID || got.OrgID != org || got.ZoneID != zone || got.Name != "fra-1" ||
		got.Version != "0.2.0" || got.CreatedAt.IsZero() || got.CertSerial.Cmp(serial) != 0 {
		t.Fatalf("GetBySerial = %+v, want the registered worker %+v", got, w)
	}
	if got.CreatedAt.Sub(w.CreatedAt) > time.Minute {
		t.Fatalf("CreatedAt = %v, far from %v", got.CreatedAt, w.CreatedAt)
	}

	byID, err := store.List(ctx, org)
	if err != nil || len(byID) != 1 || byID[0].ID != w.ID {
		t.Fatalf("List = %+v, %v", byID, err)
	}

	if err := store.Touch(ctx, w.ID, "0.2.1"); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, err = svc.GetBySerial(ctx, serial)
	if err != nil {
		t.Fatalf("GetBySerial after Touch: %v", err)
	}
	if got.LastSeen == nil || time.Since(*got.LastSeen) > time.Minute {
		t.Fatalf("Touch did not update last_seen: %v", got.LastSeen)
	}
	if got.Version != "0.2.1" {
		t.Fatalf("version = %q, want 0.2.1", got.Version)
	}

	other := seedOrg(t, q, "worker-other")
	if err := store.Delete(ctx, other, w.ID); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("other org Delete: %v", err)
	}
	if err := store.Delete(ctx, org, w.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.GetBySerial(ctx, serial); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("GetBySerial after Delete: %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, org, w.ID); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("second Delete: %v, want ErrNotFound", err)
	}
}

func TestTouchUnknownWorkerIsNoop(t *testing.T) {
	d := testdb.Open(t)
	store := workers.NewSQLStore(d)
	if err := store.Touch(ctx, uuid.Must(uuid.NewV7()), "9.9.9"); err != nil {
		t.Fatalf("Touch of an unknown worker must be a silent no-op: %v", err)
	}
}

// TestGetBySerialIsGlobalNotPerOrg registers one worker in each of two
// organizations and proves the serial lookup is scoped by nothing but the
// serial itself: A's serial resolves A's row, B's serial resolves B's row,
// and revoking A's worker kills A's serial without touching B's — possible
// only because serials are unique across organizations.
func TestGetBySerialIsGlobalNotPerOrg(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := workers.NewSQLStore(d)
	svc := workers.NewService(store)
	orgA := seedOrg(t, q, "serial-org-a")
	orgB := seedOrg(t, q, "serial-org-b")
	zoneA := seedZone(t, q, orgA, "default")
	zoneB := seedZone(t, q, orgB, "default")
	serialA, serialB := big.NewInt(111111), big.NewInt(222222)

	wA, err := svc.Register(ctx, orgA, workers.RegisterInput{ZoneID: zoneA, Name: "fra-1", CertSerial: serialA})
	if err != nil {
		t.Fatalf("Register A: %v", err)
	}
	if _, err := svc.Register(ctx, orgB, workers.RegisterInput{ZoneID: zoneB, Name: "fra-1", CertSerial: serialB}); err != nil {
		t.Fatalf("Register B: %v", err)
	}

	got, err := svc.GetBySerial(ctx, serialA)
	if err != nil || got.OrgID != orgA || got.ID != wA.ID {
		t.Fatalf("GetBySerial(A) = %+v, %v, want A's row", got, err)
	}
	got, err = svc.GetBySerial(ctx, serialB)
	if err != nil || got.OrgID != orgB {
		t.Fatalf("GetBySerial(B) = %+v, %v, want B's row", got, err)
	}

	if err := store.Delete(ctx, orgA, wA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetBySerial(ctx, serialA); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("A's serial still resolves after deletion: %v, want ErrNotFound", err)
	}
	if _, err := svc.GetBySerial(ctx, serialB); err != nil {
		t.Fatalf("B's serial must be unaffected by A's deletion: %v", err)
	}
}

func TestRegisterDuplicateNameTaken(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := workers.NewSQLStore(d)
	svc := workers.NewService(store)
	org := seedOrg(t, q, "dup-worker-org")
	zone := seedZone(t, q, org, "default")
	if _, err := svc.Register(ctx, org, workers.RegisterInput{ZoneID: zone, Name: "fra-1", CertSerial: big.NewInt(5)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(ctx, org, workers.RegisterInput{ZoneID: zone, Name: "fra-1", CertSerial: big.NewInt(6)}); !errors.Is(err, workers.ErrNameTaken) {
		t.Fatalf("duplicate name: %v, want ErrNameTaken", err)
	}
}

// TestListOrderedByName registers two workers deliberately out of order and
// expects List to sort them by name.
func TestListOrderedByName(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := workers.NewSQLStore(d)
	svc := workers.NewService(store)
	org := seedOrg(t, q, "list-worker-org")
	zone := seedZone(t, q, org, "default")
	for _, in := range []workers.RegisterInput{
		{ZoneID: zone, Name: "zrh-1", CertSerial: big.NewInt(21)},
		{ZoneID: zone, Name: "fra-1", CertSerial: big.NewInt(22)},
		{ZoneID: zone, Name: "ams-1", CertSerial: big.NewInt(23)},
	} {
		if _, err := svc.Register(ctx, org, in); err != nil {
			t.Fatal(err)
		}
	}
	list, err := store.List(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Name != "ams-1" || list[1].Name != "fra-1" || list[2].Name != "zrh-1" {
		t.Fatalf("List = %+v, want ams-1, fra-1, zrh-1", list)
	}
}

// TestRegisterForeignZoneRejected proves the composite foreign key catches a
// worker whose zone belongs to another organization; the service surfaces
// the store failure as a wrapped error, not as one of its own sentinels.
func TestRegisterForeignZoneRejected(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := workers.NewSQLStore(d)
	svc := workers.NewService(store)
	orgA := seedOrg(t, q, "fk-worker-a")
	orgB := seedOrg(t, q, "fk-worker-b")
	zoneB := seedZone(t, q, orgB, "default")
	_, err := svc.Register(ctx, orgA, workers.RegisterInput{ZoneID: zoneB, Name: "fra-1", CertSerial: big.NewInt(31)})
	if err == nil {
		t.Fatal("Register with another org's zone must fail")
	}
	if errors.Is(err, workers.ErrInvalidInput) || errors.Is(err, workers.ErrNameTaken) || errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("FK violation must surface as a plain wrapped error, got %v", err)
	}
}
