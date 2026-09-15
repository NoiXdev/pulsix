// Package credentials is each organization's encrypted secret store: SSH
// keys and passwords for managed hosts and jump hosts, Git deploy keys and
// tokens (spec §4, §5; backlog 2.7). Private material is sealed with the
// instance master key and never leaves this package in clear; the public key
// of a key credential is kept in clear so operators can install it.
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors mapped to HTTP status by the transport layer.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrNameTaken    = errors.New("name already in use")
	// ErrInUse means a jump host or a host still references the credential.
	ErrInUse = errors.New("credential is in use")
)

// Kind is what a credential holds.
type Kind string

// Credential kinds for v0.2a. Channel secrets arrive with alerting (v0.4).
const (
	KindSSHKey      Kind = "ssh_key"
	KindSSHPassword Kind = "ssh_password"
	KindGitKey      Kind = "git_key"
	KindGitToken    Kind = "git_token"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	switch k {
	case KindSSHKey, KindSSHPassword, KindGitKey, KindGitToken:
		return true
	}
	return false
}

// IsKey reports whether k holds a private key.
func (k Kind) IsKey() bool { return k == KindSSHKey || k == KindGitKey }

// IsSSH reports whether k can log in to a host or jump host.
func (k Kind) IsSSH() bool { return k == KindSSHKey || k == KindSSHPassword }

// ExpiryWarning is how long before expiry a credential is flagged.
const ExpiryWarning = 30 * 24 * time.Hour

// DefaultLoginUser is the account the snippets create when a credential
// names no username.
const DefaultLoginUser = "ansible"

var (
	namePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,63}$`)
)

// Credential is the non-secret part of a stored credential.
type Credential struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	Name        string
	Kind        Kind
	Username    string
	PublicKey   string // "<type> <base64>" without comment; key kinds only
	Fingerprint string // SHA256:...; key kinds only
	KeyType     string // ed25519, rsa-4096, ecdsa-p256; key kinds only
	ExpiresAt   *time.Time
	CreatedBy   *uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Expired reports whether the credential has expired at now.
func (c Credential) Expired(now time.Time) bool {
	return c.ExpiresAt != nil && !now.Before(*c.ExpiresAt)
}

// ExpiresSoon reports whether the credential expires within ExpiryWarning.
func (c Credential) ExpiresSoon(now time.Time) bool {
	return c.ExpiresAt != nil && !c.Expired(now) && c.ExpiresAt.Sub(now) <= ExpiryWarning
}

// Secret is the sealed payload; the field matching the kind is set.
type Secret struct {
	PrivateKey string `json:"private_key,omitempty"`
	Password   string `json:"password,omitempty"`
	Token      string `json:"token,omitempty"`
}

// Material is the secret part of a create or update request.
type Material struct {
	Generate   bool   // key kinds: generate a new ed25519 key
	PrivateKey string // key kinds: import this private key (OpenSSH or PEM)
	Passphrase string // decrypts PrivateKey once; never stored
	Password   string // ssh_password
	Token      string // git_token
}

func (m Material) empty() bool {
	return !m.Generate && m.PrivateKey == "" && m.Password == "" && m.Token == ""
}

// CreateInput is a new credential.
type CreateInput struct {
	Name      string
	Username  string
	Kind      Kind
	ExpiresAt *time.Time
	CreatedBy *uuid.UUID
	Material  Material
}

// UpdateInput replaces a credential's name, username and expiry; a non-empty
// Material also replaces its secret.
type UpdateInput struct {
	Name      string
	Username  string
	ExpiresAt *time.Time
	Material  Material
}

// Sealer is the slice of crypto.Box the service needs.
type Sealer interface {
	SealAAD(plain, aad []byte) ([]byte, error)
	OpenAAD(sealed, aad []byte) ([]byte, error)
}

// Usage counts what references a credential.
type Usage struct {
	Bastions int
	Hosts    int
}

// Store persists credentials. Every lookup that finds nothing returns
// ErrNotFound; a name clash returns ErrNameTaken; deleting a referenced
// credential returns ErrInUse.
type Store interface {
	Create(ctx context.Context, c Credential, sealed []byte) (Credential, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Credential, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Credential, error)
	// Update persists Name, Username, ExpiresAt, PublicKey, Fingerprint and
	// KeyType; a nil sealed keeps the stored secret.
	Update(ctx context.Context, c Credential, sealed []byte) (Credential, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	// Usage maps credential id to its reference counts; credentials that
	// nothing references may be absent.
	Usage(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]Usage, error)
	// LoadSecret returns the kind and the sealed secret bytes of one
	// credential — the one store read that exposes ciphertext, used only
	// by Service.OpenSecret (the job-claim path). ErrNotFound when the
	// credential does not exist in the organization.
	LoadSecret(ctx context.Context, orgID, id uuid.UUID) (Kind, []byte, error)
}

// Service implements the credential store.
type Service struct {
	store Store
	box   Sealer
	now   func() time.Time
	rand  io.Reader
	// rotation is the rotation group's enqueue port (SetRotationEnqueuer,
	// wired after construction by internal/app because the concrete
	// implementation is the jobs service, whose own construction depends
	// on this one). Nil means Rotate fails closed — a wiring gap, never a
	// silent no-op.
	rotation RotationEnqueuer
}

// NewService wires the store, the sealing box, the clock and the entropy
// source used for key generation.
func NewService(store Store, box Sealer, now func() time.Time, rand io.Reader) *Service {
	return &Service{store: store, box: box, now: now, rand: rand}
}

// SetRotationEnqueuer injects the rotation enqueue port, the same
// post-construction wiring shape the queues' sinks use: the concrete
// implementation (internal/jobs) is built after this service and depends
// on it through its own ports.
func (s *Service) SetRotationEnqueuer(e RotationEnqueuer) { s.rotation = e }

// AAD is the additional data a credential's secret is sealed with.
func AAD(orgID, id uuid.UUID) []byte {
	return []byte("credential:" + orgID.String() + ":" + id.String())
}

// Create validates, builds the secret for the kind, seals it and stores it.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, in CreateInput) (Credential, error) {
	if !in.Kind.Valid() {
		return Credential{}, fmt.Errorf("%w: kind must be ssh_key, ssh_password, git_key or git_token", ErrInvalidInput)
	}
	if err := validateMeta(in.Name, in.Username); err != nil {
		return Credential{}, err
	}
	now := s.now()
	c := Credential{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: in.Name, Kind: in.Kind, Username: in.Username,
		ExpiresAt: in.ExpiresAt, CreatedBy: in.CreatedBy, CreatedAt: now, UpdatedAt: now,
	}
	secret, err := s.buildSecret(&c, in.Material)
	if err != nil {
		return Credential{}, err
	}
	sealed, err := s.seal(c, secret)
	if err != nil {
		return Credential{}, err
	}
	return s.store.Create(ctx, c, sealed)
}

// Get returns one credential's metadata.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Credential, error) {
	return s.store.Get(ctx, orgID, id)
}

// List returns every credential of the organization, ordered by name.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Credential, error) {
	return s.store.List(ctx, orgID)
}

// Update replaces name, username and expiry, and the secret when in.Material
// carries one.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, in UpdateInput) (Credential, error) {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Credential{}, err
	}
	if err := validateMeta(in.Name, in.Username); err != nil {
		return Credential{}, err
	}
	next := cur
	next.Name, next.Username, next.ExpiresAt, next.UpdatedAt = in.Name, in.Username, in.ExpiresAt, s.now()
	var sealed []byte
	if !in.Material.empty() {
		secret, err := s.buildSecret(&next, in.Material)
		if err != nil {
			return Credential{}, err
		}
		if sealed, err = s.seal(next, secret); err != nil {
			return Credential{}, err
		}
	}
	return s.store.Update(ctx, next, sealed)
}

// Delete removes a credential that nothing references (ErrInUse otherwise).
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.Delete(ctx, orgID, id)
}

// Usage returns reference counts per credential for the "used by" column.
func (s *Service) Usage(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]Usage, error) {
	return s.store.Usage(ctx, orgID)
}

// OpenSecret decrypts and returns a credential's secret. It exists for the
// one caller that legitimately needs plaintext material — the job queue
// building a claim's payload for a worker (backlog 2.17): every other read
// stays on the metadata paths, and this method must never be wired into
// anything that logs, caches or serves its result. The sealed bytes only
// open with the credential's own AAD, so a ciphertext copied into another
// row or organization fails here.
func (s *Service) OpenSecret(ctx context.Context, orgID, id uuid.UUID) (Secret, error) {
	kind, sealed, err := s.store.LoadSecret(ctx, orgID, id)
	if err != nil {
		return Secret{}, err
	}
	plain, err := s.box.OpenAAD(sealed, AAD(orgID, id))
	if err != nil {
		return Secret{}, fmt.Errorf("open credential secret: %w", err)
	}
	var secret Secret
	if err := json.Unmarshal(plain, &secret); err != nil {
		return Secret{}, fmt.Errorf("decode credential secret: %w", err)
	}
	if secret.fieldFor(kind) == "" {
		return Secret{}, fmt.Errorf("open credential secret: stored secret carries no value for kind %s", kind)
	}
	return secret, nil
}

// fieldFor reports which Secret field the kind's material lives in.
func (s Secret) fieldFor(k Kind) string {
	switch k {
	case KindSSHKey, KindGitKey:
		return s.PrivateKey
	case KindSSHPassword:
		return s.Password
	case KindGitToken:
		return s.Token
	}
	return ""
}

// ExpiryState reports whether c has expired or expires within ExpiryWarning,
// measured with the service's clock.
func (s *Service) ExpiryState(c Credential) (expired, soon bool) {
	now := s.now()
	return c.Expired(now), c.ExpiresSoon(now)
}

func validateMeta(name, username string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%w: name must be 1-63 lowercase letters, digits, dots, underscores or hyphens, starting with a letter or digit", ErrInvalidInput)
	}
	if username != "" && !usernamePattern.MatchString(username) {
		return fmt.Errorf("%w: username may contain letters, digits, dots, underscores, @ and hyphens (at most 64)", ErrInvalidInput)
	}
	return nil
}

// buildSecret checks that m carries exactly what c.Kind needs and fills the
// key metadata of c for key kinds.
func (s *Service) buildSecret(c *Credential, m Material) (Secret, error) {
	switch {
	case c.Kind.IsKey():
		if m.Password != "" || m.Token != "" {
			return Secret{}, fmt.Errorf("%w: a key credential takes a generated or imported key only", ErrInvalidInput)
		}
		if m.Generate == (m.PrivateKey != "") {
			return Secret{}, fmt.Errorf("%w: choose exactly one: generate a key or import a private key", ErrInvalidInput)
		}
		var k keyMaterial
		var err error
		if m.Generate {
			k, err = generateKey(s.rand)
		} else {
			k, err = importKey(m.PrivateKey, m.Passphrase)
		}
		if err != nil {
			return Secret{}, err
		}
		c.PublicKey, c.Fingerprint, c.KeyType = k.publicKey, k.fingerprint, k.keyType
		return Secret{PrivateKey: k.privatePEM}, nil
	case c.Kind == KindSSHPassword:
		if m.Generate || m.PrivateKey != "" || m.Token != "" {
			return Secret{}, fmt.Errorf("%w: a password credential takes a password only", ErrInvalidInput)
		}
		if m.Password == "" {
			return Secret{}, fmt.Errorf("%w: password is required", ErrInvalidInput)
		}
		return Secret{Password: m.Password}, nil
	default:
		if m.Generate || m.PrivateKey != "" || m.Password != "" {
			return Secret{}, fmt.Errorf("%w: a token credential takes a token only", ErrInvalidInput)
		}
		if m.Token == "" {
			return Secret{}, fmt.Errorf("%w: token is required", ErrInvalidInput)
		}
		return Secret{Token: m.Token}, nil
	}
}

func (s *Service) seal(c Credential, secret Secret) ([]byte, error) {
	plain, err := json.Marshal(secret)
	if err != nil {
		return nil, fmt.Errorf("encode credential secret: %w", err)
	}
	sealed, err := s.box.SealAAD(plain, AAD(c.OrgID, c.ID))
	if err != nil {
		return nil, fmt.Errorf("seal credential secret: %w", err)
	}
	return sealed, nil
}
