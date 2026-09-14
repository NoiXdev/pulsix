// The in-process gRPC test harness: the server over a bufconn listener,
// the TLS material built from a real ca.CA (in-memory store), and clients
// built from issued worker certificates. No Docker involved.
package grpcsrv

import (
	"context"
	"crypto/tls"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/workers"
)

// fakeCAStore keeps the sealed CA bytes in memory.
type fakeCAStore struct {
	mu      sync.Mutex
	saved   []byte
	saveErr error
}

func (f *fakeCAStore) Load(context.Context) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saved, f.saved != nil, nil
}

func (f *fakeCAStore) Save(_ context.Context, sealed []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = sealed
	return nil
}

// fakeWorkerStore is the in-memory workers.Store the harness runs the
// registry service on.
type fakeWorkerStore struct {
	mu       sync.Mutex
	bySerial map[string]workers.Worker
	touched  map[string]string // serial → last touched version
}

func (f *fakeWorkerStore) Create(_ context.Context, w workers.Worker) (workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bySerial[w.CertSerial.String()] = w
	return w, nil
}

func (f *fakeWorkerStore) List(_ context.Context, orgID uuid.UUID) ([]workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []workers.Worker
	for _, w := range f.bySerial {
		if w.OrgID == orgID {
			out = append(out, w)
		}
	}
	return out, nil
}

func (f *fakeWorkerStore) GetBySerial(_ context.Context, serial *big.Int) (workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if serial == nil {
		return workers.Worker{}, workers.ErrNotFound
	}
	w, ok := f.bySerial[serial.String()]
	if !ok {
		return workers.Worker{}, workers.ErrNotFound
	}
	return w, nil
}

func (f *fakeWorkerStore) Touch(_ context.Context, serial *big.Int, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if serial == nil {
		return nil
	}
	if _, ok := f.bySerial[serial.String()]; !ok {
		return nil // an unknown serial is a silent no-op
	}
	f.touched[serial.String()] = version
	return nil
}

func (f *fakeWorkerStore) Delete(_ context.Context, _, _ uuid.UUID) error { return nil }

// harness is one fully wired server over a bufconn listener.
type harness struct {
	t *testing.T

	ca    *ca.CA
	srv   *Server
	gs    *grpc.Server
	lis   *bufconn.Listener
	store *fakeWorkerStore

	claims  *FakeClaimer
	events  *FakeEventRecorder
	results *FakeResultRecorder
	audit   *FakeSystemAuditor

	workerCert []byte // cached standard worker certificate
	workerKey  []byte

	mu  sync.Mutex
	now time.Time
}

// clock is the injected Deps.Clock.
func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

// advance moves the injected clock forward.
func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

// newHarness builds the server, serves it on a fresh bufconn listener and
// registers cleanup.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:       t,
		store:   &fakeWorkerStore{bySerial: map[string]workers.Worker{}, touched: map[string]string{}},
		claims:  &FakeClaimer{},
		events:  &FakeEventRecorder{},
		results: &FakeResultRecorder{},
		audit:   &FakeSystemAuditor{},
		now:     time.Now(),
	}
	var key [32]byte
	copy(key[:], "pulsix-grpcsrv-test-key-0123")
	caInst, err := ca.New(&fakeCAStore{}, crypto.NewBox(key), ca.Config{CommonName: "pulsix-manager", ListenHost: "manager"})
	if err != nil {
		t.Fatal(err)
	}
	h.ca = caInst
	if err := h.ca.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		CA: h.ca, Workers: workers.NewService(h.store),
		Claims: h.claims, Events: h.events, Results: h.results, Audit: h.audit,
		Clock: h.clock, CancelPollInterval: 10 * time.Millisecond,
	}
	h.srv, err = newServer(deps)
	if err != nil {
		t.Fatal(err)
	}
	h.lis = bufconn.Listen(1024 * 1024)
	h.gs = h.srv.newGRPCServer()
	go func() { _ = h.gs.Serve(h.lis) }()
	t.Cleanup(h.gs.Stop)
	return h
}

// enqueueClaim registers one claim for a job the test names.
func (h *harness) enqueueClaim(jobID, orgID, zoneID uuid.UUID) {
	h.claims.Enqueue(&JobClaim{
		Job:   workerpb.Job{Id: jobID.String(), Kind: "run", OrgId: orgID.String(), ZoneId: zoneID.String()},
		Token: testToken(jobID),
	})
}

// testToken derives the deterministic token the fakes use.
func testToken(jobID uuid.UUID) string { return "tok-" + jobID.String() }

// issue issues a worker certificate and registers the serial with the
// registry service, exactly like `worker add` does.
func (h *harness) issue(name string, orgID, zoneID uuid.UUID, register bool) (certPEM, keyPEM []byte, serial *big.Int) {
	t := h.t
	t.Helper()
	certPEM, keyPEM, serial, err := h.ca.Issue(context.Background(), &orgID, zoneID, name)
	if err != nil {
		t.Fatal(err)
	}
	if register {
		if _, err := workers.NewService(h.store).Register(context.Background(), orgID, workers.RegisterInput{
			ZoneID: zoneID, Name: name, CertSerial: serial,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return certPEM, keyPEM, serial
}

// client dials the bufconn listener with (certPEM, keyPEM) as the client
// certificate — nil, nil means no client certificate at all.
func (h *harness) client(certPEM, keyPEM []byte) workerpb.WorkerServiceClient {
	h.t.Helper()
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "manager"}
	if certPEM != nil {
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			h.t.Fatal(err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	pool, _ := h.ca.Pool()
	cfg.RootCAs = pool
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return h.lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
	)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = conn.Close() })
	return workerpb.NewWorkerServiceClient(conn)
}
