// The worker-side bufconn harness: a real grpcsrv.Server (mTLS, real ca.CA
// with an in-memory store, fake ports) served over an in-memory listener,
// with the worker's own fakes for the narrow ports — grpcsrv's fakes are
// unexported, so this mirrors internal/grpcsrv/testfakes.go. No Docker.
package worker

import (
	"context"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/grpcsrv"
	"github.com/noixdev/pulsix/internal/restoremark"
	"github.com/noixdev/pulsix/internal/workers"
)

var (
	orgID  = uuid.Must(uuid.NewV7())
	zoneID = uuid.Must(uuid.NewV7())
)

// memCAStore keeps the sealed CA bytes in memory.
type memCAStore struct {
	mu    sync.Mutex
	saved []byte
}

func (m *memCAStore) Load(context.Context) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saved, m.saved != nil, nil
}

func (m *memCAStore) Save(_ context.Context, sealed []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved = sealed
	return nil
}

// fakeWorkerStore is the in-memory workers.Store for the registry service.
type fakeWorkerStore struct {
	mu          sync.Mutex
	bySerial    map[string]workers.Worker
	enrollments map[uuid.UUID]workers.Enrollment
	hashes      map[uuid.UUID][]byte // enrollment id → SHA-256 of its secret
}

func (f *fakeWorkerStore) Create(_ context.Context, w workers.Worker, c workers.Certificate) (workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bySerial[c.Serial.String()] = w
	return w, nil
}

func (f *fakeWorkerStore) List(context.Context, uuid.UUID) ([]workers.Worker, error) { return nil, nil }

func (f *fakeWorkerStore) GetByName(context.Context, uuid.UUID, string) (workers.Worker, error) {
	return workers.Worker{}, workers.ErrNotFound
}

func (f *fakeWorkerStore) LookupSerial(_ context.Context, serial *big.Int) (workers.Worker, workers.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w, ok := f.bySerial[serial.String()]; ok {
		return w, workers.Certificate{Serial: serial, OrgID: w.OrgID, WorkerID: w.ID}, nil
	}
	return workers.Worker{}, workers.Certificate{}, workers.ErrNotFound
}

func (f *fakeWorkerStore) Touch(context.Context, uuid.UUID, string) error { return nil }

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

func (f *fakeWorkerStore) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }

func (f *fakeWorkerStore) Get(context.Context, uuid.UUID, uuid.UUID) (workers.Worker, error) {
	return workers.Worker{}, workers.ErrNotFound
}

func (f *fakeWorkerStore) Revoke(context.Context, uuid.UUID, uuid.UUID) (workers.Worker, error) {
	return workers.Worker{}, workers.ErrNotFound
}

func (f *fakeWorkerStore) ViewDetails(context.Context, uuid.UUID) (workers.ViewDetails, error) {
	return workers.ViewDetails{}, nil
}

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

// RestoreWindow reports no restore: these tests never restore a backup.
func (f *fakeWorkerStore) RestoreWindow(context.Context) (*restoremark.Window, error) {
	return nil, nil
}

// AdoptCertificate adopts nothing: these tests never restore a backup.
func (f *fakeWorkerStore) AdoptCertificate(context.Context, string, workers.Certificate, func(workers.AdoptionState) error) (workers.Worker, error) {
	return workers.Worker{}, workers.ErrNotFound
}

// fakeClaims implements grpcsrv.Claimer from a queue the test seeds.
type fakeClaims struct {
	mu    sync.Mutex
	queue []*grpcsrv.JobClaim
	calls []grpcsrv.FakeClaimCall
}

func (f *fakeClaims) Claim(_ context.Context, scope grpcsrv.ClaimScope, version string, _ bool) (*grpcsrv.JobClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, grpcsrv.FakeClaimCall{Scope: scope, Version: version})
	if len(f.queue) == 0 {
		return nil, nil
	}
	c := f.queue[0]
	f.queue = f.queue[1:]
	return c, nil
}

func (f *fakeClaims) Enqueue(c *grpcsrv.JobClaim) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, c)
}

func (f *fakeClaims) Calls() []grpcsrv.FakeClaimCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]grpcsrv.FakeClaimCall(nil), f.calls...)
}

// fakeEvents implements grpcsrv.EventRecorder.
type fakeEvents struct {
	mu          sync.Mutex
	events      []grpcsrv.Event
	cancelling  map[uuid.UUID]bool
	appendDelay time.Duration // a slow event store: each Append takes this long
}

func (f *fakeEvents) Append(_ context.Context, ev grpcsrv.Event) error {
	f.mu.Lock()
	delay := f.appendDelay
	f.mu.Unlock()
	time.Sleep(delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeEvents) setAppendDelay(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendDelay = d
}

func (f *fakeEvents) Cancelled(_ context.Context, jobID uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelling[jobID], nil
}

func (f *fakeEvents) SetCancelling(jobID uuid.UUID, cancelling bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelling == nil {
		f.cancelling = map[uuid.UUID]bool{}
	}
	f.cancelling[jobID] = cancelling
}

func (f *fakeEvents) Events() []grpcsrv.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]grpcsrv.Event(nil), f.events...)
}

// fakeResults implements grpcsrv.ResultRecorder.
type fakeResults struct {
	mu      sync.Mutex
	results []grpcsrv.Result

	events         *fakeEvents // when set, Finish notes how many events were stored
	eventsAtFinish []int
}

func (f *fakeResults) Finish(_ context.Context, res grpcsrv.Result) error {
	stored := -1
	if f.events != nil {
		stored = len(f.events.Events())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, res)
	f.eventsAtFinish = append(f.eventsAtFinish, stored)
	return nil
}

func (f *fakeResults) storedEventsAtFinish() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.eventsAtFinish...)
}

func (f *fakeResults) Results() []grpcsrv.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]grpcsrv.Result(nil), f.results...)
}

// fakeAudit implements grpcsrv.SystemAuditor as a counter.
type fakeAudit struct {
	mu      sync.Mutex
	records int
}

func (f *fakeAudit) Record(context.Context, grpcsrv.SystemEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records++
	return nil
}

// harness is one fully wired mTLS manager server on a bufconn listener.
type harness struct {
	t       *testing.T
	ca      *ca.CA
	lis     *bufconn.Listener
	store   *fakeWorkerStore
	claims  *fakeClaims
	events  *fakeEvents
	results *fakeResults
	dir     string // scratch dir for PEM files
}

// newHarness builds the server exactly like internal/grpcsrv's own harness
// does: real CA, registry over a fake store, fake ports, bufconn listener.
// CancelPollInterval is small so cancel directives arrive without waiting
// half a second per tick.
func newHarness(t *testing.T) *harness {
	t.Helper()
	caInst, err := ca.New(&memCAStore{}, crypto.NewBox([32]byte{1, 2, 3}), ca.Config{
		CommonName: "pulsix-manager", ListenHost: "manager",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := caInst.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	claims, events, results := &fakeClaims{}, &fakeEvents{}, &fakeResults{}
	store := &fakeWorkerStore{bySerial: map[string]workers.Worker{}, enrollments: map[uuid.UUID]workers.Enrollment{}, hashes: map[uuid.UUID][]byte{}}
	gs, err := grpcsrv.New(context.Background(), grpcsrv.Deps{
		CA:      caInst,
		Workers: workers.NewService(store, workers.WithSigner(caInst)),
		Claims:  claims, Events: events, Results: results, Audit: &fakeAudit{},
		Clock:              time.Now,
		CancelPollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	return &harness{
		t: t, ca: caInst, lis: lis,
		store: store, claims: claims, events: events, results: results,
		dir: t.TempDir(),
	}
}

// issue mints a worker certificate like `worker add` does, writes the
// cert/key/CA PEM to files and returns a Config pointing at them.
// ManagerAddr names the server certificate's DNS SAN ("manager") so the
// client's ServerName derivation matches; the bufconn dialer replaces the
// transport underneath.
func (h *harness) issue(name string, register bool) Config {
	h.t.Helper()
	certPEM, keyPEM, serial, err := h.ca.Issue(context.Background(), &orgID, zoneID, name)
	if err != nil {
		h.t.Fatal(err)
	}
	certPath := filepath.Join(h.dir, "cert.pem")
	keyPath := filepath.Join(h.dir, "key.pem")
	caPath := filepath.Join(h.dir, "ca.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		h.t.Fatal(err)
	}
	_, caCert := h.ca.Pool()
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw}), 0o600); err != nil {
		h.t.Fatal(err)
	}
	if register {
		if _, err := workers.NewService(h.store).Register(
			context.Background(), orgID, workers.RegisterInput{ZoneID: zoneID, Name: name, CertSerial: serial},
		); err != nil {
			h.t.Fatal(err)
		}
	}
	return Config{
		ManagerAddr:   "manager:8444",
		CertPath:      certPath,
		KeyPath:       keyPath,
		CAPath:        caPath,
		DataDir:       h.dir,
		ClaimInterval: 10 * time.Millisecond,
		MaxConcurrent: 1,
	}
}

// dialWith dials the harness with cfg over a bufconn transport.
func (h *harness) dialWith(t *testing.T, cfg Config) (*grpc.ClientConn, error) {
	return dial(context.Background(), cfg, []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return h.lis.DialContext(ctx)
		}),
	})
}

// bufconnDialer routes a worker's dials to the harness listener.
func (h *harness) bufconnDialer() grpc.DialOption {
	return grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return h.lis.DialContext(ctx)
	})
}

// renewRoots is the CA pool a Renewer must be given: without it the
// renewer refuses to renew at all, which is the point (backlog 2.64).
func (h *harness) renewRoots(cfg Config) RenewerOption {
	h.t.Helper()
	pool, err := CAPool(cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	return WithRoots(pool)
}
