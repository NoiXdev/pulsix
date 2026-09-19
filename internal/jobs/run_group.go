// Run groups: one run of a job template is split into one job per zone,
// all sharing run_group_id. Notifications (spec 2026-09-18 §7) are about
// the run, not a zone's job, so the queue reports a run group once — when
// its last job becomes terminal — through the optional RunGroupSink.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// RunGroupResult summarises a run group whose jobs are all terminal.
// Host counts come from the per-host results the worker stored in each
// job's stats (changed_hosts, failed_hosts, unreachable_hosts).
type RunGroupResult struct {
	OrgID, GroupID uuid.UUID
	// FirstJobID is the group's oldest job: the run view link.
	FirstJobID uuid.UUID
	TemplateID *uuid.UUID
	Trigger    string
	TriggerRef *uuid.UUID
	// Status aggregates the jobs: failed when any failed or errored,
	// otherwise cancelled when any was cancelled, otherwise succeeded.
	Status                                 Status
	Jobs, Hosts, HostsChanged, HostsFailed int
	StartedAt, FinishedAt                  *time.Time
}

// HostsOK is the number of targeted hosts that did not fail.
func (r RunGroupResult) HostsOK() int { return max(r.Hosts-r.HostsFailed, 0) }

// RunGroupSink receives each finished run group exactly once per terminal
// transition that completes it; ctx carries the finishing transaction, so
// what the sink writes commits with the job's terminal status. A sink
// error never fails the finish: it is logged and the notification lost —
// a notification must not wedge a job in running.
type RunGroupSink interface {
	RunGroupFinished(ctx context.Context, r RunGroupResult) error
}

// SetRunGroupSink injects the finished-run-group port (the notification
// outbox), the same post-construction wiring the other sinks use. Nil
// reports nothing and takes no group lock for run jobs.
func (q *SQLQueue) SetRunGroupSink(sink RunGroupSink) { q.runGroups = sink }

// runGroupQuery reads a run group inside the finishing transaction.
// jsonb_typeof guards the stats lists: a worker that found no host in a
// state writes JSON null there (a nil Go slice), not an empty array.
const runGroupQuery = `
WITH g AS (
  SELECT id, status, host_ids, started_at, finished_at, created_at, template_id, trigger, trigger_ref, stats
  FROM jobs WHERE org_id = $1 AND run_group_id = $2 AND kind = 'run'
),
lists AS (
  SELECT
    CASE WHEN jsonb_typeof(stats->'changed_hosts') = 'array' THEN stats->'changed_hosts' ELSE '[]'::jsonb END AS changed,
    (CASE WHEN jsonb_typeof(stats->'failed_hosts') = 'array' THEN stats->'failed_hosts' ELSE '[]'::jsonb END) ||
    (CASE WHEN jsonb_typeof(stats->'unreachable_hosts') = 'array' THEN stats->'unreachable_hosts' ELSE '[]'::jsonb END) AS failed
  FROM g
)
SELECT
  (SELECT count(*) FROM g),
  (SELECT count(*) FROM g WHERE status IN ('succeeded', 'failed', 'cancelled', 'error')),
  (SELECT count(*) FROM g WHERE status IN ('failed', 'error')),
  (SELECT count(*) FROM g WHERE status = 'cancelled'),
  (SELECT COALESCE(sum(cardinality(host_ids)), 0) FROM g),
  (SELECT count(DISTINCT h) FROM lists, jsonb_array_elements_text(lists.changed) AS h),
  (SELECT count(DISTINCT h) FROM lists, jsonb_array_elements_text(lists.failed) AS h),
  (SELECT min(started_at) FROM g),
  (SELECT max(finished_at) FROM g),
  (SELECT id FROM g ORDER BY created_at, id LIMIT 1),
  (SELECT template_id FROM g LIMIT 1),
  (SELECT trigger FROM g LIMIT 1),
  (SELECT trigger_ref FROM g LIMIT 1)`

// recordRunGroup hands a completed run group to the sink, inside the
// finishing transaction, which already holds the group's advisory lock
// (terminate): with the lock, the job that completes the group always
// sees every other member terminal, and two members finishing together
// cannot both miss the completion.
func (q *SQLQueue) recordRunGroup(ctx context.Context, jobID uuid.UUID) error {
	if q.runGroups == nil {
		return nil
	}
	var orgID, groupID uuid.UUID
	var kind string
	if err := q.db.QueryRow(ctx, `SELECT org_id, kind, run_group_id FROM jobs WHERE id = $1`, jobID).Scan(&orgID, &kind, &groupID); err != nil {
		return fmt.Errorf("record run group: look up job: %w", err)
	}
	if Kind(kind) != KindRun {
		return nil
	}
	r := RunGroupResult{OrgID: orgID, GroupID: groupID}
	var total, terminal, failed, cancelled int
	var first *uuid.UUID
	if err := q.db.QueryRow(ctx, runGroupQuery, orgID, groupID).Scan(
		&total, &terminal, &failed, &cancelled, &r.Hosts, &r.HostsChanged, &r.HostsFailed,
		&r.StartedAt, &r.FinishedAt, &first, &r.TemplateID, &r.Trigger, &r.TriggerRef,
	); err != nil {
		return fmt.Errorf("record run group: read group: %w", err)
	}
	if total == 0 || terminal < total || first == nil {
		return nil // the group is not done yet
	}
	r.Jobs, r.FirstJobID = total, *first
	switch {
	case failed > 0:
		r.Status = StatusFailed
	case cancelled > 0:
		r.Status = StatusCancelled
	default:
		r.Status = StatusSucceeded
	}
	if err := q.runGroups.RunGroupFinished(ctx, r); err != nil {
		slog.ErrorContext(ctx, "finished run group: the notification sink failed; its notifications are lost", "group", groupID, "org", orgID, "err", err)
	}
	return nil
}

// recordCancelledRunGroup is recordRunGroup for a job cancelled before it
// was claimed: that cancel is a single UPDATE (CancelJob), not a terminal
// transition, so the group check runs in its own transaction under the
// same group lock terminate takes.
func (q *SQLQueue) recordCancelledRunGroup(ctx context.Context, jobID uuid.UUID) {
	if q.runGroups == nil {
		return
	}
	var orgID, groupID uuid.UUID
	var kind string
	if err := q.db.QueryRow(ctx, `SELECT org_id, kind, run_group_id FROM jobs WHERE id = $1`, jobID).Scan(&orgID, &kind, &groupID); err != nil {
		slog.ErrorContext(ctx, "cancelled run group: look up job", "job", jobID, "err", err)
		return
	}
	if Kind(kind) != KindRun {
		return
	}
	if err := q.db.WithWriteTx(ctx, orgID, func(ctx context.Context) error {
		if _, err := q.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, finishGroupLockKeyPrefix+groupID.String()); err != nil {
			return fmt.Errorf("lock run group: %w", err)
		}
		return q.recordRunGroup(ctx, jobID)
	}); err != nil {
		slog.ErrorContext(ctx, "cancelled run group: record", "job", jobID, "err", err)
	}
}
