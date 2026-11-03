package workers_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/workers"
)

// adoptWorld is a restored database: one org and zone, an active worker
// "fra-1" whose one registered certificate was issued before the backup, a
// CA whose clock the test sets per certificate, and the times around the
// restore. The certificate times are whole seconds, as the CA issues them.
type adoptWorld struct {
	d      *db.DB
	q      *gen.Queries
	svc    *workers.Service
	ca     *ca.CA
	at     time.Time // the CA's clock: the next certificate's NotBefore
	org    uuid.UUID
	zone   uuid.UUID
	worker workers.Worker

	registeredAt, backupAt, restoredAt time.Time
}

func newAdoptWorld(t *testing.T) *adoptWorld {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	now := time.Now().UTC().Truncate(time.Second)
	w := &adoptWorld{d: d, q: q, svc: workers.NewService(workers.NewSQLStore(d)),
		registeredAt: now.Add(-4 * time.Hour), backupAt: now.Add(-2 * time.Hour), restoredAt: now.Add(-time.Hour)}
	w.org = seedOrg(t, q, "adopt-org")
	w.zone = seedZone(t, q, w.org, "default")
	var key [32]byte
	copy(key[:], "pulsix-workers-adopt-key-012345")
	authority, err := ca.New(&memCAStore{}, crypto.NewBox(key), ca.Config{CommonName: "pulsix-manager", ListenHost: "manager"},
		ca.WithClock(func() time.Time { return w.at }))
	if err != nil {
		t.Fatal(err)
	}
	w.at = now.Add(-24 * time.Hour)
	if err := authority.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.ca = authority
	first, _ := w.issue(t, w.org, w.zone, "fra-1", w.registeredAt)
	w.worker, err = w.svc.Register(ctx, w.org, workers.RegisterInput{ZoneID: w.zone, Name: "fra-1", CertSerial: first.SerialNumber,
		NotBefore: first.NotBefore, NotAfter: first.NotAfter})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// writeMarker stores the restore marker the way backup.Restore does.
func (w *adoptWorld) writeMarker(t *testing.T) {
	t.Helper()
	w.writeRawMarker(t, backup.RestoreMarker{BackupCreatedAt: w.backupAt, RestoredAt: w.restoredAt})
}

// writeRawMarker stores m as it is, whatever its fields hold.
func (w *adoptWorld) writeRawMarker(t *testing.T, m backup.RestoreMarker) {
	t.Helper()
	value, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.q.UpsertInstanceSetting(ctx, gen.UpsertInstanceSettingParams{Key: backup.RestoreMarkerKey, Value: value}); err != nil {
		t.Fatal(err)
	}
}

// attach registers leaf for the worker called name, enrolling it in the
// world's zone first when it does not exist yet.
func (w *adoptWorld) attach(t *testing.T, name string, leaf *x509.Certificate) {
	t.Helper()
	if _, err := w.svc.RegisterOrAttach(ctx, w.org, workers.RegisterInput{ZoneID: w.zone, Name: name,
		CertSerial: leaf.SerialNumber, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}); err != nil {
		t.Fatal(err)
	}
}

// issue signs a worker certificate with NotBefore at and returns the leaf
// and the identity the interceptor would verify from it.
func (w *adoptWorld) issue(t *testing.T, orgID, zoneID uuid.UUID, name string, at time.Time) (*x509.Certificate, *ca.ClientIdentity) {
	t.Helper()
	w.at = at
	certPEM, _, _, err := w.ca.Issue(context.Background(), &orgID, zoneID, name)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.ca.VerifyClient([][]byte{leaf.Raw})
	if err != nil {
		t.Fatal(err)
	}
	return leaf, id
}

// certificateCount is the number of certificate rows the org holds.
func (w *adoptWorld) certificateCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := w.d.Pool().QueryRow(ctx, `SELECT count(*) FROM worker_certificates WHERE org_id = $1`, w.org).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestAdoptWorkerCertificateInsideTheRestoreWindow: a certificate the CA
// issued for fra-1 after the backup and before the restore, newer than the
// one the restored database holds, is registered on first use.
func TestAdoptWorkerCertificateInsideTheRestoreWindow(t *testing.T) {
	w := newAdoptWorld(t)
	w.writeMarker(t)
	leaf, id := w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(30*time.Minute))

	got, err := w.svc.AdoptCertificate(ctx, leaf, id)
	if err != nil {
		t.Fatalf("AdoptCertificate: %v", err)
	}
	if got.ID != w.worker.ID || got.OrgID != w.org || got.ZoneID != w.zone || got.Name != "fra-1" ||
		got.Status != workers.StatusActive || got.CertSerial.Cmp(leaf.SerialNumber) != 0 {
		t.Fatalf("AdoptCertificate = %+v, want worker %+v carrying serial %v", got, w.worker, leaf.SerialNumber)
	}
	var notBefore, notAfter time.Time
	if err := w.d.Pool().QueryRow(ctx, `SELECT not_before, not_after FROM worker_certificates WHERE serial = $1 AND worker_id = $2`,
		leaf.SerialNumber.String(), w.worker.ID).Scan(&notBefore, &notAfter); err != nil {
		t.Fatalf("adopted certificate row: %v", err)
	}
	if !notBefore.Equal(leaf.NotBefore) || !notAfter.Equal(leaf.NotAfter) {
		t.Fatalf("row window = %v..%v, want the leaf's %v..%v", notBefore, notAfter, leaf.NotBefore, leaf.NotAfter)
	}
	bySerial, err := w.svc.GetBySerial(ctx, leaf.SerialNumber)
	if err != nil || bySerial.ID != w.worker.ID {
		t.Fatalf("GetBySerial after adoption = %+v, %v", bySerial, err)
	}

	// A second adoption of the same serial (the loser of two calls racing
	// in on a fresh connection) finds it registered and adds nothing.
	again, err := w.svc.AdoptCertificate(ctx, leaf, id)
	if err != nil || again.ID != w.worker.ID || again.CertSerial.Cmp(leaf.SerialNumber) != 0 {
		t.Fatalf("second AdoptCertificate = %+v, %v", again, err)
	}
	if n := w.certificateCount(t); n != 2 {
		t.Fatalf("certificate rows = %d, want the registered one and the adopted one", n)
	}
}

// TestAdoptWorkerCertificateRefuses: every rule of the adoption refuses on
// its own with ErrNotFound and leaves the registry untouched.
func TestAdoptWorkerCertificateRefuses(t *testing.T) {
	tests := []struct {
		name string
		// known marks a serial the registry already holds (revoked, or for
		// another worker), which GetBySerial must still resolve as before.
		known bool
		// arrange runs on the world and returns the certificate to adopt.
		arrange func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity)
	}{
		{"no restore marker", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			return w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(30*time.Minute))
		}},
		{"issued before the backup", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			return w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(-time.Second))
		}},
		{"issued at the backup", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			return w.issue(t, w.org, w.zone, "fra-1", w.backupAt)
		}},
		{"issued at the restore", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			return w.issue(t, w.org, w.zone, "fra-1", w.restoredAt)
		}},
		{"marker without a backup time", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeRawMarker(t, backup.RestoreMarker{RestoredAt: w.restoredAt})
			return w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(30*time.Minute))
		}},
		{"marker without a restore time", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeRawMarker(t, backup.RestoreMarker{BackupCreatedAt: w.backupAt})
			return w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(30*time.Minute))
		}},
		{"marker restored before its backup", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeRawMarker(t, backup.RestoreMarker{BackupCreatedAt: w.restoredAt, RestoredAt: w.backupAt})
			return w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(30*time.Minute))
		}},
		{"issued after the restore", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			return w.issue(t, w.org, w.zone, "fra-1", w.restoredAt.Add(time.Second))
		}},
		{"not newer than the newest registered certificate", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			newer, _ := w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(40*time.Minute))
			if _, err := w.svc.RegisterOrAttach(ctx, w.org, workers.RegisterInput{ZoneID: w.zone, Name: "fra-1",
				CertSerial: newer.SerialNumber, NotBefore: newer.NotBefore, NotAfter: newer.NotAfter}); err != nil {
				t.Fatal(err)
			}
			return w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(40*time.Minute))
		}},
		{"worker revoked", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			if _, err := workers.NewSQLStore(w.d).Revoke(ctx, w.org, w.worker.ID); err != nil {
				t.Fatal(err)
			}
			return w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(30*time.Minute))
		}},
		{"no worker with that name", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			return w.issue(t, w.org, w.zone, "fra-2", w.backupAt.Add(30*time.Minute))
		}},
		{"zone differs from the row", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			other := seedZone(t, w.q, w.org, "other")
			return w.issue(t, w.org, other, "fra-1", w.backupAt.Add(30*time.Minute))
		}},
		{"no org", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			leaf, id := w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(30*time.Minute))
			id.OrgID = nil
			return leaf, id
		}},
		{"registered but revoked", true, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			leaf, id := w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(30*time.Minute))
			w.attach(t, "fra-1", leaf)
			if _, err := w.d.Pool().Exec(ctx, `UPDATE worker_certificates SET revoked_at = now() WHERE serial = $1`, leaf.SerialNumber.String()); err != nil {
				t.Fatal(err)
			}
			return leaf, id
		}},
		{"registered to another worker", true, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			leaf, id := w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(30*time.Minute))
			w.attach(t, "fra-2", leaf)
			return leaf, id
		}},
		{"registered, no restore marker", true, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			leaf, id := w.issue(t, w.org, w.zone, "fra-1", w.backupAt.Add(30*time.Minute))
			w.attach(t, "fra-1", leaf)
			return leaf, id
		}},
		{"registered, issued after the restore", true, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			leaf, id := w.issue(t, w.org, w.zone, "fra-1", w.restoredAt.Add(time.Second))
			w.attach(t, "fra-1", leaf)
			return leaf, id
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newAdoptWorld(t)
			leaf, id := tt.arrange(t, w)
			before := w.certificateCount(t)
			_, err := w.svc.AdoptCertificate(ctx, leaf, id)
			if !errors.Is(err, workers.ErrNotFound) {
				t.Fatalf("AdoptCertificate err = %v, want ErrNotFound", err)
			}
			if after := w.certificateCount(t); after != before {
				t.Fatalf("certificate rows %d -> %d, want nothing inserted", before, after)
			}
			if _, err := w.svc.GetBySerial(ctx, leaf.SerialNumber); !tt.known && !errors.Is(err, workers.ErrNotFound) {
				t.Fatalf("GetBySerial after a refused adoption: %v, want ErrNotFound", err)
			}
		})
	}
}

// TestAdoptCertificateChecksTheWindowBeforeLocking: without a restore
// marker, or with a certificate outside its window, the service refuses
// without asking the store to lock the worker row — a refused worker's
// reconnect loop must not serialize with renewals and heartbeats.
func TestAdoptCertificateChecksTheWindowBeforeLocking(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	id := &ca.ClientIdentity{Serial: big.NewInt(4242), Kind: ca.KindWorker, OrgID: &org, ZoneID: zone, WorkerName: "fra-1"}
	window := &workers.RestoreWindow{BackupCreatedAt: now.Add(-2 * time.Hour), RestoredAt: now.Add(-time.Hour)}
	tests := []struct {
		name      string
		window    *workers.RestoreWindow
		notBefore time.Time
	}{
		{"no restore marker", nil, now.Add(-90 * time.Minute)},
		{"issued before the backup", window, now.Add(-3 * time.Hour)},
		{"issued after the restore", window, now},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{window: tt.window}
			_, err := workers.NewService(store).AdoptCertificate(ctx, &x509.Certificate{NotBefore: tt.notBefore, NotAfter: tt.notBefore.Add(ca.LeafValidity)}, id)
			if !errors.Is(err, workers.ErrNotFound) {
				t.Fatalf("AdoptCertificate err = %v, want ErrNotFound", err)
			}
			if store.adoptCalls != 0 {
				t.Fatalf("store.AdoptCertificate called %d times, want none", store.adoptCalls)
			}
		})
	}
}
