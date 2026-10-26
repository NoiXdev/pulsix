// Validation of a template's own fields: the shared-definition projection
// that may not be edited locally, the scope's references, the vault
// credentials, and the name and limit patterns. Split out of service.go
// under backlog 2.68 (T3), which was past the 400-line signal CLAUDE.md
// names — these are the checks a write runs BEFORE anything is built or
// sealed, and they neither read nor write the store's rows.
package templates

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/repos"
)

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
