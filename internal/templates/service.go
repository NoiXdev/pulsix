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
	"reflect"
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
	"github.com/noixdev/pulsix/internal/repos"
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
	// ErrForbidden means the request is well-formed but refused because of
	// who the resource belongs to, not because of anything the request
	// could change — repos.ErrForbidden's own doc comment, the same
	// reasoning and the same house convention (internal/tenancy.ErrForbidden,
	// mapped to 403 by internal/api/authz.go's mapTenancyErr): spec v0.2f
	// decision 6's case here, deleting a projected template from inside an
	// organization. No edit to the DELETE request (which carries no content
	// to edit) could ever make it succeed, which is what sets it apart from
	// refuseSharedFieldChange's own cases below (a value the caller COULD
	// fix by echoing back the current one) and is why it is its own
	// sentinel rather than reusing ErrInvalidInput.
	ErrForbidden = errors.New("forbidden")
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
	ID         uuid.UUID
	OrgID      uuid.UUID
	PlaybookID uuid.UUID
	Name       string
	// SharedTemplateID is non-nil when this row is a PROJECTION of an
	// installation-owned definition (migration 00061, spec v0.2f decision
	// 1): shared_job_templates.id, not composed with org_id — the
	// definition has no org_id to compose with, repos.Repo.SharedRepoID's
	// own reasoning. Name, PlaybookID, Limit, ExtraVars and ExtraVarsSchema
	// then belong to the definition and are refused on write
	// (Service.Update, spec decision 2); Scope, CredentialID and the vault
	// credential set remain this organization's own. Nil for an ordinary,
	// organization-created template. PlaybookID may be uuid.Nil for a
	// projection whose organization has not synced the definition's
	// playbook path yet — migration 00061's own comment on why
	// job_templates.playbook_id became nullable; it is never uuid.Nil for
	// an ordinary template, which Service.build always resolves through
	// playbooks.Get before writing one.
	SharedTemplateID *uuid.UUID
	Scope            Scope
	Limit            string
	ExtraVars        map[string]any
	ExtraVarsSchema  *schema.Schema
	CredentialID     *uuid.UUID
	// VaultCredentials is this template's own labelled vault password set
	// (migration 00059, spec decision 1): the passwords it adds on top of
	// its repository's base set, never the repository's own (store_sql.go's
	// VaultCredential doc). Read separately by Get/List
	// (ListVaultCredentials) and written separately by Create/Update
	// (ReplaceVaultCredentials); 4b resolves it against the repository's set
	// at run time through ToVaultIdentity and repos.ResolveVaultIdentities.
	VaultCredentials []VaultCredential
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
	// InTx runs fn inside one organization-scoped write transaction: every
	// store call fn makes with the context it is handed joins that
	// transaction, and all of them commit together or none do. Create and
	// Update need it because a template's row and its labelled vault
	// credential set are two statements that must land as one — a failure
	// between them used to leave a template stored with none of the vault
	// passwords its request named, which does not fail at save time but
	// at the next run (backlog 2.95, the same split repos.Service had).
	InTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error
	Create(ctx context.Context, t Template) (Template, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Template, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Template, error)
	Update(ctx context.Context, t Template) (Template, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	// ReplaceVaultCredentials replaces a template's own labelled vault
	// password set wholesale (spec decision 1); an empty or nil slice is the
	// legal "no added vault passwords" result.
	ReplaceVaultCredentials(ctx context.Context, orgID, templateID uuid.UUID, items []VaultCredential) error
	// ListVaultCredentials returns a template's own labelled vault password
	// set, in label order.
	ListVaultCredentials(ctx context.Context, orgID, templateID uuid.UUID) ([]VaultCredential, error)
	// ListOrgVaultCredentials is ListVaultCredentials for every template of
	// the organization in one call, keyed by template id (backlog 2.94):
	// List calls this once instead of once per row.
	ListOrgVaultCredentials(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID][]VaultCredential, error)
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

// CreateInput is a new job template. VaultCredentials is this template's own
// added labelled vault passwords (spec decision 1), never the repository's.
type CreateInput struct {
	Name             string
	PlaybookID       uuid.UUID
	Scope            Scope
	Limit            string
	ExtraVars        map[string]any
	ExtraVarsSchema  *schema.Schema
	CredentialID     *uuid.UUID
	VaultCredentials []VaultCredential
}

// UpdateInput fully replaces a template's fields (PUT semantics).
// VaultCredentials is a full replacement too: a label present before and
// absent from this call is dropped.
type UpdateInput struct {
	Name             string
	PlaybookID       uuid.UUID
	Scope            Scope
	Limit            string
	ExtraVars        map[string]any
	ExtraVarsSchema  *schema.Schema
	CredentialID     *uuid.UUID
	VaultCredentials []VaultCredential
}

// Create validates the input and stores a new template, then its own
// labelled vault credential set (ReplaceVaultCredentials is always called,
// even with an empty slice — the same "always a full replacement, never a
// silent no-op special case" rule Update follows).
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, in CreateInput) (Template, error) {
	// Create never builds a projection — a shared template's own row is
	// written directly by internal/shared's store, never through this
	// service (ProjectedTemplateColumns' own doc comment) — so the
	// playbook lookup always runs (skipPlaybookCheck=false).
	t, err := s.build(ctx, orgID, uuid.Must(uuid.NewV7()), in.Name, in.PlaybookID, false, in.Scope, in.Limit, in.ExtraVars, in.ExtraVarsSchema, in.CredentialID, nil, in.VaultCredentials)
	if err != nil {
		return Template{}, err
	}
	var created Template
	// One transaction for both writes: a template and the vault passwords
	// its request named are one fact, and half of it is worse than none
	// (backlog 2.95).
	if err := s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		if created, err = s.store.Create(ctx, t); err != nil {
			return err
		}
		if err := s.store.ReplaceVaultCredentials(ctx, orgID, created.ID, in.VaultCredentials); err != nil {
			return fmt.Errorf("store template vault credentials: %w", err)
		}
		return nil
	}); err != nil {
		return Template{}, err
	}
	created.VaultCredentials = in.VaultCredentials
	return masked(created), nil
}

// Update validates the input and replaces an existing template's fields,
// including its own labelled vault credential set.
//
// A projected template (SharedTemplateID set) refuses any attempt to
// change name, playbook_id, limit, extra vars or their schema — spec
// decision 2's table gives those to the installation-owned definition,
// read-only inside every organization; only scope, CredentialID and the
// vault credential set are this organization's to change here
// (refuseSharedFieldChange below, repos.Service.Update's own
// refuseSharedFieldChange and doc comment, same reasoning). For such a
// template, build's own playbook lookup is skipped (skipPlaybookCheck):
// refuseSharedFieldChange has already required in.PlaybookID == cur.
// PlaybookID by the time build would run it, and cur.PlaybookID may be
// uuid.Nil for a projection whose organization has not synced the
// definition's playbook path yet (migration 00061) — not a real playbook
// id a lookup could ever find, and not something this organization's edit
// could be asked to fix.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, in UpdateInput) (Template, error) {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Template{}, err
	}
	isProjection := cur.SharedTemplateID != nil
	// Normalized the same way build() normalizes its own incoming vars
	// (vars == nil -> {}), but BEFORE refuseSharedFieldChange compares
	// against cur.ExtraVars (never nil once stored): a client that omits
	// extra_vars from a PUT body entirely — leaving in.ExtraVars nil —
	// must compare equal to a stored empty map, not be refused as a
	// "change" that was never sent.
	vars := in.ExtraVars
	if vars == nil {
		vars = map[string]any{}
	}
	if isProjection {
		if err := refuseSharedFieldChange(cur, in.Name, in.PlaybookID, in.Limit, vars, in.ExtraVarsSchema); err != nil {
			return Template{}, err
		}
	}
	t, err := s.build(ctx, orgID, id, in.Name, in.PlaybookID, isProjection, in.Scope, in.Limit, vars, in.ExtraVarsSchema, in.CredentialID, cur.ExtraVars, in.VaultCredentials)
	if err != nil {
		return Template{}, err
	}
	t.SharedTemplateID = cur.SharedTemplateID
	var updated Template
	if err := s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		if updated, err = s.store.Update(ctx, t); err != nil {
			return err
		}
		if err := s.store.ReplaceVaultCredentials(ctx, orgID, id, in.VaultCredentials); err != nil {
			return fmt.Errorf("store template vault credentials: %w", err)
		}
		return nil
	}); err != nil {
		return Template{}, err
	}
	updated.VaultCredentials = in.VaultCredentials
	return masked(updated), nil
}

// Get returns one template with its extra vars masked and its own vault
// credential set attached.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Template, error) {
	t, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Template{}, err
	}
	items, err := s.store.ListVaultCredentials(ctx, orgID, id)
	if err != nil {
		return Template{}, fmt.Errorf("list template vault credentials: %w", err)
	}
	t.VaultCredentials = items
	return masked(t), nil
}

// List returns every template of the organization, ordered by name, with
// masked extra vars and each one's own vault credential set attached.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Template, error) {
	list, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	// One query for the whole organization (backlog 2.94), not one per
	// row.
	byTemplate, err := s.store.ListOrgVaultCredentials(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list template vault credentials: %w", err)
	}
	out := make([]Template, len(list))
	for i, t := range list {
		t.VaultCredentials = byTemplate[t.ID]
		out[i] = masked(t)
	}
	return out, nil
}

// Delete removes a template. Jobs reference templates by template_id with
// no foreign key on purpose, so deleting a template that ran before is
// allowed and a finished run keeps its template_id as a dangling reference
// by design — the run view must tolerate a template it can no longer
// resolve (it shows the job's own record, not the template's).
//
// A projected template (SharedTemplateID set) cannot be deleted from
// inside an organization at all (spec decision 6): the next propagation
// from the definition would simply recreate it — repos.Service.Delete's
// own doc comment, same reasoning, same ErrForbidden (not ErrInvalidInput:
// a DELETE carries no content to fix, so this is refused regardless of
// what the caller sends, the distinction 754d884 drew for the repository
// version of this exact case).
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	if cur.SharedTemplateID != nil {
		return fmt.Errorf("%w: this template is a projection of an installation-owned definition and cannot be deleted from inside an organization", ErrForbidden)
	}
	return s.store.Delete(ctx, orgID, id)
}

// refuseSharedFieldChange refuses to change any of a projected template's
// shared columns (spec decision 2: name, playbook_id, limit, extra vars and
// their schema belong to the installation-owned definition and are
// read-only inside every organization). cur is the projection's current
// row; the rest are the values Update is about to write. ExtraVars and
// ExtraVarsSchema are compared with reflect.DeepEqual: both are read back
// from JSON (map[string]any / *schema.Schema), and a shared definition's
// extra vars never contain a secret-typed value (shared.
// schemaHasSecretField refuses one at the definition's own Create/Update),
// so there is no sealed-vs-masked-marker mismatch to account for the way
// there would be for an ordinary template's own secret fields.
func refuseSharedFieldChange(cur Template, name string, playbookID uuid.UUID, limit string, vars map[string]any, sc *schema.Schema) error {
	switch {
	case name != cur.Name:
		return fmt.Errorf("%w: name: this template is a projection of an installation-owned definition; the installation owns its name", ErrInvalidInput)
	case playbookID != cur.PlaybookID:
		return fmt.Errorf("%w: playbook_id: this template is a projection of an installation-owned definition; the installation owns its playbook", ErrInvalidInput)
	case limit != cur.Limit:
		return fmt.Errorf("%w: limit: this template is a projection of an installation-owned definition; the installation owns its limit", ErrInvalidInput)
	case !reflect.DeepEqual(vars, cur.ExtraVars):
		return fmt.Errorf("%w: extra_vars: this template is a projection of an installation-owned definition; the installation owns its extra vars", ErrInvalidInput)
	case !reflect.DeepEqual(sc, cur.ExtraVarsSchema):
		return fmt.Errorf("%w: extra_vars_schema: this template is a projection of an installation-owned definition; the installation owns its extra vars schema", ErrInvalidInput)
	}
	return nil
}

// build validates an input and returns the Template to persist, with
// timestamps left to the store. Validation order is meta (name, limit),
// playbook, scope, credential, vault credentials, then the schema and the
// values against it — each step naming exactly what is wrong.
// skipPlaybookCheck is true only when Update is re-affirming a projected
// template's already-pinned playbook_id (Update's own doc comment): by the
// time it is true, refuseSharedFieldChange has already required playbookID
// == the stored one, which may be uuid.Nil for an unresolved projection —
// not a real playbook id a lookup could ever find.
func (s *Service) build(ctx context.Context, orgID, id uuid.UUID, name string, playbookID uuid.UUID, skipPlaybookCheck bool, scope Scope, limit string, vars map[string]any, sc *schema.Schema, credentialID *uuid.UUID, stored map[string]any, vaultCredentials []VaultCredential) (Template, error) {
	if err := validateMeta(name, limit); err != nil {
		return Template{}, err
	}
	if !skipPlaybookCheck {
		if _, err := s.playbooks.Get(ctx, orgID, playbookID); err != nil {
			if errors.Is(err, playbooks.ErrNotFound) {
				return Template{}, fmt.Errorf("%w: playbook does not exist", ErrNotFound)
			}
			return Template{}, fmt.Errorf("look up playbook: %w", err)
		}
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
	if err := s.validateVaultCredentials(ctx, orgID, vaultCredentials); err != nil {
		return Template{}, err
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

// validateVaultCredentials checks a template's own added labelled vault
// password set (spec decision 1): every label must pass
// repos.InvalidVaultLabel (decision 4, amended — the same rule
// internal/worker/runner enforces again as its own last gate, and the same
// rule repos.Service.validateVaultIdentities applies to a repository's own
// set) and must be unique within this set — job_template_vault_credentials'
// own PRIMARY KEY (template_id, label) would otherwise turn a duplicate into
// a 500 from the store instead of a 400 naming the mistake. Each credential
// must exist and be a vault_password credential, the rule
// repos.Service.validateVaultCredential applies to a repository's single
// VaultCredentialID slot.
func (s *Service) validateVaultCredentials(ctx context.Context, orgID uuid.UUID, items []VaultCredential) error {
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if reason := repos.InvalidVaultLabel(item.Label); reason != "" {
			return fmt.Errorf("%w: invalid vault credential label %q: %s", ErrInvalidInput, item.Label, reason)
		}
		if seen[item.Label] {
			return fmt.Errorf("%w: duplicate vault credential label %q", ErrInvalidInput, item.Label)
		}
		seen[item.Label] = true
		cred, err := s.creds.Get(ctx, orgID, item.CredentialID)
		if err != nil {
			if errors.Is(err, credentials.ErrNotFound) {
				return fmt.Errorf("%w: vault credential not found", ErrInvalidInput)
			}
			return fmt.Errorf("look up vault credential: %w", err)
		}
		if cred.Kind != credentials.KindVaultPassword {
			return fmt.Errorf("%w: the referenced vault credential must be a vault_password credential", ErrInvalidInput)
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
