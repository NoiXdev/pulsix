package agents_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/restoremark"
)

// memCAStore keeps the CA's sealed state in memory.
type memCAStore struct{ saved []byte }

func (m *memCAStore) Load(context.Context) ([]byte, bool, error) { return m.saved, m.saved != nil, nil }
func (m *memCAStore) Save(_ context.Context, b []byte) error     { m.saved = b; return nil }

// adoptWorld is a restored database: one org and zone, a host "web-1"
// whose agent "web-1" holds one certificate issued before the backup for
// the agent's key, a CA whose clock the test sets per certificate, and the
// times around the restore. The certificate times are whole seconds, as
// the CA issues them.
type adoptWorld struct {
	d     *db.DB
	q     *gen.Queries
	svc   *agents.Service
	ca    *ca.CA
	at    time.Time // the CA's clock: the next certificate's NotBefore
	org   uuid.UUID
	zone  uuid.UUID
	host  uuid.UUID
	key   *ecdsa.PrivateKey // web-1's agent key, kept across renewals
	first *x509.Certificate

	registeredAt, backupAt, restoredAt time.Time
}

func newAdoptWorld(t *testing.T) *adoptWorld {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	now := time.Now().UTC().Truncate(time.Second)
	w := &adoptWorld{d: d, q: q, svc: agents.NewService(agents.NewSQLStore(d)),
		registeredAt: now.Add(-4 * time.Hour), backupAt: now.Add(-2 * time.Hour), restoredAt: now.Add(-time.Hour)}
	w.org = seedOrg(t, q, "adopt-org")
	w.zone = seedZone(t, q, w.org, "default")
	var key [32]byte
	copy(key[:], "pulsix-agents-adopt-key-0123456")
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
	w.key = newKey(t)
	w.host = createHost(t, d, w.org, w.zone, "web-1").ID
	w.first, _ = w.issue(t, w.org, "web-1", w.registeredAt)
	w.register(t, w.host, "web-1", w.first)
	return w
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// register records leaf as the agent called name on host, the way an
// enrollment or renewal does.
func (w *adoptWorld) register(t *testing.T, host uuid.UUID, name string, leaf *x509.Certificate) {
	t.Helper()
	if err := w.svc.Register(ctx, agents.Registration{OrgID: w.org, HostID: host, AgentName: name, Version: "v1",
		Serial: leaf.SerialNumber.String(), NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter,
		PublicKeySHA256: agents.KeyFingerprint(leaf)}); err != nil {
		t.Fatal(err)
	}
}

// writeMarker stores the restore marker the way backup.Restore does
// (the marker rules themselves are internal/restoremark's tests).
func (w *adoptWorld) writeMarker(t *testing.T) {
	t.Helper()
	value, err := json.Marshal(restoremark.Marker{BackupCreatedAt: w.backupAt, RestoredAt: w.restoredAt})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.q.UpsertInstanceSetting(ctx, gen.UpsertInstanceSettingParams{Key: restoremark.Key, Value: value}); err != nil {
		t.Fatal(err)
	}
}

// issue signs an agent certificate for the world's agent key with
// NotBefore at and returns the leaf and the identity the interceptor would
// verify from it.
func (w *adoptWorld) issue(t *testing.T, orgID uuid.UUID, name string, at time.Time) (*x509.Certificate, *ca.ClientIdentity) {
	t.Helper()
	return w.issueFor(t, w.key, orgID, name, at)
}

// issueFor is issue for another key.
func (w *adoptWorld) issueFor(t *testing.T, key *ecdsa.PrivateKey, orgID uuid.UUID, name string, at time.Time) (*x509.Certificate, *ca.ClientIdentity) {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	w.at = at
	_, leaf, err := w.ca.SignAgentCSR(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), orgID, name)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.ca.VerifyClient([][]byte{leaf.Raw})
	if err != nil {
		t.Fatal(err)
	}
	return leaf, id
}

// registry is what adoption may change: every certificate row (serial,
// host, revoked) and every agent row (host, name, serial), as one string.
func (w *adoptWorld) registry(t *testing.T) string {
	t.Helper()
	var out string
	if err := w.d.Pool().QueryRow(ctx, `SELECT
		coalesce((SELECT string_agg(serial || ':' || host_id || ':' || (revoked_at IS NOT NULL), ',' ORDER BY serial) FROM agent_certificates), '') || '|' ||
		coalesce((SELECT string_agg(host_id || ':' || agent_name || ':' || cert_serial, ',' ORDER BY host_id) FROM host_agents), '')`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestAdoptAgentCertificateInsideTheRestoreWindow: a certificate the CA
// issued for web-1 after the backup and before the restore, newer than the
// one the restored database holds, is registered on first use exactly
// like a renewal: the agent row carries it, the older serial is revoked.
func TestAdoptAgentCertificateInsideTheRestoreWindow(t *testing.T) {
	w := newAdoptWorld(t)
	w.writeMarker(t)
	leaf, id := w.issue(t, w.org, "web-1", w.backupAt.Add(30*time.Minute))
	serial := leaf.SerialNumber.String()

	got, err := w.svc.AdoptCertificate(ctx, leaf, id)
	if err != nil {
		t.Fatalf("AdoptCertificate: %v", err)
	}
	if got.Serial != serial || got.OrgID != w.org || got.HostID != w.host || got.Name != "web-1" || got.Revoked {
		t.Fatalf("AdoptCertificate = %+v, want host %s's agent carrying serial %s", got, w.host, serial)
	}
	var notBefore, notAfter time.Time
	var key *string
	if err := w.d.Pool().QueryRow(ctx, `SELECT not_before, not_after, public_key_sha256 FROM agent_certificates WHERE serial = $1 AND host_id = $2 AND revoked_at IS NULL`,
		serial, w.host).Scan(&notBefore, &notAfter, &key); err != nil {
		t.Fatalf("adopted certificate row: %v", err)
	}
	if !notBefore.Equal(leaf.NotBefore) || !notAfter.Equal(leaf.NotAfter) || key == nil || *key != agents.KeyFingerprint(leaf) {
		t.Fatalf("row = %v..%v key %v, want the leaf's %v..%v key %s", notBefore, notAfter, key, leaf.NotBefore, leaf.NotAfter, agents.KeyFingerprint(leaf))
	}
	bySerial, err := w.svc.GetBySerial(ctx, serial)
	if err != nil || bySerial.HostID != w.host || bySerial.Revoked {
		t.Fatalf("GetBySerial after adoption = %+v, %v", bySerial, err)
	}
	old, err := w.svc.GetBySerial(ctx, w.first.SerialNumber.String())
	if err != nil || !old.Revoked {
		t.Fatalf("the older serial after adoption = %+v, %v; want it revoked", old, err)
	}

	// A second adoption of the same serial (the loser of two calls racing
	// in on a fresh connection) finds it registered and changes nothing.
	before := w.registry(t)
	again, err := w.svc.AdoptCertificate(ctx, leaf, id)
	if err != nil || again.HostID != w.host || again.Serial != serial || again.Revoked {
		t.Fatalf("second AdoptCertificate = %+v, %v", again, err)
	}
	if after := w.registry(t); after != before {
		t.Fatalf("registry %s -> %s, want the second adoption to change nothing", before, after)
	}
}

// TestAdoptAgentCertificateRefuses: every rule of the adoption refuses on
// its own with ErrNotFound and leaves the registry untouched.
func TestAdoptAgentCertificateRefuses(t *testing.T) {
	inside := func(w *adoptWorld) time.Time { return w.backupAt.Add(30 * time.Minute) }
	tests := []struct {
		name string
		// known marks a serial the registry already holds (revoked, or for
		// another host), which GetBySerial must still resolve as before.
		known bool
		// arrange runs on the world and returns the certificate to adopt.
		arrange func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity)
	}{
		{"no restore marker", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			return w.issue(t, w.org, "web-1", inside(w))
		}},
		{"issued before the backup", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			return w.issue(t, w.org, "web-1", w.backupAt.Add(-time.Second))
		}},
		{"issued at the backup", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			return w.issue(t, w.org, "web-1", w.backupAt)
		}},
		{"issued at the restore", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			return w.issue(t, w.org, "web-1", w.restoredAt)
		}},
		{"issued after the restore", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			return w.issue(t, w.org, "web-1", w.restoredAt.Add(time.Second))
		}},
		{"not newer than the newest registered certificate", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			newer, _ := w.issue(t, w.org, "web-1", w.backupAt.Add(40*time.Minute))
			w.register(t, w.host, "web-1", newer)
			return w.issue(t, w.org, "web-1", w.backupAt.Add(40*time.Minute))
		}},
		{"not newer than a certificate known only by its creation time", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			// A row written before migration 00039 carries no not_before:
			// its created_at stands in for it.
			if _, err := w.d.Pool().Exec(ctx, `UPDATE agent_certificates SET not_before = NULL, created_at = $1 WHERE serial = $2`,
				w.backupAt.Add(50*time.Minute), w.first.SerialNumber.String()); err != nil {
				t.Fatal(err)
			}
			return w.issue(t, w.org, "web-1", w.backupAt.Add(45*time.Minute))
		}},
		{"another key than the agent's", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			return w.issueFor(t, newKey(t), w.org, "web-1", inside(w))
		}},
		{"the name now belongs to a new host with its own key", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			// Takeover: web-1's agent was revoked and a new host enrolled
			// under the same agent name before the backup; a certificate
			// issued to the old agent's key must not move onto it.
			w.writeMarker(t)
			if err := w.svc.Revoke(ctx, w.org, w.host); err != nil {
				t.Fatal(err)
			}
			successor, _ := w.issueFor(t, newKey(t), w.org, "web-1", w.registeredAt.Add(time.Minute))
			w.register(t, createHost(t, w.d, w.org, w.zone, "web-1-new").ID, "web-1", successor)
			return w.issue(t, w.org, "web-1", inside(w))
		}},
		{"a revoked agent's registered serial after its name moved on", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			// The revoked host's agent row is gone, so its serial resolves
			// to nothing and is offered for adoption; it is still
			// registered to that host and refused.
			w.writeMarker(t)
			leaf, id := w.issue(t, w.org, "web-1", inside(w))
			w.register(t, w.host, "web-1", leaf)
			if err := w.svc.Revoke(ctx, w.org, w.host); err != nil {
				t.Fatal(err)
			}
			successor, _ := w.issueFor(t, newKey(t), w.org, "web-1", w.registeredAt.Add(time.Minute))
			w.register(t, createHost(t, w.d, w.org, w.zone, "web-1-new").ID, "web-1", successor)
			return leaf, id
		}},
		{"newest certificate records no key", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			// A row written before fingerprints were recorded, e.g. after
			// restoring an older backup.
			w.writeMarker(t)
			if _, err := w.d.Pool().Exec(ctx, `UPDATE agent_certificates SET public_key_sha256 = NULL WHERE serial = $1`,
				w.first.SerialNumber.String()); err != nil {
				t.Fatal(err)
			}
			return w.issue(t, w.org, "web-1", inside(w))
		}},
		{"no agent with that name", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			return w.issue(t, w.org, "web-2", inside(w))
		}},
		{"agent revoked", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			if err := w.svc.Revoke(ctx, w.org, w.host); err != nil {
				t.Fatal(err)
			}
			return w.issue(t, w.org, "web-1", inside(w))
		}},
		{"two agents with that name", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			twin := createHost(t, w.d, w.org, w.zone, "web-1-twin").ID
			leaf, _ := w.issue(t, w.org, "web-1", w.registeredAt.Add(time.Minute))
			w.register(t, twin, "web-1", leaf)
			return w.issue(t, w.org, "web-1", inside(w))
		}},
		{"agent of that name only in another organization", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			other := seedOrg(t, w.q, "adopt-other")
			return w.issue(t, other, "web-1", inside(w))
		}},
		{"no org", false, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			leaf, id := w.issue(t, w.org, "web-1", inside(w))
			id.OrgID = nil
			return leaf, id
		}},
		{"registered but revoked", true, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			leaf, id := w.issue(t, w.org, "web-1", inside(w))
			w.register(t, w.host, "web-1", leaf)
			if _, err := w.d.Pool().Exec(ctx, `UPDATE agent_certificates SET revoked_at = now() WHERE serial = $1`, leaf.SerialNumber.String()); err != nil {
				t.Fatal(err)
			}
			return leaf, id
		}},
		{"registered to another host", true, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			leaf, id := w.issue(t, w.org, "web-1", inside(w))
			w.register(t, createHost(t, w.d, w.org, w.zone, "web-2").ID, "web-2", leaf)
			return leaf, id
		}},
		{"registered, no restore marker", true, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			leaf, id := w.issue(t, w.org, "web-1", inside(w))
			w.register(t, w.host, "web-1", leaf)
			return leaf, id
		}},
		{"registered, issued after the restore", true, func(t *testing.T, w *adoptWorld) (*x509.Certificate, *ca.ClientIdentity) {
			w.writeMarker(t)
			leaf, id := w.issue(t, w.org, "web-1", w.restoredAt.Add(time.Second))
			w.register(t, w.host, "web-1", leaf)
			return leaf, id
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newAdoptWorld(t)
			leaf, id := tt.arrange(t, w)
			before := w.registry(t)
			_, err := w.svc.AdoptCertificate(ctx, leaf, id)
			if !errors.Is(err, agents.ErrNotFound) {
				t.Fatalf("AdoptCertificate err = %v, want ErrNotFound", err)
			}
			if after := w.registry(t); after != before {
				t.Fatalf("registry %s -> %s, want nothing changed", before, after)
			}
			if _, err := w.svc.GetBySerial(ctx, leaf.SerialNumber.String()); !tt.known && !errors.Is(err, agents.ErrNotFound) {
				t.Fatalf("GetBySerial after a refused adoption: %v, want ErrNotFound", err)
			}
		})
	}
}

// TestAdoptAgentCertificateChecksTheWindowBeforeLocking: without a
// restore marker, or with a certificate outside its window, the service
// refuses without asking the store to lock the agent rows — a refused
// agent's reconnect loop must not serialize with renewals and heartbeats.
func TestAdoptAgentCertificateChecksTheWindowBeforeLocking(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	id := &ca.ClientIdentity{Serial: big.NewInt(4242), Kind: ca.KindAgent, OrgID: &org, WorkerName: "web-1"}
	window := &restoremark.Window{BackupCreatedAt: now.Add(-2 * time.Hour), RestoredAt: now.Add(-time.Hour)}
	tests := []struct {
		name      string
		window    *restoremark.Window
		notBefore time.Time
	}{
		{"no restore marker", nil, now.Add(-90 * time.Minute)},
		{"issued before the backup", window, now.Add(-3 * time.Hour)},
		{"issued after the restore", window, now},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{window: tt.window}
			_, err := agents.NewService(store).AdoptCertificate(ctx, &x509.Certificate{NotBefore: tt.notBefore, NotAfter: tt.notBefore.Add(ca.AgentCertValidity)}, id)
			if !errors.Is(err, agents.ErrNotFound) {
				t.Fatalf("AdoptCertificate err = %v, want ErrNotFound", err)
			}
			if store.adoptCalls != 0 {
				t.Fatalf("store.AdoptCertificate called %d times, want none", store.adoptCalls)
			}
		})
	}
}
