package runrequests

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
)

// errNoSecretSources means the resolver was wired without the sources that
// name the run's other secrets; a request with extra vars is then refused
// rather than taken unchecked.
var errNoSecretSources = errors.New("run request overrides: no secret sources")

// checkSecretShadows refuses an extra var named like a secret the run's
// other variable sources hold (final review I2, controller ruling: secrets
// are never set by the assistant). Extra vars outrank every other
// variable, so such a value would replace the secret for the run:
//   - a secret field of an inventory group schema of any of hosts' groups,
//     or of those groups' ancestors (inventory.Service.SecretVarNames);
//   - a variable the template's playbook declares secret in its recorded
//     variable source (an argument spec option with no_log, a private
//     prompt, a Pulsix marker typed secret).
//
// The template's own secret fields are refused by checkOverrides. Values
// encrypted with Ansible Vault in the repository's group_vars, host_vars
// or role files are not named anywhere Pulsix records (the variable
// reader drops a !vault value without its name), so they are covered only
// as far as the template's schema or the playbook's own declarations mark
// them secret. No refusal echoes a value.
func (r *Resolver) checkSecretShadows(ctx context.Context, orgID uuid.UUID, tmpl templates.Template, ov *Overrides, hosts []uuid.UUID) error {
	if ov == nil || len(ov.ExtraVars) == 0 {
		return nil
	}
	if r.src.InventorySecrets == nil || r.src.PlaybookVars == nil {
		return errNoSecretSources
	}
	names := slices.Sorted(maps.Keys(ov.ExtraVars))
	inv, err := r.src.InventorySecrets.SecretVarNames(ctx, orgID, hosts)
	if err != nil {
		return fmt.Errorf("read the inventory's secret names: %w", err)
	}
	for _, name := range names {
		if inv[name] {
			return fmt.Errorf("%w: extra var %s: the inventory holds a secret of that name for a target host; a run request cannot replace it", ErrInvalidInput, name)
		}
	}
	if tmpl.PlaybookID == uuid.Nil {
		return nil
	}
	src, err := r.src.PlaybookVars.VarSource(ctx, orgID, tmpl.PlaybookID)
	if err != nil {
		return fmt.Errorf("read the playbook's variable source: %w", mapNotFound(err))
	}
	declared := map[string]bool{}
	for _, f := range src.Playbook.Fields {
		if schema.HoldsSecret(f) {
			declared[f.Name] = true
		}
	}
	for _, name := range names {
		if declared[name] {
			return fmt.Errorf("%w: extra var %s: the playbook declares a secret of that name; a run request cannot replace it", ErrInvalidInput, name)
		}
	}
	return nil
}
