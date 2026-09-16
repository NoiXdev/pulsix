// Package hosts implements zones and hosts: the first real domain resource
// in Pulsix, and the template every later resource (checks, jobs, ...)
// follows — a Store seam next to the service, keyset-paginated listing, and
// org-scoped everything.
//
// A host carries two independent capability flags. Managed means Ansible
// can run against it over SSH; Monitored means checks run on or against
// it. Neither implies the other: a Kubernetes node may be managed only, a
// network switch monitored only by remote checks, a web server both. See
// Service.SetCapabilities for how a partial update is kept from ever
// clearing the flag it does not mention.
package hosts

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts/selector"
)

// Sentinel errors mapped to HTTP status by the transport layer, the same
// shape tenancy and setup give their own callers.
var (
	// ErrInvalidInput means the caller-supplied value itself is malformed
	// (a bad host name, an empty zone slug) — never a store failure.
	ErrInvalidInput = errors.New("invalid input")
	// ErrNotFound means no genuine row exists for the given (org, id) pair
	// — a host or zone that does not exist, or exists in a different
	// organization. It is deliberately the same error for "does not exist"
	// and "belongs to someone else": a host in another org must be
	// invisible, not merely forbidden.
	ErrNotFound = errors.New("not found")
	// ErrNameTaken means a host name or zone slug already exists within
	// the organization (the unique (org_id, name) / (org_id, slug)
	// constraints in the schema) — reported as a conflict, not a generic
	// store failure.
	ErrNameTaken = errors.New("name already in use")
	// ErrInvalidCursor is returned by DecodeCursor when cursor is not one
	// EncodeCursor produced.
	ErrInvalidCursor = errors.New("hosts: invalid cursor")
)

// hostNamePattern is the brief's own rule: lowercase letters, digits, dots
// and hyphens, 1-254 characters, never starting or ending with a dot or
// hyphen. It exists so a host name is always safe to use as, for example,
// an Ansible inventory hostname or a DNS label component.
var hostNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,252}[a-z0-9])?$`)

// zoneSlugPattern mirrors the organization slug rule (org_handlers.go):
// lowercase letters, digits and single hyphens between segments.
var zoneSlugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Default zone constants. EnsureDefaultZone provisions exactly this zone,
// and only this zone, for every organization — see its own doc comment.
const (
	DefaultZoneSlug = "default"
	DefaultZoneName = "Default"
)

// Zone groups hosts within an organization — a datacenter, a region, an
// environment, in the operator's own vocabulary.
type Zone struct {
	ID    uuid.UUID
	OrgID uuid.UUID
	Slug  string
	Name  string
	// BastionID is the zone's default jump host. Package hosts only reads
	// it; internal/sshconf writes it (SetZoneBastion).
	BastionID *uuid.UUID
}

// Host is one managed or monitored (or both) target.
type Host struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	ZoneID     uuid.UUID
	Name       string
	Address    string
	Managed    bool
	Monitored  bool
	Facts      map[string]any
	LastSeenAt *time.Time
	CreatedAt  time.Time
}

// HostInput is the caller-supplied shape for creating or fully replacing a
// host. Managed and Monitored are plain bools, not pointers: Create and
// Update always set both flags explicitly from the request body. A caller
// that wants to change only one flag without touching the other uses
// SetCapabilities instead, whose managed/monitored parameters are pointers
// for exactly that reason.
type HostInput struct {
	Name      string
	Address   string
	ZoneID    uuid.UUID
	Managed   bool
	Monitored bool
}

// ListFilter narrows ListHosts. A zero ListFilter matches every host in the
// organization.
type ListFilter struct {
	ZoneID    *uuid.UUID
	Managed   *bool
	Monitored *bool
	// Query, if non-empty, matches hosts whose name or address contains it
	// (case-insensitive).
	Query string
}

// Store is the persistence contract for zones and hosts. Every method that
// can find nothing returns ErrNotFound — never a swallowed error — so the
// service can fail closed the same way tenancy.Store and auth.Store do.
type Store interface {
	CreateZone(ctx context.Context, z Zone) error
	ListZones(ctx context.Context, orgID uuid.UUID) ([]Zone, error)
	// GetZone returns the zone identified by (orgID, zoneID), or
	// ErrNotFound if it does not exist or belongs to a different
	// organization — a zone from another org must be exactly as
	// unusable as one that does not exist at all.
	GetZone(ctx context.Context, orgID, zoneID uuid.UUID) (Zone, error)
	CreateHost(ctx context.Context, h Host) error
	// GetHost returns the host identified by (orgID, hostID), or
	// ErrNotFound if it does not exist or belongs to a different
	// organization — the same invisible-rather-than-forbidden contract as
	// GetZone.
	GetHost(ctx context.Context, orgID, hostID uuid.UUID) (Host, error)
	// ListHosts returns a keyset-paginated page of orgID's hosts matching
	// f, ordered by (name, id) ascending, plus the cursor for the next
	// page (empty when there is none). cursor is opaque to the caller —
	// see EncodeCursor/DecodeCursor.
	ListHosts(ctx context.Context, orgID uuid.UUID, f ListFilter, cursor string, limit int) ([]Host, string, error)
	// UpdateHost persists every mutable field of h (ZoneID, Name, Address,
	// Managed, Monitored) and returns the row as stored, or ErrNotFound if
	// no host matches (h.ID, h.OrgID).
	UpdateHost(ctx context.Context, h Host) (Host, error)
	// PatchCapabilities atomically applies a partial update to the Managed
	// and/or Monitored flag of the host identified by (orgID, hostID) in a
	// single statement — a nil pointer leaves that column exactly as it
	// was — rather than a read-modify-write, so two concurrent calls
	// touching different flags can never lose one's write to the other's
	// (Task 13 fix round 1, finding 2). Returns ErrNotFound if no host
	// matches (orgID, hostID).
	PatchCapabilities(ctx context.Context, orgID, hostID uuid.UUID, managed, monitored *bool) (Host, error)
	// DeleteHost removes the host identified by (orgID, hostID), scoped to
	// the organization, and returns ErrNotFound if no such host existed.
	DeleteHost(ctx context.Context, orgID, hostID uuid.UUID) error
	// OrgsWithoutDefaultZone returns the id of every organization that has
	// no zone named DefaultZoneSlug — used only by
	// Service.ReconcileDefaultZones, the startup pass that heals an
	// organization a post-commit OrgCreatedHook failure left zoneless (see
	// OrgCreatedHook's own doc comment in package tenancy).
	OrgsWithoutDefaultZone(ctx context.Context) ([]uuid.UUID, error)

	// ListHostTags returns every tag (both "manual" and "fact" sources)
	// recorded for the host identified by (orgID, hostID). It does not
	// itself check the host exists — Service.ListTags does that first via
	// GetHost, the same "resolve and check ownership before touching the
	// child resource" shape CreateHost's own zone check follows.
	ListHostTags(ctx context.Context, orgID, hostID uuid.UUID) ([]Tag, error)
	// ReplaceManualTags replaces every "manual" tag on the host identified
	// by (orgID, hostID) with tags, leaving every "fact" tag on that host
	// untouched. Every element of tags must already carry
	// Source == TagSourceManual — Service.SetTags is what enforces that,
	// and what validates the tags themselves, before this is ever called.
	ReplaceManualTags(ctx context.Context, orgID, hostID uuid.UUID, tags []Tag) error
	// ListTagKeyValueCounts returns, for orgID, every distinct (key, value)
	// pair recorded on any of its hosts (either source) and how many hosts
	// carry it, ordered by key then value — Service.ListTagKeys groups
	// this flat list into []KeyValues, relying on that ordering to know
	// when one key's values end and the next begins.
	ListTagKeyValueCounts(ctx context.Context, orgID uuid.UUID) ([]TagValueCount, error)
	// QueryHosts returns every host in orgID whose merged tags (a "manual"
	// tag overriding a "fact" tag on the same key, the same precedence
	// Service.SetTags' own doc comment describes) match sel, ordered by
	// (name, id) ascending like ListHosts. See selector.Selector.SQL's own
	// doc comment for how the SQL-backed implementation produces that
	// merged view from host_tags' raw, per-source rows.
	QueryHosts(ctx context.Context, orgID uuid.UUID, sel selector.Selector) ([]Host, error)

	// ListAllHosts returns every host of orgID ordered by name, unpaginated,
	// for the inventory renderer.
	ListAllHosts(ctx context.Context, orgID uuid.UUID) ([]Host, error)

	// ListOrgIDs returns every organization id, for background
	// reconciliation passes (the daily re-test). organizations carries no
	// row-level security by design, so this scan sees all of them.
	ListOrgIDs(ctx context.Context) ([]uuid.UUID, error)

	// Host-key pins and connection-test history (hostkey.go, spec §5).
	//
	// SetHostKeyPin creates the host_ssh row with the defaults when the
	// host carries no SSH settings of its own, and stores the pin on the
	// row — nothing else on the row changes. A host that does not exist in
	// the organization fails (ErrNotFound via the foreign key).
	SetHostKeyPin(ctx context.Context, orgID, hostID uuid.UUID, fingerprint, publicKey string) error
	// ClearHostKeyPin clears the pin (fingerprint and key); no host_ssh row
	// (nothing pinned) is a no-op, not an error.
	ClearHostKeyPin(ctx context.Context, orgID, hostID uuid.UUID) error
	// BackfillHostKeyPin stores publicKey on a pin that carries only the
	// fingerprint (pinned before migration 00021) — and only when the pin
	// equals fingerprint; anything else is a no-op.
	BackfillHostKeyPin(ctx context.Context, orgID, hostID uuid.UUID, fingerprint, publicKey string) error
	// ListPinnedKeys maps each host id that carries a pin to its
	// fingerprint and, when stored, its full public key.
	ListPinnedKeys(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]HostKeyPin, error)
	// ListChangedKeys returns the host ids whose LATEST connection test
	// reports a changed host key — the block-on-mismatch input.
	ListChangedKeys(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) ([]uuid.UUID, error)
	// CreateHostTest appends one connection-test outcome row.
	CreateHostTest(ctx context.Context, t HostTest) error
	// LatestHostTest returns the newest test row of the host, ErrNotFound
	// when the host has never been tested.
	LatestHostTest(ctx context.Context, orgID, hostID uuid.UUID) (HostTest, error)
	// ListHostsDueForTest returns the org's managed hosts with no test
	// newer than since and none still queued — the daily re-test selector.
	ListHostsDueForTest(ctx context.Context, orgID uuid.UUID, since time.Time) ([]Host, error)
}

// DefaultListLimit and MaxListLimit bound ListHosts' page size, the same
// clamping shape audit.DefaultListLimit/MaxListLimit give the activity
// feed: DefaultListLimit when the caller asks for no particular size
// (limit <= 0), MaxListLimit as a hard ceiling.
const (
	DefaultListLimit = 20
	MaxListLimit     = 100
)

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultListLimit
	case limit > MaxListLimit:
		return MaxListLimit
	default:
		return limit
	}
}

// Service implements zones and hosts.
type Service struct {
	store Store
	now   func() time.Time
}

// NewService wires the store and clock.
func NewService(store Store, now func() time.Time) *Service {
	return &Service{store: store, now: now}
}

// validateSlug reports ErrInvalidInput if slug or name is empty or slug
// does not match zoneSlugPattern.
func validateZoneInput(slug, name string) error {
	var problems []string
	if !zoneSlugPattern.MatchString(slug) {
		problems = append(problems, "slug must be lowercase letters, digits and single hyphens between segments")
	}
	if strings.TrimSpace(name) == "" {
		problems = append(problems, "name is required")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidInput, strings.Join(problems, "; "))
	}
	return nil
}

// CreateZone creates a zone under orgID.
func (s *Service) CreateZone(ctx context.Context, orgID uuid.UUID, slug, name string) (Zone, error) {
	if err := validateZoneInput(slug, name); err != nil {
		return Zone{}, err
	}
	z := Zone{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Slug: slug, Name: name}
	if err := s.store.CreateZone(ctx, z); err != nil {
		return Zone{}, err
	}
	return z, nil
}

// ListZones lists every zone in orgID.
func (s *Service) ListZones(ctx context.Context, orgID uuid.UUID) ([]Zone, error) {
	return s.store.ListZones(ctx, orgID)
}

// EnsureDefaultZone creates orgID's "default" zone if it does not already
// have one. It is idempotent — safe to call more than once for the same
// organization — which is what lets it double as both the org-creation
// hook (see tenancy.OrgCreatedHook) and, if that first call ever fails, a
// safe target for a later manual retry.
func (s *Service) EnsureDefaultZone(ctx context.Context, orgID uuid.UUID) error {
	_, err := s.CreateZone(ctx, orgID, DefaultZoneSlug, DefaultZoneName)
	if err != nil && !errors.Is(err, ErrNameTaken) {
		return fmt.Errorf("ensure default zone: %w", err)
	}
	return nil
}

// ReconcileDefaultZones finds every organization with no "default" zone —
// the state a post-commit tenancy.OrgCreatedHook failure can leave behind
// (see that type's own doc comment) and that a caller retrying org
// creation cannot repair, since the org's slug is already taken — and
// gives each one its default zone via EnsureDefaultZone.
//
// It is meant to run once at startup (internal/app.Build), turning a
// permanent inconsistency into one that heals itself on the next restart.
// One organization failing to repair does not stop the rest from being
// tried: repaired counts every organization that was actually fixed, and
// err — when non-nil — joins (errors.Join) every failure that occurred, for
// the caller to log; it is never returned in a way that would make the
// caller treat "a handful of organizations are still broken" the same as
// "the database is unreachable".
func (s *Service) ReconcileDefaultZones(ctx context.Context) (repaired int, err error) {
	orgIDs, listErr := s.store.OrgsWithoutDefaultZone(ctx)
	if listErr != nil {
		return 0, fmt.Errorf("list organizations without a default zone: %w", listErr)
	}
	var errs []error
	for _, orgID := range orgIDs {
		if ensureErr := s.EnsureDefaultZone(ctx, orgID); ensureErr != nil {
			errs = append(errs, fmt.Errorf("org %s: %w", orgID, ensureErr))
			continue
		}
		repaired++
	}
	return repaired, errors.Join(errs...)
}

// CreateHost creates a host under orgID. The zone must already belong to
// orgID (checked via store.GetZone, so a zone id from another org fails
// exactly like an unknown one — ErrNotFound), and the name must be valid
// and unique within the organization.
func (s *Service) CreateHost(ctx context.Context, orgID uuid.UUID, in HostInput) (Host, error) {
	if !hostNamePattern.MatchString(in.Name) {
		return Host{}, fmt.Errorf("%w: host name must match %s", ErrInvalidInput, hostNamePattern.String())
	}
	// store.GetZone's contract already returns exactly ErrNotFound for a
	// zone that does not exist or belongs to a different organization
	// (see the Store interface's own doc comment), so that sentinel
	// propagates unchanged — a zone from another org must fail exactly
	// like an unknown one.
	if _, err := s.store.GetZone(ctx, orgID, in.ZoneID); err != nil {
		return Host{}, err
	}
	h := Host{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, ZoneID: in.ZoneID, Name: in.Name, Address: in.Address,
		Managed: in.Managed, Monitored: in.Monitored, Facts: map[string]any{}, CreatedAt: s.now(),
	}
	if err := s.store.CreateHost(ctx, h); err != nil {
		return Host{}, err
	}
	return h, nil
}

// GetHost returns the host identified by (orgID, id), or ErrNotFound if it
// does not exist or belongs to a different organization.
func (s *Service) GetHost(ctx context.Context, orgID, id uuid.UUID) (Host, error) {
	return s.store.GetHost(ctx, orgID, id)
}

// UpdateHost replaces every mutable field of the host identified by
// (orgID, id) with in, after the same name and zone-ownership validation
// CreateHost applies.
func (s *Service) UpdateHost(ctx context.Context, orgID, id uuid.UUID, in HostInput) (Host, error) {
	if !hostNamePattern.MatchString(in.Name) {
		return Host{}, fmt.Errorf("%w: host name must match %s", ErrInvalidInput, hostNamePattern.String())
	}
	if _, err := s.store.GetZone(ctx, orgID, in.ZoneID); err != nil {
		return Host{}, err
	}
	h := Host{ID: id, OrgID: orgID, ZoneID: in.ZoneID, Name: in.Name, Address: in.Address, Managed: in.Managed, Monitored: in.Monitored}
	return s.store.UpdateHost(ctx, h)
}

// SetCapabilities updates the Managed and/or Monitored flag of the host
// identified by (orgID, id) in one atomic store call (Store.PatchCapabilities)
// rather than a read-modify-write. A nil pointer leaves that flag exactly
// as it was — this is what keeps the two capabilities genuinely
// independent: a caller setting only Managed can never accidentally clear
// Monitored (or vice versa) — and expressing the update as a single
// statement is what keeps two *concurrent* callers independent too: a
// read-modify-write here would let one caller's write silently clobber the
// other's (Task 13 fix round 1, finding 2 — see PatchHostCapabilities'
// own comment, internal/db/queries/hosts.sql, and
// TestSQLStorePatchCapabilitiesConcurrent, store_sql_test.go, for the race
// this closes).
func (s *Service) SetCapabilities(ctx context.Context, orgID, id uuid.UUID, managed, monitored *bool) (Host, error) {
	return s.store.PatchCapabilities(ctx, orgID, id, managed, monitored)
}

// ListHosts returns a keyset-paginated page of orgID's hosts matching f.
// limit is clamped by clampLimit exactly like audit.Recorder.List clamps
// its own limit.
func (s *Service) ListHosts(ctx context.Context, orgID uuid.UUID, f ListFilter, cursor string, limit int) ([]Host, string, error) {
	return s.store.ListHosts(ctx, orgID, f, cursor, clampLimit(limit))
}

// DeleteHost removes the host identified by (orgID, id), scoped to the
// organization: a host id that exists but belongs to a different org is
// reported exactly like one that does not exist at all (ErrNotFound).
func (s *Service) DeleteHost(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.DeleteHost(ctx, orgID, id)
}

// ListAllHosts returns every host of orgID ordered by name.
func (s *Service) ListAllHosts(ctx context.Context, orgID uuid.UUID) ([]Host, error) {
	return s.store.ListAllHosts(ctx, orgID)
}

// EncodeCursor and DecodeCursor are the one keyset-cursor format both Store
// implementations in this package use (the in-memory fake in
// service_test.go and the SQL store in store_sql.go), mirroring
// audit.EncodeCursor/DecodeCursor's own role for the activity feed. The
// cursor carries exactly the keyset (name, id) a stable
// ORDER BY name ASC, id ASC needs to resume after the last row of the
// previous page. It is opaque to callers.
func EncodeCursor(name string, id uuid.UUID) string {
	raw := name + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor reverses EncodeCursor. An empty cursor decodes to an empty
// name and uuid.Nil with no error — the "first page" case every caller in
// this package treats as "no lower bound yet", not a malformed cursor.
// Host names can never contain "|" (hostNamePattern only allows
// [a-z0-9.-]), so splitting on the first "|" unambiguously recovers name.
func DecodeCursor(cursor string) (string, uuid.UUID, error) {
	if cursor == "" {
		return "", uuid.Nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", uuid.Nil, ErrInvalidCursor
	}
	name, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return "", uuid.Nil, ErrInvalidCursor
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return "", uuid.Nil, ErrInvalidCursor
	}
	return name, id, nil
}
