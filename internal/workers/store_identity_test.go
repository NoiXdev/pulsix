package workers_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/workers"
)

// TestEnrollIsSingleUseUnderConcurrency: two redeems of one token race;
// the row lock lets exactly one through.
func TestEnrollIsSingleUseUnderConcurrency(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "enroll-race")
	zone := seedZone(t, q, org, "default")
	store := workers.NewSQLStore(d)
	secret := bytes.Repeat([]byte{3}, 32)
	hash := sha256.Sum256(secret)
	e, err := store.CreateEnrollment(db.WithOrgScope(ctx, org), workers.Enrollment{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Name: "fra-1", ExpiresAt: time.Now().Add(time.Hour)}, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	wins, refusals := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		serial := big.NewInt(int64(9000 + i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Enroll(ctx, e.ID, func(got workers.Enrollment, h []byte) (workers.Worker, workers.Certificate, error) {
				if got.UsedAt != nil || !bytes.Equal(h, hash[:]) {
					return workers.Worker{}, workers.Certificate{}, workers.ErrEnrollmentRefused
				}
				id := uuid.Must(uuid.NewV7())
				return workers.Worker{ID: id, OrgID: org, ZoneID: zone, Name: got.Name, CertSerial: serial, Status: workers.StatusActive},
					workers.Certificate{Serial: serial, OrgID: org, WorkerID: id, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}, nil
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, workers.ErrEnrollmentRefused):
				refusals++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || refusals != 1 {
		t.Fatalf("wins %d, refusals %d; want exactly one redeem", wins, refusals)
	}
	if pending, _ := store.ListEnrollments(db.WithOrgScope(ctx, org), org, time.Now()); len(pending) != 0 {
		t.Fatalf("a used enrollment is not pending: %v", pending)
	}
	if err := store.DeleteEnrollment(db.WithOrgScope(ctx, org), org, e.ID); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("a used enrollment cannot be withdrawn: %v", err)
	}
	if _, err := store.Enroll(ctx, uuid.Must(uuid.NewV7()), nil); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("unknown enrollment: %v", err)
	}
}

// TestMarkEnrollmentUsedIsAtomic proves the single-use guard lives in the
// database's conditional UPDATE, not in the caller's closure: a closure
// that never looks at got.UsedAt or the expiry still cannot redeem an
// already-used or (by the database's own clock) expired enrollment, and of
// two concurrent redeems through such a forgetful closure exactly one
// succeeds. Each redeem names its worker uniquely so a UNIQUE(org_id,
// name) violation can never masquerade as the guard under test.
func TestMarkEnrollmentUsedIsAtomic(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "atomic-enroll")
	zone := seedZone(t, q, org, "default")
	store := workers.NewSQLStore(d)

	// The worker name is suffixed with the (per-call, caller-chosen) serial
	// rather than a UUID prefix: two UUIDv7s minted microseconds apart in
	// this test share their high-order timestamp bits, so a short prefix
	// of the string form is not actually unique here — the serial is.
	forgetfulRedeem := func(serial *big.Int) func(got workers.Enrollment, h []byte) (workers.Worker, workers.Certificate, error) {
		return func(got workers.Enrollment, _ []byte) (workers.Worker, workers.Certificate, error) {
			id := uuid.Must(uuid.NewV7())
			return workers.Worker{ID: id, OrgID: org, ZoneID: zone, Name: got.Name + "-" + serial.String(), CertSerial: serial, Status: workers.StatusActive},
				workers.Certificate{Serial: serial, OrgID: org, WorkerID: id, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}, nil
		}
	}

	t.Run("already used", func(t *testing.T) {
		secret := bytes.Repeat([]byte{5}, 32)
		hash := sha256.Sum256(secret)
		e, err := store.CreateEnrollment(db.WithOrgScope(ctx, org), workers.Enrollment{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Name: "used-1", ExpiresAt: time.Now().Add(time.Hour)}, hash[:])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Enroll(ctx, e.ID, forgetfulRedeem(big.NewInt(7001))); err != nil {
			t.Fatalf("first redeem: %v", err)
		}
		if _, err := store.Enroll(ctx, e.ID, forgetfulRedeem(big.NewInt(7002))); !errors.Is(err, workers.ErrEnrollmentRefused) {
			t.Fatalf("redeem of an already-used enrollment (forgetful closure) = %v, want ErrEnrollmentRefused", err)
		}
	})

	t.Run("expired by the database clock", func(t *testing.T) {
		secret := bytes.Repeat([]byte{6}, 32)
		hash := sha256.Sum256(secret)
		// ExpiresAt is already in the past; the closure below never checks
		// it — only the database's conditional UPDATE can refuse this.
		e, err := store.CreateEnrollment(db.WithOrgScope(ctx, org), workers.Enrollment{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Name: "expired-1", ExpiresAt: time.Now().Add(-time.Minute)}, hash[:])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Enroll(ctx, e.ID, forgetfulRedeem(big.NewInt(7003))); !errors.Is(err, workers.ErrEnrollmentRefused) {
			t.Fatalf("redeem of an expired enrollment (forgetful closure) = %v, want ErrEnrollmentRefused", err)
		}
	})

	t.Run("concurrent redeems through a forgetful closure", func(t *testing.T) {
		secret := bytes.Repeat([]byte{8}, 32)
		hash := sha256.Sum256(secret)
		e, err := store.CreateEnrollment(db.WithOrgScope(ctx, org), workers.Enrollment{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Name: "race-1", ExpiresAt: time.Now().Add(time.Hour)}, hash[:])
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		wins, refusals := 0, 0
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			serial := big.NewInt(int64(7100 + i))
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := store.Enroll(ctx, e.ID, forgetfulRedeem(serial))
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					wins++
				case errors.Is(err, workers.ErrEnrollmentRefused):
					refusals++
				default:
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if wins != 1 || refusals != 1 {
			t.Fatalf("wins %d, refusals %d; want exactly one redeem even through a closure with no UsedAt/expiry check", wins, refusals)
		}
	})
}

func TestLookupSerialSeesRevocationAndRenewals(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "lookup-serial")
	zone := seedZone(t, q, org, "default")
	store := workers.NewSQLStore(d)
	svc := workers.NewService(store)
	w, err := svc.Register(ctx, org, workers.RegisterInput{ZoneID: zone, Name: "fra-1", CertSerial: big.NewInt(1001)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddCertificate(ctx, workers.Certificate{Serial: big.NewInt(1002), OrgID: org, WorkerID: w.ID, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetBySerial(ctx, big.NewInt(1002))
	if err != nil || got.ID != w.ID || got.CertSerial.Int64() != 1002 {
		t.Fatalf("renewed serial = %+v, %v", got, err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE worker_certificates SET revoked_at = now() WHERE serial = '1001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetBySerial(ctx, big.NewInt(1001)); !errors.Is(err, workers.ErrRevoked) {
		t.Fatalf("revoked certificate: %v", err)
	}
	if _, err := svc.GetBySerial(ctx, big.NewInt(1002)); err != nil {
		t.Fatalf("the other certificate is unaffected: %v", err)
	}
	byName, err := store.GetByName(ctx, org, "fra-1")
	if err != nil || byName.ID != w.ID || byName.Status != workers.StatusActive {
		t.Fatalf("GetByName = %+v, %v", byName, err)
	}
	if _, err := store.GetByName(ctx, org, "nope"); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("GetByName unknown: %v", err)
	}
}

func TestStoreRevokeAndViewDetails(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "revoke-store")
	zone := seedZone(t, q, org, "default")
	store := workers.NewSQLStore(d)
	svc := workers.NewService(store)
	w, err := svc.Register(ctx, org, workers.RegisterInput{ZoneID: zone, Name: "fra-1", CertSerial: big.NewInt(2001)})
	if err != nil {
		t.Fatal(err)
	}
	details, err := store.ViewDetails(ctx, org)
	if err != nil || details.Expiry[w.ID].IsZero() {
		t.Fatalf("details = %+v, %v", details, err)
	}
	job := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(ctx, `INSERT INTO jobs (id, org_id, zone_id, kind, status, run_group_id, worker_id, claimed_at) VALUES ($1, $2, $3, 'adhoc', 'running', $1, $4, now())`, job, org, zone, w.ID); err != nil {
		t.Fatal(err)
	}
	details, _ = store.ViewDetails(ctx, org)
	if details.CurrentJob[w.ID] != job {
		t.Fatalf("current job = %v", details.CurrentJob)
	}
	revoked, err := store.Revoke(ctx, org, w.ID)
	if err != nil || revoked.Status != workers.StatusRevoked || revoked.RevokedAt == nil {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	if _, err := svc.GetBySerial(ctx, big.NewInt(2001)); !errors.Is(err, workers.ErrRevoked) {
		t.Fatalf("revoked serial: %v", err)
	}
	details, _ = store.ViewDetails(ctx, org)
	if _, ok := details.Expiry[w.ID]; ok {
		t.Fatal("revoked certificates have no expiry to show")
	}
	if _, err := store.Revoke(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if got, err := store.Get(ctx, org, w.ID); err != nil || got.Status != workers.StatusRevoked {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	// Revoking twice keeps the first timestamp.
	again, err := store.Revoke(ctx, org, w.ID)
	if err != nil || again.RevokedAt == nil || !again.RevokedAt.Equal(*revoked.RevokedAt) {
		t.Fatalf("second revoke = %+v, %v; want revoked_at %v kept", again, err, revoked.RevokedAt)
	}
	// Another organization sees neither the worker nor its revocation.
	otherOrg := seedOrg(t, q, "revoke-store-other")
	if _, err := store.Get(ctx, otherOrg, w.ID); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("Get from another org: %v", err)
	}
	if _, err := store.Revoke(ctx, otherOrg, w.ID); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("Revoke from another org: %v", err)
	}
	// Delete removes the row and, by cascade, its certificates.
	if err := svc.Delete(ctx, org, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetBySerial(ctx, big.NewInt(2001)); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("deleted worker's serial: %v", err)
	}
}
