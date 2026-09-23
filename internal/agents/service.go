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
}

// AgentIdentity is the org-unscoped serial lookup's answer: which
// organization and host the serial belongs to, and whether its certificate
// has been revoked. Task 7's interceptor decides on Revoked.
type AgentIdentity struct {
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
// belongs to and whether this claim created it.
type ClaimResult struct {
	HostID  uuid.UUID
	Created bool
}

// HostProvisioner is the narrow slice of the hosts service Claim needs:
// resolve a bound host inside the organization, create an unbound claim's
// host in the token's zone. The implementation is hosts.Service, wired in
// Task 7's build-up (it must not be imported here: hosts depends on nothing
// of agents, and agents must not depend on hosts' full surface to stay
// testable with a fake). The fake in this package's tests stands in until
// then.
type HostProvisioner interface {
	GetHost(ctx context.Context, orgID, hostID uuid.UUID) error
	CreateHost(ctx context.Context, orgID, zoneID uuid.UUID, name string) (uuid.UUID, error)
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
	// Revoke marks every certificate of the host's agent revoked and
	// removes the host_agents row in one transaction. It does NOT touch
	// hosts.monitored: the caller owns that flip (the API layer sets
	// monitored=false when the last agent of the host is gone).
	Revoke(ctx context.Context, orgID, hostID uuid.UUID) error
}

// Service implements the agent registry.
type Service struct {
	store       Store
	provisioner HostProvisioner
	now         func() time.Time
	rand        io.Reader
}

// Option configures a Service.
type Option func(*Service)

// WithProvisioner wires the host resolution/creation port claim needs.
func WithProvisioner(p HostProvisioner) Option { return func(s *Service) { s.provisioner = p } }

// WithClock injects the clock (tests).
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithRand injects the entropy source for ids and enrollment secrets.
func WithRand(r io.Reader) Option { return func(s *Service) { s.rand = r } }

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
	t := Token{
		ID: id, OrgID: orgID, ZoneID: in.ZoneID, HostID: in.HostID, NameHint: in.NameHint,
		MaxUses: in.MaxUses, ExpiresAt: in.ExpiresAt, CreatedBy: in.CreatedBy, CreatedAt: s.now(),
	}
	stored, err := s.store.CreateToken(ctx, t, hash[:])
	if err != nil {
		return enrolltoken.Token{}, err
	}
	// The pin travels inside the token; the CA ensures it matches the
	// manager's own certificate (Task 6/7 wire the real CA here).
	return enrolltoken.Token{ID: stored.ID, Secret: secret, CAPin: [32]byte{}}, nil
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
// name the provisioner reports as taken is ErrTokenConflict.
func (s *Service) ClaimToken(ctx context.Context, orgID uuid.UUID, token enrolltoken.Token, name string) (ClaimResult, error) {
	if s.provisioner == nil {
		return ClaimResult{}, errors.New("agents: claim: service was built without WithProvisioner")
	}
	var hostID uuid.UUID
	created := false
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
			return nil
		}
		hostName := t.NameHint
		if hostName == "" {
			hostName = name
		}
		if hostName == "" {
			return fmt.Errorf("%w: token carries no name hint and none was presented", ErrInvalidInput)
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
		hostID, created = id, true
		return nil
	})
	// The store owns the unknown-token refusal (ErrTokenInvalid); the
	// closure's own sentinels — ErrTokenInvalid for the dead token,
	// ErrNotFound for a vanished bound host, ErrTokenConflict for a taken
	// name — pass through unchanged.
	if err != nil {
		return ClaimResult{}, err
	}
	return ClaimResult{HostID: hostID, Created: created}, nil
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
func (s *Service) Register(ctx context.Context, orgID, hostID uuid.UUID, agentName, version, serial string) error {
	if agentName == "" {
		return fmt.Errorf("%w: agent name is required", ErrInvalidInput)
	}
	if serial == "" {
		return fmt.Errorf("%w: certificate serial is required", ErrInvalidInput)
	}
	return s.store.Register(ctx, Agent{HostID: hostID, OrgID: orgID, AgentName: agentName, CertSerial: serial, Version: version})
}

// GetBySerial is the gRPC authentication path: the presented serial's
// identity, org-unscoped. Unknown serials are ErrNotFound; a revoked
// certificate is reported through AgentIdentity.Revoked — Task 7's
// interceptor refuses on it (workers refuse inside the service; here the
// renewal path needs to distinguish a live from a revoked serial, so the
// raw fact is returned).
func (s *Service) GetBySerial(ctx context.Context, serial string) (AgentIdentity, error) {
	if serial == "" {
		return AgentIdentity{}, ErrNotFound
	}
	return s.store.GetBySerial(ctx, serial)
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

// Revoke ends an agent: every certificate serial the host's agent held is
// marked revoked (the interceptor refuses them from then on) and the
// host_agents row is removed. hosts.monitored is NOT flipped here — the
// caller owns that flip (documented on Store.Revoke).
func (s *Service) Revoke(ctx context.Context, orgID, hostID uuid.UUID) error {
	return s.store.Revoke(ctx, orgID, hostID)
}
