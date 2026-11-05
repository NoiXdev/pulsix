// Package sshconf stores how Pulsix reaches managed hosts over SSH (spec §5,
// backlog 2.7a): organization-level jump hosts that may chain through each
// other, a default jump host per zone, and each host's user, port,
// credential and jump host mode. Resolving the effective jump host along the
// inventory tree is the renderer's job (internal/inventory/render).
package sshconf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
)

// Sentinel errors mapped to HTTP status by the transport layer.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrNameTaken    = errors.New("name already in use")
	// ErrRevisionMismatch is a host SSH write whose If-Match named a host
	// revision that is no longer current (backlog 7.79).
	ErrRevisionMismatch = errors.New("revision does not match")
	// ErrInUse means a zone, group, host or another jump host still
	// references the jump host.
	ErrInUse = errors.New("jump host is in use")
)

// BastionMode says how a host picks its jump host.
type BastionMode string

// Jump host modes.
const (
	ModeInherit  BastionMode = "inherit"
	ModeNone     BastionMode = "none"
	ModeExplicit BastionMode = "explicit"
)

// Valid reports whether m is a known mode.
func (m BastionMode) Valid() bool { return m == ModeInherit || m == ModeNone || m == ModeExplicit }

// Defaults and limits.
const (
	DefaultPort = 22
	MaxChain    = 4
)

var (
	namePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,63}$`)
)

// Bastion is a jump host.
type Bastion struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	Name         string
	Address      string
	Port         int
	Username     string
	CredentialID *uuid.UUID
	ViaBastionID *uuid.UUID
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// BastionInput creates or fully replaces a jump host. Port 0 means 22.
type BastionInput struct {
	Name         string
	Address      string
	Port         int
	Username     string
	CredentialID *uuid.UUID
	ViaBastionID *uuid.UUID
}

// HostSSH is a host's SSH connection settings.
type HostSSH struct {
	HostID       uuid.UUID
	OrgID        uuid.UUID
	Username     string
	Port         int
	CredentialID *uuid.UUID
	BastionMode  BastionMode
	BastionID    *uuid.UUID
	// HostRevision is the host's revision after the write that returned
	// this value (backlog 7.79); zero on a read.
	HostRevision int64
}

// HostSSHInput replaces a host's SSH settings. Port 0 means 22 and an empty
// mode means inherit.
type HostSSHInput struct {
	Username     string
	Port         int
	CredentialID *uuid.UUID
	BastionMode  BastionMode
	BastionID    *uuid.UUID
	// IfRevision, when set, is the host revision the caller's edit is based
	// on; a stale one is ErrRevisionMismatch. Nil writes unconditionally.
	IfRevision *int64
}

// Store persists jump hosts, zone defaults and host SSH settings.
type Store interface {
	CreateBastion(ctx context.Context, b Bastion) (Bastion, error)
	GetBastion(ctx context.Context, orgID, id uuid.UUID) (Bastion, error)
	ListBastions(ctx context.Context, orgID uuid.UUID) ([]Bastion, error)
	UpdateBastion(ctx context.Context, b Bastion) (Bastion, error)
	DeleteBastion(ctx context.Context, orgID, id uuid.UUID) error
	SetZoneBastion(ctx context.Context, orgID, zoneID uuid.UUID, bastionID *uuid.UUID) error
	ZoneBastions(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]uuid.UUID, error)
	// GetHostSSH returns ErrNotFound when the host has no stored settings.
	GetHostSSH(ctx context.Context, orgID, hostID uuid.UUID) (HostSSH, error)
	// PutHostSSH upserts the settings and bumps the host's revision in one
	// transaction (backlog 7.79), returning them with HostRevision set. A
	// non-nil ifRevision that is no longer current writes nothing and is
	// ErrRevisionMismatch; an unknown host is ErrNotFound.
	PutHostSSH(ctx context.Context, h HostSSH, ifRevision *int64) (HostSSH, error)
	ListHostSSH(ctx context.Context, orgID uuid.UUID) ([]HostSSH, error)
	// WithChainLock runs fn against a Store bound to one transaction holding
	// orgID's jump-host chain advisory lock (backlog 2.15): a chain read
	// inside fn followed by a write inside the same fn call is atomic
	// against any concurrent WithChainLock call for the same organization.
	// Every write that depends on a chain snapshot (a create or update that
	// sets ViaBastionID) must run inside it.
	//
	// fn must call through txStore alone, never through the outer ctx, a
	// field on Service (s.creds, s.hosts), or any other *db.DB-backed
	// store or service not bound to this transaction — see
	// inventory.Store.WithTreeLock's doc comment for the pool-deadlock
	// this guards against (the same hazard applies here: a call through
	// the ambient ctx opens its own transaction on its own pool
	// connection while this one already holds one, which a small,
	// contended pool may never free up again).
	WithChainLock(ctx context.Context, orgID uuid.UUID, fn func(Store) error) error
}

// CredentialLookup is the slice of credentials.Service this package needs.
type CredentialLookup interface {
	Get(ctx context.Context, orgID, id uuid.UUID) (credentials.Credential, error)
}

// HostLookup is the slice of hosts.Service this package needs.
type HostLookup interface {
	GetHost(ctx context.Context, orgID, id uuid.UUID) (hosts.Host, error)
}

// Service implements jump hosts and host SSH settings.
type Service struct {
	store Store
	creds CredentialLookup
	hosts HostLookup
}

// NewService wires the store and the lookups used to validate references.
func NewService(store Store, creds CredentialLookup, hostLookup HostLookup) *Service {
	return &Service{store: store, creds: creds, hosts: hostLookup}
}

// CreateBastion validates and stores a new jump host.
func (s *Service) CreateBastion(ctx context.Context, orgID uuid.UUID, in BastionInput) (Bastion, error) {
	b := bastionFrom(uuid.Must(uuid.NewV7()), orgID, in)
	if err := s.checkBastionBasics(ctx, b); err != nil {
		return Bastion{}, err
	}
	if b.ViaBastionID == nil {
		// Nothing can chain through a jump host that does not exist yet, so
		// creating one without a via cannot affect any chain: no lock needed.
		return s.store.CreateBastion(ctx, b)
	}
	var result Bastion
	err := s.store.WithChainLock(ctx, orgID, func(txStore Store) error {
		if err := s.checkChain(ctx, txStore, b); err != nil {
			return err
		}
		created, err := txStore.CreateBastion(ctx, b)
		if err != nil {
			return err
		}
		result = created
		return nil
	})
	if err != nil {
		return Bastion{}, err
	}
	return result, nil
}

// GetBastion returns one jump host.
func (s *Service) GetBastion(ctx context.Context, orgID, id uuid.UUID) (Bastion, error) {
	return s.store.GetBastion(ctx, orgID, id)
}

// ListBastions returns the organization's jump hosts ordered by name.
func (s *Service) ListBastions(ctx context.Context, orgID uuid.UUID) ([]Bastion, error) {
	return s.store.ListBastions(ctx, orgID)
}

// UpdateBastion fully replaces a jump host, rejecting the change when its
// own chain, or the chain of any other jump host of the organization, would
// become cyclic or too long.
func (s *Service) UpdateBastion(ctx context.Context, orgID, id uuid.UUID, in BastionInput) (Bastion, error) {
	// The existence check runs before field validation, same as before
	// this method gained a chain lock: an update to a jump host that does
	// not exist must 404, never 400, regardless of what else about the
	// request is also wrong.
	if _, err := s.store.GetBastion(ctx, orgID, id); err != nil {
		return Bastion{}, err
	}
	b := bastionFrom(id, orgID, in)
	if err := s.checkBastionBasics(ctx, b); err != nil {
		return Bastion{}, err
	}
	if b.ViaBastionID == nil {
		// Clearing or never setting a via cannot create a cycle: no chain
		// lock needed, just write it.
		return s.store.UpdateBastion(ctx, b)
	}
	var result Bastion
	err := s.store.WithChainLock(ctx, orgID, func(txStore Store) error {
		if err := s.checkChain(ctx, txStore, b); err != nil {
			return err
		}
		updated, err := txStore.UpdateBastion(ctx, b)
		if err != nil {
			return err
		}
		result = updated
		return nil
	})
	if err != nil {
		return Bastion{}, err
	}
	return result, nil
}

// DeleteBastion removes a jump host nothing references (ErrInUse
// otherwise). It runs under the same per-organization chain lock as
// CreateBastion and UpdateBastion (2.15) so it never races a concurrent
// chain edit that read this jump host's existence into a now-stale
// snapshot.
func (s *Service) DeleteBastion(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.WithChainLock(ctx, orgID, func(txStore Store) error {
		return txStore.DeleteBastion(ctx, orgID, id)
	})
}

// SetZoneBastion sets or clears (nil) a zone's default jump host.
func (s *Service) SetZoneBastion(ctx context.Context, orgID, zoneID uuid.UUID, bastionID *uuid.UUID) error {
	if bastionID != nil {
		if err := s.bastionExists(ctx, orgID, *bastionID); err != nil {
			return err
		}
	}
	return s.store.SetZoneBastion(ctx, orgID, zoneID, bastionID)
}

// ZoneBastions maps zone id to default jump host id for zones that have one.
func (s *Service) ZoneBastions(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	return s.store.ZoneBastions(ctx, orgID)
}

// HostSSH returns a host's SSH settings, or the defaults when none are stored.
func (s *Service) HostSSH(ctx context.Context, orgID, hostID uuid.UUID) (HostSSH, error) {
	if err := s.hostExists(ctx, orgID, hostID); err != nil {
		return HostSSH{}, err
	}
	h, err := s.store.GetHostSSH(ctx, orgID, hostID)
	if errors.Is(err, ErrNotFound) {
		return HostSSH{HostID: hostID, OrgID: orgID, Port: DefaultPort, BastionMode: ModeInherit}, nil
	}
	return h, err
}

// SetHostSSH validates and replaces a host's SSH settings.
func (s *Service) SetHostSSH(ctx context.Context, orgID, hostID uuid.UUID, in HostSSHInput) (HostSSH, error) {
	if err := s.hostExists(ctx, orgID, hostID); err != nil {
		return HostSSH{}, err
	}
	h := HostSSH{HostID: hostID, OrgID: orgID, Username: in.Username, Port: in.Port, CredentialID: in.CredentialID, BastionMode: in.BastionMode, BastionID: in.BastionID}
	if h.Port == 0 {
		h.Port = DefaultPort
	}
	if h.BastionMode == "" {
		h.BastionMode = ModeInherit
	}
	if err := validatePort(h.Port); err != nil {
		return HostSSH{}, err
	}
	if h.Username != "" && !usernamePattern.MatchString(h.Username) {
		return HostSSH{}, fmt.Errorf("%w: username may contain letters, digits, dots, underscores, @ and hyphens (at most 64)", ErrInvalidInput)
	}
	if !h.BastionMode.Valid() {
		return HostSSH{}, fmt.Errorf("%w: jump host mode must be inherit, none or explicit", ErrInvalidInput)
	}
	if (h.BastionMode == ModeExplicit) != (h.BastionID != nil) {
		return HostSSH{}, fmt.Errorf("%w: a jump host is named exactly when the mode is explicit", ErrInvalidInput)
	}
	if h.BastionID != nil {
		if err := s.bastionExists(ctx, orgID, *h.BastionID); err != nil {
			return HostSSH{}, err
		}
	}
	if err := s.checkCredential(ctx, orgID, h.CredentialID); err != nil {
		return HostSSH{}, err
	}
	return s.store.PutHostSSH(ctx, h, in.IfRevision)
}

// ListHostSSH returns every stored host SSH setting of the organization.
func (s *Service) ListHostSSH(ctx context.Context, orgID uuid.UUID) ([]HostSSH, error) {
	return s.store.ListHostSSH(ctx, orgID)
}

func bastionFrom(id, orgID uuid.UUID, in BastionInput) Bastion {
	b := Bastion{ID: id, OrgID: orgID, Name: in.Name, Address: in.Address, Port: in.Port, Username: in.Username, CredentialID: in.CredentialID, ViaBastionID: in.ViaBastionID}
	if b.Port == 0 {
		b.Port = DefaultPort
	}
	return b
}

// checkBastionBasics validates everything about b that does not depend on
// the organization's other jump hosts: field shape and the credential
// reference. It runs before any lock is taken.
func (s *Service) checkBastionBasics(ctx context.Context, b Bastion) error {
	if !namePattern.MatchString(b.Name) {
		return fmt.Errorf("%w: name must be 1-63 lowercase letters, digits, dots, underscores or hyphens", ErrInvalidInput)
	}
	if net.ParseIP(b.Address) == nil && !hostnamePattern.MatchString(b.Address) {
		return fmt.Errorf("%w: address must be a hostname or an IP address", ErrInvalidInput)
	}
	if err := validatePort(b.Port); err != nil {
		return err
	}
	if !usernamePattern.MatchString(b.Username) {
		return fmt.Errorf("%w: username is required and may contain letters, digits, dots, underscores, @ and hyphens", ErrInvalidInput)
	}
	return s.checkCredential(ctx, b.OrgID, b.CredentialID)
}

// checkChain validates b's jump host chain (and every other jump host's
// chain that reaches through b) against st's current state. b.ViaBastionID
// must be non-nil; callers only need this check — and the WithChainLock it
// must run inside — when it is. st is the lock-bound Store passed into
// WithChainLock's fn, so this reads the same transaction the write commits
// to, never a snapshot a concurrent caller could have moved on from.
func (s *Service) checkChain(ctx context.Context, st Store, b Bastion) error {
	all, err := st.ListBastions(ctx, b.OrgID)
	if err != nil {
		return err
	}
	m := make(map[uuid.UUID]Bastion, len(all)+1)
	for _, x := range all {
		m[x.ID] = x
	}
	m[b.ID] = b
	if _, err := Chain(m, b.ID); err != nil {
		return err
	}
	// The change also moves every jump host reached through this one: none
	// of their chains may become cyclic or too long either.
	for _, x := range all {
		if x.ID == b.ID {
			continue
		}
		if _, problem := chain(m, x.ID); problem != "" {
			return fmt.Errorf("%w: this change breaks the chain of jump host %s: %s", ErrInvalidInput, x.Name, problem)
		}
	}
	return nil
}

func (s *Service) checkCredential(ctx context.Context, orgID uuid.UUID, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	c, err := s.creds.Get(ctx, orgID, *id)
	if errors.Is(err, credentials.ErrNotFound) {
		return fmt.Errorf("%w: credential %s does not exist", ErrInvalidInput, *id)
	}
	if err != nil {
		return err
	}
	if !c.Kind.IsSSH() {
		return fmt.Errorf("%w: SSH access needs an SSH key or SSH password credential", ErrInvalidInput)
	}
	return nil
}

func (s *Service) bastionExists(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.store.GetBastion(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: jump host %s does not exist", ErrInvalidInput, id)
	}
	return err
}

func (s *Service) hostExists(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.hosts.GetHost(ctx, orgID, id)
	if errors.Is(err, hosts.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func validatePort(p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("%w: port must be between 1 and 65535", ErrInvalidInput)
	}
	return nil
}
