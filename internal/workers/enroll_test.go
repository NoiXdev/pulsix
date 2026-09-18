package workers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/workers"
)

// memCAStore keeps the sealed CA in memory.
type memCAStore struct{ saved []byte }

func (m *memCAStore) Load(context.Context) ([]byte, bool, error) { return m.saved, m.saved != nil, nil }
func (m *memCAStore) Save(_ context.Context, b []byte) error     { m.saved = b; return nil }

type fakeZones struct{ zones []hosts.Zone }

func (f fakeZones) ListZones(_ context.Context, orgID uuid.UUID) ([]hosts.Zone, error) {
	var out []hosts.Zone
	for _, z := range f.zones {
		if z.OrgID == orgID {
			out = append(out, z)
		}
	}
	return out, nil
}

type enrollWorld struct {
	svc            *workers.Service
	store          *fakeStore
	ca             *ca.CA
	org, zone      uuid.UUID
	actor          uuid.UUID
	now            time.Time
	existing       workers.Worker
	existingSerial *big.Int
}

func newEnrollWorld(t *testing.T) *enrollWorld {
	t.Helper()
	var key [32]byte
	copy(key[:], "pulsix-workers-test-key-0123456")
	authority, err := ca.New(&memCAStore{}, crypto.NewBox(key), ca.Config{CommonName: "pulsix-manager", ListenHost: "manager"})
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	w := &enrollWorld{store: &fakeStore{}, ca: authority, org: uuid.Must(uuid.NewV7()), zone: uuid.Must(uuid.NewV7()),
		actor: uuid.Must(uuid.NewV7()), now: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC), existingSerial: big.NewInt(777)}
	w.svc = workers.NewService(w.store, workers.WithSigner(authority),
		workers.WithZones(fakeZones{zones: []hosts.Zone{{ID: w.zone, OrgID: w.org, Slug: "default"}}}),
		workers.WithClock(func() time.Time { return w.now }))
	w.existing, err = w.svc.Register(ctx, w.org, workers.RegisterInput{ZoneID: w.zone, Name: "old-1", CertSerial: w.existingSerial})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// newCSR is an ECDSA P-256 certificate request as the worker sends it.
func newCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestCreateEnrollmentAndEnroll(t *testing.T) {
	w := newEnrollWorld(t)
	e, token, err := w.svc.CreateEnrollment(ctx, w.org, &w.actor, w.zone, "fra-1")
	if err != nil {
		t.Fatal(err)
	}
	if !e.ExpiresAt.Equal(w.now.Add(workers.EnrollmentTTL)) {
		t.Fatalf("expires = %v", e.ExpiresAt)
	}
	tok, err := enrolltoken.Parse(token)
	if err != nil || tok.ID != e.ID || tok.CAPin != w.ca.CAPin() {
		t.Fatalf("token = %+v, %v", tok, err)
	}
	if hash := w.store.enrollHashes[e.ID]; bytes.Equal(hash, tok.Secret) || len(hash) != 32 {
		t.Fatal("only the SHA-256 of the secret may be stored")
	}
	if pending, _ := w.svc.ListEnrollments(ctx, w.org); len(pending) != 1 {
		t.Fatalf("pending = %v", pending)
	}
	res, err := w.svc.Enroll(ctx, workers.EnrollInput{TokenID: tok.ID, Secret: tok.Secret, CSRPEM: newCSR(t), Version: "v0.2c"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Worker.Name != "fra-1" || res.Worker.ZoneID != w.zone || res.Worker.Status != workers.StatusActive || len(res.CAPEM) == 0 {
		t.Fatalf("result = %+v", res)
	}
	leaf, err := ca.ParseLeaf(res.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.GetBySerial(ctx, leaf.SerialNumber); err != nil {
		t.Fatalf("the enrolled certificate must authenticate: %v", err)
	}
	// Single use.
	if _, err := w.svc.Enroll(ctx, workers.EnrollInput{TokenID: tok.ID, Secret: tok.Secret, CSRPEM: newCSR(t)}); !errors.Is(err, workers.ErrEnrollmentRefused) {
		t.Fatalf("second use: %v", err)
	}
}

func TestEnrollRefusals(t *testing.T) {
	w := newEnrollWorld(t)
	_, token, _ := w.svc.CreateEnrollment(ctx, w.org, &w.actor, w.zone, "fra-2")
	tok, _ := enrolltoken.Parse(token)
	wrong := bytes.Repeat([]byte{9}, 32)
	if _, err := w.svc.Enroll(ctx, workers.EnrollInput{TokenID: tok.ID, Secret: wrong, CSRPEM: newCSR(t)}); !errors.Is(err, workers.ErrEnrollmentRefused) {
		t.Fatalf("wrong secret: %v", err)
	}
	if _, err := w.svc.Enroll(ctx, workers.EnrollInput{TokenID: uuid.Must(uuid.NewV7()), Secret: tok.Secret, CSRPEM: newCSR(t)}); !errors.Is(err, workers.ErrEnrollmentRefused) {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := w.svc.Enroll(ctx, workers.EnrollInput{TokenID: tok.ID, Secret: tok.Secret, CSRPEM: []byte("x")}); !errors.Is(err, workers.ErrBadCSR) {
		t.Fatalf("bad csr: %v", err)
	}
	// A bad CSR must not burn the token: the redeem transaction rolls
	// back, so a later valid redeem of the same token still succeeds.
	// (Uses its own enrollment so it does not disturb the expiry check
	// below, which needs tok to still be pending.)
	_, notBurnedToken, _ := w.svc.CreateEnrollment(ctx, w.org, &w.actor, w.zone, "fra-2b")
	notBurned, _ := enrolltoken.Parse(notBurnedToken)
	if _, err := w.svc.Enroll(ctx, workers.EnrollInput{TokenID: notBurned.ID, Secret: notBurned.Secret, CSRPEM: []byte("x")}); !errors.Is(err, workers.ErrBadCSR) {
		t.Fatalf("bad csr (not-burned setup): %v", err)
	}
	if _, err := w.svc.Enroll(ctx, workers.EnrollInput{TokenID: notBurned.ID, Secret: notBurned.Secret, CSRPEM: newCSR(t)}); err != nil {
		t.Fatalf("a bad CSR must not burn the token — retry with a valid CSR: %v", err)
	}
	w.now = w.now.Add(workers.EnrollmentTTL + time.Second)
	if _, err := w.svc.Enroll(ctx, workers.EnrollInput{TokenID: tok.ID, Secret: tok.Secret, CSRPEM: newCSR(t)}); !errors.Is(err, workers.ErrEnrollmentRefused) {
		t.Fatalf("expired: %v", err)
	}
}

func TestCreateEnrollmentValidationAndWithdraw(t *testing.T) {
	w := newEnrollWorld(t)
	for name, c := range map[string]struct {
		zone uuid.UUID
		name string
		want error
	}{
		"bad name":      {w.zone, "Bad_Name", workers.ErrInvalidInput},
		"foreign zone":  {uuid.Must(uuid.NewV7()), "x", workers.ErrInvalidInput},
		"existing name": {w.zone, w.existing.Name, workers.ErrNameTaken},
	} {
		if _, _, err := w.svc.CreateEnrollment(ctx, w.org, &w.actor, c.zone, c.name); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	e, _, err := w.svc.CreateEnrollment(ctx, w.org, &w.actor, w.zone, "fra-3")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.svc.WithdrawEnrollment(ctx, w.org, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.WithdrawEnrollment(ctx, w.org, e.ID); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("second withdraw: %v", err)
	}
}

func TestGetBySerialRefusesRevokedCertificatesAndWorkers(t *testing.T) {
	w := newEnrollWorld(t)
	serial := w.existingSerial
	if _, err := w.svc.GetBySerial(ctx, serial); err != nil {
		t.Fatal(err)
	}
	w.store.revokeCert(serial)
	if _, err := w.svc.GetBySerial(ctx, serial); !errors.Is(err, workers.ErrRevoked) {
		t.Fatalf("revoked certificate: %v", err)
	}
	w.store.unrevokeCert(serial)
	w.store.setStatus(w.existing.ID, workers.StatusRevoked)
	if _, err := w.svc.GetBySerial(ctx, serial); !errors.Is(err, workers.ErrRevoked) {
		t.Fatalf("revoked worker: %v", err)
	}
	if _, err := w.svc.GetBySerial(ctx, big.NewInt(999)); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := w.svc.GetBySerial(ctx, nil); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("nil: %v", err)
	}
}

func TestRenewAddsACertificate(t *testing.T) {
	w := newEnrollWorld(t)
	certPEM, err := w.svc.Renew(ctx, w.existing, newCSR(t))
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := ca.ParseLeaf(certPEM)
	got, err := w.svc.GetBySerial(ctx, leaf.SerialNumber)
	if err != nil || got.ID != w.existing.ID || got.CertSerial.Cmp(leaf.SerialNumber) != 0 {
		t.Fatalf("renewed lookup = %+v, %v", got, err)
	}
	if _, err := w.svc.GetBySerial(ctx, w.existingSerial); err != nil {
		t.Fatalf("the old certificate stays valid: %v", err)
	}
	if _, err := w.svc.Renew(ctx, w.existing, []byte("x")); !errors.Is(err, workers.ErrBadCSR) {
		t.Fatalf("bad csr: %v", err)
	}
}

func TestRegisterOrAttachReusesTheRow(t *testing.T) {
	w := newEnrollWorld(t)
	got, err := w.svc.RegisterOrAttach(ctx, w.org, workers.RegisterInput{ZoneID: w.zone, Name: w.existing.Name, CertSerial: big.NewInt(4242)})
	if err != nil || got.ID != w.existing.ID {
		t.Fatalf("attach = %+v, %v", got, err)
	}
	if _, err := w.svc.GetBySerial(ctx, big.NewInt(4242)); err != nil {
		t.Fatalf("attached certificate: %v", err)
	}
	fresh, err := w.svc.RegisterOrAttach(ctx, w.org, workers.RegisterInput{ZoneID: w.zone, Name: "embedded", CertSerial: big.NewInt(4244)})
	if err != nil || fresh.ID == w.existing.ID {
		t.Fatalf("register = %+v, %v", fresh, err)
	}
	w.store.setStatus(w.existing.ID, workers.StatusRevoked)
	if _, err := w.svc.RegisterOrAttach(ctx, w.org, workers.RegisterInput{ZoneID: w.zone, Name: w.existing.Name, CertSerial: big.NewInt(4243)}); !errors.Is(err, workers.ErrRevoked) {
		t.Fatalf("revoked row: %v", err)
	}
}

// TestRegisterOrAttachRefusesAnotherZone: attaching a certificate for zone B
// to a row enrolled in zone A would hand out a certificate whose zone SAN
// the interceptor refuses (the row's zone wins), so the attach is refused
// up front and nothing is recorded.
func TestRegisterOrAttachRefusesAnotherZone(t *testing.T) {
	w := newEnrollWorld(t)
	other := uuid.Must(uuid.NewV7())
	_, err := w.svc.RegisterOrAttach(ctx, w.org, workers.RegisterInput{ZoneID: other, Name: w.existing.Name, CertSerial: big.NewInt(4245)})
	if !errors.Is(err, workers.ErrZoneMismatch) {
		t.Fatalf("attach in another zone: %v, want ErrZoneMismatch", err)
	}
	if _, err := w.svc.GetBySerial(ctx, big.NewInt(4245)); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("the refused certificate must not be recorded: %v", err)
	}
}

// TestEmbeddedNameIsReserved: only the manager's embedded worker
// (RegisterOrAttach) may use the name "embedded"; the CLI registration and
// token enrollment refuse it, so no remote worker can squat on the row the
// embedded worker re-attaches to.
func TestEmbeddedNameIsReserved(t *testing.T) {
	w := newEnrollWorld(t)
	if _, err := w.svc.Register(ctx, w.org, workers.RegisterInput{ZoneID: w.zone, Name: workers.EmbeddedName, CertSerial: big.NewInt(4246)}); !errors.Is(err, workers.ErrInvalidInput) {
		t.Fatalf("Register(embedded): %v, want ErrInvalidInput", err)
	}
	if _, _, err := w.svc.CreateEnrollment(ctx, w.org, &w.actor, w.zone, workers.EmbeddedName); !errors.Is(err, workers.ErrInvalidInput) {
		t.Fatalf("CreateEnrollment(embedded): %v, want ErrInvalidInput", err)
	}
	if pending, _ := w.svc.ListEnrollments(ctx, w.org); len(pending) != 0 {
		t.Fatalf("a refused enrollment must not be stored: %v", pending)
	}
	got, err := w.svc.RegisterOrAttach(ctx, w.org, workers.RegisterInput{ZoneID: w.zone, Name: workers.EmbeddedName, CertSerial: big.NewInt(4247)})
	if err != nil || got.Name != workers.EmbeddedName {
		t.Fatalf("the embedded worker's own path: %+v, %v", got, err)
	}
}
