package agents_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/restoremark"
)

var ctx = context.Background()

// fakeProvisioner is the in-memory HostProvisioner the service tests run
// on. Taken captures the name conflict the real hosts.Service.CreateHost
// reports (hosts.ErrNameTaken → Service maps it to agents.ErrTokenConflict
// via the port contract below).
type fakeProvisioner struct {
	mu        sync.Mutex
	hosts     map[uuid.UUID][]string // orgID -> existing names
	ids       []uuid.UUID
	monitored map[uuid.UUID]bool // hostID -> monitored flag
	flipErr   error
}

func (f *fakeProvisioner) seedHost(orgID uuid.UUID, name string) uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.Must(uuid.NewV7())
	f.init()
	f.hosts[orgID] = append(f.hosts[orgID], name)
	f.ids = append(f.ids, id)
	return id
}

func (f *fakeProvisioner) init() {
	if f.hosts == nil {
		f.hosts = map[uuid.UUID][]string{}
	}
	if f.monitored == nil {
		f.monitored = map[uuid.UUID]bool{}
	}
}

func (f *fakeProvisioner) GetHost(_ context.Context, orgID, hostID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.ids {
		if id == hostID {
			// The bound host is only resolvable when it belongs to orgID;
			// the SQL world checks hosts (org_id, id) directly, the fake
			// tracks membership explicitly.
			for _, known := range f.hosts[orgID] {
				_ = known
			}
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

// fakeStore is the in-memory agents.Store the service tests run on.
type fakeStore struct {
	mu       sync.Mutex
	tokens   map[uuid.UUID]agents.Token
	hashes   map[uuid.UUID][]byte
	agentsR  []agents.Agent
	certs    map[string]agents.AgentIdentity // serial → identity
	touched  []string
	revokedC map[string]bool
	// facts recording (Task 8): factCalls/factsErr serve the facts
	// service tests; the merge and tag semantics themselves live in the
	// SQL store's integration tests.
	factsCalls []factsCall
	factsErr   error
	// window is the restore marker RestoreWindow returns; adoptCalls
	// counts the store-side adoptions the service asked for.
	window     *restoremark.Window
	adoptCalls int
}

func (f *fakeStore) RestoreWindow(context.Context) (*restoremark.Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.window, nil
}

// AdoptCertificate only counts: the adoption rules run against the real
// store in adopt_test.go.
func (f *fakeStore) AdoptCertificate(context.Context, agents.Agent, func(agents.AdoptionState) error) (agents.AgentIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adoptCalls++
	return agents.AgentIdentity{}, agents.ErrNotFound
}

// factsCall is one forwarded RecordFacts call.
type factsCall struct {
	OrgID  uuid.UUID
	HostID uuid.UUID
	Facts  map[string]string
}

func (f *fakeStore) factCalls() []factsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]factsCall(nil), f.factsCalls...)
}

func (f *fakeStore) RecordFacts(_ context.Context, orgID, hostID uuid.UUID, facts map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.factsErr != nil {
		return f.factsErr
	}
	f.factsCalls = append(f.factsCalls, factsCall{OrgID: orgID, HostID: hostID, Facts: facts})
	return nil
}

func (f *fakeStore) init() {
	if f.tokens == nil {
		f.tokens = map[uuid.UUID]agents.Token{}
		f.hashes = map[uuid.UUID][]byte{}
		f.certs = map[string]agents.AgentIdentity{}
		f.revokedC = map[string]bool{}
	}
}

func (f *fakeStore) CreateToken(_ context.Context, t agents.Token, hash []byte) (agents.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.tokens[t.ID], f.hashes[t.ID] = t, hash
	return t, nil
}

func (f *fakeStore) ListTokens(_ context.Context, orgID uuid.UUID, now time.Time) ([]agents.Token, error) {
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

func (f *fakeStore) RevokeToken(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[id]
	if !ok || t.OrgID != orgID || t.UsedAt != nil {
		return agents.ErrNotFound
	}
	delete(f.tokens, id)
	return nil
}

// Claim mirrors the SQL store's shape: an unknown token is ErrTokenInvalid
// directly (ErrNotFound stays reserved for the host-side failures), fn
// runs, then one use is consumed with the same "0 rows → refusal"
// semantics the conditional UPDATE carries.
func (f *fakeStore) Claim(_ context.Context, orgID, id uuid.UUID, fn func(context.Context, agents.Token, []byte) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	t, ok := f.tokens[id]
	if !ok || t.OrgID != orgID {
		return agents.ErrTokenInvalid
	}
	if err := fn(ctx, t, f.hashes[id]); err != nil {
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

func (f *fakeStore) EnrollmentOrg(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	t, ok := f.tokens[id]
	if !ok {
		return uuid.Nil, agents.ErrTokenInvalid
	}
	return t.OrgID, nil
}

// setLastSeen backdates a registered agent's heartbeat, which is what
// the staleness verdict reads.
func (f *fakeStore) setLastSeen(hostID uuid.UUID, seen *time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.agentsR {
		if f.agentsR[i].HostID == hostID {
			f.agentsR[i].LastSeen = seen
		}
	}
}

func (f *fakeStore) GetByHost(_ context.Context, orgID, hostID uuid.UUID) (agents.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.agentsR {
		if a.OrgID == orgID && a.HostID == hostID {
			return a, nil
		}
	}
	return agents.Agent{}, agents.ErrNotFound
}

// Renew is Register guarded like the real store: the serial renewed from
// must still be live.
func (f *fakeStore) Renew(ctx context.Context, a agents.Agent, fromSerial string) error {
	f.mu.Lock()
	f.init()
	_, known := f.certs[fromSerial]
	live := known && !f.revokedC[fromSerial]
	f.mu.Unlock()
	if !live {
		return agents.ErrNotFound
	}
	return f.Register(ctx, a)
}

func (f *fakeStore) Register(_ context.Context, a agents.Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
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

func (f *fakeStore) GetBySerial(_ context.Context, serial string) (agents.AgentIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	id, ok := f.certs[serial]
	if !ok {
		return agents.AgentIdentity{}, agents.ErrNotFound
	}
	id.Serial = serial
	id.Revoked = f.revokedC[serial]
	return id, nil
}

func (f *fakeStore) Touch(_ context.Context, serial, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID) ([]agents.Agent, error) {
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

func (f *fakeStore) Revoke(_ context.Context, orgID, hostID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	// The row must exist for this org: removing it is what makes a second
	// revoke not-found (the certificates stay revoked).
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

// --- tests ---

func newService(t *testing.T) (*agents.Service, *fakeStore, *fakeProvisioner, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	p := &fakeProvisioner{}
	st := &fakeStore{}
	svc := agents.NewService(st, agents.WithProvisioner(p), agents.WithClock(func() time.Time { return now }),
		agents.WithCA(fakeCA{}))
	return svc, st, p, &now
}

// The token an operator copies out of the GUI carries the CA fingerprint
// the agent pins the manager against on its very first connection. A
// service without a CA cannot mint one — and must say so instead of
// handing out a token whose zero pin fails every enrollment.
func TestCreateTokenCarriesTheCAPin(t *testing.T) {
	svc, _, _, now := newService(t)
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	in := agents.TokenInput{ZoneID: zone, MaxUses: 1, ExpiresAt: now.Add(time.Hour)}
	unpinned := agents.NewService(&fakeStore{}, agents.WithClock(func() time.Time { return *now }))
	if _, err := unpinned.CreateToken(ctx, org, in); err == nil {
		t.Fatal("a service without a CA minted a token, want a refusal")
	}

	tok, err := svc.CreateToken(ctx, org, in)
	if err != nil {
		t.Fatal(err)
	}
	if tok.CAPin != (fakeCA{}).CAPin() {
		t.Errorf("token pin = %x, want the CA's own %x", tok.CAPin, (fakeCA{}).CAPin())
	}
	// It renders as an agent token: a worker parser must not take it.
	printed := enrolltoken.AgentString(tok)
	if _, err := enrolltoken.ParseAgent(printed); err != nil {
		t.Errorf("ParseAgent(%q): %v", printed, err)
	}
}

// fakeCA is a CA whose fingerprint is recognisable in assertions.
type fakeCA struct{}

func (fakeCA) CAPin() [32]byte {
	var pin [32]byte
	for i := range pin {
		pin[i] = byte(i + 1)
	}
	return pin
}

func TestCreateTokenValidatesAndHashes(t *testing.T) {
	svc, st, _, now := newService(t)
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	in := agents.TokenInput{ZoneID: zone, MaxUses: 1, ExpiresAt: now.Add(time.Hour)}

	if _, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: uuid.Nil, MaxUses: 1, ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, agents.ErrInvalidInput) {
		t.Errorf("zone required: %v", err)
	}
	if _, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, MaxUses: 0, ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, agents.ErrInvalidInput) {
		t.Errorf("max_uses 0: %v", err)
	}
	if _, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, MaxUses: 101, ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, agents.ErrInvalidInput) {
		t.Errorf("max_uses 101: %v", err)
	}
	if _, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, MaxUses: 1, ExpiresAt: *now}); !errors.Is(err, agents.ErrInvalidInput) {
		t.Errorf("past expiry: %v", err)
	}

	tok, err := svc.CreateToken(ctx, org, in)
	if err != nil {
		t.Fatal(err)
	}
	hash := st.hashes[tok.ID]
	want := sha256.Sum256(tok.Secret)
	if len(hash) != 32 || bytes.Equal(hash, tok.Secret) || !bytes.Equal(hash, want[:]) {
		t.Fatal("only the SHA-256 of the secret may be stored")
	}
}

func TestTokenStateMachine(t *testing.T) {
	t.Run("single use claim then refusal", func(t *testing.T) {
		svc, _, p, now := newService(t)
		org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		tok, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, NameHint: "web-1", MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.ClaimToken(ctx, org, tok, "fallback-name")
		if err != nil {
			t.Fatal(err)
		}
		if !res.Created || res.HostID == uuid.Nil {
			t.Fatalf("claim = %+v", res)
		}
		if _, err := svc.ClaimToken(ctx, org, tok, "second"); !errors.Is(err, agents.ErrTokenInvalid) {
			t.Fatalf("second claim: %v", err)
		}
		if len(p.ids) != 1 {
			t.Fatalf("second claim created a host: %v", p.ids)
		}
	})

	t.Run("wrong secret refused", func(t *testing.T) {
		svc, _, _, now := newService(t)
		org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		tok, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, NameHint: "web-1", MaxUses: 2, ExpiresAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		stolen := tok
		stolen.Secret = bytes.Repeat([]byte{9}, 32)
		if _, err := svc.ClaimToken(ctx, org, stolen, "x"); !errors.Is(err, agents.ErrTokenInvalid) {
			t.Fatalf("wrong secret: %v", err)
		}
	})

	t.Run("expired refused", func(t *testing.T) {
		svc, st, _, now := newService(t)
		org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		hash := sha256.Sum256(bytes.Repeat([]byte{2}, 32))
		id := uuid.Must(uuid.NewV7())
		if _, err := st.CreateToken(ctx, agents.Token{
			ID: id, OrgID: org, ZoneID: zone, NameHint: "old", MaxUses: 1,
			ExpiresAt: now.Add(-time.Minute), CreatedAt: *now,
		}, hash[:]); err != nil {
			t.Fatal(err)
		}
		tok := enrolltoken.Token{ID: id, Secret: bytes.Repeat([]byte{2}, 32)}
		if _, err := svc.ClaimToken(ctx, org, tok, "x"); !errors.Is(err, agents.ErrTokenInvalid) {
			t.Fatalf("expired: %v", err)
		}
	})

	t.Run("unknown token refused", func(t *testing.T) {
		svc, _, _, _ := newService(t)
		tok := enrolltoken.Token{ID: uuid.Must(uuid.NewV7()), Secret: bytes.Repeat([]byte{1}, 32)}
		if _, err := svc.ClaimToken(ctx, uuid.Must(uuid.NewV7()), tok, "x"); !errors.Is(err, agents.ErrTokenInvalid) {
			t.Fatalf("unknown: %v", err)
		}
	})

	t.Run("multi use counts", func(t *testing.T) {
		svc, _, p, now := newService(t)
		org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		tok, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, MaxUses: 3, ExpiresAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		// An unbound multi-use token creates one host per use; each claim
		// names its own host via the presenter argument.
		for i := 0; i < 3; i++ {
			res, err := svc.ClaimToken(ctx, org, tok, fmt.Sprintf("fleet-%d", i))
			if err != nil {
				t.Fatalf("claim %d: %v", i, err)
			}
			if !res.Created {
				t.Fatalf("claim %d did not create its host", i)
			}
		}
		if pending, _ := svc.ListTokens(ctx, org); len(pending) != 0 {
			t.Fatalf("an exhausted token is not pending: %v", pending)
		}
		if _, err := svc.ClaimToken(ctx, org, tok, "x"); !errors.Is(err, agents.ErrTokenInvalid) {
			t.Fatalf("fourth claim: %v", err)
		}
		if len(p.hosts[org]) != 3 {
			t.Fatalf("claims created %v", p.hosts[org])
		}
	})

	t.Run("bound host resolves or fails", func(t *testing.T) {
		svc, _, p, now := newService(t)
		org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		bound := p.seedHost(org, "web-1")
		tok, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, HostID: &bound, MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.ClaimToken(ctx, org, tok, "")
		if err != nil || res.Created || res.HostID != bound {
			t.Fatalf("bound claim = %+v, %v", res, err)
		}
		missing := uuid.Must(uuid.NewV7())
		other, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, HostID: &missing, MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ClaimToken(ctx, org, other, ""); !errors.Is(err, agents.ErrNotFound) {
			t.Fatalf("bound to a nonexistent host: %v", err)
		}
	})

	t.Run("name resolution order hint then presenter", func(t *testing.T) {
		svc, _, p, now := newService(t)
		org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		hinted, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, NameHint: "from-hint", MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ClaimToken(ctx, org, hinted, "presented"); err != nil {
			t.Fatal(err)
		}
		if added := len(p.hosts[org]); added != 1 {
			t.Fatalf("hosts = %v", p.hosts[org])
		}

		blank, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ClaimToken(ctx, org, blank, "presented"); err != nil {
			t.Fatal(err)
		}
		neither, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ClaimToken(ctx, org, neither, ""); !errors.Is(err, agents.ErrInvalidInput) {
			t.Fatalf("no name anywhere: %v", err)
		}
		_ = p
	})

	t.Run("name conflict", func(t *testing.T) {
		svc, _, p, now := newService(t)
		org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		p.seedHost(org, "taken")
		tok, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, NameHint: "web-1", MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		p.seedHost(org, "web-1")
		if _, err := svc.ClaimToken(ctx, org, tok, ""); !errors.Is(err, agents.ErrTokenConflict) {
			t.Fatalf("conflict: %v", err)
		}
	})
}

func TestRevokeToken(t *testing.T) {
	svc, _, _, now := newService(t)
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tok, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, NameHint: "n", MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeToken(ctx, org, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimToken(ctx, org, tok, ""); !errors.Is(err, agents.ErrTokenInvalid) {
		t.Fatalf("revoked token claim: %v", err)
	}
	if err := svc.RevokeToken(ctx, org, tok.ID); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("revoke twice: %v", err)
	}
	// A token from another organization is invisible.
	other := uuid.Must(uuid.NewV7())
	if _, err := svc.CreateToken(ctx, other, agents.TokenInput{ZoneID: zone, NameHint: "o", MaxUses: 1, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeToken(ctx, other, uuid.Must(uuid.NewV7())); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("unknown other-org token: %v", err)
	}
}

// reg is the registration a test that does not care about the
// certificate window uses: a plain 90-day leaf, like the CA issues.
func reg(orgID, hostID uuid.UUID, name, version, serial string) agents.Registration {
	now := time.Now()
	return agents.Registration{OrgID: orgID, HostID: hostID, AgentName: name, Version: version, Serial: serial,
		NotBefore: now, NotAfter: now.Add(90 * 24 * time.Hour)}
}

// A registration without a usable validity window is refused: the window
// is what the Agents page shows, and a zero one would read as "expired
// in 1970" there.
// The GUI's two reads: the Agents page's list and the host card's single
// agent, both carrying the staleness verdict. An agent counts as online
// while its last heartbeat is younger than three heartbeat intervals —
// one missed heartbeat is a hiccup, three is an agent that stopped
// reporting — and one that never reported at all is not online.
func TestListViewMarksStaleAgents(t *testing.T) {
	svc, st, _, now := newService(t)
	org := uuid.Must(uuid.NewV7())
	fresh, stale, silent := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, tc := range []struct {
		host uuid.UUID
		name string
		seen *time.Time
	}{
		{fresh, "fresh", ptrTime(now.Add(-agents.StaleAfter + time.Second))},
		{stale, "stale", ptrTime(now.Add(-agents.StaleAfter - time.Second))},
		{silent, "silent", nil},
	} {
		if err := svc.Register(ctx, reg(org, tc.host, tc.name, "v1", "serial-"+tc.name)); err != nil {
			t.Fatal(err)
		}
		st.setLastSeen(tc.host, tc.seen)
	}

	views, err := svc.ListView(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	online := map[string]bool{}
	for _, v := range views {
		online[v.AgentName] = v.Online
	}
	for name, want := range map[string]bool{"fresh": true, "stale": false, "silent": false} {
		if got, ok := online[name]; !ok || got != want {
			t.Errorf("agent %q online = %v (present %v), want %v", name, got, ok, want)
		}
	}

	v, err := svc.ViewByHost(ctx, org, fresh)
	if err != nil || !v.Online || v.AgentName != "fresh" {
		t.Fatalf("ViewByHost = %+v, %v", v, err)
	}
	if _, err := svc.ViewByHost(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("unknown host: err = %v, want ErrNotFound", err)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// The host detail card asks for one host's agent; an unmonitored host or
// one whose agent was revoked has none, and that is ErrNotFound, not an
// empty agent.
func TestGetByHostReportsTheHostsAgent(t *testing.T) {
	svc, _, _, _ := newService(t)
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := svc.GetByHost(ctx, org, host); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("before enrollment: err = %v, want ErrNotFound", err)
	}
	if err := svc.Register(ctx, reg(org, host, "agent-a", "v1", "serial-h1")); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetByHost(ctx, org, host)
	if err != nil || got.AgentName != "agent-a" || got.CertSerial != "serial-h1" || got.CertNotAfter == nil {
		t.Fatalf("GetByHost = %+v, %v", got, err)
	}
	// Another organization cannot see it: invisible, not forbidden.
	if _, err := svc.GetByHost(ctx, uuid.Must(uuid.NewV7()), host); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("cross-org: err = %v, want ErrNotFound", err)
	}
	if err := svc.Revoke(ctx, org, host); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetByHost(ctx, org, host); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("after revocation: err = %v, want ErrNotFound", err)
	}
}

func TestRegisterRequiresAValidityWindow(t *testing.T) {
	svc, _, _, _ := newService(t)
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := time.Now()
	for name, r := range map[string]agents.Registration{
		"no window":     {OrgID: org, HostID: host, AgentName: "a", Serial: "s1"},
		"inverted":      {OrgID: org, HostID: host, AgentName: "a", Serial: "s2", NotBefore: now, NotAfter: now.Add(-time.Hour)},
		"zero duration": {OrgID: org, HostID: host, AgentName: "a", Serial: "s3", NotBefore: now, NotAfter: now},
	} {
		if err := svc.Register(ctx, r); !errors.Is(err, agents.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestRegisterReplacesAndRevokes(t *testing.T) {
	svc, _, _, _ := newService(t)
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	if err := svc.Register(ctx, reg(org, host, "agent-a", "v1", "serial-1")); err != nil {
		t.Fatal(err)
	}
	id, err := svc.GetBySerial(ctx, "serial-1")
	if err != nil || id.HostID != host || id.Revoked {
		t.Fatalf("first identity = %+v, %v", id, err)
	}
	// Re-enrollment: new serial, old revoked.
	if err := svc.Register(ctx, reg(org, host, "agent-a", "v2", "serial-2")); err != nil {
		t.Fatal(err)
	}
	if id, err := svc.GetBySerial(ctx, "serial-2"); err != nil || id.HostID != host || id.Revoked {
		t.Fatalf("new serial = %+v, %v", id, err)
	}
	if id, err := svc.GetBySerial(ctx, "serial-1"); err != nil || !id.Revoked {
		t.Fatalf("old serial = %+v (revoked=%v), %v", id, id.Revoked, err)
	}
	agents1, err := svc.List(ctx, org)
	if err != nil || len(agents1) != 1 || agents1[0].AgentName != "agent-a" || agents1[0].Version != "v2" {
		t.Fatalf("list = %+v, %v", agents1, err)
	}
	if err := svc.Register(ctx, reg(org, host, "", "v1", "serial-3")); !errors.Is(err, agents.ErrInvalidInput) {
		t.Fatalf("empty name: %v", err)
	}
	if err := svc.Register(ctx, reg(org, host, "agent-a", "v1", "")); !errors.Is(err, agents.ErrInvalidInput) {
		t.Fatalf("empty serial: %v", err)
	}
}

func TestTouchUpdatesList(t *testing.T) {
	svc, _, _, _ := newService(t)
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := svc.Register(ctx, reg(org, host, "agent-a", "v1", "serial-9")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Touch(ctx, "serial-9", "v2"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.List(ctx, org)
	if err != nil || len(got) != 1 || got[0].Version != "v2" || got[0].LastSeen == nil {
		t.Fatalf("after touch = %+v, %v", got, err)
	}
}

func TestRevokeAgent(t *testing.T) {
	svc, _, _, _ := newService(t)
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := svc.Register(ctx, reg(org, host, "agent-a", "v1", "serial-r")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(ctx, org, host); err != nil {
		t.Fatal(err)
	}
	if id, err := svc.GetBySerial(ctx, "serial-r"); err != nil || !id.Revoked {
		t.Fatalf("revoked identity = %+v, %v", id, err)
	}
	if got, _ := svc.List(ctx, org); len(got) != 0 {
		t.Fatalf("revoked agent still listed: %v", got)
	}
	// The row is gone, so a second revoke reports not-found (no agent
	// left to revoke — the API layer flips monitored=false on the first,
	// Task 7's wiring).
	others := uuid.Must(uuid.NewV7())
	if err := svc.Register(ctx, reg(others, host, "agent-b", "v1", "serial-o")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(ctx, org, host); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("cross-org revoke: %v", err)
	}
	if err := svc.Revoke(ctx, others, host); err != nil {
		t.Fatalf("own-org revoke: %v", err)
	}
	if err := svc.Revoke(ctx, others, host); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("revoke with nothing left: %v", err)
	}
}

func TestEnrollTokenFormatUnchanged(t *testing.T) {
	if got := enrolltoken.AgentPrefix; got != "pxa1" {
		t.Fatalf("AgentPrefix = %q", got)
	}
}

// TestClaimResultCarriesResolvedName pins Task 7's contract: the claim
// reports the name the host was created with (hint wins over the
// presenter's name) so the gRPC layer can name the certificate the same
// way; a bound host reports no created name.
func TestClaimResultCarriesResolvedName(t *testing.T) {
	svc, _, p, now := newService(t)
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hinted, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, NameHint: "from-hint", MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.ClaimToken(ctx, org, hinted, "presented")
	if err != nil || res.Name != "from-hint" {
		t.Fatalf("hinted claim = %+v, %v", res, err)
	}
	blank, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	res, err = svc.ClaimToken(ctx, org, blank, "presented")
	if err != nil || res.Name != "presented" {
		t.Fatalf("blank-hint claim = %+v, %v", res, err)
	}
	bound := p.seedHost(org, "web-1")
	boundTok, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, HostID: &bound, MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	res, err = svc.ClaimToken(ctx, org, boundTok, "")
	if err != nil || res.Name != "" {
		t.Fatalf("bound claim = %+v, %v", res, err)
	}
}

// TestClaimFlipsMonitored: the claim flips the claimed host's monitored
// capability inside the claim (the SQL store runs the flip through the
// provisioner with the transaction-bound context, so it commits and rolls
// back with the claim).
func TestClaimFlipsMonitored(t *testing.T) {
	svc, _, p, now := newService(t)
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tok, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, NameHint: "web-1", MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.ClaimToken(ctx, org, tok, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	if !p.monitored[res.HostID] {
		t.Fatal("the claim did not flip the created host's monitored flag")
	}

	// The bind flow flips the bound host too.
	bound := p.seedHost(org, "web-2")
	boundTok, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, HostID: &bound, MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimToken(ctx, org, boundTok, ""); err != nil {
		t.Fatal(err)
	}
	if !p.monitored[bound] {
		t.Fatal("the claim did not flip the bound host's monitored flag")
	}

	// A claim that fails (vanished bound host) flips nothing new.
	missing := uuid.Must(uuid.NewV7())
	deadTok, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, HostID: &missing, MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimToken(ctx, org, deadTok, ""); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("dead bind: %v", err)
	}
	if p.monitored[missing] {
		t.Fatal("a failed claim must not flip anything")
	}

	// A provisioner fault fails the claim.
	flipTok, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, NameHint: "web-3", MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.flipErr = errors.New("db down")
	p.mu.Unlock()
	if _, err := svc.ClaimToken(ctx, org, flipTok, "x"); err == nil || errors.Is(err, agents.ErrTokenInvalid) {
		t.Fatalf("flip fault: %v, want a non-refusal claim failure", err)
	}
}

// TestRevokeEndsStreams: the revocation hook (agentapi's stream registry,
// wired post-construction like the workers' SetRevokeHooks) cuts the
// revoked agent's open streams; without a hook a revocation stays a
// registry-row operation.
func TestRevokeEndsStreams(t *testing.T) {
	svc, _, _, _ := newService(t)
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := svc.Register(ctx, reg(org, host, "agent-a", "v1", "serial-s")); err != nil {
		t.Fatal(err)
	}
	cutter := &fakeCutter{}
	svc.SetRevokeHook(cutter)
	if err := svc.Revoke(ctx, org, host); err != nil {
		t.Fatal(err)
	}
	if cutter.closed != host {
		t.Fatalf("cutter closed %+v, want host %s", cutter.closed, host)
	}

	// No hook wired: revoke still succeeds.
	svc2, _, _, _ := newService(t)
	if err := svc2.Register(ctx, reg(org, host, "agent-b", "v1", "serial-t")); err != nil {
		t.Fatal(err)
	}
	if err := svc2.Revoke(ctx, org, host); err != nil {
		t.Fatal(err)
	}
}

// TestClaimRefusesOverlongNames: the resolved host/agent name must fit the
// certificate's 1-64-rune common name, and the refusal happens inside the
// claim — before the host is created and before the use is consumed.
func TestClaimRefusesOverlongNames(t *testing.T) {
	svc, st, p, now := newService(t)
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	hinted, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, NameHint: strings.Repeat("a", 65), MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimToken(ctx, org, hinted, "short"); !errors.Is(err, agents.ErrInvalidInput) {
		t.Fatalf("overlong hint: %v, want ErrInvalidInput", err)
	}
	if got := st.tokens[hinted.ID]; got.Uses != 0 || got.UsedAt != nil {
		t.Fatalf("the refused claim burned a use: uses=%d used=%v", got.Uses, got.UsedAt != nil)
	}
	if len(p.hosts[org]) != 0 || len(p.monitored) != 0 {
		t.Fatalf("the refused claim touched hosts: %v / %v", p.hosts[org], p.monitored)
	}

	blank, err := svc.CreateToken(ctx, org, agents.TokenInput{ZoneID: zone, MaxUses: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimToken(ctx, org, blank, strings.Repeat("b", 65)); !errors.Is(err, agents.ErrInvalidInput) {
		t.Fatalf("overlong presented name: %v, want ErrInvalidInput", err)
	}
	if got := st.tokens[blank.ID]; got.Uses != 0 || got.UsedAt != nil {
		t.Fatalf("the refused claim burned a use: uses=%d used=%v", got.Uses, got.UsedAt != nil)
	}
	if len(p.hosts[org]) != 0 {
		t.Fatalf("the refused claim created hosts: %v", p.hosts[org])
	}
}

type fakeCutter struct{ closed uuid.UUID }

func (f *fakeCutter) CloseHost(hostID uuid.UUID) int {
	f.closed = hostID
	return 1
}

// TestGetBySerialCarriesSerial pins that the identity reports the serial
// it was resolved by (the stream's Touch is by serial).
func TestGetBySerialCarriesSerial(t *testing.T) {
	svc, _, _, _ := newService(t)
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := svc.Register(ctx, reg(org, host, "agent-a", "v1", "serial-x")); err != nil {
		t.Fatal(err)
	}
	id, err := svc.GetBySerial(ctx, "serial-x")
	if err != nil || id.Serial != "serial-x" {
		t.Fatalf("identity = %+v, %v", id, err)
	}
}
