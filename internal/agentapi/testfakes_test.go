// The in-process gRPC test harness for the agent service: the real
// grpcsrv listener machinery (mTLS, interceptors) with the agent service
// mounted on it, over a bufconn listener; the agents registry runs on
// in-memory fakes, the CA is real. No Docker involved.
package agentapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/grpcsrv"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
	"github.com/noixdev/pulsix/internal/workers"
)

// fakeProvisioner is the in-memory HostProvisioner: seeded hosts resolve,
// created hosts join the org's list, SetMonitored records the flip.
type fakeProvisioner struct {
	mu        sync.Mutex
	hosts     map[uuid.UUID][]string
	ids       []uuid.UUID
	monitored map[uuid.UUID]bool
	flipErr   error
}

func (f *fakeProvisioner) init() {
	if f.hosts == nil {
		f.hosts = map[uuid.UUID][]string{}
		f.monitored = map[uuid.UUID]bool{}
	}
}

func (f *fakeProvisioner) seedHost(orgID uuid.UUID, name string) uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	id := uuid.Must(uuid.NewV7())
	f.hosts[orgID] = append(f.hosts[orgID], name)
	f.ids = append(f.ids, id)
	return id
}

func (f *fakeProvisioner) GetHost(_ context.Context, orgID, hostID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.ids {
		if id == hostID {
			return nil
		}
	}
	return agents.ErrNotFound
}

func (f *fakeProvisioner) CreateHost(_ context.Context, orgID, _ uuid.UUID, name string) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	for _, existing := range f.hosts[orgID] {
		if existing == name {
			return uuid.Nil, agents.ErrTokenConflict
		}
	}
	id := uuid.Must(uuid.NewV7())
	f.hosts[orgID] = append(f.hosts[orgID], name)
	f.ids = append(f.ids, id)
	return id, nil
}

func (f *fakeProvisioner) SetMonitored(_ context.Context, _, hostID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.flipErr != nil {
		return f.flipErr
	}
	f.init()
	f.monitored[hostID] = true
	return nil
}

// monitoredOf exposes the flip state to tests.
func (f *fakeProvisioner) monitoredOf(hostID uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.monitored[hostID]
}

// fakeAgentStore is the in-memory agents.Store. The claim mirrors the SQL
// store's shape: unknown tokens are refused directly, fn runs, then one
// use is consumed with the same "0 rows → refusal" semantics the
// conditional UPDATE carries — so two concurrent claims of the last use
// cannot both win.
type fakeAgentStore struct {
	mu                sync.Mutex
	tokens            map[uuid.UUID]agents.Token
	hashes            map[uuid.UUID][]byte
	agentsR           []agents.Agent
	certs             map[string]agents.AgentIdentity // serial → identity
	revokedC          map[string]bool
	touched           []string // "serial:version" per Touch call
	touchErr          error
	lookupErr         error
	revokeAfterLookup string
	orgErr            error // EnrollmentOrg fault
	claimErr          error // Claim fault (after fn)
	registerErr       error // Register fault
	faultNextLookup   bool  // arm: the next lookup proceeds, the one after fails
	faultAfterNext    bool
}

func newFakeAgentStore() *fakeAgentStore {
	return &fakeAgentStore{
		tokens:   map[uuid.UUID]agents.Token{},
		hashes:   map[uuid.UUID][]byte{},
		certs:    map[string]agents.AgentIdentity{},
		revokedC: map[string]bool{},
	}
}

func (f *fakeAgentStore) CreateToken(_ context.Context, t agents.Token, hash []byte) (agents.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[t.ID], f.hashes[t.ID] = t, hash
	return t, nil
}

func (f *fakeAgentStore) ListTokens(_ context.Context, orgID uuid.UUID, now time.Time) ([]agents.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []agents.Token
	for _, t := range f.tokens {
		if t.OrgID == orgID && t.UsedAt == nil && now.Before(t.ExpiresAt) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeAgentStore) RevokeToken(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[id]
	if !ok || t.OrgID != orgID || t.UsedAt != nil {
		return agents.ErrNotFound
	}
	delete(f.tokens, id)
	return nil
}

func (f *fakeAgentStore) EnrollmentOrg(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.orgErr != nil {
		return uuid.Nil, f.orgErr
	}
	t, ok := f.tokens[id]
	if !ok {
		return uuid.Nil, agents.ErrTokenInvalid
	}
	return t.OrgID, nil
}

func (f *fakeAgentStore) Claim(_ context.Context, orgID, id uuid.UUID, fn func(context.Context, agents.Token, []byte) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[id]
	if !ok || t.OrgID != orgID {
		return agents.ErrTokenInvalid
	}
	if err := fn(context.Background(), t, f.hashes[id]); err != nil {
		return err
	}
	if f.claimErr != nil {
		return f.claimErr
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

func (f *fakeAgentStore) Register(_ context.Context, a agents.Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.registerErr != nil {
		return f.registerErr
	}
	// Replace: every previous certificate of this host is revoked.
	for serial, identity := range f.certs {
		if identity.HostID == a.HostID && identity.OrgID == a.OrgID {
			f.revokedC[serial] = true
		}
	}
	for i := range f.agentsR {
		if f.agentsR[i].HostID == a.HostID && f.agentsR[i].OrgID == a.OrgID {
			f.agentsR[i] = a
			f.certs[a.CertSerial] = agents.AgentIdentity{OrgID: a.OrgID, HostID: a.HostID, Name: a.AgentName, Version: a.Version}
			return nil
		}
	}
	f.agentsR = append(f.agentsR, a)
	f.certs[a.CertSerial] = agents.AgentIdentity{OrgID: a.OrgID, HostID: a.HostID, Name: a.AgentName, Version: a.Version}
	return nil
}

func (f *fakeAgentStore) GetBySerial(_ context.Context, serial string) (agents.AgentIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.faultNextLookup {
		// This lookup proceeds normally and arms the one after it — the
		// handler's post-track re-check — to fail.
		f.faultNextLookup = false
		f.faultAfterNext = true
	} else if f.faultAfterNext {
		f.faultAfterNext = false
		return agents.AgentIdentity{}, errors.New("db down")
	}
	if f.lookupErr != nil {
		return agents.AgentIdentity{}, f.lookupErr
	}
	id, ok := f.certs[serial]
	if !ok {
		return agents.AgentIdentity{}, agents.ErrNotFound
	}
	id.Serial = serial
	id.Revoked = f.revokedC[serial]
	if f.revokeAfterLookup == serial {
		f.revokeAfterLookup, f.revokedC[serial] = "", true
	}
	return id, nil
}

func (f *fakeAgentStore) Touch(_ context.Context, serial, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.touchErr != nil {
		return f.touchErr
	}
	f.touched = append(f.touched, serial+":"+version)
	for i := range f.agentsR {
		if f.agentsR[i].CertSerial == serial {
			now := time.Now()
			f.agentsR[i].LastSeen = &now
			f.agentsR[i].Version = version
		}
	}
	return nil
}

func (f *fakeAgentStore) List(_ context.Context, orgID uuid.UUID) ([]agents.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []agents.Agent
	for _, a := range f.agentsR {
		if a.OrgID == orgID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeAgentStore) Revoke(_ context.Context, orgID, hostID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := -1
	for i, a := range f.agentsR {
		if a.HostID == hostID && a.OrgID == orgID {
			row = i
			break
		}
	}
	if row < 0 {
		return agents.ErrNotFound
	}
	for serial, id := range f.certs {
		if id.HostID == hostID && id.OrgID == orgID {
			f.revokedC[serial] = true
		}
	}
	f.agentsR = append(f.agentsR[:row], f.agentsR[row+1:]...)
	return nil
}

// touches returns every Touch call as "serial:version" pairs.
func (f *fakeAgentStore) touches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.touched...)
}

// useOf reports a token's consumption state (tests assert a refused claim
// consumed nothing).
func (f *fakeAgentStore) useOf(id uuid.UUID) (uses int, used bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[id]
	if !ok {
		return 0, false
	}
	return t.Uses, t.UsedAt != nil
}

// armRevokeAfterLookup arms the post-track re-check race: the next
// GetBySerial still succeeds and then revokes the serial.
func (f *fakeAgentStore) armRevokeAfterLookup(serial string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokeAfterLookup = serial
}

// armLookupFaultAfterNext makes the GetBySerial AFTER the next one fail —
// the interceptor's lookup succeeds, the handler's post-track re-check
// runs into the fault.
func (f *fakeAgentStore) armLookupFaultAfterNext() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faultNextLookup = true
}

// fakeFacts records facts pushed through the FactsSink port.
type fakeFacts struct {
	mu    sync.Mutex
	calls []factCall
	err   error
}

type factCall struct {
	OrgID  uuid.UUID
	HostID uuid.UUID
	Facts  map[string]string
}

func (f *fakeFacts) RecordFacts(_ context.Context, orgID, hostID uuid.UUID, facts map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, factCall{OrgID: orgID, HostID: hostID, Facts: facts})
	return nil
}

func (f *fakeFacts) recorded() []factCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]factCall(nil), f.calls...)
}

// fakeAuditor records the audit events; recordErr injects a fault.
type fakeAuditor struct {
	mu        sync.Mutex
	events    []grpcsrv.SystemEvent
	recordErr error
}

func (f *fakeAuditor) Record(_ context.Context, ev grpcsrv.SystemEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return f.recordErr
	}
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeAuditor) recorded() []grpcsrv.SystemEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]grpcsrv.SystemEvent(nil), f.events...)
}

// fakeSigner wraps the real CA with a fault switch (the sign-error paths
// are otherwise unreachable: the CA cannot fail on demand).
type fakeSigner struct {
	inner *ca.CA
	err   error
}

func (f *fakeSigner) SignAgentCSR(csrPEM []byte, orgID uuid.UUID, agentName string) ([]byte, *x509.Certificate, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.inner.SignAgentCSR(csrPEM, orgID, agentName)
}

func (f *fakeSigner) CAPEM() []byte { return f.inner.CAPEM() }

// stubWorkerStore keeps grpcsrv's Deps validation happy: the agent tests
// never exercise the worker surface, so every lookup is not-found.
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

// harness is one fully wired listener: the real grpcsrv server (mTLS,
// interceptors, agent certificate class) with the agentapi Service
// mounted, over a bufconn listener.
type harness struct {
	t *testing.T

	ca       *ca.CA
	signer   *fakeSigner
	store    *fakeAgentStore
	prov     *fakeProvisioner
	sink     *fakeFacts
	audit    *fakeAuditor
	agentSvc *agents.Service
	svc      *Service
	gs       *grpc.Server
	lis      *bufconn.Listener

	mu  sync.Mutex
	now time.Time
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

// newHarness wires the service with a facts sink; newHarnessWithoutFacts
// leaves Deps.Facts nil (the documented drop path).
func newHarness(t *testing.T) *harness { return newHarnessOpts(t, true) }

func newHarnessWithoutFacts(t *testing.T) *harness { return newHarnessOpts(t, false) }

func newHarnessOpts(t *testing.T, withFacts bool) *harness {
	t.Helper()
	h := &harness{
		t:     t,
		store: newFakeAgentStore(),
		prov:  &fakeProvisioner{},
		sink:  &fakeFacts{},
		audit: &fakeAuditor{},
		now:   time.Now(),
	}
	var key [32]byte
	copy(key[:], "pulsix-agentapi-test-key-1")
	caInst, err := ca.New(&fakeCAStore{}, crypto.NewBox(key), ca.Config{CommonName: "pulsix-manager", ListenHost: "manager"})
	if err != nil {
		t.Fatal(err)
	}
	h.ca = caInst
	if err := h.ca.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.agentSvc = agents.NewService(h.store, agents.WithProvisioner(h.prov))
	h.signer = &fakeSigner{inner: h.ca}
	deps := Deps{
		CA: h.signer, Agents: h.agentSvc, Audit: h.audit,
		Clock: h.clock,
	}
	if withFacts {
		deps.Facts = h.sink
	}
	h.svc, err = New(deps)
	if err != nil {
		t.Fatal(err)
	}
	// The stream registry is the agents service's revoke hook, exactly as
	// the app wiring does it.
	h.agentSvc.SetRevokeHook(h.svc.Streams())

	gs, err := grpcsrv.New(context.Background(), grpcsrv.Deps{
		CA: h.ca, Workers: workers.NewService(&stubWorkerStore{}, workers.WithSigner(h.ca)),
		Agents: h.agentSvc, AgentService: h.svc,
		Audit: h.audit, Clock: h.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.gs = gs
	h.lis = bufconn.Listen(1024 * 1024)
	go func() { _ = h.gs.Serve(h.lis) }()
	t.Cleanup(h.gs.Stop)
	return h
}

// fakeCAStore keeps the sealed CA bytes in memory.
type fakeCAStore struct {
	mu    sync.Mutex
	saved []byte
}

func (f *fakeCAStore) Load(context.Context) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saved, f.saved != nil, nil
}

func (f *fakeCAStore) Save(_ context.Context, sealed []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = sealed
	return nil
}

// newKeyAndCSR is what an agent does before Enroll or Renew.
func newKeyAndCSR(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

// enrollToken creates an agent token and renders it in its wire form.
func (h *harness) enrollToken(orgID uuid.UUID, in agents.TokenInput) enrolltokenWire {
	h.t.Helper()
	tok, err := h.agentSvc.CreateToken(context.Background(), orgID, in)
	if err != nil {
		h.t.Fatal(err)
	}
	return enrolltokenWire{raw: enrolltoken.AgentString(tok), parsed: tok}
}

// enrolltokenWire carries the printed token and its parsed parts.
type enrolltokenWire struct {
	raw    string
	parsed enrolltoken.Token
}

// client dials the bufconn listener with (certPEM, keyPEM) as the client
// certificate — nil, nil means no client certificate at all.
func (h *harness) client(certPEM, keyPEM []byte) agentpb.AgentServiceClient {
	h.t.Helper()
	return agentpb.NewAgentServiceClient(h.conn(certPEM, keyPEM))
}

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

// enroll calls Enroll anonymously with the given token, CSR and name.
func (h *harness) enroll(token, csr, name string) (*agentpb.EnrollResponse, error) {
	h.t.Helper()
	return h.client(nil, nil).Enroll(h.t.Context(), &agentpb.EnrollRequest{Token: token, CsrPem: csr, Name: name, Version: "v0.3a"})
}

// keyPEMOf serializes the enrolled agent's key for a follow-up connection.
func keyPEMOf(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}
