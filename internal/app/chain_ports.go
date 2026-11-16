package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/chain"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
)

// scheduleReader reads one schedule (schedules.Service).
type scheduleReader interface {
	Get(ctx context.Context, orgID, id uuid.UUID) (schedules.Schedule, error)
}

// chainScheduleGuards adapts the schedules service to chain.ScheduleGuards:
// the maintenance guard of a chain's root schedule.
type chainScheduleGuards struct{ schedules scheduleReader }

// NewChainScheduleGuards wires the adapter (also used by
// internal/api/apitest).
func NewChainScheduleGuards(s scheduleReader) chain.ScheduleGuards {
	return chainScheduleGuards{schedules: s}
}

// MaintenanceMode implements chain.ScheduleGuards; a deleted schedule is
// found == false, and a guard never set reads as "".
func (g chainScheduleGuards) MaintenanceMode(ctx context.Context, orgID, scheduleID uuid.UUID) (string, bool, error) {
	sch, err := g.schedules.Get(ctx, orgID, scheduleID)
	if errors.Is(err, schedules.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read schedule: %w", err)
	}
	return string(sch.Guards.Maintenance), true, nil
}

// templateReader reads one template (templates.Service).
type templateReader interface {
	Get(ctx context.Context, orgID, id uuid.UUID) (templates.Template, error)
}

// chainTemplateNames adapts the templates service to chain.TemplateNames: a
// deleted template is the jobs.ErrNotFound the starter expects.
type chainTemplateNames struct{ templates templateReader }

// NewChainTemplateNames wires the adapter (also used by
// internal/api/apitest).
func NewChainTemplateNames(t templateReader) chain.TemplateNames {
	return chainTemplateNames{templates: t}
}

// TemplateName implements chain.TemplateNames.
func (n chainTemplateNames) TemplateName(ctx context.Context, orgID, id uuid.UUID) (string, error) {
	t, err := n.templates.Get(ctx, orgID, id)
	if errors.Is(err, templates.ErrNotFound) {
		return "", fmt.Errorf("%w: template %s", jobs.ErrNotFound, id)
	}
	if err != nil {
		return "", fmt.Errorf("read template: %w", err)
	}
	return t.Name, nil
}

// followUpDecisions queues a notification inside a transaction (notify.Outbox).
type followUpDecisions interface {
	FollowUpDecision(ctx context.Context, tx pgx.Tx, d notify.FollowUpDecision) error
}

// chainNotifier adapts the outbox to chain.Notifier.
type chainNotifier struct{ outbox followUpDecisions }

// NewChainNotifier wires the adapter (also used by internal/api/apitest).
func NewChainNotifier(o followUpDecisions) chain.Notifier { return chainNotifier{outbox: o} }

// FollowUpSkipped implements chain.Notifier.
func (n chainNotifier) FollowUpSkipped(ctx context.Context, tx pgx.Tx, s chain.SkipNotice) error {
	return n.outbox.FollowUpDecision(ctx, tx, notify.FollowUpDecision{
		OrgID: s.OrgID, ScheduleID: s.ScheduleID, RowID: s.RowID, TemplateID: s.TemplateID, ParentTemplateID: s.ParentTemplateID,
		Category: notify.Category(s.Category), Reason: s.Reason, DecidedAt: s.DecidedAt,
	})
}
