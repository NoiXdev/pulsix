// The agent loop against the REAL manager surface: the real grpcsrv
// listener (mTLS, interceptors, agent certificate class) with the real
// agentapi service mounted over a bufconn listener; the registry runs on
// in-memory fakes, the CA is real. This is what pins dial.go's pinned and
// mTLS transports and the loop's enrollment/stream/renew contract against
// the manager's actual behavior — no Docker involved.
package agent

import (
	"context"
	"io"
	"log/slog"
	"math/big"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/noixdev/pulsix/internal/agentapi"
	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/grpcsrv"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/workers"
)

// Facts keys the integration assertions read (Task 8's contract keys).
const (
	factsKeyVersion = "agent_version"
)

// stubWorkerStore keeps grpcsrv's Deps validation happy: the agent
// integration tests never exercise the worker surface.
type stubWorkerStore struct{}

func (s *stubWorkerStore) Create(_ context.Context, w workers.Worker, _ workers.Certificate) (workers.Worker, error) {
	return w, nil
}
func (s *stubWorkerStore) List(context.Context, uuid.UUID) ([]workers.Worker, error) { return nil, nil }
func (s *stubWorkerStore) GetByName(context.Context, uuid.UUID, string) (workers.Worker, error) {
	return workers.Worker{}, workers.ErrNotFound
}
func (s *stubWorkerStore) LookupSerial(context.Context, *big.Int) (workers.Worker, workers.Certificate, error) {
	return workers.Worker{}, workers.Certificate{}, workers.ErrNotFound
}
func (s *stubWorkerStore) Touch(context.Context, uuid.UUID, string) error            { return nil }
func (s *stubWorkerStore) AddCertificate(context.Context, workers.Certificate) error { return nil }
func (s *stubWorkerStore) Get(context.Context, uuid.UUID, uuid.UUID) (workers.Worker, error) {
	return workers.Worker{}, workers.ErrNotFound
}
func (s *stubWorkerStore) Revoke(context.Context, uuid.UUID, uuid.UUID) (workers.Worker, error) {
	return workers.Worker{}, workers.ErrNotFound
}
func (s *stubWorkerStore) ViewDetails(context.Context, uuid.UUID) (workers.ViewDetails, error) {
	return workers.ViewDetails{}, nil
}
func (s *stubWorkerStore) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (s *stubWorkerStore) CreateEnrollment(_ context.Context, e workers.Enrollment, _ []byte) (workers.Enrollment, error) {
	return e, nil
}
func (s *stubWorkerStore) ListEnrollments(context.Context, uuid.UUID, time.Time) ([]workers.Enrollment, error) {
	return nil, nil
}
func (s *stubWorkerStore) DeleteEnrollment(context.Context, uuid.UUID, uuid.UUID) error {
	return workers.ErrNotFound
}
func (s *stubWorkerStore) Enroll(context.Context, uuid.UUID, func(workers.Enrollment, []byte) (workers.Worker, workers.Certificate, error)) (workers.Worker, error) {
	return workers.Worker{}, workers.ErrNotFound
}
func (s *stubWorkerStore) AdoptCertificate(context.Context, string, workers.Certificate, func(workers.AdoptionState) error) (workers.Worker, error) {
	return workers.Worker{}, workers.ErrNotFound
}

// integAgentStore is the in-memory agents.Store: the claim mirrors the
// SQL store's shape (secret hash check, one use per claim), Register
// replaces the host's active serial (revoking previous ones).
type integAgentStore struct {
	mu       sync.Mutex
	tokens   map[uuid.UUID]agents.Token
	hashes   map[uuid.UUID][]byte
	agents   []agents.Agent
	certs    map[string]agents.AgentIdentity
	touches  []string
	touchErr error
}

func newIntegAgentStore() *integAgentStore {
	return &integAgentStore{
		tokens: map[uuid.UUID]agents.Token{}, hashes: map[uuid.UUID][]byte{},
		certs: map[string]agents.AgentIdentity{},
	}
}

func (f *integAgentStore) CreateToken(_ context.Context, t agents.Token, hash []byte) (agents.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[t.ID], f.hashes[t.ID] = t, hash
	return t, nil
}

func (f *integAgentStore) ListTokens(context.Context, uuid.UUID, time.Time) ([]agents.Token, error) {
	return nil, nil
}

func (f *integAgentStore) RevokeToken(context.Context, uuid.UUID, uuid.UUID) error {
	return agents.ErrNotFound
}

func (f *integAgentStore) EnrollmentOrg(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[id]
	if !ok {
		return uuid.Nil, agents.ErrTokenInvalid
	}
	return t.OrgID, nil
}

func (f *integAgentStore) Claim(_ context.Context, orgID, id uuid.UUID, fn func(context.Context, agents.Token, []byte) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[id]
	if !ok || t.OrgID != orgID {
		return agents.ErrTokenInvalid
	}
	if err := fn(context.Background(), t, f.hashes[id]); err != nil {
		return err
	}
	if t.UsedAt != nil || t.Uses >= t.MaxUses {
		return agents.ErrTokenInvalid
	}
	t.Uses++
	if t.Uses >= t.MaxUses {
		used := time.Now()
		t.UsedAt = &used
	}
	f.tokens[id] = t
	return nil
}

// Renew is Register guarded like the real store: the serial renewed from
// must still be live.
func (f *integAgentStore) Renew(ctx context.Context, a agents.Agent, fromSerial string) error {
	f.mu.Lock()
	id, known := f.certs[fromSerial]
	live := known && !id.Revoked
	f.mu.Unlock()
	if !live {
		return agents.ErrNotFound
	}
	return f.Register(ctx, a)
}

func (f *integAgentStore) Register(_ context.Context, a agents.Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for serial, identity := range f.certs {
		if identity.HostID == a.HostID && identity.OrgID == a.OrgID {
			identity.Revoked = true
			f.certs[serial] = identity
		}
	}
	for i := range f.agents {
		if f.agents[i].HostID == a.HostID && f.agents[i].OrgID == a.OrgID {
			f.agents[i] = a
			f.certs[a.CertSerial] = agents.AgentIdentity{OrgID: a.OrgID, HostID: a.HostID, Name: a.AgentName, Version: a.Version}
			return nil
		}
	}
	f.agents = append(f.agents, a)
	f.certs[a.CertSerial] = agents.AgentIdentity{OrgID: a.OrgID, HostID: a.HostID, Name: a.AgentName, Version: a.Version}
	return nil
}

func (f *integAgentStore) GetBySerial(_ context.Context, serial string) (agents.AgentIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.certs[serial]
	if !ok {
		return agents.AgentIdentity{}, agents.ErrNotFound
	}
	id.Serial = serial
	return id, nil
}

func (f *integAgentStore) Touch(_ context.Context, serial, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.touchErr != nil {
		return f.touchErr
	}
	f.touches = append(f.touches, serial+":"+version)
	return nil
}

func (f *integAgentStore) List(context.Context, uuid.UUID) ([]agents.Agent, error) { return nil, nil }

// GetByHost belongs to the API's host card, which this harness does not
// exercise: the agent reaches the manager over gRPC only.
func (f *integAgentStore) GetByHost(context.Context, uuid.UUID, uuid.UUID) (agents.Agent, error) {
	return agents.Agent{}, agents.ErrNotFound
}

func (f *integAgentStore) Revoke(context.Context, uuid.UUID, uuid.UUID) error {
	return agents.ErrNotFound
}

func (f *integAgentStore) RecordFacts(context.Context, uuid.UUID, uuid.UUID, map[string]string) error {
	return nil
}

func (f *integAgentStore) touchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.touches)
}

func (f *integAgentStore) claimsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.tokens {
		if t.Uses > 0 {
			n++
		}
	}
	return n
}

// integProvisioner is the in-memory agents.HostProvisioner.
type integProvisioner struct {
	mu        sync.Mutex
	monitored map[uuid.UUID]bool
	names     map[uuid.UUID]string
}

func (f *integProvisioner) GetHost(_ context.Context, _, hostID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.names[hostID]; !ok {
		return hosts.ErrNotFound
	}
	return nil
}

func (f *integProvisioner) CreateHost(_ context.Context, _, _ uuid.UUID, name string) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.Must(uuid.NewV7())
	f.names[id] = name
	return id, nil
}

func (f *integProvisioner) SetMonitored(_ context.Context, _, hostID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.monitored[hostID] = true
	return nil
}

// hostCount reports how many hosts the enrollment path created. Like
// every other accessor here it takes the lock: the manager's Enroll runs
// on a gRPC goroutine while the test polls from its own, so reading the
// map directly is a data race the race detector catches (it did).
func (f *integProvisioner) hostCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.names)
}

// monitoredOfFirst reports the flip of the (single) created host.
func (f *integProvisioner) monitoredOfFirst() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ok := range f.monitored {
		if ok {
			return true
		}
	}
	return false
}

// nameOfAny reports the host name for a host id held as a string (the
// registry's host ids are uuids; the string form is what the identity
// metadata carries).
func (f *integProvisioner) nameOfAny(hostID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, name := range f.names {
		if id.String() == hostID {
			return name
		}
	}
	return ""
}

// integSink records the facts pushes.
type integSink struct {
	mu    sync.Mutex
	calls []integFactCall
}

type integFactCall struct {
	HostID uuid.UUID
	Facts  map[string]string
}

func (f *integSink) RecordFacts(_ context.Context, _ uuid.UUID, hostID uuid.UUID, facts map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, integFactCall{HostID: hostID, Facts: facts})
	return nil
}

func (f *integSink) recorded() []integFactCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]integFactCall(nil), f.calls...)
}

// currentSerial is the live (non-revoked) certificate serial of the
// single host's agent.
func (h *integHarness) currentSerial() (string, bool) {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	for serial, id := range h.store.certs {
		if !id.Revoked {
			return serial, true
		}
	}
	return "", false
}

type integAuditor struct {
	mu  sync.Mutex
	n   int
	err error
}

func (f *integAuditor) Record(context.Context, grpcsrv.SystemEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.n++
	return nil
}

// quietLogger discards log output (the tests that assert logs use their
// own capture).
func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// factsCollector is the production Collector adapter's test double shape
// — the loop tests use it where the real facts collection is fine to run.
type factsCollector struct{}

func (factsCollector) Collect(ctx context.Context) (map[string]string, error) {
	return map[string]string{factsKeyVersion: "v0.3a-integ"}, nil
}

// integHarness is the manager side: real grpcsrv + agentapi over bufconn.
type integHarness struct {
	t         *testing.T
	ca        *ca.CA
	store     *integAgentStore
	prov      *integProvisioner
	sink      *integSink
	agentSvc  *agents.Service
	lis       *bufconn.Listener
	dialExtra []grpc.DialOption
	orgID     uuid.UUID
	zoneID    uuid.UUID
}

func newIntegHarness(t *testing.T) *integHarness {
	t.Helper()
	h := &integHarness{
		t:      t,
		store:  newIntegAgentStore(),
		prov:   &integProvisioner{monitored: map[uuid.UUID]bool{}, names: map[uuid.UUID]string{}},
		sink:   &integSink{},
		orgID:  uuid.Must(uuid.NewV7()),
		zoneID: uuid.Must(uuid.NewV7()),
	}
	h.ca = newTestCA(t)
	// The same CA the manager signs with: the token the harness mints
	// then carries the fingerprint the agent pins its first connection
	// against, exactly as in production.
	h.agentSvc = agents.NewService(h.store, agents.WithProvisioner(h.prov), agents.WithCA(h.ca))
	svc, err := agentapi.New(agentapi.Deps{
		CA: h.ca, Agents: h.agentSvc, Audit: &integAuditor{},
		Streams: agentapi.NewStreamRegistry(), Facts: h.sink, Clock: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.agentSvc.SetRevokeHook(svc.Streams())
	gs, err := grpcsrv.New(context.Background(), grpcsrv.Deps{
		CA:      h.ca,
		Workers: workers.NewService(&stubWorkerStore{}, workers.WithSigner(h.ca)),
		Agents:  h.agentSvc, AgentService: svc,
		Audit: &integAuditor{}, Clock: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.lis = bufconn.Listen(1024 * 1024)
	go func() { _ = gs.Serve(h.lis) }()
	t.Cleanup(gs.Stop)
	h.dialExtra = []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return h.lis.DialContext(ctx)
		}),
	}
	return h
}

// withToken creates an agent enrollment token and renders it with the
// CA's fingerprint (the token's pin is carried by the CALLER — the API
// layer fills it in production; agents.Service does not).
func (h *integHarness) withToken() string {
	h.t.Helper()
	tok, err := h.agentSvc.CreateToken(context.Background(), h.orgID, agents.TokenInput{
		ZoneID: h.zoneID, MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	full := enrolltoken.Token{ID: tok.ID, Secret: tok.Secret, CAPin: h.ca.CAPin()}
	return enrolltoken.AgentString(full)
}

// agentConfig builds a loop config pointed at the harness.
func (h *integHarness) agentConfig(dir, token string) Config {
	return Config{
		ManagerAddr:       "manager:8444",
		DataDir:           dir,
		AgentName:         testAgentName,
		Version:           "v0.3a-integ",
		HeartbeatInterval: 20 * time.Millisecond,
		FactsInterval:     200 * time.Millisecond,
		ConfigInterval:    30 * time.Second,
		CertPath:          filepath.Join(dir, certFileName),
		KeyPath:           filepath.Join(dir, keyFileName),
		MetaPath:          filepath.Join(dir, metaFileName),
		EnrollToken:       token,
	}
}

// harnessDialer is the harness's dialer: the production grpcDialer with
// the bufconn transport underneath.
func (h *integHarness) dialer(cfg Config) Dialer {
	return &grpcDialer{cfg: cfg, extra: h.dialExtra}
}

func TestLoopEnrollsStreamsAndRenewsAgainstRealManager(t *testing.T) {
	h := newIntegHarness(t)
	token := h.withToken()

	dir := t.TempDir()
	cfg := h.agentConfig(dir, token)
	l := NewLoop(cfg, h.dialer(cfg), &factsCollector{}, WithLogger(quietLogger()))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()

	// Enrollment: the host row exists (created, named, monitored), the
	// identity persisted, facts recorded, heartbeats touching.
	waitFor(t, 5*time.Second, func() bool {
		return h.prov.hostCount() == 1 && h.prov.monitoredOfFirst() &&
			len(h.sink.recorded()) >= 1 && h.store.touchCount() >= 1
	})
	id, err := LoadIdentity(dir)
	if err != nil || id == nil || !id.Valid(time.Now()) {
		t.Fatalf("identity after enrollment = %v, %v; want a valid identity", id, err)
	}
	if id.HostID() == "" {
		t.Fatal("the enrolled identity carries no host id")
	}
	if got := h.prov.nameOfAny(id.HostID()); got != testAgentName {
		t.Errorf("host name = %q, want %q", got, testAgentName)
	}
	if facts := h.sink.recorded()[0].Facts; facts[factsKeyVersion] != "v0.3a-integ" {
		t.Errorf("facts agent_version = %q, want v0.3a-integ (all: %v)", facts[factsKeyVersion], facts)
	}

	// A reconnect (a fresh loop, no token) must NOT re-enroll: the token
	// stays used-up, the new stream just reconnects.
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("first Run = %v, want nil", err)
	}

	// Restart the clock into the renewal window: the reconnect renews
	// (certificate-authenticated) before streaming again.
	renewCfg := h.agentConfig(dir, "")
	leaf := id.Leaf()
	lifetime := leaf.NotAfter.Sub(leaf.NotBefore)
	renewNow := func() time.Time { return leaf.NotBefore.Add(2*lifetime/3 + time.Hour) }
	l2 := NewLoop(renewCfg, h.dialer(renewCfg), &factsCollector{},
		WithLogger(quietLogger()), WithNow(renewNow))
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- l2.Run(ctx2) }()
	waitFor(t, 5*time.Second, func() bool {
		// The registry row's serial must have swapped to the renewed one
		// and the stream must be touching again.
		return h.store.claimsCount() == 1 && h.store.touchCount() >= 2 && h.renewedSerialDiffers(leaf.SerialNumber)
	})
	cancel2()
	if err := <-done2; err != nil {
		t.Fatalf("second Run = %v, want nil", err)
	}
	if uses := h.store.claimsCount(); uses != 1 {
		t.Errorf("token claims = %d, want 1 (a reconnect never re-enrolls)", uses)
	}
}

// renewedSerialDiffers reports that the host agent's live serial is not
// the one the first enrollment issued — the renewal swapped it.
func (h *integHarness) renewedSerialDiffers(old *big.Int) bool {
	serial, ok := h.currentSerial()
	if !ok {
		return false
	}
	current, ok := new(big.Int).SetString(serial, 10)
	if !ok {
		return false
	}
	return current.Cmp(old) != 0
}
