// Triggered runs: the entry point schedules and webhook triggers enqueue
// through (spec 2026-09-18 §2 step 4, §3). Same validation, zone split and
// host-key checks as a manual run — enqueueRunScope is shared — plus the
// trigger kind and reference and the sealed per-run overrides.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Trigger kinds beyond manual and system.
const (
	TriggerSchedule = "schedule"
	TriggerWebhook  = "webhook"
)

// RunOverrides is the plaintext override a run carries — sealed with
// JobAAD in the job's payload, merged over the template at claim time.
type RunOverrides struct {
	Limit     *string        `json:"limit,omitempty"`
	ExtraVars map[string]any `json:"extra_vars,omitempty"`
	// Untrusted marks values an unauthenticated sender supplied (a webhook
	// payload): the claim marks every string in them unsafe for Ansible and
	// re-checks the limit (ansiblevars.Inert, CheckUntrustedLimit). It is
	// sealed together with the values, so it cannot be dropped on its own.
	Untrusted bool `json:"untrusted,omitempty"`
}

func (o *RunOverrides) empty() bool {
	return o == nil || (o.Limit == nil && len(o.ExtraVars) == 0)
}

// ErrAllHostsExcluded means every host of the run's scope was excluded
// by the request (a schedule's maintenance guard, spec §6): nothing is
// left to run. Schedules record it as skipped_maintenance.
var ErrAllHostsExcluded = errors.New("every targeted host is excluded from this run")

// RunRequest says who or what starts a run and with which overrides.
type RunRequest struct {
	Trigger    string
	TriggerRef *uuid.UUID
	Actor      *uuid.UUID
	Overrides  *RunOverrides
	// ExcludeHosts drops these hosts from the resolved scope (a schedule's
	// exclude_hosts maintenance guard). When none is left the enqueue
	// fails with ErrAllHostsExcluded.
	ExcludeHosts []uuid.UUID
}

// validate checks the trigger kind and that TriggerRef fits it: schedule
// and webhook runs always name the schedule or trigger that started them,
// manual and system runs never carry one (a mismatch here is a wiring
// slip in a caller — Tasks 6/9/10 — not something an operator can cause).
func (r RunRequest) validate() error {
	switch r.Trigger {
	case TriggerManual, TriggerSystem:
		if r.TriggerRef != nil {
			return fmt.Errorf("%w: trigger %q carries no trigger reference", ErrInvalidInput, r.Trigger)
		}
	case TriggerSchedule, TriggerWebhook:
		if r.TriggerRef == nil {
			return fmt.Errorf("%w: trigger %q requires a trigger reference", ErrInvalidInput, r.Trigger)
		}
	default:
		return fmt.Errorf("%w: unknown trigger %q", ErrInvalidInput, r.Trigger)
	}
	return nil
}

// EnqueueRunWith is EnqueueRun with an explicit trigger and overrides.
func (s *Service) EnqueueRunWith(ctx context.Context, orgID, templateID uuid.UUID, req RunRequest) ([]Job, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	tmpl, err := s.templates.TemplateLookup(ctx, orgID, templateID)
	if err != nil {
		return nil, err
	}
	scope, err := parseScope(tmpl.Scope)
	if err != nil {
		return nil, err
	}
	return s.enqueueRunScope(ctx, orgID, tmpl.ID, scope, effectiveLimit(tmpl.Limit, req.Overrides), req)
}

// effectiveLimit is the limit the scope resolution gates zones on: the run
// request's own limit replaces the template's, exactly the precedence the
// claim applies when it hands the limit to ansible — so the enqueue never
// creates a job whose zone ansible would empty on a different pattern
// (backlog 2.45).
func effectiveLimit(tmplLimit string, ov *RunOverrides) string {
	if ov != nil && ov.Limit != nil {
		return *ov.Limit
	}
	return tmplLimit
}

// RunTargets resolves the template's scope into the managed host ids a
// run started now would target (every zone together), before the
// changed-host-key exclusion. The maintenance guard asks it which hosts a
// scheduled run would touch.
func (s *Service) RunTargets(ctx context.Context, orgID, templateID uuid.UUID) ([]uuid.UUID, error) {
	tmpl, err := s.templates.TemplateLookup(ctx, orgID, templateID)
	if err != nil {
		return nil, err
	}
	scope, err := parseScope(tmpl.Scope)
	if err != nil {
		return nil, err
	}
	targets, err := s.resolver.Resolve(ctx, orgID, scope, "")
	if err != nil {
		return nil, err
	}
	var out []uuid.UUID
	for _, t := range targets {
		out = append(out, t.HostIDs...)
	}
	return out, nil
}

// excludeHosts drops the given hosts from per-zone targets; a zone left
// without hosts is dropped too.
func excludeHosts(targets []ZoneTargets, exclude []uuid.UUID) []ZoneTargets {
	if len(exclude) == 0 {
		return targets
	}
	drop := make(map[uuid.UUID]bool, len(exclude))
	for _, id := range exclude {
		drop[id] = true
	}
	out := make([]ZoneTargets, 0, len(targets))
	for _, t := range targets {
		kept := t.HostIDs[:0:0]
		for _, id := range t.HostIDs {
			if !drop[id] {
				kept = append(kept, id)
			}
		}
		if len(kept) > 0 {
			out = append(out, ZoneTargets{ZoneID: t.ZoneID, HostIDs: kept})
		}
	}
	return out
}

// EnqueueSyncWith is EnqueueSync with an explicit trigger (a repo_sync
// webhook). A sync takes no overrides.
func (s *Service) EnqueueSyncWith(ctx context.Context, orgID, repoID uuid.UUID, req RunRequest) (Job, error) {
	if err := req.validate(); err != nil {
		return Job{}, err
	}
	if !req.Overrides.empty() {
		return Job{}, fmt.Errorf("%w: a repository sync takes no overrides", ErrInvalidInput)
	}
	return s.enqueueSync(ctx, orgID, repoID, req)
}

// HasActiveRun reports whether a job of the template is not terminal yet.
func (s *Service) HasActiveRun(ctx context.Context, orgID, templateID uuid.UUID) (bool, error) {
	return s.store.HasActiveTemplateJob(ctx, orgID, templateID)
}

// HasActiveSync reports whether a sync of the repository is not terminal.
func (s *Service) HasActiveSync(ctx context.Context, orgID, repoID uuid.UUID) (bool, error) {
	return s.store.HasActiveRepoSync(ctx, orgID, repoID)
}

// runPayload seals the overrides for one job; nil when there are none.
func (s *Service) runPayload(j Job, ov *RunOverrides) (json.RawMessage, error) {
	if ov.empty() {
		return nil, nil
	}
	raw, err := json.Marshal(ov)
	if err != nil {
		return nil, fmt.Errorf("encode run overrides: %w", err)
	}
	return s.sealPayload(j, raw)
}
