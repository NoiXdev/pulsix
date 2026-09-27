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
// created. Only the former is ever restricted (see UpdateDefinition):
// origin itself is never caller-supplied, so there is no exported type for
// it — CreateDefinition always stamps originCustom, SeedForOrg always
// stamps originSeed.
const (
	originSeed   = "seed"
	originCustom = "custom"
)

// The four plugins this milestone ships (spec's Decisions table: "Plugin
// set"). A table in code rather than a free-form string so an unknown
// plugin is refused at write time instead of silently never running —
// there is no fifth plugin the agent could execute even if a definition
// named one.
const (
	PluginCPU     = "cpu"
	PluginMemory  = "memory"
	PluginDisk    = "disk"
	PluginService = "service"
)

var supportedPlugins = map[string]bool{
	PluginCPU: true, PluginMemory: true, PluginDisk: true, PluginService: true,
}

// keyPattern is the same shape a plugin name itself always satisfies, so a
// key left to default to the plugin name never fails this check.
var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

const (
	minInterval = 10 * time.Second
	maxInterval = 24 * time.Hour
)

// Definition is a plugin bound to a scope. Samples (a later task) are keyed
// by Key, not ID: editing or deleting a definition must not orphan or
// rewrite history the operator is currently looking at on a graph.
type Definition struct {
	ID, OrgID            uuid.UUID
	Key, Plugin          string
	Params               map[string]any
	Interval             time.Duration
	Enabled              bool
	Origin               string // "seed" | "custom"
	BindingKind          BindingKind
	GroupID, HostID      *uuid.UUID
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
// optional mount allow-list ("mounts"), service a required non-empty unit
// list ("units"), cpu and memory take no parameters at all. Rejecting
// unknown keys (rather than ignoring them) is deliberate — a typo'd
// parameter name must fail loudly at write time, not be silently dropped
// and discovered missing at the agent weeks later.
func validateParams(plugin string, params map[string]any) error {
	switch plugin {
	case PluginCPU, PluginMemory:
		if len(params) > 0 {
			return fmt.Errorf("%w: plugin %q takes no parameters", ErrInvalidInput, plugin)
		}
	case PluginDisk:
		for k := range params {
			if k != "mounts" {
				return fmt.Errorf("%w: disk: unknown parameter %q", ErrInvalidInput, k)
			}
		}
		if v, ok := params["mounts"]; ok {
			mounts, ok := stringList(v)
			if !ok {
				return fmt.Errorf("%w: disk: mounts must be a list of strings", ErrInvalidInput)
			}
			for _, m := range mounts {
				if strings.TrimSpace(m) == "" {
					return fmt.Errorf("%w: disk: mounts must not contain an empty entry", ErrInvalidInput)
				}
			}
		}
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

// normalizeDefinition validates in into a definition's mutable fields; id,
// org, origin and the timestamps are the caller's (CreateDefinition,
// UpdateDefinition, SeedForOrg each set them their own way).
func normalizeDefinition(in DefinitionInput) (Definition, error) {
	var d Definition
	plugin := strings.TrimSpace(in.Plugin)
	if !supportedPlugins[plugin] {
		return d, fmt.Errorf("%w: plugin must be one of cpu, memory, disk, service, got %q", ErrInvalidInput, plugin)
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		key = plugin // "key defaults to the plugin name" (spec's Data model)
	}
	if !keyPattern.MatchString(key) {
		return d, fmt.Errorf("%w: key must match %s", ErrInvalidInput, keyPattern.String())
	}
	if in.Interval < minInterval || in.Interval > maxInterval {
		return d, fmt.Errorf("%w: interval must be between %s and %s", ErrInvalidInput, minInterval, maxInterval)
	}
	if err := validateParams(plugin, in.Params); err != nil {
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
	d.ID, d.OrgID, d.Origin = s.newID(), orgID, origin
	return s.store.CreateDefinition(ctx, d)
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
	d.ID, d.OrgID, d.Origin = id, orgID, existing.Origin
	return s.store.UpdateDefinition(ctx, d)
}

// DeleteDefinition removes the definition identified by (orgID, id),
// seeded or not — disabling (UpdateDefinition with Enabled: false) is how
// the GUI offers to turn a seeded check off while still showing that it
// exists; deleting it outright is a deliberate, available action, not one
// this task's brief restricts.
func (s *Service) DeleteDefinition(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.DeleteDefinition(ctx, orgID, id)
}

// ListDefinitions returns orgID's check definitions, at every scope.
func (s *Service) ListDefinitions(ctx context.Context, orgID uuid.UUID) ([]Definition, error) {
	return s.store.ListDefinitions(ctx, orgID)
}
