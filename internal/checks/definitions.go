// Definitions in this file are what is measured, and where (v0.3b-1, spec
// docs/superpowers/specs/2026-09-27-v0.3b-checks-design.md, "Data model"):
// a plugin bound to exactly one scope — the organization, one monitoring
// group, or one host — under a key that a narrower scope overrides on (see
// the same spec's "Resolution", which Task 3 implements against the
// fields this file produces: Key, BindingKind, Enabled, GroupID/HostID).
package checks

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// BindingKind names the scope a check definition applies at.
type BindingKind string

// The three scopes "Resolution" precedence runs over, narrowest last:
// host beats group beats organization.
const (
	BindingOrg   BindingKind = "org"
	BindingGroup BindingKind = "group"
	BindingHost  BindingKind = "host"
)

// Origin distinguishes a row the seed set created from one an operator
// created, and — since migration 00062, spec v0.2f decision 2 — from one
// that is a projection of an installation-owned definition. Only the first
// and third are ever restricted (see UpdateDefinition/DeleteDefinition):
// origin itself is never caller-supplied, so there is no exported type for
// it — CreateDefinition always stamps originCustom, SeedForOrg always
// stamps originSeed, and internal/shared always stamps originShared when it
// writes a projection directly (bypassing this package's Store entirely,
// the same way it bypasses internal/repos and internal/templates for their
// own projections).
const (
	originSeed   = "seed"
	originCustom = "custom"
	originShared = "shared"
)

// The plugins the agent can run: the four v0.3b shipped (spec's Decisions
// table: "Plugin set") plus network and uptime (backlog 3.7). A table in
// code rather than a free-form string so an unknown plugin is refused at
// write time instead of silently never running — there is no other plugin
// the agent could execute even if a definition named one.
const (
	PluginCPU     = "cpu"
	PluginMemory  = "memory"
	PluginDisk    = "disk"
	PluginService = "service"
	PluginNetwork = "network"
	PluginUptime  = "uptime"
)

var supportedPlugins = map[string]bool{
	PluginCPU: true, PluginMemory: true, PluginDisk: true, PluginService: true,
	PluginNetwork: true, PluginUptime: true,
}

// keyPattern is the same shape a plugin name itself always satisfies, so a
// key left to default to the plugin name never fails this check.
var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

const (
	// MinInterval is the shortest check interval a definition may carry. It
	// is exported because it is also the fastest an agent can legitimately
	// produce observations, which internal/agentapi's per-stream ingest
	// budget uses as its refill period (backlog 7.111).
	MinInterval = 10 * time.Second
	maxInterval = 24 * time.Hour
)

// Definition is a plugin bound to a scope. Samples (a later task) are keyed
// by Key, not ID: editing or deleting a definition must not orphan or
// rewrite history the operator is currently looking at on a graph.
type Definition struct {
	ID, OrgID       uuid.UUID
	Key, Plugin     string
	Params          map[string]any
	Interval        time.Duration
	Enabled         bool
	Origin          string // "seed" | "custom" | "shared"
	BindingKind     BindingKind
	GroupID, HostID *uuid.UUID
	// SharedDefinitionID is non-nil when this row is a PROJECTION of an
	// installation-owned definition (migration 00062, spec v0.2f decision
	// 1): shared_check_definitions.id, not composed with org_id — the
	// definition has no org_id to compose with, repos.Repo.SharedRepoID's
	// own doc comment, unchanged reasoning. Key, Plugin, Params and
	// Interval then belong to the definition and are refused on write
	// (UpdateDefinition, spec decision 2); BindingKind, GroupID, HostID and
	// Enabled remain this organization's own — the OPPOSITE split from a
	// repository or job template, where the binding is what stays
	// editable. Nil for an ordinary seed or custom definition.
	SharedDefinitionID   *uuid.UUID
	CreatedAt, UpdatedAt time.Time
}

// DefinitionInput is the caller-supplied shape for creating or fully
// replacing a definition.
type DefinitionInput struct {
	Key, Plugin     string
	Params          map[string]any
	Interval        time.Duration
	Enabled         bool
	BindingKind     BindingKind
	GroupID, HostID *uuid.UUID
}

// stringList reads a []string out of a value that may be either — a
// literal []string (a Go caller, or SeedDefinitions' own literals) or a
// []any of strings (what encoding/json decodes a JSON array into, which is
// what every params value coming off the wire actually is).
func stringList(v any) ([]string, bool) {
	switch vv := v.(type) {
	case []string:
		return vv, true
	case []any:
		out := make([]string, len(vv))
		for i, e := range vv {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	default:
		return nil, false
	}
}

// validateParams checks params against plugin's own schema: disk takes an
// optional mount allow-list ("mounts"), network an optional interface list
// ("interfaces"), service a required non-empty unit list ("units"), cpu,
// memory and uptime take no parameters at all. Rejecting
// unknown keys (rather than ignoring them) is deliberate — a typo'd
// parameter name must fail loudly at write time, not be silently dropped
// and discovered missing at the agent weeks later.
func validateParams(plugin string, params map[string]any) error {
	switch plugin {
	case PluginCPU, PluginMemory, PluginUptime:
		if len(params) > 0 {
			return fmt.Errorf("%w: plugin %q takes no parameters", ErrInvalidInput, plugin)
		}
	case PluginDisk, PluginNetwork:
		return validateOptionalList(plugin, itemParam[plugin], params)
	case PluginService:
		for k := range params {
			if k != "units" {
				return fmt.Errorf("%w: service: unknown parameter %q", ErrInvalidInput, k)
			}
		}
		units, ok := stringList(params["units"])
		if !ok || len(units) == 0 {
			return fmt.Errorf("%w: service: units is required and must be a non-empty list of strings", ErrInvalidInput)
		}
		for _, u := range units {
			if strings.TrimSpace(u) == "" {
				return fmt.Errorf("%w: service: units must not contain an empty entry", ErrInvalidInput)
			}
		}
	default:
		// Reachable only if a caller adds a plugin constant without
		// adding it to supportedPlugins and this switch — normalizeDefinition
		// itself already refuses an unsupported plugin before this runs.
		return fmt.Errorf("%w: unsupported plugin %q", ErrInvalidInput, plugin)
	}
	return nil
}

// validateOptionalList checks the params of a plugin whose only parameter
// is an optional list of the items it watches — disk's mounts, network's
// interfaces: no other key, and when the list is given, strings that are
// not blank.
func validateOptionalList(plugin, param string, params map[string]any) error {
	for k := range params {
		if k != param {
			return fmt.Errorf("%w: %s: unknown parameter %q", ErrInvalidInput, plugin, k)
		}
	}
	v, ok := params[param]
	if !ok {
		return nil
	}
	list, ok := stringList(v)
	if !ok {
		return fmt.Errorf("%w: %s: %s must be a list of strings", ErrInvalidInput, plugin, param)
	}
	for _, e := range list {
		if strings.TrimSpace(e) == "" {
			return fmt.Errorf("%w: %s: %s must not contain an empty entry", ErrInvalidInput, plugin, param)
		}
	}
	return nil
}

// ValidateDefinitionShape validates and normalizes the four columns spec
// decision 2 gives a check definition's installation-owned half — plugin,
// key, params and interval — independently of any binding. normalizeDefinition
// below calls this for an organization's own definition (which additionally
// needs the binding this function knows nothing about); internal/shared
// calls it too, for its own installation-owned check definitions, which
// bypass this package's Store entirely when writing a projection (the same
// way shared.Service bypasses internal/templates for a shared template's
// extra vars) but still need the identical plugin set, key pattern and
// per-plugin parameter schema applied before anything is written. Exported
// specifically so internal/shared does not re-derive or duplicate any of
// the three — "Reuse, do not copy" (task-7-brief).
func ValidateDefinitionShape(plugin, key string, params map[string]any, interval time.Duration) (normalizedKey string, err error) {
	plugin = strings.TrimSpace(plugin)
	if !supportedPlugins[plugin] {
		return "", fmt.Errorf("%w: plugin must be one of cpu, memory, disk, service, network, uptime, got %q", ErrInvalidInput, plugin)
	}
	normalizedKey = strings.TrimSpace(key)
	if normalizedKey == "" {
		normalizedKey = plugin // "key defaults to the plugin name" (spec's Data model)
	}
	if !keyPattern.MatchString(normalizedKey) {
		return "", fmt.Errorf("%w: key must match %s", ErrInvalidInput, keyPattern.String())
	}
	if interval < MinInterval || interval > maxInterval {
		return "", fmt.Errorf("%w: interval must be between %s and %s", ErrInvalidInput, MinInterval, maxInterval)
	}
	if err := validateParams(plugin, params); err != nil {
		return "", err
	}
	return normalizedKey, nil
}

// normalizeDefinition validates in into a definition's mutable fields; id,
// org, origin and the timestamps are the caller's (CreateDefinition,
// UpdateDefinition, SeedForOrg each set them their own way).
func normalizeDefinition(in DefinitionInput) (Definition, error) {
	var d Definition
	plugin := strings.TrimSpace(in.Plugin)
	key, err := ValidateDefinitionShape(plugin, in.Key, in.Params, in.Interval)
	if err != nil {
		return d, err
	}
	switch in.BindingKind {
	case BindingOrg:
		if in.GroupID != nil || in.HostID != nil {
			return d, fmt.Errorf("%w: an organization-bound definition must not set a group or host", ErrInvalidInput)
		}
	case BindingGroup:
		if in.GroupID == nil || in.HostID != nil {
			return d, fmt.Errorf("%w: a group-bound definition must set exactly a group", ErrInvalidInput)
		}
	case BindingHost:
		if in.HostID == nil || in.GroupID != nil {
			return d, fmt.Errorf("%w: a host-bound definition must set exactly a host", ErrInvalidInput)
		}
	default:
		return d, fmt.Errorf("%w: binding_kind must be org, group or host, got %q", ErrInvalidInput, in.BindingKind)
	}
	d.Key, d.Plugin, d.Params = key, plugin, in.Params
	d.Interval, d.Enabled = in.Interval, in.Enabled
	d.BindingKind, d.GroupID, d.HostID = in.BindingKind, in.GroupID, in.HostID
	return d, nil
}

// createDefinition is CreateDefinition and SeedForOrg's shared body; only
// the origin they stamp differs.
func (s *Service) createDefinition(ctx context.Context, orgID uuid.UUID, in DefinitionInput, origin string) (Definition, error) {
	d, err := normalizeDefinition(in)
	if err != nil {
		return Definition{}, err
	}
	if origin == originCustom {
		if err := s.refuseKeyHeldBySharedProjection(ctx, orgID, d.Key); err != nil {
			return Definition{}, err
		}
	}
	d.ID, d.OrgID, d.Origin = s.newID(), orgID, origin
	return s.store.CreateDefinition(ctx, d)
}

// refuseKeyHeldBySharedProjection keeps a shared projection's organization-
// scope slot reserved, because migration 00063's trigger needs it free.
//
// That trigger rebinds a projection to ('org', nil, nil) when the group or
// host it was bound to is deleted, so a customer deleting their own group
// does not destroy the provider's check. But check_definitions carries
// UNIQUE NULLS NOT DISTINCT (org_id, binding_kind, group_id, host_id, key),
// so the rebind only succeeds while that slot is free. A customer who
// rebinds the projection to a group and then creates their own check with
// the same key at organization scope takes it — and the next deletion of
// that group fails on 23505, aborting DELETE FROM monitoring_groups and
// leaving them unable to remove their own group, with an error naming a
// table they never touched.
//
// Refusing here rather than coping there is deliberate: by the time the
// trigger runs, the only choices left are failing the customer's delete or
// silently dropping the provider's check, and both are worse than refusing
// a key up front. Migration 00063's comment names this function as what
// guarantees its UPDATE can land.
//
// Narrow on purpose. It bites only on a key a SHARED projection in this
// organization already holds, at any scope on create and whenever an
// update lands the row at org scope. The same key at two different
// scopes stays legal otherwise — NULLS NOT DISTINCT allows it deliberately,
// and disk org-wide alongside disk on one host with different parameters is
// a real configuration — so widening this to a general cross-scope ban
// would change behaviour for ordinary checks that have nothing to do with
// shared resources. Seeded rows skip the check: SeedForOrg writes only the
// reserved keys, which validateCheckDefinitionInput already refuses to a
// shared definition, so the two sets cannot meet.
func (s *Service) refuseKeyHeldBySharedProjection(ctx context.Context, orgID uuid.UUID, key string) error {
	defs, err := s.store.ListDefinitions(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list definitions to check shared keys: %w", err)
	}
	for _, d := range defs {
		if d.Origin == originShared && d.Key == key {
			return fmt.Errorf("%w: key %q belongs to a check the installation owns here; its organization-wide slot stays reserved so the check can return to it", ErrInvalidInput, key)
		}
	}
	return nil
}

// CreateDefinition validates in and stores a new, operator-created check
// definition (origin = "custom" — only SeedForOrg ever writes "seed").
func (s *Service) CreateDefinition(ctx context.Context, orgID uuid.UUID, in DefinitionInput) (Definition, error) {
	return s.createDefinition(ctx, orgID, in, originCustom)
}

// UpdateDefinition validates in and replaces the definition identified by
// (orgID, id) — a full replace, exactly like UpdateGroup — except that a
// seeded row's key and plugin are fixed: an operator who wants a different
// plugin or key at that scope copies the row (CreateDefinition) and edits
// the copy, rather than this call quietly turning the seed set into
// something else out from under whatever assumed it still measured cpu.
// Its interval, parameters, enabled flag and binding are all ordinary
// mutable columns even on a seeded row.
func (s *Service) UpdateDefinition(ctx context.Context, orgID, id uuid.UUID, in DefinitionInput) (Definition, error) {
	// An update must NAME the key. normalizeDefinition defaults a blank one
	// to the plugin name, which is right for a create — "key defaults to the
	// plugin name" is the spec's own rule and the only key most definitions
	// ever need. On a full-replace PUT it means renaming by omission: a
	// custom "disk-data" check submitted with an empty or absent key comes
	// back as "disk", quietly moving the identifier the override precedence
	// keys on (and, where that name is taken at the same scope, tripping
	// migration 00043's unique index as an opaque conflict instead). Neither
	// is something a caller asked for, so it is refused here rather than
	// guessed at (task-10 review round 2, finding 1).
	if strings.TrimSpace(in.Key) == "" {
		return Definition{}, fmt.Errorf("%w: an update must name the definition's key; omitting it would rename the check to its plugin name", ErrInvalidInput)
	}
	d, err := normalizeDefinition(in)
	if err != nil {
		return Definition{}, err
	}
	existing, err := s.store.GetDefinition(ctx, orgID, id)
	if err != nil {
		return Definition{}, err
	}
	if existing.Origin == originSeed && (d.Key != existing.Key || d.Plugin != existing.Plugin) {
		return Definition{}, fmt.Errorf("%w: a seeded definition's key and plugin are fixed; copy it to change them", ErrInvalidInput)
	}
	// A projected definition (origin = "shared", spec v0.2f decision 2):
	// key, plugin, params and interval belong to the installation-owned
	// definition and are refused here, the same way repos.Service.Update's
	// own refuseSharedFieldChange refuses name/url/branch/subpath on a
	// projected repository — except the SET of refused columns is the
	// OPPOSITE one, because decision 2 gives a check definition's binding to
	// the projection, not the definition (task-7-brief's own reasoning: a
	// group or host is the customer's own data the installation cannot
	// know, so the binding is something the customer fills in, like a
	// credential). BindingKind, GroupID, HostID and Enabled stay ordinary,
	// mutable columns even on a projected row, exactly as they already are
	// on a seeded one.
	if existing.Origin == originShared {
		if err := refuseSharedDefinitionFieldChange(existing, d); err != nil {
			return Definition{}, err
		}
	}
	// Whole-branch review finding: the slot refuseKeyHeldBySharedProjection
	// protects is a BINDING (org scope), not a key by itself, but this used
	// to call the guard only when the key changed
	// (existing.Origin == originCustom && d.Key != existing.Key) — so a
	// customer who left the key alone and instead rebound their own check
	// FROM a group or host TO org scope took the slot with nobody checking
	// at all (TestUpdateDefinitionRefusesRebindingOntoASharedKeysOrgSlot's
	// own doc comment walks the five-step sequence that reaches it). The
	// only tuple migration 00063's trigger ever rebinds a shared projection
	// onto is ('org', nil, nil, key), so a row landing anywhere else can
	// never collide with it regardless of its key — which is why this is
	// gated on d.BindingKind == BindingOrg rather than running for every
	// update: a group- or host-bound row costs nothing extra, and neither
	// does an ordinary edit that leaves an already org-scoped row at org
	// scope under the same key it already had, since nothing about that
	// could newly create a collision that was not already ruled out the
	// last time this row was written at this scope.
	if existing.Origin == originCustom && d.BindingKind == BindingOrg &&
		(d.Key != existing.Key || existing.BindingKind != BindingOrg) {
		if err := s.refuseKeyHeldBySharedProjection(ctx, orgID, d.Key); err != nil {
			return Definition{}, err
		}
	}
	d.ID, d.OrgID, d.Origin, d.SharedDefinitionID = id, orgID, existing.Origin, existing.SharedDefinitionID
	return s.store.UpdateDefinition(ctx, d)
}

// refuseSharedDefinitionFieldChange refuses to change any of a projected
// check definition's shared columns (spec decision 2: key, plugin, params
// and interval belong to the installation-owned definition and are
// read-only inside every organization) — repos.refuseSharedFieldChange's
// own doc comment and shape, for check_definitions' own, different column
// split. next is the value UpdateDefinition is about to write, already
// normalized by normalizeDefinition/ValidateDefinitionShape; cur is the
// projection's current row.
func refuseSharedDefinitionFieldChange(cur, next Definition) error {
	switch {
	case next.Key != cur.Key:
		return fmt.Errorf("%w: key: this check is a projection of an installation-owned definition; the installation owns its key", ErrInvalidInput)
	case next.Plugin != cur.Plugin:
		return fmt.Errorf("%w: plugin: this check is a projection of an installation-owned definition; the installation owns its plugin", ErrInvalidInput)
	case next.Interval != cur.Interval:
		return fmt.Errorf("%w: interval_seconds: this check is a projection of an installation-owned definition; the installation owns its interval", ErrInvalidInput)
	case !paramsEqual(cur.Params, next.Params):
		return fmt.Errorf("%w: params: this check is a projection of an installation-owned definition; the installation owns its parameters", ErrInvalidInput)
	}
	return nil
}

// paramsEqual compares two params maps the way a client-visible round trip
// does, not by Go identity: both sides are normalized through marshalParams
// (nil -> "{}", the same normalization store_sql.go already applies before
// writing either one to the database) and then compared as JSON text, which
// encoding/json always emits with sorted map keys — so two maps with the
// same key/value pairs compare equal regardless of iteration order, and a
// client that merely echoes a GET response's own params: {} back on a PUT
// is never mistaken for one that tried to change them.
func paramsEqual(a, b map[string]any) bool {
	aj, errA := marshalParams(a)
	bj, errB := marshalParams(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(aj) == string(bj)
}

// DeleteDefinition removes the definition identified by (orgID, id), seeded
// or not — disabling (UpdateDefinition with Enabled: false) is how the GUI
// offers to turn a seeded check off while still showing that it exists;
// deleting it outright is a deliberate, available action, not one this
// task's brief restricts.
//
// A projected definition (origin = "shared") cannot be deleted from inside
// an organization at all (spec decision 6): the next propagation from the
// definition would simply recreate it. ErrForbidden, not ErrInvalidInput —
// repos.Service.Delete's own doc comment on why that distinction matters:
// no change to this DELETE's content (it carries none) could ever make it
// succeed, because the refusal is about who owns the row.
func (s *Service) DeleteDefinition(ctx context.Context, orgID, id uuid.UUID) error {
	cur, err := s.store.GetDefinition(ctx, orgID, id)
	if err != nil {
		return err
	}
	if cur.Origin == originShared {
		return fmt.Errorf("%w: this check is a projection of an installation-owned definition and cannot be deleted from inside an organization", ErrForbidden)
	}
	return s.store.DeleteDefinition(ctx, orgID, id)
}

// GetDefinition returns the single definition identified by (orgID, id),
// or ErrNotFound when the organization has no such row — a definition of
// another organization reads exactly the same, never as a refusal that
// would confirm the id exists somewhere.
func (s *Service) GetDefinition(ctx context.Context, orgID, id uuid.UUID) (Definition, error) {
	return s.store.GetDefinition(ctx, orgID, id)
}

// ListDefinitions returns orgID's check definitions, at every scope.
func (s *Service) ListDefinitions(ctx context.Context, orgID uuid.UUID) ([]Definition, error) {
	return s.store.ListDefinitions(ctx, orgID)
}
