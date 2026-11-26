package runrequests

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/secretshadow"
	"github.com/noixdev/pulsix/internal/templates"
)

// errNoSecretSources means the resolver was wired without the sources that
// name the run's other secrets; a request with extra vars is then refused
// rather than taken unchecked.
var errNoSecretSources = errors.New("run request overrides: no secret sources")

// checkSecretShadows refuses an extra var named like a secret the run's
// other variable sources hold (final review I2, controller ruling: secrets
// are never set by the assistant): an inventory secret of hosts, a
// variable the template's playbook declares secret, or one its repository
// sets to a vault-encrypted value (secretshadow.Checker, shared with the
// webhook triggers' mappings, backlog 2.139). Extra vars outrank every
// other variable, so such a value would replace the secret for the run.
// The template's own secret fields are refused by checkOverrides. No
// refusal echoes a value.
func (r *Resolver) checkSecretShadows(ctx context.Context, orgID uuid.UUID, tmpl templates.Template, ov *Overrides, hosts []uuid.UUID) error {
	if ov == nil || len(ov.ExtraVars) == 0 {
		return nil
	}
	if r.src.InventorySecrets == nil || r.src.PlaybookVars == nil {
		return errNoSecretSources
	}
	c := secretshadow.Checker{Inventory: r.src.InventorySecrets, Playbooks: notFoundMapped{r.src.PlaybookVars}}
	err := c.Check(ctx, orgID, tmpl.PlaybookID, hosts, slices.Collect(maps.Keys(ov.ExtraVars)), "extra var")
	switch {
	case errors.Is(err, secretshadow.ErrShadow):
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	case errors.Is(err, secretshadow.ErrNoSources):
		return errNoSecretSources
	}
	return err
}

// notFoundMapped is a playbook variable source whose not-found errors are
// the resolver's own (mapNotFound).
type notFoundMapped struct{ PlaybookVarSource }

func (n notFoundMapped) VarSource(ctx context.Context, orgID, playbookID uuid.UUID) (playbooks.VarSource, error) {
	src, err := n.PlaybookVarSource.VarSource(ctx, orgID, playbookID)
	if err != nil {
		return src, mapNotFound(err)
	}
	return src, nil
}
