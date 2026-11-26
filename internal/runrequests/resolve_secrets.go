package runrequests

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
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
//     prompt, a Pulsix marker typed secret);
//   - a variable the playbook's repository sets to a value encrypted with
//     Ansible Vault (group_vars, host_vars, role defaults and vars, play
//     vars and vars files: varsource.Vault, backlog 2.134).
//
// The template's own secret fields are refused by checkOverrides. The
// vault names come with a repository read by a current worker; when the
// last read did not report them (an older worker, a sync before the
// upgrade) or did not read every variable file, they are not all known
// and checkVaultNames refuses what it still can. No refusal echoes a
// value.
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
			return fmt.Errorf("%w: extra var %s: the inventory holds a secret of that name for a target host, which an extra var must not replace", ErrInvalidInput, name)
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
			return fmt.Errorf("%w: extra var %s: the playbook declares a secret of that name, which an extra var must not replace", ErrInvalidInput, name)
		}
	}
	return checkVaultNames(src, names)
}

// checkVaultNames refuses an extra var named like a variable the
// repository of a repository playbook sets to a vault-encrypted value.
// When those names are not all known (src.Vault nil: the last read came
// from a worker that does not report them; or a read that left variable
// files unread, found a file encrypted as a whole or was cut), it falls
// back to what the sources do say (controller ruling on 2.134): a name
// the playbook declares but whose value could not be read
// (varsource.Playbook.Unreadable) is refused, any other passes. Full
// protection needs an updated worker and a fresh sync (docs/upgrades.md).
// An editor playbook has no repository variables.
func checkVaultNames(src playbooks.VarSource, names []string) error {
	if src.Kind != playbooks.VarSourceRepo {
		return nil
	}
	if src.Vault != nil {
		files := map[string]string{}
		for _, v := range src.Vault.Names {
			if _, seen := files[v.Name]; !seen {
				files[v.Name] = v.File
			}
		}
		for _, name := range names {
			if file, ok := files[name]; ok {
				return fmt.Errorf("%w: extra var %s: the repository sets a variable of that name to a value encrypted with Ansible Vault (%s), which an extra var must not replace", ErrInvalidInput, name, file)
			}
		}
	}
	if src.Vault.Complete() {
		return nil
	}
	for _, name := range names {
		if slices.Contains(src.Playbook.Unreadable, name) {
			return fmt.Errorf("%w: extra var %s: the playbook declares a variable of that name whose value could not be read, and the repository's vault-encrypted variables are not known (sync the repository with an updated worker); an extra var must not replace it", ErrInvalidInput, name)
		}
	}
	return nil
}
