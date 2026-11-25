package runrequests

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// repeatedOverrides is what a retry of a job of run group group repeats of
// the request that started the run (spec 2026-11-25 A, final review M1): its
// extra vars. The rest of its overrides is spent already: the hosts and the
// limit narrowed the run, and the retry runs on its failed hosts only, which
// lie inside them; a dry run is never retried. A retry request stores what
// it repeats, so a retry of that retry repeats it again. nil when the run
// came from no request or the request set no extra vars.
func (s *Service) repeatedOverrides(ctx context.Context, orgID, group uuid.UUID) (*Overrides, error) {
	orig, err := s.store.ByRunGroup(ctx, orgID, group)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up the retried run's request: %w", err)
	}
	if orig.Overrides == nil || len(orig.Overrides.ExtraVars) == 0 {
		return nil, nil
	}
	return &Overrides{ExtraVars: maps.Clone(orig.Overrides.ExtraVars)}, nil
}

// RetryOverrides is what a person's direct retry of jobID repeats (the
// retry route, jobs.Service.RetryFailedWith): the extra vars of the run
// request that started its run, checked against the template and the
// retry's hosts as they are now, as the retry's run overrides; nil when
// there are none. A job that cannot be retried has none here: the retry
// itself then says why, as it always did.
func (s *Service) RetryOverrides(ctx context.Context, orgID, jobID uuid.UUID) (*jobs.RunOverrides, error) {
	_, group, _, err := s.validator.ValidateRetry(ctx, orgID, jobID)
	if err != nil {
		return nil, nil //nolint:nilerr // the retry route reports the job's own refusal
	}
	ov, err := s.repeatedOverrides(ctx, orgID, group)
	if err != nil || ov == nil {
		return nil, err
	}
	run, err := s.validator.RetryRunOverrides(ctx, orgID, jobID, ov)
	if errors.Is(err, ErrInvalidInput) {
		return nil, &RetryVarsError{Reason: strings.TrimPrefix(err.Error(), ErrInvalidInput.Error()+": ")}
	}
	return run, err
}

// RetryVarsError refuses a person's retry whose repeated extra vars (the
// original run's, RetryOverrides) are no longer allowed; Reason names the
// variable and the rule, never a value. It is an ErrInvalidInput.
type RetryVarsError struct{ Reason string }

func (e *RetryVarsError) Error() string {
	return "the extra variables of the run this retries are no longer allowed: " + e.Reason
}

// Is makes errors.Is(err, ErrInvalidInput) hold.
func (e *RetryVarsError) Is(target error) bool { return target == ErrInvalidInput }

// RetryRunOverrides implements Validator: ov's extra vars pass the checks a
// run's do (checkOverrides, checkSecretShadows on the retry's failed hosts)
// and travel Untrusted, like the original run's did.
func (r *Resolver) RetryRunOverrides(ctx context.Context, orgID, jobID uuid.UUID, ov *Overrides) (*jobs.RunOverrides, error) {
	if ov == nil || len(ov.ExtraVars) == 0 {
		return nil, nil
	}
	templateID, _, failed, err := r.src.Jobs.RetryTargets(ctx, orgID, jobID)
	if err != nil {
		return nil, mapNotFound(err)
	}
	tmpl, err := r.src.Templates.Get(ctx, orgID, templateID)
	if err != nil {
		return nil, mapNotFound(err)
	}
	if err := r.checkOverrides(ctx, orgID, tmpl, ov); err != nil {
		return nil, err
	}
	if err := r.checkSecretShadows(ctx, orgID, tmpl, ov, failed); err != nil {
		return nil, err
	}
	return &jobs.RunOverrides{ExtraVars: maps.Clone(ov.ExtraVars), Untrusted: true}, nil
}
