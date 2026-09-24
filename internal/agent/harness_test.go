// Shared test harness for the agent package: a real instance CA with an
// in-memory store (the same shape the agentapi test harness uses), test
// keys/CSRs and an enrolled-data-dir helper. The loop tests add gRPC
// fakes on top.
package agent

import (
	"context"
	"crypto/ecdsa"
	"encoding/pem"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/crypto"
)

var testOrgID = uuid.Must(uuid.NewV7())

// testCAStore keeps the sealed CA bytes in memory.
type testCAStore struct {
	mu    sync.Mutex
	saved []byte
}

func (m *testCAStore) Load(context.Context) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saved, m.saved != nil, nil
}

func (m *testCAStore) Save(_ context.Context, sealed []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved = sealed
	return nil
}

// newTestCA builds the in-memory instance CA and ensures its key exists.
func newTestCA(t *testing.T) *ca.CA {
	t.Helper()
	return newTestCAWithClock(t, nil)
}

// expiredCA builds an instance CA whose clock sits at offset (negative
// into the past), so certificates it issues carry shifted validity
// windows — an expired agent identity for the remediation tests.
func expiredCA(t *testing.T, offset time.Duration) *ca.CA {
	t.Helper()
	return newTestCAWithClock(t, func() time.Time { return time.Now().Add(offset) })
}

// newTestCAWithClock builds the in-memory instance CA with an optional
// clock override (tests).
func newTestCAWithClock(t *testing.T, now func() time.Time) *ca.CA {
	t.Helper()
	var key [32]byte
	copy(key[:], "pulsix-agent-test-box-key-1")
	opts := []ca.Option{}
	if now != nil {
		opts = append(opts, ca.WithClock(now))
	}
	inst, err := ca.New(&testCAStore{}, crypto.NewBox(key), ca.Config{
		CommonName: "pulsix-manager", ListenHost: "manager",
	}, opts...)
	if err != nil {
		t.Fatalf("ca.New: %v", err)
	}
	if err := inst.Ensure(context.Background()); err != nil {
		t.Fatalf("ca.Ensure: %v", err)
	}
	return inst
}

// newTestKey generates a key and its CSR for the test agent name.
func newTestKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	csrPEM, err := MakeCSR(key, testAgentName)
	if err != nil {
		t.Fatalf("make CSR: %v", err)
	}
	return key, csrPEM
}

// issueAgentCert mints an agent certificate for the test org and returns
// the leaf PEM and the CA PEM.
func issueAgentCert(t *testing.T, csrPEM []byte) (certPEM, caPEM []byte) {
	t.Helper()
	caInst := newTestCA(t)
	certPEM, _, err := caInst.SignAgentCSR(csrPEM, testOrgID, testAgentName)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}
	_, caCert := caInst.Pool()
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
	return certPEM, caPEM
}

// enrollDataDir prepares a data dir holding a freshly enrolled agent
// identity and returns the loaded identity plus the dir path.
func enrollDataDir(t *testing.T) (*Identity, string) {
	t.Helper()
	dir := t.TempDir()
	key, csrPEM := newTestKey(t)
	certPEM, caPEM := issueAgentCert(t, csrPEM)
	if err := SaveEnrollment(dir, key, certPEM, caPEM, "host-1"); err != nil {
		t.Fatalf("SaveEnrollment: %v", err)
	}
	id, err := LoadIdentity(dir)
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if id == nil {
		t.Fatal("LoadIdentity = nil after SaveEnrollment")
	}
	return id, dir
}

// testAgentName is the name every test certificate carries.
const testAgentName = "staging-node-1"
