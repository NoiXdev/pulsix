package notify

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/jobs"
)

// Outbox turns facts into queued deliveries inside the transaction that
// records the fact (a transactional outbox): a finished run group inside
// the job's terminal transaction, a schedule decision inside the ticker's
// row savepoint, the weekly digest inside the digest ticker's. Each event
// has a key — run:<group>, decision:<schedule run>, digest:<org>:<date> —
// and (org, key, channel) is unique, so a retried source never enqueues
// twice. Every write runs in its own savepoint: a notification failure
// rolls back only the notification, never the fact.
type Outbox struct {
	db        *db.DB
	publicURL string
	now       func() time.Time
	rand      io.Reader
	log       *slog.Logger
}

// NewOutbox wires the outbox; nil now, rand and log take defaults. rand is
// the entropy for delivery ids, and it defaults rather than staying nil
// because a nil reader would panic inside the caller's own transaction —
// the one place a notification must never reach.
func NewOutbox(d *db.DB, publicURL string, now func() time.Time, rand io.Reader, log *slog.Logger) *Outbox {
	if now == nil {
		now = time.Now
	}
	if rand == nil {
		rand = cryptorand.Reader
	}
	if log == nil {
		log = slog.Default()
	}
	return &Outbox{db: d, publicURL: strings.TrimRight(publicURL, "/"), now: now, rand: rand, log: log}
}

// Decision is a schedule decision the ticker recorded.
type Decision struct {
	OrgID, ScheduleID, RunID uuid.UUID
	Outcome, Reason          string
	DueAt, DecidedAt         time.Time
}

// RunGroupFinished implements jobs.RunGroupSink: a scheduled run group
// that finished is reported to the channels its schedule's rules name for
// the run's categories. ctx carries the finishing transaction.
func (o *Outbox) RunGroupFinished(ctx context.Context, r jobs.RunGroupResult) error {
	if r.Trigger != jobs.TriggerSchedule || r.TriggerRef == nil {
		return nil
	}
	result := "succeeded"
	switch r.Status {
	case jobs.StatusFailed, jobs.StatusError:
		result = "failed"
	case jobs.StatusCancelled:
		result = "cancelled"
	}
	sp, err := o.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin notification savepoint: %w", err)
	}
	return o.inSavepoint(ctx, sp, func(q *gen.Queries) error {
		return o.enqueueSchedule(ctx, q, r.OrgID, *r.TriggerRef, "run:"+r.GroupID.String(), runCategories(result, r.HostsChanged),
			func(sc gen.NotifyScheduleContextRow, cats []Category) Event {
				run := &RunSummary{
					ID: r.FirstJobID, Result: result, Hosts: r.Hosts, HostsOK: r.HostsOK(), HostsChanged: r.HostsChanged,
					HostsFailed: r.HostsFailed, HostsNoResult: r.HostsNoResult,
					StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
				}
				if r.StartedAt != nil && r.FinishedAt != nil {
					run.DurationSeconds = int64(r.FinishedAt.Sub(*r.StartedAt).Round(time.Second) / time.Second)
				}
				at := o.now()
				if r.FinishedAt != nil {
					at = *r.FinishedAt
				}
				return Event{
					Kind: EventRunFinished, At: at, Org: OrgRef{ID: r.OrgID, Slug: sc.OrgSlug, Name: sc.OrgName}, Categories: cats,
					Schedule: &NamedRef{ID: *r.TriggerRef, Name: sc.ScheduleName}, Template: &NamedRef{ID: sc.TemplateID, Name: sc.TemplateName},
					Run: run, URL: o.publicURL + "/o/" + sc.OrgSlug + "/runs/" + r.FirstJobID.String(),
				}
			})
	})
}

// EnqueueDecision reports a decision that started no run (skipped,
// missed, failed) inside tx, the ticker's row savepoint. A triggered
// decision is reported when its run group finishes, so it enqueues
// nothing here.
func (o *Outbox) EnqueueDecision(ctx context.Context, tx pgx.Tx, d Decision) error {
	cat, kind, ok := decisionCategory(d.Outcome)
	if !ok {
		return nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin notification savepoint: %w", err)
	}
	return o.inSavepoint(ctx, sp, func(q *gen.Queries) error {
		return o.enqueueSchedule(ctx, q, d.OrgID, d.ScheduleID, "decision:"+d.RunID.String(), []Category{cat},
			func(sc gen.NotifyScheduleContextRow, cats []Category) Event {
				return Event{
					Kind: kind, At: d.DecidedAt, Org: OrgRef{ID: d.OrgID, Slug: sc.OrgSlug, Name: sc.OrgName}, Categories: cats,
					Schedule: &NamedRef{ID: d.ScheduleID, Name: sc.ScheduleName}, Template: &NamedRef{ID: sc.TemplateID, Name: sc.TemplateName},
					Decision: &DecisionSummary{Outcome: d.Outcome, Reason: d.Reason, DueAt: d.DueAt},
					URL:      o.publicURL + "/o/" + sc.OrgSlug + "/schedules?id=" + d.ScheduleID.String(),
				}
			})
	})
}

// inSavepoint runs fn on sp and releases it, or rolls it back on error.
func (o *Outbox) inSavepoint(ctx context.Context, sp pgx.Tx, fn func(q *gen.Queries) error) error {
	if err := fn(gen.New(sp)); err != nil {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		return err
	}
	if err := sp.Commit(ctx); err != nil {
		return fmt.Errorf("release notification savepoint: %w", err)
	}
	return nil
}

// enqueueSchedule queues ev for every enabled channel the schedule's
// rules route any of cats to. A schedule deleted meanwhile (its rules go
// with it) enqueues nothing.
func (o *Outbox) enqueueSchedule(ctx context.Context, q *gen.Queries, orgID, scheduleID uuid.UUID, key string, cats []Category,
	build func(sc gen.NotifyScheduleContextRow, cats []Category) Event) error {
	names := make([]string, len(cats))
	for i, c := range cats {
		names[i] = string(c)
	}
	channels, err := q.NotifyRuleChannels(ctx, gen.NotifyRuleChannelsParams{OrgID: orgID, ScheduleID: scheduleID, Categories: names})
	if err != nil {
		return fmt.Errorf("read notification rules: %w", err)
	}
	if len(channels) == 0 {
		return nil
	}
	sc, err := q.NotifyScheduleContext(ctx, gen.NotifyScheduleContextParams{OrgID: orgID, ScheduleID: scheduleID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read notification context: %w", err)
	}
	return o.insert(ctx, q, orgID, key, build(sc, cats), channels)
}

// EnqueueDigest queues a weekly digest for the organization's enabled
// digest channels inside tx (the digest ticker's row savepoint).
func (o *Outbox) EnqueueDigest(ctx context.Context, tx pgx.Tx, d DueDigest, sum DigestSummary) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin notification savepoint: %w", err)
	}
	return o.inSavepoint(ctx, sp, func(q *gen.Queries) error {
		channels, err := q.DigestChannelsForSend(ctx, d.OrgID)
		if err != nil {
			return fmt.Errorf("read digest channels: %w", err)
		}
		if len(channels) == 0 {
			return nil
		}
		org, err := q.NotifyOrgContext(ctx, d.OrgID)
		if err != nil {
			return fmt.Errorf("read organization: %w", err)
		}
		base := o.publicURL + "/o/" + org.Slug
		for i := range sum.Slowest {
			sum.Slowest[i].URL = base + "/runs/" + sum.Slowest[i].RunID.String()
		}
		ev := Event{Kind: EventDigest, At: d.Occurrence, Org: OrgRef{ID: d.OrgID, Slug: org.Slug, Name: org.Name}, Digest: &sum, URL: base + "/schedules"}
		return o.insert(ctx, q, d.OrgID, d.Key(), ev, channels)
	})
}

// Enqueue is the exported door onto insert, for callers outside this
// package that have already decided who should be told. It adds no policy
// of its own: resolution, grouping and deduplication of events belong to
// the caller (internal/alertroutes), and delivery, retries and the log
// stay here. Like EnqueueDecision and EnqueueDigest, it runs in its own
// savepoint nested inside tx, so a notification failure rolls back only
// the notification, never the caller's own transaction.
func (o *Outbox) Enqueue(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, key string, ev Event, channels []uuid.UUID) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin notification savepoint: %w", err)
	}
	return o.inSavepoint(ctx, sp, func(q *gen.Queries) error {
		return o.insert(ctx, q, orgID, key, ev, channels)
	})
}

// insert queues one delivery per channel; the unique event key makes a
// repeated insert a no-op.
func (o *Outbox) insert(ctx context.Context, q *gen.Queries, orgID uuid.UUID, key string, ev Event, channels []uuid.UUID) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode notification event: %w", err)
	}
	now := o.now()
	for _, ch := range channels {
		id, err := uuid.NewV7FromReader(o.rand)
		if err != nil {
			return fmt.Errorf("delivery id: %w", err)
		}
		if _, err := q.InsertNotificationDelivery(ctx, gen.InsertNotificationDeliveryParams{
			ID: id, OrgID: orgID, ChannelID: ch, EventKey: key, EventKind: string(ev.Kind), Payload: payload,
			Status: string(DeliveryPending), NextAttemptAt: now, CreatedAt: now,
		}); err != nil {
			return fmt.Errorf("queue notification delivery: %w", err)
		}
	}
	return nil
}
