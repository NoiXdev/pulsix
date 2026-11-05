// The sync_repo half of the finish path: the repository's sync state
// (the sync sink), the variable sources the sync read (the source sink,
// backlog 7.75) and their apply once the finish committed.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// varSourcesStatsKey is the sync result's stats key carrying the encoded
// variable sources report (internal/worker's VarSourcesStatsKey).
const varSourcesStatsKey = "var_sources"

// takeVarSources splits a sync result's variable sources report off its
// stats: the stats without it (a copy, the caller's map is left alone) and
// the report as JSON, nil when the worker sent none (an older worker). A
// value that does not encode is passed on as JSON null, which the sink
// ignores as malformed.
func takeVarSources(ctx context.Context, stats map[string]any) (map[string]any, json.RawMessage) {
	v, ok := stats[varSourcesStatsKey]
	if !ok {
		return stats, nil
	}
	rest := make(map[string]any, len(stats)-1)
	for k, val := range stats {
		if k != varSourcesStatsKey {
			rest[k] = val
		}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		slog.ErrorContext(ctx, "sync result: variable sources do not encode", "err", err)
		return rest, json.RawMessage("null")
	}
	return rest, raw
}

// recordSync hands a finished sync_repo job's outcome to the optional
// sync sink, inside Finish's transaction (ctx carries it). A database or
// sink failure is returned and rolls the whole finish back; a data anomaly
// (a sync job without a repo id) and a repository deleted meanwhile are
// logged and skipped — retrying could never fix them. A cancelled sync
// records nothing: it never ran, and recording last_sync for it would look
// like a successful fetch. A non-sync job records nothing.
//
// A succeeded sync also hands its variable sources report varSources (nil:
// the worker sent none) to the optional source sink, after the sync sink.
// The returned repository id is the one whose sources the finish should
// apply once it committed: set only when the sink reports it stored new
// sources (uuid.Nil otherwise).
func (q *SQLQueue) recordSync(ctx context.Context, jobID uuid.UUID, status Status, errText string, varSources json.RawMessage) (uuid.UUID, error) {
	if (q.sync == nil && q.sources == nil) || status == StatusCancelled {
		return uuid.Nil, nil
	}
	var orgID uuid.UUID
	var kind string
	var payload json.RawMessage
	if err := q.db.QueryRow(ctx,
		`SELECT org_id, kind, payload FROM jobs WHERE id = $1`, jobID,
	).Scan(&orgID, &kind, &payload); err != nil {
		return uuid.Nil, fmt.Errorf("record sync result: look up finished job: %w", err)
	}
	if Kind(kind) != KindSyncRepo {
		return uuid.Nil, nil
	}
	var sp syncPayload
	if err := json.Unmarshal(payload, &sp); err != nil || sp.RepoID == uuid.Nil {
		slog.ErrorContext(ctx, "record sync result: job payload carries no repo id", "job", jobID)
		return uuid.Nil, nil
	}
	at := q.now()
	if q.sync != nil {
		if gone, err := q.recordSyncResult(ctx, jobID, orgID, sp.RepoID, at, errText); err != nil || gone {
			return uuid.Nil, err
		}
	}
	if status != StatusSucceeded || q.sources == nil {
		return uuid.Nil, nil
	}
	stored, err := q.sources.RecordVarSources(ctx, orgID, sp.RepoID, at, varSources)
	if err != nil {
		return uuid.Nil, fmt.Errorf("record variable sources: %w", err)
	}
	if !stored {
		return uuid.Nil, nil
	}
	return sp.RepoID, nil
}

// recordSyncResult is recordSync's call of the sync sink; gone reports a
// repository deleted meanwhile (logged, not an error).
func (q *SQLQueue) recordSyncResult(ctx context.Context, jobID, orgID, repoID uuid.UUID, at time.Time, errText string) (gone bool, err error) {
	var payload json.RawMessage
	// The observed git server key rides the sync result event (backlog
	// 2.41): the newest ok/failed event is parsed the way recordTest parses
	// the probe result. A sync without a result event (a failed fetch) — or
	// a malformed one — records nothing to pin; the sink drops what cannot
	// be trusted.
	var res syncResultPayload
	if err := q.db.QueryRow(ctx,
		`SELECT payload FROM job_events WHERE job_id = $1 AND state IN ('ok','failed') ORDER BY seq DESC LIMIT 1`,
		jobID,
	).Scan(&payload); err == nil {
		if err := json.Unmarshal(payload, &res); err != nil {
			slog.ErrorContext(ctx, "record sync result: result event is not a result", "err", err, "job", jobID)
			res = syncResultPayload{}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("record sync result: look up result event: %w", err)
	}
	if err := q.sync.RecordSyncResult(ctx, orgID, repoID, at, errText, res.HostKey, res.HostPublicKey); err != nil {
		if sinkGone(err) {
			slog.WarnContext(ctx, "record sync result: repository no longer exists", "job", jobID)
			return true, nil
		}
		return false, fmt.Errorf("record sync result: %w", err)
	}
	return false, nil
}

// sourceApplyTimeout bounds the apply after a sync (plan decision 9).
const sourceApplyTimeout = 30 * time.Second

// applySources hands a repository whose sync just committed new variable
// sources to the optional applier, on a context detached from the
// finish's (a worker that hangs up after its result does not cut the
// apply short) and bounded to sourceApplyTimeout. A failure is logged; a
// manager gone in between leaves the apply to the next sync.
func (q *SQLQueue) applySources(ctx context.Context, orgID, repoID uuid.UUID) {
	if q.applier == nil {
		return
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sourceApplyTimeout)
	defer cancel()
	if err := q.applier.ApplyRepoSources(actx, orgID, repoID); err != nil {
		slog.ErrorContext(ctx, "apply repository variable sources", "err", err, "org", orgID, "repo", repoID)
	}
}
