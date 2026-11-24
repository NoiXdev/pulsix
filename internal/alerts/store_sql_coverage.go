// When the maintenance and silence windows covering a host last stopped
// covering it within a range, rather than which cover it at one instant
// (WindowsForHost, store_sql_read.go). The escalation pass (internal/
// alertroutes, follow_escalate.go) is the reader: decision 5 of the v0.4d
// escalation design anchors a chain's next rung on the end of the
// occurrence that covered it, and an occurrence that opened and closed
// between two of its ticks is never active at any instant it samples
// (backlog 4.41).
package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/maintenance"
)

// stretchPage is how many stretches of one recurring window are walked per
// maintenance.Occurrences call; latestEndBetween pages until the range is
// exhausted, so this bounds one call's allocation, not the answer.
const stretchPage = 64

// LatestCoverageEnd returns the latest end in (from, to] of any stretch of
// a maintenance or silence window whose target covers hostID — the same
// candidate set and target resolution as WindowsForHost — and false when
// no stretch ended in that range. A stretch still running at to has not
// ended and does not count; one ending exactly at to does.
//
// Only the latest end is kept, never the stretches themselves: a dense
// recurring window has thousands of stretches in a long range, and the
// escalation anchor needs one instant. The walk is still proportional to
// the stretches in the range, so the caller bounds it — the escalation
// pass asks only for a naively due rung, over exactly its delay. A window
// that cannot be evaluated is logged and skipped, the same best-effort
// contract WindowsForHost keeps (backlog 4.29).
func (s *SQLStore) LatestCoverageEnd(ctx context.Context, orgID, hostID uuid.UUID, from, to time.Time) (time.Time, bool, error) {
	rows, err := gen.New(s.d).ListMaintenanceWindowsForHost(ctx, gen.ListMaintenanceWindowsForHostParams{
		OrgID: orgID, HostID: db.NullUUID(&hostID), FromAt: from, ToAt: to,
	})
	if err != nil {
		return time.Time{}, false, fmt.Errorf("list maintenance windows for host: %w", err)
	}
	var latest time.Time
	found := false
	for _, r := range rows {
		end, ok, err := latestEndBetween(r, from, to)
		if err != nil {
			slog.ErrorContext(ctx, "maintenance window cannot be evaluated and is suppressing nothing",
				"window", r.ID, "org", orgID, "host", hostID, "err", err)
			continue
		}
		if ok && (!found || end.After(latest)) {
			latest, found = end, true
		}
	}
	return latest, found, nil
}

// latestEndBetween is LatestCoverageEnd for one row: the latest end in
// (from, to] of r's stretches, paging through maintenance.Occurrences
// until it reports no truncation. Each page restarts at the last
// stretch's end: every stretch of one window has the same duration, so
// the next one Occurrences finds from there is the first that started
// after the last one did.
func latestEndBetween(r gen.MaintenanceWindow, from, to time.Time) (time.Time, bool, error) {
	w, err := windowOf(r)
	if err != nil {
		return time.Time{}, false, err
	}
	var latest time.Time
	found := false
	for {
		page, more, err := maintenance.Occurrences(w, from, to, stretchPage)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("evaluate: %w", err)
		}
		for _, o := range page {
			// Occurrences returns stretches overlapping [from, to), so
			// every End is after from; one past to is still running.
			if !o.End.After(to) && (!found || o.End.After(latest)) {
				latest, found = o.End, true
			}
		}
		if !more {
			return latest, found, nil
		}
		from = page[len(page)-1].End
	}
}

// windowOf is the maintenance.Window a raw row describes, with only the
// fields evaluation needs — the same fields occurrenceAt builds. A
// one-off row missing its start or end is left for maintenance.Occurrences
// to reject; a recurring one missing its timezone or duration is rejected
// here, naming what is missing.
func windowOf(r gen.MaintenanceWindow) (maintenance.Window, error) {
	w := maintenance.Window{ID: r.ID, Reason: maintenance.Reason(r.Reason)}
	if r.Cron == nil {
		w.StartsAt, w.EndsAt = db.TimePtr(r.StartsAt), db.TimePtr(r.EndsAt)
		return w, nil
	}
	if r.Timezone == nil || r.DurationSeconds == nil {
		return w, fmt.Errorf("recurring window has no %s", missingRecurringField(r))
	}
	w.Cron, w.Timezone = *r.Cron, *r.Timezone
	w.Duration = time.Duration(*r.DurationSeconds) * time.Second
	return w, nil
}
