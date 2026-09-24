// Package agents is the host-agent registry (v0.3a, spec §8): which hosts
// run a Pulsix agent, which client certificate each one authenticates
// with, and the enrollment tokens new agents redeem. It mirrors the worker
// identity machinery of migration 00027 (internal/workers) with the
// agent-specific token semantics: a token may be pre-bound to a host or
// let its first claim create one, and it may be multi-use (a staging token
// enrolling a fleet). The gRPC interceptor (Task 7) resolves every
// presented agent certificate serial here; a revoked certificate or a
// replaced registration is refused.
package agents

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/hosts"
)

// Sentinel errors mapped to HTTP / gRPC status by the transport layers.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	// ErrTokenInvalid covers unknown, used-up, expired and wrong-secret
	// tokens alike: the caller cannot tell which.
	ErrTokenInvalid = errors.New("enrollment token invalid")
	// ErrTokenConflict means the claim's host-side step failed on a name
	// the provisioner already carries (a host with the resolved name
	// exists in the organization) — a conflict, not a refusal.
	ErrTokenConflict = errors.New("host name already in use")
)

// HeartbeatInterval is the cadence the agent reports at by default
// (PULSIX_HEARTBEAT_INTERVAL, spec §8) and StaleAfter is three of them:
// the window the GUI calls an agent online in. One missed heartbeat is a
// hiccup — a reconnect, a slow network — three is an agent that stopped
// reporting. The verdict is computed at read time from last_seen, so
// nothing has to run to keep it current.
const (
	HeartbeatInterval = 30 * time.Second
	StaleAfter        = 3 * HeartbeatInterval
)

// AgentTTL is how long a token is valid by default shape (the caller
// passes ExpiresAt explicitly; the worker side pins one hour).
const AgentTTL = time.Hour

// Agent is a registered host agent. CertSerial is the serial the agent
// authenticated with (GetBySerial) or its registration's serial.
type Agent struct {
	HostID     uuid.UUID
	OrgID      uuid.UUID
	AgentName  string
	CertSerial string
	LastSeen   *time.Time
	Version    string
	CreatedAt  time.Time
	// CertNotBefore/CertNotAfter is the validity window of CertSerial's
	// certificate — display data for the Agents page and the host's Agent
	// card. Register carries it in from the issued leaf; reads fill only
	// CertNotAfter (what the GUI shows), and it is nil for a row written
	// before migration 00039 that the backfill could not reach.
	CertNotBefore time.Time
	CertNotAfter  *time.Time
	// HostName is the host's name, read along with the agent so the
	// Agents page can name its rows without resolving every host id.
	HostName string
}

// Registration is one enrollment's or renewal's identity as the registry
// records it: which host now runs which agent, under which certificate
// serial, and the window that certificate is valid in.
type Registration struct {
	OrgID     uuid.UUID
	HostID    uuid.UUID
	AgentName string
	Version   string
	Serial    string
	NotBefore time.Time
	NotAfter  time.Time
}

// AgentIdentity is the org-unscoped serial lookup's answer: which
// organization and host the serial belongs to, and whether its certificate
// has been revoked. Task 7's interceptor decides on Revoked; Serial echoes
// the serial the lookup resolved by (the stream's Touch is by serial).
type AgentIdentity struct {
	Serial   string
	OrgID    uuid.UUID
	HostID   uuid.UUID
	Name     string
	Version  string
	Revoked  bool
	LastSeen *time.Time
}

// Token is a pending agent enrollment record (the stored half; the secret
// itself never comes back).
type Token struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	ZoneID    uuid.UUID
	HostID    *uuid.UUID
	NameHint  string
	MaxUses   int
	Uses      int
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedBy *uuid.UUID
	CreatedAt time.Time
}

// TokenInput is the caller-supplied shape for creating an enrollment
// token. HostID set binds the token to that host (the claim takes its
// identity); nil lets the first claim create the host, named by NameHint
// or the presenter. MaxUses below 1 or above 100 is refused (the column's
// CHECK); ExpiresAt must lie in the future.
type TokenInput struct {
	ZoneID    uuid.UUID
	HostID    *uuid.UUID
	NameHint  string
	MaxUses   int
	ExpiresAt time.Time
	CreatedBy *uuid.UUID
}

// ClaimResult is the outcome of a successful Claim: the host the agent now
// belongs to, whether this claim created it, and — for a created host — the
// name it was created under (token name hint, else the presenter-provided
// name; Task 7's gRPC layer names the certificate the same way). A bound
// host keeps its own name, so Name is empty there.
type ClaimResult struct {
	HostID  uuid.UUID
	Created bool
	Name    string
}

// StreamCutter ends an agent's open streams (agentapi's StreamRegistry,
// wired post-construction with SetRevokeHook — the same shape as the
// workers' StreamCloser). Streams are keyed by host id: it is the stable
// revocation handle across certificate renewals, and Revoke is host-scoped.
type StreamCutter interface {
	CloseHost(hostID uuid.UUID) int
}

// CAPinner is the slice of the CA a created enrollment token needs: the
// SHA-256 of the manager's own CA certificate. It travels inside the
// token, and the enrolling agent pins the manager's chain against it on
// its first connection (internal/agent's pinned dial) — a token without
// it cannot enrol anything, which is why CreateToken refuses to mint one.
type CAPinner interface {
	CAPin() [32]byte
}

// HostProvisioner is the narrow slice of the hosts service Claim needs:
// resolve a bound host inside the organization, create an unbound claim's
// host in the token's zone, and flip the claimed host's monitored
// capability — enrollment is what makes a host monitored, and the flip
// runs inside the claim's transaction (the closure's context is
// transaction-bound, so a store-backed implementation commits and rolls
// back with the claim). The implementation is hosts.Service, wired in
// app.Build; this package depends only on the port, so its own tests run
// against a fake. It does import internal/hosts for one thing — mapping
// that service's sentinel errors (a taken name) onto its own — which is a
// dependency on hosts' error values, not on its surface.
type HostProvisioner interface {
	GetHost(ctx context.Context, orgID, hostID uuid.UUID) error
	CreateHost(ctx context.Context, orgID, zoneID uuid.UUID, name string) (uuid.UUID, error)
	SetMonitored(ctx context.Context, orgID, hostID uuid.UUID) error
}

// Store persists agents, their certificates and enrollment tokens.
// GetBySerial and Touch run outside any org scope (the gRPC interceptor
// has none).
type Store interface {
	CreateToken(ctx context.Context, t Token, secretHash []byte) (Token, error)
	ListTokens(ctx context.Context, orgID uuid.UUID, now time.Time) ([]Token, error)
	RevokeToken(ctx context.Context, orgID, id uuid.UUID) error
	// EnrollmentOrg resolves the token's organization, org-unscoped (the
	// id is the token's public half). Task 7's unauthenticated Enroll
	// resolves the org for its transaction and audit record with this;
	// an unknown token is ErrTokenInvalid.
	EnrollmentOrg(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	// Claim locks the token row for orgID, runs fn and, on success,
	// consumes one use with a conditional UPDATE in the same transaction;
	// 0 affected rows (exhausted / expired / withdrawn / unknown) is
	// ErrTokenInvalid. fn receives the transaction-bound context, so host
	// work it does through other stores joins the claim's atomicity.
	Claim(ctx context.Context, orgID, id uuid.UUID, fn func(ctx context.Context, t Token, secretHash []byte) error) error
	// Register replaces the host's active agent in one transaction: the
	// previous serial is revoked, the row upserted.
	Register(ctx context.Context, a Agent) error
	// GetBySerial resolves a presented serial without an organization
	// filter; ErrNotFound when unknown.
	GetBySerial(ctx context.Context, serial string) (AgentIdentity, error)
	// Touch records a heartbeat by serial, org-unscoped.
	Touch(ctx context.Context, serial, version string) error
	List(ctx context.Context, orgID uuid.UUID) ([]Agent, error)
	// GetByHost is one host's active agent, org-scoped; ErrNotFound when
	// the host runs none (never enrolled, or revoked).
	GetByHost(ctx context.Context, orgID, hostID uuid.UUID) (Agent, error)
	// Revoke marks every certificate of the host's agent revoked and
	// removes the host_agents row in one transaction. It does NOT touch
	// hosts.monitored: the caller owns that flip (the API layer sets
	// monitored=false when the last agent of the host is gone).
	Revoke(ctx context.Context, orgID, hostID uuid.UUID) error
	// RecordFacts (Task 8) merges agent-reported facts into hosts.facts
	// and re-applies the automatic tags, in ONE org-bound transaction.
	// Reported keys replace their previous values, keys the agent did
	// not report survive (stale facts from an older agent version stay
	// visible); the three automatic tags (os, arch, virt) upsert with
	// source='fact', skipping any key that carries a manual row — a
	// manual tag always wins, and once it is removed the next push
	// re-applies the fact. ErrNotFound when the host does not exist in
	// orgID (or belongs to another organization — invisible, not
	// forbidden). Validation of the bounds themselves is the service's
	// job (RecordFacts below); the store receives only well-bounded maps.
	RecordFacts(ctx context.Context, orgID, hostID uuid.UUID, facts map[string]string) error
}

// Service implements the agent registry.
type Service struct {
	store       Store
	provisioner HostProvisioner
	now         func() time.Time
	rand        io.Reader
	// streams is the revocation hook (SetRevokeHook); nil leaves a
	// revocation at the registry rows.
	streams StreamCutter
	// ca supplies the fingerprint a created token carries; without it
	// CreateToken refuses (see CAPinner).
	ca CAPinner
}

// Option configures a Service.
type Option func(*Service)

// WithProvisioner wires the host resolution/creation port claim needs.
func WithProvisioner(p HostProvisioner) Option { return func(s *Service) { s.provisioner = p } }

// WithCA wires the CA whose fingerprint created tokens carry.
func WithCA(p CAPinner) Option { return func(s *Service) { s.ca = p } }

// WithClock injects the clock (tests).
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithRand injects the entropy source for ids and enrollment secrets.
func WithRand(r io.Reader) Option { return func(s *Service) { s.rand = r } }

// SetRevokeHook wires the stream cutter a revocation triggers
// (post-construction, like the workers' SetRevokeHooks). nil is a no-op.
func (s *Service) SetRevokeHook(c StreamCutter) { s.streams = c }

// NewService wires the store; claim needs WithProvisioner.
func NewService(store Store, opts ...Option) *Service {
	s := &Service{store: store, now: time.Now, rand: rand.Reader}
	for _, o := range opts {
		o(s)
	}
	return s
}

// CreateToken creates an enrollment token and returns it printed ONCE —
// only SHA-256(secret) is stored. Zone must be validated by the caller
// (the API layer holds the zone list, agents does not).
func (s *Service) CreateToken(ctx context.Context, orgID uuid.UUID, in TokenInput) (enrolltoken.Token, error) {
	if s.ca == nil {
		return enrolltoken.Token{}, errors.New("agents: create token: service was built without WithCA")
	}
	if in.ZoneID == uuid.Nil {
		return enrolltoken.Token{}, fmt.Errorf("%w: zone is required", ErrInvalidInput)
	}
	if in.MaxUses < 1 || in.MaxUses > 100 {
		return enrolltoken.Token{}, fmt.Errorf("%w: max_uses must be between 1 and 100", ErrInvalidInput)
	}
	if !in.ExpiresAt.After(s.now()) {
		return enrolltoken.Token{}, fmt.Errorf("%w: expires_at must be in the future", ErrInvalidInput)
	}
	if len(in.NameHint) > 253 {
		return enrolltoken.Token{}, fmt.Errorf("%w: name_hint too long", ErrInvalidInput)
	}
	secret := make([]byte, 32)
	if _, err := io.ReadFull(s.rand, secret); err != nil {
		return enrolltoken.Token{}, fmt.Errorf("draw enrollment secret: %w", err)
	}
	hash := sha256.Sum256(secret)
	id, err := uuid.NewV7FromReader(s.rand)
	if err != nil {
		return enrolltoken.Token{}, err
	}
	// Taken before the store write, like the worker side: a CA that is
	// wired but not initialised panics inside CAPin, and asking after the
	// insert would leave a pending token row behind that panic — one that
	// nothing can ever redeem.
	pin := s.ca.CAPin()
	t := Token{
		ID: id, OrgID: orgID, ZoneID: in.ZoneID, HostID: in.HostID, NameHint: in.NameHint,
		MaxUses: in.MaxUses, ExpiresAt: in.ExpiresAt, CreatedBy: in.CreatedBy, CreatedAt: s.now(),
	}
	stored, err := s.store.CreateToken(ctx, t, hash[:])
	if err != nil {
		return enrolltoken.Token{}, err
	}
	// The pin travels inside the token: it is what lets the agent trust
	// this manager's CA on a connection it has no certificate for yet.
	return enrolltoken.Token{ID: stored.ID, Secret: secret, CAPin: pin}, nil
}

// ListTokens returns orgID's pending (unused-up, unexpired) tokens.
func (s *Service) ListTokens(ctx context.Context, orgID uuid.UUID) ([]Token, error) {
	return s.store.ListTokens(ctx, orgID, s.now())
}

// RevokeToken deletes a pending token before it is redeemed (worker
// semantics: a used token is already gone from the pending list and
// therefore cannot be revoked again — ErrNotFound).
func (s *Service) RevokeToken(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.RevokeToken(ctx, orgID, id)
}

// ClaimToken redeems one use of a token for orgID. The hash is verified in
// constant time; the use is consumed by the store's conditional UPDATE
// inside the claim transaction, so two concurrent claims of the last use
// cannot both win. A token of another organization reads as unknown. Host
// binding: HostID set → it must exist in the organization (else
// ErrNotFound, the use is NOT consumed — the failure rolls the transaction
// back); HostID nil → the host is created, named by the token's NameHint,
// else the presenter-provided name; neither present is ErrInvalidInput. A
// name the provisioner reports as taken is ErrTokenConflict. Either way
// the claimed host's monitored capability flips to true inside the claim
// transaction (the provisioner's SetMonitored, on the transaction-bound
// context) — enrollment is what makes a host monitored.
func (s *Service) ClaimToken(ctx context.Context, orgID uuid.UUID, token enrolltoken.Token, name string) (ClaimResult, error) {
	if s.provisioner == nil {
		return ClaimResult{}, errors.New("agents: claim: service was built without WithProvisioner")
	}
	var hostID uuid.UUID
	created := false
	resolvedName := ""
	err := s.store.Claim(ctx, orgID, token.ID, func(ctx context.Context, t Token, secretHash []byte) error {
		sum := sha256.Sum256(token.Secret)
		if subtle.ConstantTimeCompare(sum[:], secretHash) != 1 || t.UsedAt != nil || !s.now().Before(t.ExpiresAt) {
			return ErrTokenInvalid
		}
		if t.HostID != nil {
			if err := s.provisioner.GetHost(ctx, t.OrgID, *t.HostID); err != nil {
				if errors.Is(err, hosts.ErrNotFound) {
					// A bound host that does not exist (or belongs to
					// another organization) is refused as not-found; the
					// whole claim — use not consumed — rolls back.
					return ErrNotFound
				}
				return err
			}
			hostID = *t.HostID
			if err := s.provisioner.SetMonitored(ctx, t.OrgID, hostID); err != nil {
				return fmt.Errorf("flip host monitored: %w", err)
			}
			return nil
		}
		hostName := t.NameHint
		if hostName == "" {
			hostName = name
		}
		if hostName == "" {
			return fmt.Errorf("%w: token carries no name hint and none was presented", ErrInvalidInput)
		}
		// The resolved name becomes both the host's name and the agent
		// certificate's common name, so it must fit the certificate's
		// 1-64-rune shape — refused here, inside the claim, so an over-long
		// hint or name burns nothing (the transaction rolls back).
		if err := ca.CheckAgentName(hostName); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		id, err := s.provisioner.CreateHost(ctx, t.OrgID, t.ZoneID, hostName)
		if err != nil {
			switch {
			case errors.Is(err, hosts.ErrNameTaken):
				return ErrTokenConflict
			case errors.Is(err, hosts.ErrInvalidInput):
				return fmt.Errorf("%w: %s", ErrInvalidInput, err)
			}
			return err
		}
		hostID, created, resolvedName = id, true, hostName
		if err := s.provisioner.SetMonitored(ctx, t.OrgID, hostID); err != nil {
			return fmt.Errorf("flip host monitored: %w", err)
		}
		return nil
	})
	// The store owns the unknown-token refusal (ErrTokenInvalid); the
	// closure's own sentinels — ErrTokenInvalid for the dead token,
	// ErrNotFound for a vanished bound host, ErrTokenConflict for a taken
	// name — pass through unchanged.
	if err != nil {
		return ClaimResult{}, err
	}
	return ClaimResult{HostID: hostID, Created: created, Name: resolvedName}, nil
}

// EnrollmentOrg resolves the organization an enrollment token belongs to
// (org-unscoped; the id is the token's public half). Task 7's
// unauthenticated Enroll resolves the org for its transaction and audit
// record with this before claiming.
func (s *Service) EnrollmentOrg(ctx context.Context, tokenID uuid.UUID) (uuid.UUID, error) {
	return s.store.EnrollmentOrg(ctx, tokenID)
}

// Register records a newly enrolled agent as its host's active one and
// issues the identity: the certificate row is written and any previous
// serial of the host is revoked, all in the store's single transaction.
func (s *Service) Register(ctx context.Context, r Registration) error {
	if r.AgentName == "" {
		return fmt.Errorf("%w: agent name is required", ErrInvalidInput)
	}
	if r.Serial == "" {
		return fmt.Errorf("%w: certificate serial is required", ErrInvalidInput)
	}
	if r.NotAfter.IsZero() || !r.NotAfter.After(r.NotBefore) {
		return fmt.Errorf("%w: the certificate validity window is required (not_before < not_after)", ErrInvalidInput)
	}
	notAfter := r.NotAfter
	return s.store.Register(ctx, Agent{
		HostID: r.HostID, OrgID: r.OrgID, AgentName: r.AgentName, CertSerial: r.Serial, Version: r.Version,
		CertNotBefore: r.NotBefore, CertNotAfter: &notAfter,
	})
}

// GetBySerial is the gRPC authentication path: the presented serial's
// identity, org-unscoped, with the interceptor deciding on Revoked rather
// than this service (the renewal path needs to tell a live serial from a
// revoked one).
//
// Two different answers mean "revoked", and which one a caller gets
// depends on how the serial was retired: a serial replaced by a
// re-enrollment keeps its row and comes back with Revoked true, while
// Revoke deletes the host's agent row, so its serials resolve to nothing
// and come back as ErrNotFound. Both refuse the caller; only a diagnostic
// reading them apart needs to know the difference.
func (s *Service) GetBySerial(ctx context.Context, serial string) (AgentIdentity, error) {
	if serial == "" {
		return AgentIdentity{}, ErrNotFound
	}
	id, err := s.store.GetBySerial(ctx, serial)
	if err != nil {
		return AgentIdentity{}, err
	}
	id.Serial = serial
	return id, nil
}

// Touch records a heartbeat: last_seen becomes now, version the agent's
// self-reported build.
func (s *Service) Touch(ctx context.Context, serial, version string) error {
	return s.store.Touch(ctx, serial, version)
}

// List returns orgID's agents ordered by agent name.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Agent, error) {
	return s.store.List(ctx, orgID)
}

// View is one agent as the GUI shows it: the registry row plus the
// staleness verdict (see StaleAfter). The host's name rides along so the
// Agents page can name its rows without a second query.
type View struct {
	Agent
	Online bool
}

// ListView is the Agents page's read: every agent of the organization
// with its staleness verdict.
func (s *Service) ListView(ctx context.Context, orgID uuid.UUID) ([]View, error) {
	list, err := s.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]View, len(list))
	for i, a := range list {
		out[i] = View{Agent: a, Online: isOnline(a.LastSeen, now)}
	}
	return out, nil
}

// ViewByHost is the host detail card's read: the host's agent with the
// same verdict, ErrNotFound when the host runs none.
func (s *Service) ViewByHost(ctx context.Context, orgID, hostID uuid.UUID) (View, error) {
	a, err := s.GetByHost(ctx, orgID, hostID)
	if err != nil {
		return View{}, err
	}
	return View{Agent: a, Online: isOnline(a.LastSeen, s.now())}, nil
}

// isOnline: an agent that never reported is not online, and one whose
// last heartbeat is older than StaleAfter has stopped reporting.
func isOnline(lastSeen *time.Time, now time.Time) bool {
	return lastSeen != nil && now.Sub(*lastSeen) < StaleAfter
}

// GetByHost is the host detail card's read: the host's active agent, or
// ErrNotFound when it runs none — a host of another organization is
// invisible the same way.
func (s *Service) GetByHost(ctx context.Context, orgID, hostID uuid.UUID) (Agent, error) {
	return s.store.GetByHost(ctx, orgID, hostID)
}

// Revoke ends an agent: every certificate serial the host's agent held is
// marked revoked (the interceptor refuses them from then on), the
// host_agents row is removed, and the agent's open streams are cut (the
// SetRevokeHook port; nil leaves the cut to nothing). hosts.monitored is
// NOT flipped here — the caller owns that flip (documented on Store.Revoke).
func (s *Service) Revoke(ctx context.Context, orgID, hostID uuid.UUID) error {
	if err := s.store.Revoke(ctx, orgID, hostID); err != nil {
		return err
	}
	if s.streams != nil {
		s.streams.CloseHost(hostID)
	}
	return nil
}
