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
		return &scopeError{err: err}
	}
	return s.d.Secrets.Check(ctx, orgID, tmpl.PlaybookID, hosts, names, "mapping target")
}

// scopeError is a failure to resolve the template's hosts.
type scopeError struct{ err error }

func (e *scopeError) Error() string { return "resolve the template's hosts: " + e.err.Error() }
func (e *scopeError) Unwrap() error { return e.err }

// checkMappingSecrets is the save's check: a refusal is ErrInvalidInput,
// and so is a scope that names something that does not exist (the
// operator's to fix). Any other failure is internal.
func (s *Service) checkMappingSecrets(ctx context.Context, orgID uuid.UUID, tmpl templates.Template, m Mapping) error {
	err := s.checkSecretShadows(ctx, orgID, tmpl, slices.Collect(maps.Keys(m.ExtraVars)))
	if errors.Is(err, secretshadow.ErrShadow) {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	if se, ok := errors.AsType[*scopeError](err); ok && errors.Is(se.err, jobs.ErrNotFound) {
		return fmt.Errorf("%w: the template's targets cannot be resolved, so the mapping cannot be checked against the inventory's secrets: %s", ErrInvalidInput, se.err.Error())
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

// refuseCollected checks a pending run's collected values before the settle
// ticker (or run now) starts it: a secret that appeared since they were
// collected fails the window, its deliveries naming the variable and the
// kind of secret, never a value. A check the context ended is kept for the
// next try, as an interrupted enqueue is; any other failure to check fails
// the window, as a refused enqueue does. ok is false when the run may
// start.
func (s *Service) refuseCollected(ctx context.Context, t Trigger, ov templates.Overrides) (Firing, bool) {
	field, err := s.refuseSecretShadows(ctx, t, ov)
	switch {
	case err == nil:
		return Firing{}, false
	case field != "":
		return Firing{Outcome: OutcomeFailed, Note: "not started: " + err.Error()}, true
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return Firing{Keep: true}, true
	}
	s.d.Log.ErrorContext(ctx, "collected webhook values could not be checked against the run's secrets", "trigger", t.ID, "org", t.OrgID, "err", err)
	return Firing{Outcome: OutcomeFailed, Note: "the collected values could not be checked against the run's secrets (see the manager log)"}, true
}
