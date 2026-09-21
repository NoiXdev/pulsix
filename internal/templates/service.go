// Package templates implements job templates (spec §7, backlog 2.10): a
// named configuration for Ansible runs — the playbook to run, the inventory
// scope selecting its targets, an optional ansible limit pattern, extra
// vars typed by an optional schema (the same internal/inventory/schema
// model inventory groups use), and the default SSH credential for hosts
// that have none of their own. Scope is stored in the render.Scope wire
// format (JSON keys group_id/host_ids) so the job queue's scope resolver
// consumes it unchanged; the struct is declared locally, with exactly that
// JSON shape, to keep this package free of a renderer import — the two
// types are JSON- and field-wise equivalent (Scope{GroupID *uuid.UUID,
// HostIDs []uuid.UUID}).
//
// Extra-var values under secret-typed fields are sealed at rest with the
// template's own AAD (ExtraVarsAAD) and masked on every read this package
// serves: a sealed value, and any non-empty value under a secret-typed
// field (a template saved before sealing existed), is replaced by the
// {"$secret": "set"} marker (backlog 2.21's direction, mirrored from
// inventory reads). A write that sends that marker back — or an explicit
// {"$secret": "keep", "from": <path>} — keeps the stored secret: a sealed
// envelope, or the legacy plaintext a template saved before sealing holds,
// which that save seals (backlog 2.47). The store
// is the raw persistence layer: the job queue's claim path reads through it
// and opens the sealed values (internal/app's template lookup), the only
// place they are revealed.
package templates

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
)

// Sentinel errors mapped to HTTP status by the transport layer. Value
// problems come back as *schema.ValidationError instead (422 with one
// problem per detail), exactly like inventory.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrNameTaken    = errors.New("name already in use")
	// ErrUnmanagedHost is the invalid-input class for a scope naming an
	// existing but unmanaged host: the API answers it with 422, not the
	// 400 a plain ErrInvalidInput earns.
	ErrUnmanagedHost = fmt.Errorf("%w: only managed hosts can be targeted", ErrInvalidInput)
	// ErrNotSSH is the invalid-input class for a credential that exists but
	// cannot log in to a host (a Git deploy key or token): 422 as well.
	ErrNotSSH = fmt.Errorf("%w: the credential must be an SSH key or password", ErrInvalidInput)
	// ErrBadSchema marks a *schema.ValidationError raised by the extra-vars
	// schema document itself being malformed (as opposed to the values
	// failing a well-formed schema), so the transport can point its problem
	// paths at body.extra_vars_schema rather than body.extra_vars.
	ErrBadSchema = errors.New("invalid extra vars schema")
)

// namePattern is the shared resource-name rule (credentials, repos, ...).
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// maxLimit bounds the free-form ansible limit pattern. A limit is passed to
// ansible-playbook verbatim, so the only checks are length and the absence
// of NUL — everything else is a legal host pattern.
const maxLimit = 500

// Scope selects the hosts a run targets: no fields set renders every
// managed host of the organization, GroupID the members of that group (and
// its descendants), HostIDs exactly those hosts; both together intersect.
// The JSON shape is render.Scope's wire format.
type Scope struct {
	GroupID *uuid.UUID  `json:"group_id"`
	HostIDs []uuid.UUID `json:"host_ids"`
}

// Empty reports whether the scope selects the whole organization.
func (s Scope) Empty() bool { return s.GroupID == nil && len(s.HostIDs) == 0 }

// Template is one stored job template. ExtraVars are masked on every read
// the service returns (see the package comment); ExtraVarsSchema is nil
// when the template carries no schema.
type Template struct {
	ID              uuid.UUID
	OrgID           uuid.UUID
	PlaybookID      uuid.UUID
	Name            string
	Scope           Scope
	Limit           string
	ExtraVars       map[string]any
	ExtraVarsSchema *schema.Schema
	CredentialID    *uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// PlaybookLookup is the slice of playbooks.Service validation needs: the
// referenced playbook must exist in the organization (a cross-org id is as
// invisible as a missing one).
type PlaybookLookup interface {
	Get(ctx context.Context, orgID, id uuid.UUID) (playbooks.Playbook, error)
}

// GroupLookup is the slice of inventory.Service scope validation needs: an
// unknown group id is ErrNotFound, mirroring render semantics.
type GroupLookup interface {
	GetGroup(ctx context.Context, orgID, id uuid.UUID) (inventory.GroupView, error)
}

// HostLookup is the slice of hosts.Service scope validation needs: every
// host id in a scope must exist and be managed. One ListAllHosts call
// covers the whole scope.
type HostLookup interface {
	ListAllHosts(ctx context.Context, orgID uuid.UUID) ([]hosts.Host, error)
}

// CredentialLookup is the slice of credentials.Service validation needs:
// the template's default credential must exist and be SSH kind.
type CredentialLookup interface {
	Get(ctx context.Context, orgID, id uuid.UUID) (credentials.Credential, error)
}

// Store persists job templates. Every lookup that finds nothing returns
// ErrNotFound; a name clash within the organization returns ErrNameTaken;
// a reference to a playbook or credential that does not exist in the
// organization (the composite foreign keys) returns ErrNotFound.
type Store interface {
	Create(ctx context.Context, t Template) (Template, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Template, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Template, error)
	Update(ctx context.Context, t Template) (Template, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
}

// Sealer is the slice of crypto.Box the service needs: secret-typed extra
// vars are sealed at rest under the template's own AAD.
type Sealer interface {
	SealAAD(plain, aad []byte) ([]byte, error)
	OpenAAD(sealed, aad []byte) ([]byte, error)
}

// ExtraVarsAAD is the additional data a template's sealed extra vars are
// boxed with, so a ciphertext copied into another template or organization
// no longer opens.
func ExtraVarsAAD(orgID, templateID uuid.UUID) []byte {
	return []byte("template-extra-vars:" + orgID.String() + ":" + templateID.String())
}

// Service implements the job-template registry.
type Service struct {
	store     Store
	playbooks PlaybookLookup
	groups    GroupLookup
	hosts     HostLookup
	creds     CredentialLookup
	box       Sealer
}

// NewService wires the store, the four lookups validation needs and the
// sealing box for secret-typed extra vars.
func NewService(store Store, playbooks PlaybookLookup, groups GroupLookup, hosts HostLookup, creds CredentialLookup, box Sealer) *Service {
	return &Service{store: store, playbooks: playbooks, groups: groups, hosts: hosts, creds: creds, box: box}
}

// CreateInput is a new job template.
type CreateInput struct {
	Name            string
	PlaybookID      uuid.UUID
	Scope           Scope
	Limit           string
	ExtraVars       map[string]any
	ExtraVarsSchema *schema.Schema
	CredentialID    *uuid.UUID
}

// UpdateInput fully replaces a template's fields (PUT semantics).
type UpdateInput struct {
	Name            string
	PlaybookID      uuid.UUID
	Scope           Scope
	Limit           string
	ExtraVars       map[string]any
	ExtraVarsSchema *schema.Schema
	CredentialID    *uuid.UUID
}

// Create validates the input and stores a new template.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, in CreateInput) (Template, error) {
	t, err := s.build(ctx, orgID, uuid.Must(uuid.NewV7()), in.Name, in.PlaybookID, in.Scope, in.Limit, in.ExtraVars, in.ExtraVarsSchema, in.CredentialID, nil)
	if err != nil {
		return Template{}, err
	}
	created, err := s.store.Create(ctx, t)
	if err != nil {
		return Template{}, err
	}
	return masked(created), nil
}

// Update validates the input and replaces an existing template's fields.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, in UpdateInput) (Template, error) {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Template{}, err
	}
	t, err := s.build(ctx, orgID, id, in.Name, in.PlaybookID, in.Scope, in.Limit, in.ExtraVars, in.ExtraVarsSchema, in.CredentialID, cur.ExtraVars)
	if err != nil {
		return Template{}, err
	}
	updated, err := s.store.Update(ctx, t)
	if err != nil {
		return Template{}, err
	}
	return masked(updated), nil
}

// Get returns one template with its extra vars masked.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Template, error) {
	t, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Template{}, err
	}
	return masked(t), nil
}

// List returns every template of the organization, ordered by name, with
// masked extra vars.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Template, error) {
	list, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Template, len(list))
	for i, t := range list {
		out[i] = masked(t)
	}
	return out, nil
}

// Delete removes a template. Jobs reference templates by template_id with
// no foreign key on purpose, so deleting a template that ran before is
// allowed and a finished run keeps its template_id as a dangling reference
// by design — the run view must tolerate a template it can no longer
// resolve (it shows the job's own record, not the template's).
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.Delete(ctx, orgID, id)
}

// build validates an input and returns the Template to persist, with
// timestamps left to the store. Validation order is meta (name, limit),
// playbook, scope, credential, then the schema and the values against it —
// each step naming exactly what is wrong.
func (s *Service) build(ctx context.Context, orgID, id uuid.UUID, name string, playbookID uuid.UUID, scope Scope, limit string, vars map[string]any, sc *schema.Schema, credentialID *uuid.UUID, stored map[string]any) (Template, error) {
	if err := validateMeta(name, limit); err != nil {
		return Template{}, err
	}
	if _, err := s.playbooks.Get(ctx, orgID, playbookID); err != nil {
		if errors.Is(err, playbooks.ErrNotFound) {
			return Template{}, fmt.Errorf("%w: playbook does not exist", ErrNotFound)
		}
		return Template{}, fmt.Errorf("look up playbook: %w", err)
	}
	if err := s.validateScope(ctx, orgID, scope); err != nil {
		return Template{}, err
	}
	if credentialID != nil {
		c, err := s.creds.Get(ctx, orgID, *credentialID)
		if err != nil {
			if errors.Is(err, credentials.ErrNotFound) {
				return Template{}, fmt.Errorf("%w: credential does not exist", ErrNotFound)
			}
			return Template{}, fmt.Errorf("look up credential: %w", err)
		}
		if !c.Kind.IsSSH() {
			return Template{}, fmt.Errorf("%w: credential %s has kind %s", ErrNotSSH, c.Name, c.Kind)
		}
	}
	if vars == nil {
		vars = map[string]any{}
	}
	// Extra vars carry Ansible's highest precedence: the variables that
	// decide whether a pinned host key is checked (and with which SSH
	// arguments) are reserved for the worker (re-review N1).
	if names := ansiblevars.ForbiddenIn(vars); len(names) > 0 {
		return Template{}, forbiddenProblems(names)
	}
	if sc != nil {
		if sc.Fields == nil {
			sc.Fields = []schema.Field{}
		}
		if err := sc.Check(); err != nil {
			return Template{}, fmt.Errorf("%w: %w", ErrBadSchema, err)
		}
		sealed, err := s.sealExtraVars(ExtraVarsAAD(orgID, id), sc.Fields, vars, stored)
		if err != nil {
			return Template{}, err
		}
		vars = sealed
	}
	return Template{
		ID: id, OrgID: orgID, PlaybookID: playbookID, Name: name, Scope: scope,
		Limit: limit, ExtraVars: vars, ExtraVarsSchema: sc, CredentialID: credentialID,
	}, nil
}

// validateScope checks the scope's references: the group must exist
// (ErrNotFound, mirroring render semantics — a cross-org id is as invisible
// as a missing one) and every host id must exist and be managed. An empty
// scope is the whole organization and needs nothing.
func (s *Service) validateScope(ctx context.Context, orgID uuid.UUID, scope Scope) error {
	if scope.GroupID != nil {
		if _, err := s.groups.GetGroup(ctx, orgID, *scope.GroupID); err != nil {
			if errors.Is(err, inventory.ErrNotFound) {
				return fmt.Errorf("%w: group does not exist", ErrNotFound)
			}
			return fmt.Errorf("look up group: %w", err)
		}
	}
	if len(scope.HostIDs) == 0 {
		return nil
	}
	all, err := s.hosts.ListAllHosts(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list hosts: %w", err)
	}
	byID := make(map[uuid.UUID]hosts.Host, len(all))
	for _, h := range all {
		byID[h.ID] = h
	}
	for _, id := range scope.HostIDs {
		h, ok := byID[id]
		if !ok {
			return fmt.Errorf("%w: host %s does not exist", ErrNotFound, id)
		}
		if !h.Managed {
			return fmt.Errorf("%w: host %s is not managed", ErrUnmanagedHost, id)
		}
	}
	return nil
}

// validateMeta checks the name against the shared resource-name rule and
// the free-form limit pattern.
func validateMeta(name, limit string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%w: name must be 1-63 lowercase letters, digits, dots, underscores or hyphens, starting with a letter or digit", ErrInvalidInput)
	}
	return validateLimit(limit)
}

// validateLimit applies the limit rules a template and a run override
// share: a limit is passed to ansible-playbook verbatim, so the only
// checks are length and the absence of NUL — everything else is a legal
// host pattern.
func validateLimit(limit string) error {
	if len(limit) > maxLimit || strings.ContainsRune(limit, 0) {
		return fmt.Errorf("%w: limit must be at most %d characters and contain no NUL", ErrInvalidInput, maxLimit)
	}
	return nil
}

// forbiddenProblems builds the validation error for extra-var names the
// connection-security denylist (internal/ansiblevars) refused: the
// variables that decide whether a pinned host key is checked (and with
// which SSH arguments) are reserved for the worker (re-review N1). Shared
// by a template save (build) and a run override (ValidateOverrides).
func forbiddenProblems(names []string) error {
	problems := make([]schema.Problem, len(names))
	for i, n := range names {
		problems[i] = schema.Problem{Path: n, Message: "this variable name is reserved (connection security, set by Pulsix itself, or Ansible's own markers); remove it"}
	}
	return &schema.ValidationError{Problems: problems}
}

// masked returns t with its extra vars masked for a reader: every sealed
// value, and every non-empty value under a secret-typed field of the
// template's current schema, replaced by the {"$secret": "set"} marker
// (backlog 2.21's direction — a string→secret schema change must not
// surface the old plaintext on reads). Values whose shape no longer fits
// their field are copied unchanged; validation reports those at write time.
func masked(t Template) Template {
	t.ExtraVars = maskExtraVars(t.ExtraVarsSchema, t.ExtraVars)
	return t
}

// maskExtraVars delegates to schema.MaskDeclared, the shared
// fields-aware masking backlog 2.21 introduced (also used by inventory
// group and membership reads): with no schema, fields is empty and
// MaskDeclared's declared-field pass has nothing to declare, so it
// degrades to exactly MaskVars's sealed-only behavior — the same fallback
// this function used to implement by hand.
func maskExtraVars(sc *schema.Schema, vars map[string]any) map[string]any {
	var fields []schema.Field
	if sc != nil {
		fields = sc.Fields
	}
	return schema.MaskDeclared(fields, vars)
}

// sealExtraVars prepares incoming extra vars for storage under fields: a
// plaintext value of a secret-typed field is sealed; the masked marker a
// read returned, or a keep marker, keeps the stored value at that path —
// a sealed envelope, or the legacy plaintext a template saved before
// sealing holds (backlog 2.47), which the save seals fresh; a marker
// without a stored secret is a validation problem; a sealed envelope sent
// in directly is refused. The result is validated against the schema
// afterwards, sealed values included.
func (s *Service) sealExtraVars(aad []byte, fields []schema.Field, incoming, stored map[string]any) (map[string]any, error) {
	out, err := s.sealSecrets(aad, fields, incoming, stored)
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(fields, out); err != nil {
		return nil, err
	}
	return out, nil
}

// sealSecrets prepares incoming extra vars for storage under fields: a
// plaintext value of a secret-typed field is sealed; the masked marker a
// read returned, or a keep marker, keeps the stored value at that path.
// What a marker keeps is a sealed envelope, or the legacy plaintext a
// template stored before sealing shipped (backlog 2.47, 2.52): the marker
// resolves to the stored string, which is sealed fresh with the row's AAD —
// the stored row keeps the plaintext until this save replaces it with the
// sealed envelope, so a legacy template saves cleanly and its secret is
// sealed from that save on. A marker without a stored value at all, one
// over an empty string (no secret stored — the same "nothing to hide" rule
// the read path applies), or one whose stored value is neither a string nor
// a sealed envelope (e.g. the number a string→secret schema change leaves
// behind) is a validation problem. A sealed envelope sent in directly is
// refused. It does not validate the result against the schema — callers
// that need strict validation run schema.Validate themselves.
func (s *Service) sealSecrets(aad []byte, fields []schema.Field, incoming, stored map[string]any) (map[string]any, error) {
	var problems []schema.Problem
	out, err := schema.MapSecrets(fields, incoming, func(path string, v any) (any, error) {
		from, keep := schema.KeepFrom(v)
		if !keep && isMaskMarker(v) {
			from, keep = path, true
		}
		if keep {
			prev, found := schema.Lookup(stored, from)
			if plain, isStr := prev.(string); isStr && plain != "" {
				// Legacy plaintext (backlog 2.47): seal it now with this
				// row's AAD, the same sealing a freshly entered plaintext
				// goes through.
				ct, err := s.box.SealAAD([]byte(plain), aad)
				if err != nil {
					return nil, fmt.Errorf("seal extra var %s: %w", path, err)
				}
				return schema.Sealed(ct), nil
			}
			if _, sealed := schema.SealedBytes(prev); !found || !sealed {
				problems = append(problems, schema.Problem{Path: path, Message: "the stored secret this refers to no longer exists; enter it again"})
				return nil, nil
			}
			return prev, nil
		}
		if _, ok := schema.SealedBytes(v); ok {
			problems = append(problems, schema.Problem{Path: path, Message: "sealed values cannot be written directly"})
			return nil, nil
		}
		if plain, ok := v.(string); ok && plain != "" {
			ct, err := s.box.SealAAD([]byte(plain), aad)
			if err != nil {
				return nil, fmt.Errorf("seal extra var %s: %w", path, err)
			}
			return schema.Sealed(ct), nil
		}
		return v, nil
	})
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, &schema.ValidationError{Problems: problems}
	}
	return out, nil
}

// isMaskMarker reports whether v is the {"$secret": "set"} marker a masked
// read returns.
func isMaskMarker(v any) bool {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return false
	}
	return equalMarker(m, schema.Mask())
}

func equalMarker(a, b map[string]any) bool {
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	return len(a) == len(b)
}
