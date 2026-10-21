// Package shared implements the installation-owned definition/projection
// mechanism (spec docs/superpowers/specs/2026-10-20-v0.2f-installation-
// owned-resources-design.md, decisions 1 and 2): the installation owns a
// *definition* with no org_id, and each organization holds an ordinary
// row — a *projection* — that carries its own org_id, its own composite
// foreign keys and its own row-level security, so nothing in the existing
// schema changes meaning. This file holds the rules that do not need a
// database to state: which organizations collide on a name (decision 7),
// and what a projection's shared columns should be, given a definition
// (decision 2). service.go wires them to Postgres for the first kind this
// sub-project builds, repository definitions; Tasks 6 and 7 apply the same
// rules to job templates and check definitions, reusing CollidingOrgs
// unchanged (it never looks at what kind of row a name belongs to) and
// writing their own ProjectedRepoColumns-shaped function for their own,
// different column split.
package shared

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// DefaultBranch mirrors repos.DefaultBranch (that package cannot be
// imported here — see service.go's own layering note): a definition's
// checkout still needs a branch, and every projection inherits whichever
// one the definition carries.
const DefaultBranch = "main"

// RepoDefinition is the installation-owned half of a shared repository
// (spec decision 2's table): url, branch and subpath are the provider's
// own and read-only inside every organization. Name is part of this type
// even though decision 2's table does not list it on either side of the
// repository row — it has nowhere else to come from: a projection's
// git_repos.name IS the definition's own name (ProjectedRepoColumns below),
// which is exactly what makes decision 7's collision check meaningful in
// the first place. The organization's own credential is never part of this
// type; it belongs to the projection (repos.Repo.SharedRepoID, Task 3).
type RepoDefinition struct {
	ID      uuid.UUID
	Name    string
	URL     string
	Branch  string
	Subpath string
}

// RepoDefinitionInput is a RepoDefinition's editable fields, taken by both
// Service.CreateRepo and Service.UpdateRepo — the same full-replacement
// shape repos.UpdateInput already uses for its own row.
type RepoDefinitionInput struct {
	Name    string
	URL     string
	Branch  string
	Subpath string
}

// ProjectedRepoColumns returns the shared columns a git_repos projection of
// def must carry. For a repository this is an identity mapping — decision
// 2 gives the definition nothing to transform before it reaches a
// projection — but Service.CreateRepo and Service.UpdateRepo both build a
// projection's shared half from this one function rather than each
// repeating the field list, and it is the single place that changes if
// decision 2's column split for repositories is ever revisited. Tasks 6
// and 7 write their own analogous function for job_templates' and
// check_definitions' own, different split (a template's playbook_id, for
// one, is resolved per organization rather than copied straight from the
// definition).
func ProjectedRepoColumns(def RepoDefinition) (name, url, branch, subpath string) {
	return def.Name, def.URL, def.Branch, def.Subpath
}

// TemplateDefinition is the installation-owned half of a shared job
// template (spec decision 2's table): playbook path, limit, extra vars and
// their schema are the provider's own and read-only inside every
// organization; scope and the ssh credential are the projection's
// (internal/templates.Template, Task 6's own SharedTemplateID field).
// Name is part of this type for the reason RepoDefinition's own doc
// comment gives for carrying one: a projection's job_templates.name IS the
// definition's own name, which is what makes decision 7's collision check
// mean anything.
//
// SharedRepoID is NOT in spec decision 2's own table (which lists only
// "playbook path" on the definition's side), but the text right below that
// table requires it: "each one syncs its own clone of the repository and
// gets its own playbooks row" only works when every organization is
// guaranteed a clone under the SAME path, which holds only for a
// repository that is itself a shared definition — never an organization's
// own, which would differ per organization by construction. Checked
// against the schema, not assumed: shared_job_templates.shared_repo_id is
// a NOT NULL foreign key to shared_repos (migration 00061).
type TemplateDefinition struct {
	ID              uuid.UUID
	Name            string
	SharedRepoID    uuid.UUID
	PlaybookPath    string
	Limit           string
	ExtraVars       map[string]any
	ExtraVarsSchema *schema.Schema
}

// TemplateDefinitionInput is a TemplateDefinition's editable fields, taken
// by both Service.CreateTemplate and Service.UpdateTemplate — the same
// full-replacement shape RepoDefinitionInput already uses.
type TemplateDefinitionInput struct {
	Name            string
	SharedRepoID    uuid.UUID
	PlaybookPath    string
	Limit           string
	ExtraVars       map[string]any
	ExtraVarsSchema *schema.Schema
}

// ProjectedTemplateColumns returns the shared columns a job_templates
// projection of def must carry — ProjectedRepoColumns' own doc comment,
// Task 6's analogous function for job templates' own, different column
// split. Deliberately does NOT return a playbook_id: unlike every column
// here, that one is not copied from the definition at all. It is resolved
// PER ORGANIZATION, by joining that organization's own projection of
// SharedRepoID to its own playbooks row at PlaybookPath — store_sql.go's
// own queries (queries/shared.sql) do that resolution in SQL, where the
// join lives; there is nothing pure left for a Go function to compute
// here, which is exactly why this function's signature has one fewer
// return value than ProjectedRepoColumns' four.
func ProjectedTemplateColumns(def TemplateDefinition) (name, limit string, extraVars map[string]any, extraVarsSchema *schema.Schema) {
	return def.Name, def.Limit, def.ExtraVars, def.ExtraVarsSchema
}

// schemaHasSecretField reports whether fields declares a TypeSecret field,
// at any depth (inside a repeater's Item or an object's own Fields). A
// shared template definition's schema may not declare one: propagating a
// definition writes its extra_vars straight into every organization's own
// job_templates.extra_vars column (store_sql.go's INSERT/UPDATE, bypassing
// internal/templates.Service entirely — ProjectedRepoColumns' own doc
// comment on why there is nothing left here for a Go function to seal),
// which never seals a secret-typed value the way
// templates.Service.sealExtraVars does for an ordinary template. Refusing
// the schema at Service.CreateTemplate/UpdateTemplate is what keeps a
// provider's shared extra var from ever reaching a customer's own database
// column as plaintext — the same principle the spec's own "What may be
// shared" section states for the feature as a whole: "A shared definition
// carries no secret by construction, so there is nothing for a policy to
// get wrong."
func schemaHasSecretField(fields []schema.Field) bool {
	for _, f := range fields {
		if f.Type == schema.TypeSecret {
			return true
		}
		if f.Item != nil && schemaHasSecretField([]schema.Field{*f.Item}) {
			return true
		}
		if schemaHasSecretField(f.Fields) {
			return true
		}
	}
	return false
}

// OrgName is one organization-scoped row's own name, the raw material
// CollidingOrgs matches a candidate name against. It is deliberately not a
// map keyed by organization: one organization owns many rows in the table a
// definition projects into (git_repos today), and only one of them need
// match for that organization to collide.
type OrgName struct {
	OrgID uuid.UUID
	Name  string
}

// CollidingOrgs reports which organizations already have a row named name,
// given existing — every organization-scoped row's own (org, name) pair
// from the table a definition projects into. The caller excludes a
// definition's own current projections from existing before calling this
// for a rename (shared.Store.ExistingNames' own exclude parameter), so
// renaming a definition back to the name its own projections already
// carry is never reported as colliding with itself.
//
// This returns organizations, not a formatted message, on purpose: the
// service must not decide who may read that list, and compose a sentence
// only the one layer allowed to show it (Task 4, to an instance admin,
// never inside an organization — spec's own Risk: "a customer discovering
// the provider's other customers") may build. The result is sorted by
// organization id for a stable, testable order; CollidingOrgs itself never
// receives the same organization twice for the colliding name (an
// organization cannot have two rows with the same name — the same unique
// constraint decision 7 is about), but is written to de-duplicate anyway
// rather than assume that always holds upstream.
func CollidingOrgs(existing []OrgName, name string) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(existing))
	var out []uuid.UUID
	for _, e := range existing {
		if e.Name != name || seen[e.OrgID] {
			continue
		}
		seen[e.OrgID] = true
		out = append(out, e.OrgID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// NameCollisionError is ErrNameTaken's richer form for spec decision 7:
// Orgs names every organization that already has its own row named Name,
// so an instance-admin-only caller (Task 4) can list them without this
// package ever composing a sentence that might reach an organization.
// errors.Is(err, ErrNameTaken) still reports true through Unwrap.
type NameCollisionError struct {
	Name string
	Orgs []uuid.UUID
}

func (e *NameCollisionError) Error() string {
	return fmt.Sprintf("name %q is already used by %d organization(s)", e.Name, len(e.Orgs))
}

// Unwrap is what makes errors.Is(err, ErrNameTaken) true for this richer
// error, the same way every other sentinel-wrapping error in this codebase
// (routeRowError's fmt.Errorf("%w: ...", ErrNameTaken, ...) in
// internal/alertroutes/store_sql.go, for one) stays matchable by its
// sentinel.
func (e *NameCollisionError) Unwrap() error { return ErrNameTaken }

// InUseError is ErrInUse's richer form for spec decision 5: OrgID names the
// organization whose projection is still referenced by something inside
// it ("a projection may be named by a schedule, a trigger or a job — a
// customer's configuration"), and Reasons names what, in whatever terms
// the caller's own dependant check produced ("a job trigger", "a
// playbook", …) — kept as a slice of strings rather than a single
// formatted sentence so Task 4 can decide how (or whether) to render the
// list, the same reasoning that keeps NameCollisionError's Orgs as data
// rather than prose. Reasons may be empty: a foreign-key violation caught
// as a backstop (not from an explicit dependant check) has no named reason
// to offer, only the organization.
type InUseError struct {
	OrgID   uuid.UUID
	Reasons []string
}

func (e *InUseError) Error() string {
	if len(e.Reasons) == 0 {
		return fmt.Sprintf("organization %s still has a dependant referencing this definition's projection", e.OrgID)
	}
	return fmt.Sprintf("organization %s: %s still reference this definition's projection", e.OrgID, strings.Join(e.Reasons, ", "))
}

// Unwrap is what makes errors.Is(err, ErrInUse) true for this richer
// error — NameCollisionError's own Unwrap, for the other sentinel.
func (e *InUseError) Unwrap() error { return ErrInUse }
