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
