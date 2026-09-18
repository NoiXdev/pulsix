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
// registry service on. bySerial maps every certificate serial a worker
// holds to its row; touched maps a serial to the last touched version.
type fakeWorkerStore struct {
	mu          sync.Mutex
	bySerial    map[string]workers.Worker
	revoked     map[string]bool // serial → certificate revoked
	touched     map[string]string
	enrollments map[uuid.UUID]workers.Enrollment
	hashes      map[uuid.UUID][]byte // enrollment id → SHA-256 of its secret
}

func (f *fakeWorkerStore) Create(_ context.Context, w workers.Worker, c workers.Certificate) (workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bySerial[c.Serial.String()] = w
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

func (f *fakeWorkerStore) GetByName(_ context.Context, orgID uuid.UUID, name string) (workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.bySerial {
		if w.OrgID == orgID && w.Name == name {
			return w, nil
		}
	}
	return workers.Worker{}, workers.ErrNotFound
}

func (f *fakeWorkerStore) LookupSerial(_ context.Context, serial *big.Int) (workers.Worker, workers.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if serial == nil {
		return workers.Worker{}, workers.Certificate{}, workers.ErrNotFound
	}
	w, ok := f.bySerial[serial.String()]
	if !ok {
		return workers.Worker{}, workers.Certificate{}, workers.ErrNotFound
	}
	c := workers.Certificate{Serial: serial, OrgID: w.OrgID, WorkerID: w.ID}
	if f.revoked[serial.String()] {
		now := time.Now()
		c.RevokedAt = &now
	}
	return w, c, nil
}

// GetBySerial is a test helper: the row a serial resolves to.
func (f *fakeWorkerStore) GetBySerial(ctx context.Context, serial *big.Int) (workers.Worker, error) {
	w, _, err := f.LookupSerial(ctx, serial)
	return w, err
}

func (f *fakeWorkerStore) Touch(_ context.Context, workerID uuid.UUID, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for s, w := range f.bySerial {
		if w.ID == workerID {
			f.touched[s] = version
		}
	}
	return nil
}

func (f *fakeWorkerStore) AddCertificate(_ context.Context, c workers.Certificate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.bySerial {
		if w.ID == c.WorkerID {
			f.bySerial[c.Serial.String()] = w
			return nil
		}
	}
	return workers.ErrNotFound
}

func (f *fakeWorkerStore) Delete(_ context.Context, _, _ uuid.UUID) error { return nil }

func (f *fakeWorkerStore) CreateEnrollment(_ context.Context, e workers.Enrollment, hash []byte) (workers.Enrollment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enrollments[e.ID], f.hashes[e.ID] = e, hash
	return e, nil
}

func (f *fakeWorkerStore) ListEnrollments(context.Context, uuid.UUID, time.Time) ([]workers.Enrollment, error) {
	return nil, nil
}

func (f *fakeWorkerStore) DeleteEnrollment(context.Context, uuid.UUID, uuid.UUID) error {
	return workers.ErrNotFound
}

func (f *fakeWorkerStore) Enroll(_ context.Context, id uuid.UUID, fn func(workers.Enrollment, []byte) (workers.Worker, workers.Certificate, error)) (workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.enrollments[id]
	if !ok {
		return workers.Worker{}, workers.ErrNotFound
	}
	w, c, err := fn(e, f.hashes[id])
	if err != nil {
		return workers.Worker{}, err
	}
	for _, other := range f.bySerial { // UNIQUE (org_id, name), as in the SQL store
		if other.OrgID == w.OrgID && other.Name == w.Name {
			return workers.Worker{}, workers.ErrNameTaken
		}
	}
	f.bySerial[c.Serial.String()] = w
	now := time.Now()
	e.UsedAt = &now
	f.enrollments[id] = e
	return w, nil
}

// harness is one fully wired server over a bufconn listener.
type harness struct {
	t *testing.T

	ca      *ca.CA
	caStore *fakeCAStore // the CA's sealed state, to open it again with another clock
	caBox   *crypto.Box
	srv     *Server
	gs      *grpc.Server
	lis     *bufconn.Listener
	store   *fakeWorkerStore

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
		t: t,
		store: &fakeWorkerStore{bySerial: map[string]workers.Worker{}, revoked: map[string]bool{}, touched: map[string]string{},
			enrollments: map[uuid.UUID]workers.Enrollment{}, hashes: map[uuid.UUID][]byte{}},
		claims:  &FakeClaimer{},
		events:  &FakeEventRecorder{},
		results: &FakeResultRecorder{},
		audit:   &FakeSystemAuditor{},
		now:     time.Now(),
	}
	var key [32]byte
	copy(key[:], "pulsix-grpcsrv-test-key-0123")
	h.caStore, h.caBox = &fakeCAStore{}, crypto.NewBox(key)
	caInst, err := ca.New(h.caStore, h.caBox, ca.Config{CommonName: "pulsix-manager", ListenHost: "manager"})
	if err != nil {
		t.Fatal(err)
	}
	h.ca = caInst
	if err := h.ca.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		CA: h.ca, Workers: workers.NewService(h.store, workers.WithSigner(h.ca)),
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
	return workerpb.NewWorkerServiceClient(h.conn(certPEM, keyPEM))
}

// conn is client's raw connection, for calls by method name.
func (h *harness) conn(certPEM, keyPEM []byte) *grpc.ClientConn {
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
	return conn
}
