package chain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/jobs"
)

// sweepAge is how old a pending row must be before the sweep looks at it:
// a group's finish report normally resolves it within moments.
const sweepAge = time.Minute

// SQLStore keeps the follow-ups of run groups. Planner writes, resolving and
// dismissing are org-scoped writes that join the transaction ctx carries
// (as a savepoint) or open their own; SweepStale runs on the raw pool across
// organizations like the schedule ticker's due scan.
type SQLStore struct {
	d   *db.DB
	log *slog.Logger
}

var _ jobs.FollowUpPlanner = (*SQLStore)(nil)

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{d: d, log: slog.Default()} }

// inOrgTx runs fn on an org-scoped transaction: a savepoint of the write
// transaction ctx carries, else a transaction of its own.
func (s *SQLStore) inOrgTx(ctx context.Context, orgID uuid.UUID, fn func(q *gen.Queries) error) error {
	tx, err := s.d.Begin(db.WithOrgScope(ctx, orgID))
	if err != nil {
		return fmt.Errorf("begin follow-up transaction: %w", err)
	}
	if err := fn(gen.New(tx)); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit follow-up transaction: %w", err)
	}
	return nil
}

// PlanFollowUps implements jobs.FollowUpPlanner. Rows that already exist for
// the group are left exactly as they are.
func (s *SQLStore) PlanFollowUps(ctx context.Context, orgID, groupID, templateID uuid.UUID, override *[]uuid.UUID, c jobs.ChainContext) error {
	path := c.Path
	if path == nil {
		path = []uuid.UUID{}
	}
	return s.inOrgTx(ctx, orgID, func(q *gen.Queries) error {
		if override == nil {
			if _, err := q.PlanTemplateFollowUps(ctx, gen.PlanTemplateFollowUpsParams{
				OrgID: orgID, ParentGroupID: groupID, TemplateID: templateID, RootTrigger: c.RootTrigger,
				RootRef: db.NullUUID(c.RootRef), Depth: int32(c.Depth), Path: path, ConfiguredBy: db.NullUUID(c.ConfiguredBy), //nolint:gosec // depth is capped at 10
			}); err != nil {
				return fmt.Errorf("plan template follow-ups: %w", err)
			}
			return nil
		}
		for i, id := range *override {
			if _, err := q.InsertRunFollowUp(ctx, gen.InsertRunFollowUpParams{
				ID: uuid.Must(uuid.NewV7()), OrgID: orgID, ParentGroupID: groupID, TemplateID: id, Condition: conditionSuccess,
				Position: int32(i), RootTrigger: c.RootTrigger, RootRef: db.NullUUID(c.RootRef), //nolint:gosec // at most 10 follow-ups
				Depth: int32(c.Depth), Path: path, ConfiguredBy: db.NullUUID(c.ConfiguredBy), //nolint:gosec // depth is capped at 10
			}); err != nil {
				return fmt.Errorf("plan override follow-up: %w", err)
			}
		}
		return nil
	})
}

// CopyFollowUps implements jobs.FollowUpPlanner. A nil only copies every
// row, a non-nil one only the rows of the listed templates.
func (s *SQLStore) CopyFollowUps(ctx context.Context, orgID, fromGroupID, toGroupID uuid.UUID, configuredBy *uuid.UUID, only *[]uuid.UUID) error {
	var templateIDs []uuid.UUID // nil is SQL NULL: no filter
	if only != nil {
		templateIDs = append([]uuid.UUID{}, *only...) // empty, not nil: copies none
	}
	return s.inOrgTx(ctx, orgID, func(q *gen.Queries) error {
		if _, err := q.CopyRunFollowUps(ctx, gen.CopyRunFollowUpsParams{
			OrgID: orgID, FromGroup: fromGroupID, ToGroup: toGroupID, ConfiguredBy: db.NullUUID(configuredBy), TemplateIds: templateIDs,
		}); err != nil {
			return fmt.Errorf("copy follow-ups: %w", err)
		}
		return nil
	})
}

// ClearUnstarted deletes the group's rows that never started a run
// (pending or skipped).
func (s *SQLStore) ClearUnstarted(ctx context.Context, orgID, groupID uuid.UUID) error {
	return s.inOrgTx(ctx, orgID, func(q *gen.Queries) error {
		if _, err := q.ClearUnstartedRunFollowUps(ctx, gen.ClearUnstartedRunFollowUpsParams{OrgID: orgID, ParentGroupID: groupID}); err != nil {
			return fmt.Errorf("clear unstarted follow-ups: %w", err)
		}
		return nil
	})
}

// ResolveGroup decides the pending follow-ups of a finished run group: due
// with the hosts the run played on after a success, otherwise skipped with
// the run's outcome as the reason. A group without pending rows is left
// alone after one cheap check.
func (s *SQLStore) ResolveGroup(ctx context.Context, orgID, groupID uuid.UUID, status jobs.Status, now time.Time) error {
	return s.inOrgTx(ctx, orgID, func(q *gen.Queries) error { return resolve(ctx, q, orgID, groupID, status, now) })
}

func resolve(ctx context.Context, q *gen.Queries, orgID, groupID uuid.UUID, status jobs.Status, now time.Time) error {
	has, err := q.HasPendingFollowUps(ctx, gen.HasPendingFollowUpsParams{OrgID: orgID, ParentGroupID: groupID})
	if err != nil {
		return fmt.Errorf("check pending follow-ups: %w", err)
	}
	if !has {
		return nil
	}
	state, reason, hosts := StateSkipped, "", []uuid.UUID{}
	switch status {
	case jobs.StatusSucceeded:
		played, err := q.PlayedHosts(ctx, gen.PlayedHostsParams{OrgID: orgID, RunGroupID: groupID})
		if err != nil {
			return fmt.Errorf("read played hosts: %w", err)
		}
		if len(played) == 0 {
			reason = ReasonNoHostPlayed
		} else {
			state, hosts = StateDue, played
		}
	case jobs.StatusCancelled:
		reason = ReasonRunCancelled
	default:
		reason = ReasonRunFailed
	}
	return resolvePending(ctx, q, orgID, groupID, state, reason, hosts, now)
}

func resolvePending(ctx context.Context, q *gen.Queries, orgID, groupID uuid.UUID, state State, reason string, hosts []uuid.UUID, now time.Time) error {
	if _, err := q.ResolvePendingFollowUps(ctx, gen.ResolvePendingFollowUpsParams{
		OrgID: orgID, ParentGroupID: groupID, State: string(state), Reason: reason, HostIds: hosts, Now: now,
	}); err != nil {
		return fmt.Errorf("resolve follow-ups: %w", err)
	}
	return nil
}

// groupKey names one run group across organizations.
type groupKey struct{ org, group uuid.UUID }

// SweepStale resolves the groups whose pending follow-ups the finish report
// never reached: their jobs are all terminal (or there are none) and the
// rows are older than a minute. It handles at most limit stale rows and
// returns the number of groups resolved. Each group has its own savepoint,
// so one bad group never undoes another.
func (s *SQLStore) SweepStale(ctx context.Context, now time.Time, limit int) (int, error) {
	tx, err := s.d.Pool().Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin follow-up sweep: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)
	stale, err := q.ListStalePendingFollowUps(ctx, gen.ListStalePendingFollowUpsParams{Before: now.Add(-sweepAge), Lim: int32(limit)}) //nolint:gosec // limit is a small batch size
	if err != nil {
		return 0, fmt.Errorf("list stale follow-ups: %w", err)
	}
	var order []groupKey
	seen := map[groupKey]bool{}
	for _, r := range stale {
		k := groupKey{r.OrgID, r.ParentGroupID}
		if !seen[k] {
			seen[k] = true
			order = append(order, k)
		}
	}
	resolved := 0
	for _, k := range order {
		if ctx.Err() != nil {
			break
		}
		if err := sweepGroup(ctx, tx, k, now); err != nil {
			s.log.WarnContext(ctx, "chain sweep: leaving a run group pending", "org", k.org, "group", k.group, "error", err)
			continue
		}
		resolved++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit follow-up sweep: %w", err)
	}
	return resolved, nil
}

func sweepGroup(ctx context.Context, tx pgx.Tx, k groupKey, now time.Time) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin sweep savepoint: %w", err)
	}
	q := gen.New(sp)
	err = func() error {
		out, err := q.RunGroupOutcome(ctx, gen.RunGroupOutcomeParams{
			OrgID: k.org, RunGroupID: k.group, WithdrawnError: jobs.WithdrawnAtClaimError,
		})
		if err != nil {
			return fmt.Errorf("read run group outcome: %w", err)
		}
		if out.Jobs == 0 {
			// No job at all, or only jobs withdrawn at claim: the request
			// whose approval created them was not approved again (yet). A
			// later re-approval clears these skipped rows and plans its own.
			return resolvePending(ctx, q, k.org, k.group, StateSkipped, ReasonRunNotStart, []uuid.UUID{}, now)
		}
		// The same verdict as jobs.RunGroupResult.Status.
		status := jobs.StatusSucceeded
		switch {
		case out.FailedOrError > 0:
			status = jobs.StatusFailed
		case out.Cancelled > 0:
			status = jobs.StatusCancelled
		}
		return resolve(ctx, q, k.org, k.group, status, now)
	}()
	if err != nil {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		return err
	}
	return sp.Commit(ctx)
}

// ListForGroup returns the follow-ups of a run group in configured order.
func (s *SQLStore) ListForGroup(ctx context.Context, orgID, groupID uuid.UUID) ([]FollowUp, error) {
	rows, err := gen.New(s.d).ListRunFollowUps(db.WithOrgScope(ctx, orgID), gen.ListRunFollowUpsParams{OrgID: orgID, ParentGroupID: groupID})
	if err != nil {
		return nil, fmt.Errorf("list follow-ups: %w", err)
	}
	out := make([]FollowUp, len(rows))
	for i, r := range rows {
		out[i] = toFollowUp(followUpRow(r))
	}
	return out, nil
}

// ParentOf returns the follow-up a chained run of templateID started from
// parentGroupID (the run's trigger_ref) is for, or nil when there is none.
// It reads the row by its unique key, not by child_group_id, which the
// starter writes only when its batch commits.
func (s *SQLStore) ParentOf(ctx context.Context, orgID, parentGroupID, templateID uuid.UUID) (*FollowUp, error) {
	r, err := gen.New(s.d).GetFollowUpOfChainedRun(db.WithOrgScope(ctx, orgID), gen.GetFollowUpOfChainedRunParams{
		OrgID: orgID, ParentGroupID: parentGroupID, TemplateID: templateID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get follow-up of chained run: %w", err)
	}
	f := toFollowUp(followUpRow(r))
	return &f, nil
}

// Dismiss cancels the group's pending follow-ups and returns how many; rows
// already decided or started are untouched.
func (s *SQLStore) Dismiss(ctx context.Context, orgID, groupID uuid.UUID, reason string, now time.Time) (int, error) {
	var n int64
	err := s.inOrgTx(ctx, orgID, func(q *gen.Queries) (err error) {
		n, err = q.DismissPendingFollowUps(ctx, gen.DismissPendingFollowUpsParams{OrgID: orgID, ParentGroupID: groupID, Reason: reason, Now: now})
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("dismiss follow-ups: %w", err)
	}
	return int(n), nil
}
