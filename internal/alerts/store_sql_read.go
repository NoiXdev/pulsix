// Everything the API and the notification follower READ: one alert, a
// page of them, an alert's own event timeline, a subject's state
// history, and the maintenance windows covering a host. Split out of
// store_sql.go under backlog 4.26.
//
// None of it takes a caller's transaction — these run on the pool
// through db.DB, under the request's own organization scope — which is
// the line between this file and store_sql_execute.go.
package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/rules"
)

// GetAlert implements Store.
func (s *SQLStore) GetAlert(ctx context.Context, orgID, id uuid.UUID) (AlertWithHost, error) {
	r, err := gen.New(s.d).GetAlert(ctx, gen.GetAlertParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return AlertWithHost{}, ErrNotFound
	}
	if err != nil {
		return AlertWithHost{}, fmt.Errorf("get alert: %w", err)
	}
	return toAlertFromGetRow(r), nil
}

// textOrNil turns "" into a nil *string, the narg idiom every optional
// text Filter field below forwards to ListAlerts: an empty State or Scope
// means "match any", never the literal empty string.
func textOrNil[T ~string](v T) *string {
	if v == "" {
		return nil
	}
	s := string(v)
	return &s
}

// ListAlerts implements Store.
// It asks the query for one row more than limit — the standard way to
// learn "is there a next page" without a second COUNT, the same technique
// hosts.SQLStore.ListHosts and audit.SQLStore.List both use — and encodes
// the cursor from the last row it actually returns.
func (s *SQLStore) ListAlerts(ctx context.Context, orgID uuid.UUID, f Filter, cursor string, limit int) ([]AlertWithHost, string, error) {
	// Clamped here as well as in Service.List, through the same function,
	// so the two can never disagree and a direct call (a test, a future
	// caller) cannot reach the slicing below with a limit of zero — which
	// would index out[-1]. Service clamps so that every Store
	// implementation, fakes included, is handed a sane page size; this
	// clamps so that this one cannot be got wrong at all.
	limit = clampLimit(limit)
	cursorAt, cursorID, err := DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := gen.New(s.d).ListAlerts(ctx, gen.ListAlertsParams{
		OrgID: orgID, Open: f.Open, Severity: textOrNil(f.Severity), HostID: db.NullUUID(f.HostID),
		Scope: textOrNil(f.Scope), Acknowledged: f.Acknowledged,
		CursorOpenedAt: cursorTimeToPg(cursor, cursorAt), CursorID: cursorIDToPg(cursor, cursorID),
		RowLimit: int32(limit + 1), //nolint:gosec // limit is clamped to MaxListLimit by Service.List before it ever reaches here
	})
	if err != nil {
		return nil, "", fmt.Errorf("list alerts: %w", err)
	}
	out := make([]AlertWithHost, len(rows))
	for i, r := range rows {
		out[i] = toAlertFromListRow(r)
	}
	next := ""
	if len(out) > limit {
		last := out[limit-1]
		next = EncodeCursor(last.OpenedAt, last.ID)
		out = out[:limit]
	}
	return out, next, nil
}

// cursorTimeToPg and cursorIDToPg return an unset pgtype value for the
// empty cursor — the "first page" case. DecodeCursor already reports the
// zero time and uuid.Nil for it, but the query tests "cursor_opened_at IS
// NULL" to disable the keyset condition, and a zero timestamptz marked
// Valid is not NULL. The same pair hosts.SQLStore and audit.SQLStore each
// carry, for the same reason.
func cursorTimeToPg(cursor string, at time.Time) pgtype.Timestamptz {
	if cursor == "" {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: at, Valid: true}
}

func cursorIDToPg(cursor string, id uuid.UUID) pgtype.UUID {
	if cursor == "" {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}

// Events implements Store.
func (s *SQLStore) Events(ctx context.Context, orgID, alertID uuid.UUID) ([]Event, error) {
	rows, err := gen.New(s.d).ListAlertEvents(ctx, gen.ListAlertEventsParams{OrgID: orgID, AlertID: alertID})
	if err != nil {
		return nil, fmt.Errorf("list alert events: %w", err)
	}
	out := make([]Event, len(rows))
	for i, r := range rows {
		out[i] = Event{ID: r.ID, At: r.At, Kind: r.Kind, ActorID: db.UUIDPtr(r.ActorID), Payload: r.Payload}
	}
	return out, nil
}

// StateChanges implements Store.
func (s *SQLStore) StateChanges(ctx context.Context, orgID uuid.UUID, subject Subject, from, to time.Time) ([]StateChange, error) {
	rows, err := gen.New(s.d).ListStateChangesForTuple(ctx, gen.ListStateChangesForTupleParams{
		OrgID: orgID, HostID: subject.HostID, CheckKey: subject.CheckKey, Item: subject.Item, Metric: subject.Metric,
		FromAt: from, ToAt: to,
	})
	if err != nil {
		return nil, fmt.Errorf("list state changes: %w", err)
	}
	out := make([]StateChange, len(rows))
	for i, r := range rows {
		out[i] = StateChange{At: r.At, FromState: rules.State(r.FromState), ToState: rules.State(r.ToState)}
	}
	return out, nil
}

// occurrenceAt decides whether one raw maintenance_windows row (r) covers
// the instant now and, if so, the stretch's own Start/End — the same
// question internal/maintenance.ActiveAt answers, kept here rather than
// exported from that package because WindowsForHost is the only caller
// and needs the stretch's bounds, not just a bool. A one-off window reads
// its own starts_at/ends_at directly; a recurring one delegates to
// maintenance.ActiveAt for the yes/no and maintenance.Occurrences — asked
// for the sliver [now, now+1ns) — for the active stretch's own bounds,
// both already proven against the same cron math (occur_test.go). A
// window that does not evaluate (a hand-edited bad cron or timezone) is
// reported inactive rather than failing the whole call — suppression
// display is best-effort the same way OccurrencesBetween's own calendar
// is, never a safety gate.
//
// Inactive is not the whole story, which is why the third return exists
// (backlog 4.29). "This window does not cover now" and "this window can
// no longer be evaluated at all" are the same answer here and must not
// be the same EVENT: the second un-mutes every alert the window covered,
// and before this nothing recorded it anywhere, so the symptom reached
// an operator as pages firing during a declared silence with no trace
// pointing at the cause. The caller logs it and carries on; the
// best-effort contract above is unchanged.
func occurrenceAt(r gen.MaintenanceWindow, now time.Time) (maintenance.Occurrence, bool, error) {
	reason := maintenance.Reason(r.Reason)
	if r.Cron == nil {
		// Through maintenance.ActiveAt, not a second `!now.Before(start)
		// && now.Before(end)` written here (backlog 4.28). Whether a
		// window's stretch is half-open at the end is a convention that
		// package owns; stating it again in this one gives the two
		// somewhere to drift, and an operator would see a window cover
		// its final instant on the alerts page and not on the calendar.
		w := maintenance.Window{
			ID: r.ID, Reason: reason,
			StartsAt: db.TimePtr(r.StartsAt), EndsAt: db.TimePtr(r.EndsAt),
		}
		active, err := maintenance.ActiveAt(w, now)
		if err != nil {
			// A one-off row with no start or end: unevaluable, the same
			// kind of broken row the recurring branch reports below
			// rather than reading as "not now" (backlog 4.29).
			return maintenance.Occurrence{}, false, fmt.Errorf("evaluate: %w", err)
		}
		if !active {
			return maintenance.Occurrence{}, false, nil
		}
		return maintenance.Occurrence{WindowID: r.ID, Reason: reason, Start: *w.StartsAt, End: *w.EndsAt}, true, nil
	}
	if r.Timezone == nil || r.DurationSeconds == nil {
		// A recurring row missing either is not a window this code can
		// evaluate; the schema allows the shape, so say so rather than
		// reading it as "not now".
		return maintenance.Occurrence{}, false, fmt.Errorf("recurring window has no %s", missingRecurringField(r))
	}
	w := maintenance.Window{
		ID: r.ID, Reason: reason, Cron: *r.Cron, Timezone: *r.Timezone,
		Duration: time.Duration(*r.DurationSeconds) * time.Second,
	}
	active, err := maintenance.ActiveAt(w, now)
	if err != nil {
		return maintenance.Occurrence{}, false, fmt.Errorf("evaluate: %w", err)
	}
	if !active {
		return maintenance.Occurrence{}, false, nil
	}
	occs, _, err := maintenance.Occurrences(w, now, now.Add(time.Nanosecond), 1)
	if err != nil {
		return maintenance.Occurrence{}, false, fmt.Errorf("resolve the active occurrence: %w", err)
	}
	if len(occs) == 0 {
		// ActiveAt said yes and the occurrence walk found nothing: not a
		// parse failure, so not an error — the two ask the same question
		// of the same expression and only disagree at a boundary instant.
		return maintenance.Occurrence{}, false, nil
	}
	return occs[0], true, nil
}

// missingRecurringField names which half of a recurring window's required
// pair is absent, so the log line says what to fix.
func missingRecurringField(r gen.MaintenanceWindow) string {
	switch {
	case r.Timezone == nil && r.DurationSeconds == nil:
		return "timezone or duration"
	case r.Timezone == nil:
		return "timezone"
	default:
		return "duration"
	}
}

// WindowsForHost implements Store.
func (s *SQLStore) WindowsForHost(ctx context.Context, orgID, hostID uuid.UUID, now time.Time) ([]maintenance.Occurrence, error) {
	rows, err := gen.New(s.d).ListMaintenanceWindowsForHost(ctx, gen.ListMaintenanceWindowsForHostParams{
		OrgID: orgID, HostID: db.NullUUID(&hostID), Now: now,
	})
	if err != nil {
		return nil, fmt.Errorf("list maintenance windows for host: %w", err)
	}
	var out []maintenance.Occurrence
	for _, r := range rows {
		occ, active, err := occurrenceAt(r, now)
		if err != nil {
			// Logged, never returned: suppression stays best-effort, and
			// failing this call would take down alerting for the whole
			// host over one bad row. But it is no longer silent — this
			// window has stopped covering anything, and an operator
			// looking for why a silenced alert paged has a line naming
			// the window (backlog 4.29).
			slog.ErrorContext(ctx, "maintenance window cannot be evaluated and is suppressing nothing",
				"window", r.ID, "org", orgID, "host", hostID, "err", err)
			continue
		}
		if active {
			out = append(out, occ)
		}
	}
	return out, nil
}
