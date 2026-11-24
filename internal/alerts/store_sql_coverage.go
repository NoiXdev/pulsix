// The maintenance and silence windows that covered a host at any instant
// of a range, rather than at one instant (WindowsForHost,
// store_sql_read.go). The escalation pass (internal/alertroutes,
// follow_escalate.go) is the reader: decision 5 of the v0.4d escalation
// design anchors a chain's next rung on the end of the occurrence that
// covered it, and an occurrence that opened and closed between two of
// its ticks is never active at any instant it samples (backlog 4.41).
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
// maintenance.Occurrences call; stretchesBetween pages until the range is
// exhausted, so this bounds a call, not the answer.
const stretchPage = 500

// WindowsForHostBetween returns every stretch of a maintenance or silence
// window whose target covers hostID (the same candidate set and target
// resolution as WindowsForHost) that overlaps [from, to), in no particular
// order. Unlike WindowsForHost it also returns stretches that have already
// ended, which is the point.
//
// The answer is complete, not capped: a cap would drop the latest
// stretches, which are the ones the escalation anchor needs. The cost is
// therefore the number of stretches in the range, and the caller bounds
// the range — the escalation pass never asks for more than one rung's
// delay. A window that cannot be evaluated is logged and skipped, the same
// best-effort contract WindowsForHost keeps (backlog 4.29).
func (s *SQLStore) WindowsForHostBetween(ctx context.Context, orgID, hostID uuid.UUID, from, to time.Time) ([]maintenance.Occurrence, error) {
	rows, err := gen.New(s.d).ListMaintenanceWindowsForHost(ctx, gen.ListMaintenanceWindowsForHostParams{
		OrgID: orgID, HostID: db.NullUUID(&hostID), FromAt: from, ToAt: to,
	})
	if err != nil {
		return nil, fmt.Errorf("list maintenance windows for host: %w", err)
	}
	var out []maintenance.Occurrence
	for _, r := range rows {
		occ, err := stretchesBetween(r, from, to)
		if err != nil {
			slog.ErrorContext(ctx, "maintenance window cannot be evaluated and is suppressing nothing",
				"window", r.ID, "org", orgID, "host", hostID, "err", err)
			continue
		}
		out = append(out, occ...)
	}
	return out, nil
}

// stretchesBetween lists every stretch of r overlapping [from, to),
// paging through maintenance.Occurrences until it reports no truncation.
// Each page restarts at the last stretch's end: every stretch of one
// window has the same duration, so the next one Occurrences finds from
// there is the first that started after the last one did.
func stretchesBetween(r gen.MaintenanceWindow, from, to time.Time) ([]maintenance.Occurrence, error) {
	w, err := windowOf(r)
	if err != nil {
		return nil, err
	}
	var out []maintenance.Occurrence
	for {
		page, more, err := maintenance.Occurrences(w, from, to, stretchPage)
		if err != nil {
			return nil, fmt.Errorf("evaluate: %w", err)
		}
		out = append(out, page...)
		if !more {
			return out, nil
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
