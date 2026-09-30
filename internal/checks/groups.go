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
	// ErrKeyTaken means a check definition's key already exists at the
	// same (org, binding_kind, group, host) scope — the unique
	// (org_id, binding_kind, group_id, host_id, key) NULLS NOT DISTINCT
	// constraint definitions.go's Store extension enforces.
	ErrKeyTaken = errors.New("key already in use at this scope")
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

// HostRoster tells the resolver (resolver.go) which hosts in an
// organization are currently monitored — the only hosts any binding ever
// resolves to a non-empty set for ("Resolution": "Only hosts with
// monitored = true resolve to a non-empty set"). internal/hosts
// (hosts.Service.ListHosts filtered to Monitored = true) satisfies it.
//
// This is a second narrow port, deliberately not folded into HostMatcher
// or Store: HostMatcher answers "which hosts does this selector match"
// (a per-group question, used by Preview too); Store persists this
// package's own rows and every method on it must be satisfiable by
// SQLStore, which nothing here requires touching. Like HostMatcher, this
// package keeps no Go-level dependency on the hosts service surface, and
// production wiring is deferred to whichever task wires HostMatcher's own
// adapter — a Service built without WithHostRoster refuses to resolve,
// the same fail-closed posture Preview takes without WithMatcher.
type HostRoster interface {
	// MonitoredHosts returns the ids of every host in orgID with
	// monitored = true. Order is not significant — the resolver treats
	// the result as a set and sorts whatever it derives from it before
	// that ordering matters (ResolvedSet.Checks, ResolveOrg's version).
	MonitoredHosts(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error)
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

	// Definitions (Task 2, definitions.go): check_definitions rows, the
	// same (org, id) scoping discipline as the group methods above.
	CreateDefinition(ctx context.Context, d Definition) (Definition, error)
	// GetDefinition looks up the definition identified by (orgID, id);
	// ErrNotFound if no such row exists. UpdateDefinition needs it to see
	// a seeded row's current key and plugin before deciding whether the
	// caller's input may replace them.
	GetDefinition(ctx context.Context, orgID, id uuid.UUID) (Definition, error)
	ListDefinitions(ctx context.Context, orgID uuid.UUID) ([]Definition, error)
	// UpdateDefinition replaces every mutable column of the definition
	// identified by (d.OrgID, d.ID); ErrNotFound if no such row exists.
	UpdateDefinition(ctx context.Context, d Definition) (Definition, error)
	// DeleteDefinition removes the definition identified by (orgID, id);
	// ErrNotFound if no such row exists.
	DeleteDefinition(ctx context.Context, orgID, id uuid.UUID) error

	// RecordObservations (Task 4, samples.go) writes obs — already
	// validated by the service (plausibility window, well-shaped
	// value/label/err) — in one transaction: check_samples rows inserted,
	// check_current rows upserted, both committing together. It applies
	// exactly one rejection rule of its own: an Observation whose HostID
	// does not belong to orgID is skipped rather than inserted, and
	// counted in rejected instead of failing the rest of the batch.
	// accepted + rejected == len(obs) always.
	RecordObservations(ctx context.Context, orgID uuid.UUID, obs []Observation) (accepted, rejected int, err error)
	// CurrentForHost returns hostID's latest known value per (check_key,
	// item, metric). Rows are never deleted (Current's own doc comment),
	// so an item that stopped being reported is still listed, however
	// stale its At.
	CurrentForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]Current, error)
	// SeriesForHost returns metric's samples for hostID's (checkKey, item)
	// in [from, to), downsampled server-side into at most buckets points.
	SeriesForHost(ctx context.Context, orgID, hostID uuid.UUID, checkKey, item, metric string, from, to time.Time, buckets int) (Series, error)
}

// Service manages monitoring groups. Later tasks (check definitions, the
// resolver, sample storage) extend it in the same package. It carries no
// clock: nothing here validates against "now" (unlike, say, maintenance's
// cron windows), so CreatedAt/UpdatedAt are left for the store to stamp —
// SQLStore relies on the columns' own DEFAULT now() and reports back
// whatever the database actually wrote, the same way maintenance.Window
// and agents.Token do.
type Service struct {
	store      Store
	matcher    HostMatcher
	hostRoster HostRoster
	rand       io.Reader
	now        func() time.Time
	// offsets is what ingest learned about each host's clock, kept in
	// this process for the host page to read back (skew.go).
	offsets *clockOffsets
}

// Option configures a Service.
type Option func(*Service)

// WithMatcher wires the host-selector port Preview and the resolver
// (resolver.go) need; a Service built without it refuses Preview or a
// resolve that actually needs to expand a group rather than reaching into
// a nil interface.
func WithMatcher(m HostMatcher) Option { return func(s *Service) { s.matcher = m } }

// WithHostRoster wires the monitored-hosts port the resolver needs; a
// Service built without it refuses ResolveHost/ResolveOrg outright (see
// HostRoster's own doc comment).
func WithHostRoster(r HostRoster) Option { return func(s *Service) { s.hostRoster = r } }

// WithRand injects the entropy source for ids.
func WithRand(r io.Reader) Option { return func(s *Service) { s.rand = r } }

// WithClock injects the clock RecordObservations (samples.go) checks the
// plausibility window against; time.Now is the default. Tests pin the
// window's exact boundaries against a fixed clock.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// NewService wires the store; Preview needs WithMatcher.
func NewService(store Store, opts ...Option) *Service {
	s := &Service{store: store, rand: rand.Reader, now: time.Now, offsets: newClockOffsets()}
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

// GetGroup returns the single group identified by (orgID, id), or
// ErrNotFound when the organization has no such group — including when it
// belongs to another organization, which reads exactly the same (see
// ErrNotFound's own doc comment).
//
// It is a filter over List rather than a Store method of its own: an
// organization's monitoring groups are a short, already-org-scoped list
// (they exist to be enumerated in a selector editor), the resolver loads
// all of them for every single resolve anyway, and adding a
// GetMonitoringGroup query would mean a second SQL path that could answer
// differently from the one every other read here already goes through.
func (s *Service) GetGroup(ctx context.Context, orgID, id uuid.UUID) (Group, error) {
	groups, err := s.store.List(ctx, orgID)
	if err != nil {
		return Group{}, err
	}
	for _, g := range groups {
		if g.ID == id {
			return g, nil
		}
	}
	return Group{}, ErrNotFound
}

// GroupsForHost returns the monitoring groups in orgID whose selector
// currently matches hostID, built from the exact same
// loadResolveBundle/groupHosts membership the resolver (resolver.go) uses
// to expand a group into the hosts its own definitions reach — never a
// second selector evaluation written beside it, so a host can never
// receive a group's rules (internal/rules' precedence resolver, wired
// through this method) without also receiving that group's checks.
// Requires WithMatcher if orgID has any monitoring groups, exactly like
// ResolveHost; an organization with none needs no matcher, also like
// ResolveHost. Order is not significant — same contract as ListGroups.
func (s *Service) GroupsForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]Group, error) {
	bundle, err := s.loadResolveBundle(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var out []Group
	for _, g := range bundle.groups {
		if _, matched := bundle.groupHosts[g.ID][hostID]; matched {
			out = append(out, g)
		}
	}
	return out, nil
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
