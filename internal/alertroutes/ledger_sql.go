// The ledger (migration 00052's alert_notify_state) and the two passes
// that read and write it (spec docs/superpowers/specs/2026-10-12-v0.4b-
// alert-routes-design.md, "The follower"). Split out of store_sql.go
// rather than appended to it, once that file's own route CRUD pushed past
// ~400 lines — one responsibility per file, the same rule that already
// split route.go from service.go in this package.
//
// Every method here takes a caller-owned pgx.Tx, never s.d directly: the
// design's own "The ledger write and the enqueue share one transaction"
// means the follower's tick (a later task) opens exactly one transaction
// per organization and every statement below — the reads and the writes —
// runs on it, so a decision and its outbox row commit or roll back
// together. That is also why these statements name org_id in their own
// WHERE/params rather than leaning on row-level security: the tick's
// transaction carries no pulsix.org_id binding, the same posture
// internal/alerts.SQLStore.Execute already takes for the same reason (that
// file's own doc comment explains it once; this is the same shape for a
// different table).
package alertroutes

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/rules"
)

// LedgerState is one value of alert_notify_state.state (migration 00052's
// own CHECK).
type LedgerState string

const (
	StateSent       LedgerState = "sent"
	StateSuppressed LedgerState = "suppressed"
	StateUnrouted   LedgerState = "unrouted"
	StateExpired    LedgerState = "expired"
)

// PendingEvent is one alert_events row neither pass has finished with yet,
// carrying everything Match (match.go, a later task) needs to resolve it
// without a second query: the host it is about, the tuple subject, the
// event kind and the alert's current severity.
//
// There is deliberately no Suppressed field here, even though an earlier
// draft of this type carried one. The store cannot fill it: suppression
// is a Go decision over maintenance windows, silences and snoozes,
// resolved through alerts.Mute in a specific order, and re-implementing
// that order in SQL is exactly the divergence backlog 4.30 already had to
// settle once, between the suppression CTE and
// maintenance.HostsInMaintenance. A field this store returned but never
// set would be a trap for the next reader; the follower keeps the
// suppression verdict in a local variable per event instead.
type PendingEvent struct {
	EventID uuid.UUID
	AlertID uuid.UUID
	HostID  uuid.UUID

	HostName string
	CheckKey string
	Item     string
	Metric   string

	Severity rules.State
	Kind     alerts.EventKind
	At       time.Time
}

// toPendingEvent converts either ledger query's row shape — the two are
// identical column-for-column, but sqlc gives each :many statement its
// own row type even when they agree — into the package's own PendingEvent.
func toPendingEvent(eventID, alertID, hostID uuid.UUID, hostName, checkKey, item, metric, severity, kind string, at time.Time) PendingEvent {
	return PendingEvent{
		EventID: eventID, AlertID: alertID, HostID: hostID,
		HostName: hostName, CheckKey: checkKey, Item: item, Metric: metric,
		Severity: rules.State(severity), Kind: alerts.EventKind(kind), At: at,
	}
}

// UndecidedEvents is the follower's forward pass: every event in org with
// no row in alert_notify_state yet (UndecidedAlertEvents' own doc comment
// explains why an anti-join, not a watermark, is what makes
// TestUndecidedEventsSurvivesOutOfOrderCommits hold). limit bounds how
// many rows one tick claims; the caller — the follower, not this store —
// decides what that number is.
func (s *Store) UndecidedEvents(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, limit int) ([]PendingEvent, error) {
	rows, err := gen.New(tx).UndecidedAlertEvents(ctx, gen.UndecidedAlertEventsParams{
		OrgID: orgID, Lim: int32(limit), //nolint:gosec // the follower's own per-tick batch size, never user input
	})
	if err != nil {
		return nil, fmt.Errorf("undecided alert events: %w", err)
	}
	out := make([]PendingEvent, len(rows))
	for i, r := range rows {
		out[i] = toPendingEvent(r.EventID, r.AlertID, r.HostID, r.HostName, r.CheckKey, r.Item, r.Metric, r.Severity, r.Kind, r.At)
	}
	return out, nil
}

// CatchUpEvents is the follower's catch-up pass: every event still
// ledgered 'suppressed' whose alert is still open (SuppressedAlertEvents'
// own doc comment). A row whose alert has since closed never appears here
// — ExpireClosedSuppressed is what retires those instead.
func (s *Store) CatchUpEvents(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, limit int) ([]PendingEvent, error) {
	rows, err := gen.New(tx).SuppressedAlertEvents(ctx, gen.SuppressedAlertEventsParams{
		OrgID: orgID, Lim: int32(limit), //nolint:gosec // the follower's own per-tick batch size, never user input
	})
	if err != nil {
		return nil, fmt.Errorf("suppressed alert events: %w", err)
	}
	out := make([]PendingEvent, len(rows))
	for i, r := range rows {
		out[i] = toPendingEvent(r.EventID, r.AlertID, r.HostID, r.HostName, r.CheckKey, r.Item, r.Metric, r.Severity, r.Kind, r.At)
	}
	return out, nil
}

// RecordDecision writes the one outcome both passes above produce: a
// fresh row for the forward pass, or an existing 'suppressed' row turned
// into something else for the catch-up pass. RecordAlertNotifyState's own
// ON CONFLICT DO UPDATE is what makes this idempotent either way —
// alert_event_id is the table's primary key, so a second call for the
// same event only ever overwrites, never duplicates.
func (s *Store) RecordDecision(ctx context.Context, tx pgx.Tx, orgID, eventID uuid.UUID, st LedgerState, at time.Time) error {
	if err := gen.New(tx).RecordAlertNotifyState(ctx, gen.RecordAlertNotifyStateParams{
		OrgID: orgID, AlertEventID: eventID, State: string(st), DecidedAt: at,
	}); err != nil {
		return fmt.Errorf("record alert notify state: %w", err)
	}
	return nil
}

// ExpireClosedSuppressed retires every 'suppressed' row in org whose alert
// has since closed to 'expired' (migration 00052's own doc comment on why
// that state is terminal: the catch-up pass only ever offers an open
// alert, so a closed one's row would sit in that pass's working set
// forever otherwise). It returns how many rows were retired, for the
// follower to log; zero is not an error, it is simply nothing to do this
// tick.
func (s *Store) ExpireClosedSuppressed(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, at time.Time) (int64, error) {
	n, err := gen.New(tx).ExpireClosedSuppressed(ctx, gen.ExpireClosedSuppressedParams{
		Now: at, OrgID: orgID,
	})
	if err != nil {
		return 0, fmt.Errorf("expire closed suppressed: %w", err)
	}
	return n, nil
}
