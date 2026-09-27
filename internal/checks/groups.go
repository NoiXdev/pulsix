// Package checks implements the manager-side model of the monitoring check
// pipeline (v0.3b-1, spec docs/superpowers/specs/2026-09-27-v0.3b-checks-
// design.md): what is measured, and where. This task adds monitoring
// groups — a named, saved host-tag selector with a priority, used both to
// bind check definitions to a set of hosts and to break ties when more
// than one group's definitions apply to the same host (the resolver,
// "Resolution": higher priority wins, an older group breaks a tie). Check
// definitions, the resolver and sample storage are later tasks that extend
// the Service, Store and Option this file establishes, in the same
// package.
package checks

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts/selector"
)

// Sentinel errors mapped to HTTP status by the transport layer, the same
// shape every other domain service in this codebase uses.
var (
	// ErrInvalidInput means the caller-supplied value itself is malformed
	// — an empty or over-long name, an out-of-range priority, or a
	// selector internal/hosts/selector cannot parse — never a store
	// failure.
	ErrInvalidInput = errors.New("invalid input")
	// ErrNotFound means no genuine row exists for the given (org, id) pair
	// — a group that does not exist, or belongs to a different
	// organization; the two read the same, so a group in another org
	// stays invisible rather than merely forbidden.
	ErrNotFound = errors.New("not found")
	// ErrNameTaken means a group name already exists within the
	// organization (the unique (org_id, name) constraint).
	ErrNameTaken = errors.New("name already in use")
)

// maxGroupNameRunes bounds a group's name, the same shape maintenance
// windows use for theirs.
const maxGroupNameRunes = 100

// Group is a named, saved host-tag selector with a priority.
type Group struct {
	ID, OrgID            uuid.UUID
	Name, Selector       string
	Priority             int
	CreatedAt, UpdatedAt time.Time
}

// GroupInput is the caller-supplied shape for creating or fully replacing
// a group.
type GroupInput struct {
	Name     string
	Selector string
	Priority int
}

// HostMatcher resolves a selector expression to the ids of the hosts in
// orgID it currently matches. internal/hosts satisfies it — Service.
// QueryHosts adapted to take the raw selector text instead of a parsed
// selector.Selector — so this package depends on the port only, never on
// the hosts service surface, and stays drivable from a fake in tests.
// Both the monitoring-group editor's live preview (Service.Preview below)
// and the resolver's expansion of a group into hosts share the one
// implementation wired behind this port.
type HostMatcher interface {
	MatchingHosts(ctx context.Context, orgID uuid.UUID, selector string) ([]uuid.UUID, error)
}

// Store persists monitoring groups (org-scoped).
type Store interface {
	Create(ctx context.Context, g Group) (Group, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Group, error)
	// Update replaces every mutable column (Name, Selector, Priority) of
	// the group identified by (g.OrgID, g.ID); ErrNotFound if no such row
	// exists.
	Update(ctx context.Context, g Group) (Group, error)
	// Delete removes the group identified by (orgID, id); ErrNotFound if
	// no such row exists.
	Delete(ctx context.Context, orgID, id uuid.UUID) error
}

// Service manages monitoring groups. Later tasks (check definitions, the
// resolver, sample storage) extend it in the same package. It carries no
// clock: nothing here validates against "now" (unlike, say, maintenance's
// cron windows), so CreatedAt/UpdatedAt are left for the store to stamp —
// SQLStore relies on the columns' own DEFAULT now() and reports back
// whatever the database actually wrote, the same way maintenance.Window
// and agents.Token do.
type Service struct {
	store   Store
	matcher HostMatcher
	rand    io.Reader
}

// Option configures a Service.
type Option func(*Service)

// WithMatcher wires the host-selector port Preview needs; a Service built
// without it refuses Preview rather than reaching into a nil interface.
func WithMatcher(m HostMatcher) Option { return func(s *Service) { s.matcher = m } }

// WithRand injects the entropy source for ids.
func WithRand(r io.Reader) Option { return func(s *Service) { s.rand = r } }

// NewService wires the store; Preview needs WithMatcher.
func NewService(store Store, opts ...Option) *Service {
	s := &Service{store: store, rand: rand.Reader}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Service) newID() uuid.UUID {
	id, err := uuid.NewV7FromReader(s.rand)
	if err != nil {
		panic(err) //nolint:forbidigo // entropy failure is unrecoverable, like uuid.Must
	}
	return id
}

// normalize validates in into a group's mutable fields (Name, Selector,
// Priority); id, org and the timestamps are the caller's. The selector is
// validated by parsing it — an unparseable expression is ErrInvalidInput
// with the parser's own message, never stored (the parsed value itself is
// discarded; only the source text is kept, so a group's selector reads
// back exactly as it was typed).
func normalize(in GroupInput) (Group, error) {
	var g Group
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > maxGroupNameRunes {
		return g, fmt.Errorf("%w: name must be 1-%d characters", ErrInvalidInput, maxGroupNameRunes)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return g, fmt.Errorf("%w: name must not contain control characters", ErrInvalidInput)
		}
	}
	if in.Priority < math.MinInt32 || in.Priority > math.MaxInt32 {
		return g, fmt.Errorf("%w: priority must fit in a 32-bit integer", ErrInvalidInput)
	}
	sel := strings.TrimSpace(in.Selector)
	if _, err := selector.Parse(sel); err != nil {
		return g, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	g.Name, g.Selector, g.Priority = name, sel, in.Priority
	return g, nil
}

// CreateGroup validates in and stores a new monitoring group.
func (s *Service) CreateGroup(ctx context.Context, orgID uuid.UUID, in GroupInput) (Group, error) {
	g, err := normalize(in)
	if err != nil {
		return Group{}, err
	}
	g.ID, g.OrgID = s.newID(), orgID
	return s.store.Create(ctx, g)
}

// UpdateGroup validates in and replaces the group identified by
// (orgID, id).
func (s *Service) UpdateGroup(ctx context.Context, orgID, id uuid.UUID, in GroupInput) (Group, error) {
	g, err := normalize(in)
	if err != nil {
		return Group{}, err
	}
	g.ID, g.OrgID = id, orgID
	return s.store.Update(ctx, g)
}

// DeleteGroup removes the group identified by (orgID, id).
func (s *Service) DeleteGroup(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.Delete(ctx, orgID, id)
}

// ListGroups returns orgID's monitoring groups.
func (s *Service) ListGroups(ctx context.Context, orgID uuid.UUID) ([]Group, error) {
	return s.store.List(ctx, orgID)
}

// Preview resolves sel against orgID's current hosts without saving
// anything: the group editor's live match count, and the same computation
// the resolver later uses to expand a group into hosts, so the two
// answers to "what does this selector mean" can never disagree. An
// unparseable selector is ErrInvalidInput, exactly like Create/UpdateGroup
// give for the same input.
func (s *Service) Preview(ctx context.Context, orgID uuid.UUID, sel string) ([]uuid.UUID, error) {
	if s.matcher == nil {
		return nil, errors.New("checks: preview: service was built without WithMatcher")
	}
	if _, err := selector.Parse(sel); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return s.matcher.MatchingHosts(ctx, orgID, sel)
}
