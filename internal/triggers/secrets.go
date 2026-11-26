package triggers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/secretshadow"
	"github.com/noixdev/pulsix/internal/templates"
)

// A mapping target is an extra var, and extra vars outrank every other
// variable of the run: a target named like a secret the run's other
// variable sources hold would let the sender replace that secret's value
// (backlog 2.139, the run-request rule of final review I2 and 2.134). So a
// target may not name an inventory secret of the template's hosts, a
// variable its playbook declares secret, or one its repository sets to a
// vault-encrypted value. It is checked on save, and again at every
// delivery, because such a secret can appear after the trigger was saved.

// SecretShadows refuses names a run's other variable sources hold a secret
// under (secretshadow.Checker): the refusals wrap secretshadow.ErrShadow
// and are *secretshadow.Refusal.
type SecretShadows interface {
	Check(ctx context.Context, orgID, playbookID uuid.UUID, hosts []uuid.UUID, names []string, what string) error
}

// ScopeTargets is a template's hosts before any limit (jobs.Service): a
// mapped limit only narrows them, so a secret of any of them is refused.
type ScopeTargets interface {
	RunTargets(ctx context.Context, orgID, templateID uuid.UUID) ([]uuid.UUID, error)
}

// errNoSecretChecks means the service was wired without the secret checks;
// a mapping with extra vars is then refused rather than taken unchecked.
var errNoSecretChecks = errors.New("trigger mapping: no secret checks")

// checkSecretShadows refuses the first of names a secret of tmpl's run
// holds. With no names nothing is read. A scope that reaches no host now
// (jobs.ErrEmptyScope, or jobs.ErrTemplateNotReady: no run can start
// either way) leaves the inventory nothing to ask about; the next delivery
// checks again.
func (s *Service) checkSecretShadows(ctx context.Context, orgID uuid.UUID, tmpl templates.Template, names []string) error {
	if len(names) == 0 {
		return nil
	}
	if s.d.Secrets == nil || s.d.Scope == nil {
		return errNoSecretChecks
	}
	hosts, err := s.d.Scope.RunTargets(ctx, orgID, tmpl.ID)
	switch {
	case errors.Is(err, jobs.ErrEmptyScope), errors.Is(err, jobs.ErrTemplateNotReady):
		hosts = nil
	case err != nil:
		return fmt.Errorf("resolve the template's hosts: %w", err)
	}
	return s.d.Secrets.Check(ctx, orgID, tmpl.PlaybookID, hosts, names, "mapping target")
}

// checkMappingSecrets is the save's check: a refusal is ErrInvalidInput.
func (s *Service) checkMappingSecrets(ctx context.Context, orgID uuid.UUID, tmpl templates.Template, m Mapping) error {
	err := s.checkSecretShadows(ctx, orgID, tmpl, slices.Collect(maps.Keys(m.ExtraVars)))
	if errors.Is(err, secretshadow.ErrShadow) {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return err
}

// refuseSecretShadows is the delivery's check of the mapped values ov: a
// refusal names its field (extra_vars.<name>), an empty field with an
// error is an internal failure.
func (s *Service) refuseSecretShadows(ctx context.Context, t Trigger, ov templates.Overrides) (string, error) {
	if len(ov.ExtraVars) == 0 {
		return "", nil
	}
	tmpl, err := s.d.Templates.Get(ctx, t.OrgID, *t.TemplateID)
	if err != nil {
		return "", fmt.Errorf("read the trigger's template: %w", err)
	}
	err = s.checkSecretShadows(ctx, t.OrgID, tmpl, slices.Collect(maps.Keys(ov.ExtraVars)))
	if r, ok := errors.AsType[*secretshadow.Refusal](err); ok {
		return "extra_vars." + r.Name, err
	}
	return "", err
}
