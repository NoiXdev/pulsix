// Package secretshadow refuses a variable name a run takes from an
// outside party (a run request's extra vars, a webhook trigger's mapping)
// when one of the run's other variable sources holds a secret under that
// name: the outside value would outrank and replace the secret for the
// run (final review I2 of spec 2026-11-25: secrets are never set by the
// assistant; backlog 2.134 and 2.139).
package secretshadow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
)

// InventorySecrets names the variables the inventory holds a secret under
// for some hosts (inventory.Service).
type InventorySecrets interface {
	SecretVarNames(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[string]bool, error)
}

// PlaybookVars is a playbook's recorded variable source (playbooks.Service).
type PlaybookVars interface {
	VarSource(ctx context.Context, orgID, playbookID uuid.UUID) (playbooks.VarSource, error)
}

// ErrNoSources means the checker was wired without the sources that name
// the run's secrets; a check with names is then refused rather than
// passed unchecked.
var ErrNoSources = errors.New("secret shadow check: no secret sources")

// ErrShadow is wrapped by every refusal (a *Refusal); callers wrap the
// refusal into their own invalid-input error.
var ErrShadow = errors.New("names a secret")

// Refusal is a name refused because a secret holds it. Its message names
// the variable and the kind of secret, never a value.
type Refusal struct {
	Name string
	msg  string
}

func (r *Refusal) Error() string { return r.msg }

// Unwrap makes every refusal ErrShadow.
func (r *Refusal) Unwrap() error { return ErrShadow }

// Checker reads the secret names of a run's other variable sources.
type Checker struct {
	Inventory InventorySecrets
	Playbooks PlaybookVars
}

// Check refuses the first of names (sorted) that is a secret the run's
// other variable sources hold:
//   - a secret field of an inventory group schema of any of hosts' groups,
//     or of those groups' ancestors (inventory.Service.SecretVarNames);
//   - a variable the playbook declares secret in its recorded variable
//     source (an argument spec option with no_log, a private prompt, a
//     Pulsix marker typed secret);
//   - a variable the playbook's repository sets to a value encrypted with
//     Ansible Vault (group_vars, host_vars, role defaults and vars, play
//     vars and vars files: varsource.Vault, backlog 2.134).
//
// what is the noun the messages use ("extra var", "mapping target"). With
// no names nothing is read; with playbookID uuid.Nil (a template whose
// playbook is not registered) only the inventory is asked. A read failure
// is returned wrapped, a playbooks.ErrNotFound among them.
func (c Checker) Check(ctx context.Context, orgID, playbookID uuid.UUID, hosts []uuid.UUID, names []string, what string) error {
	if len(names) == 0 {
		return nil
	}
	if c.Inventory == nil || c.Playbooks == nil {
		return ErrNoSources
	}
	names = slices.Sorted(slices.Values(names))
	inv, err := c.Inventory.SecretVarNames(ctx, orgID, hosts)
	if err != nil {
		return fmt.Errorf("read the inventory's secret names: %w", err)
	}
	for _, name := range names {
		if inv[name] {
			return refuse(name, "%s %s: the inventory holds a secret of that name for a target host, which %s must not replace", what, name, article(what))
		}
	}
	if playbookID == uuid.Nil {
		return nil
	}
	src, err := c.Playbooks.VarSource(ctx, orgID, playbookID)
	if err != nil {
		return fmt.Errorf("read the playbook's variable source: %w", err)
	}
	declared := map[string]bool{}
	for _, f := range src.Playbook.Fields {
		if schema.HoldsSecret(f) {
			declared[f.Name] = true
		}
	}
	for _, name := range names {
		if declared[name] {
			return refuse(name, "%s %s: the playbook declares a secret of that name, which %s must not replace", what, name, article(what))
		}
	}
	return checkVaultNames(src, names, what)
}

// checkVaultNames refuses a name the repository of a repository playbook
// sets to a vault-encrypted value. When those names are not all known
// (src.Vault nil: the last read came from a worker that does not report
// them; or a read that left variable files unread, found a file encrypted
// as a whole or was cut), it falls back to what the sources do say
// (controller ruling on 2.134): a name the playbook declares but whose
// value could not be read (varsource.Playbook.Unreadable) is refused, any
// other passes. Full protection needs an updated worker and a fresh sync
// (docs/upgrades.md). An editor playbook has no repository variables.
func checkVaultNames(src playbooks.VarSource, names []string, what string) error {
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
				return refuse(name, "%s %s: the repository sets a variable of that name to a value encrypted with Ansible Vault (%s), which %s must not replace", what, name, file, article(what))
			}
		}
	}
	if src.Vault.Complete() {
		return nil
	}
	for _, name := range names {
		if slices.Contains(src.Playbook.Unreadable, name) {
			return refuse(name, "%s %s: the playbook declares a variable of that name whose value could not be read, and the repository's vault-encrypted variables are not known (sync the repository with an updated worker); %s must not replace it", what, name, article(what))
		}
	}
	return nil
}

func refuse(name, format string, args ...any) error {
	return &Refusal{Name: name, msg: fmt.Sprintf(format, args...)}
}

// article is what with its indefinite article ("an extra var", "a mapping
// target").
func article(what string) string {
	if what != "" && strings.ContainsRune("aeiou", rune(what[0])) {
		return "an " + what
	}
	return "a " + what
}
