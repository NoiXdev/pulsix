// Service is the package's own front door: the lifecycle sink
// internal/rules.AlertSink asks for (Apply, Sweep), plus the three
// operator verbs the design doc's "Suppression" section adds —
// Acknowledge, Snooze and their reversals — and the two reads, List and
// Get, that serve them back. It composes pieces every earlier task in
// this milestone already built: Reconcile (reconcile.go, pure), Mute
// (mute.go, pure) and Store (store_sql.go, the only thing that touches a
// database). Service adds no lifecycle decision of its own — Apply is
// exactly OpenForHost, then Reconcile, then Execute, on the caller's own
// tx.
package alerts

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/rules"
)

// View is one alert as the list shows it: the row itself plus whether it
// is currently muted (decision 3's own reason View carries a Suppression
// at all — "the list must be able to label 'in maintenance'"). In v0.4a
// Suppression has no effect beyond this label; nothing is notified yet.
type View struct {
	Alert
	Suppression Suppression
}

// Detail is View plus the alert's own timeline (timeline.go's
// mergeTimeline) — what Get returns.
type Detail struct {
	Alert
	Suppression Suppression
	Timeline    []TimelineEntry
}

// Service composes Store, Reconcile and Mute. It carries a clock because,
// unlike Apply and Sweep — each driven by a `now` its own caller already
// has (the evaluation's own in.At, the ticker's own tick) — the operator
// actions have none of their own: Acknowledge and Snooze both stamp an
// instant nothing else supplies, Snooze compares `until` against it to
// reject the past, and Get needs it as the open end of a still-open
// alert's own timeline window.
type Service struct {
	store Store
	clock func() time.Time
}

// NewService wires store and clock. clock is a required positional
// argument, not an Option — the same ruling internal/items' checkKeys and
// internal/maintenance's GroupHosts each got (their own doc comments say
// why): every timestamp this Service stamps or compares against comes
// from here, never an ad hoc time.Now, so a test can pin it exactly.
func NewService(store Store, clock func() time.Time) *Service {
	return &Service{store: store, clock: clock}
}

// Apply is the lifecycle's whole composition for one host's evaluation:
// OpenForHost loads what is already open, Reconcile (pure) decides what
// must change, Execute carries it out — all on tx, the evaluator's own
// per-host transaction (store_sql.go's own doc comment on why). It adds
// no decision of its own, and does nothing at all when Reconcile returns
// no actions.
func (s *Service) Apply(ctx context.Context, tx pgx.Tx, in rules.AlertInput) error {
	open, err := s.store.OpenForHost(ctx, tx, in.OrgID, in.HostID)
	if err != nil {
		return fmt.Errorf("apply: load open alerts: %w", err)
	}
	actions := Reconcile(in, open)
	if len(actions) == 0 {
		return nil
	}
	if err := s.store.Execute(ctx, tx, in.OrgID, actions); err != nil {
		return fmt.Errorf("apply: execute actions: %w", err)
	}
	return nil
}

// Sweep closes every alert whose subject has stopped reporting altogether
// (design doc "The sweep"). Its signature matches
// internal/rules.AlertSink's own Sweep exactly — Store.Sweep's own count
// is for store_sql_test.go's own assertions, not for this sink interface,
// which has no use for it.
func (s *Service) Sweep(ctx context.Context, now time.Time) error {
	_, err := s.store.Sweep(ctx, now)
	return err
}

// suppression computes Mute's verdict for one alert, fetching the windows
// that cover its host right now — or, when cache is non-nil, reusing a
// prior fetch for the same host within this same call. Get passes a nil
// cache (exactly one alert, nothing to reuse); List passes one shared
// across every row.
//
// Review finding (Important) corrected an earlier version of this
// comment, which argued *against* caching on the grounds that "a window
// can start or end between two alerts' own lookups" — backwards: List
// pins `now` once (its own caller, below) and hands the identical instant
// to every Mute either way, so two alerts on the same host already get
// the same verdict as of the same instant whether or not the windows
// fetch is shared. Sharing it is strictly more consistent, not less, and
// turns what was an unbounded N+1 — one recursive CTE per alert, for
// every alert List returns — into one fetch per distinct host.
func (s *Service) suppression(ctx context.Context, orgID uuid.UUID, a Alert, now time.Time, cache map[uuid.UUID][]maintenance.Occurrence) (Suppression, error) {
	windows, ok := cache[a.Subject.HostID]
	if !ok {
		var err error
		windows, err = s.store.WindowsForHost(ctx, orgID, a.Subject.HostID, now)
		if err != nil {
			return Suppression{}, fmt.Errorf("windows for host %s: %w", a.Subject.HostID, err)
		}
		if cache != nil {
			cache[a.Subject.HostID] = windows
		}
	}
	return Mute(a, windows, now), nil
}

// List returns the organization's alerts matching f, each with its own
// Suppression. Muted has no SQL column to filter on (Filter's own doc
// comment) — it is applied here, after Mute has run for the row. now is
// pinned once, and the per-host windows cache is shared across every row
// (suppression's own doc comment) — one WindowsForHost call per distinct
// host among the results, not one per alert.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, f Filter) ([]View, error) {
	rows, err := s.store.ListAlerts(ctx, orgID, f)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	now := s.clock()
	cache := map[uuid.UUID][]maintenance.Occurrence{}
	out := make([]View, 0, len(rows))
	for _, a := range rows {
		sup, err := s.suppression(ctx, orgID, a, now, cache)
		if err != nil {
			return nil, err
		}
		if f.Muted != nil && *f.Muted != sup.Muted {
			continue
		}
		out = append(out, View{Alert: a, Suppression: sup})
	}
	return out, nil
}

// Get returns one alert with its Suppression and its timeline: the
// alert's own alert_events merged with the check_state_changes rows
// inside [OpenedAt, ClosedAt] (mergeTimeline, timeline.go) — or, for a
// still-open alert, [OpenedAt, now), since there is no ClosedAt yet to
// bound it. A host-scope alert (alert.go's own ScopeHost) never reaches
// StateChanges at all: check_host_states is a roll-up with no change log,
// so its timeline is its own events only, by construction rather than by
// a query that happens to match nothing.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Detail, error) {
	a, err := s.store.GetAlert(ctx, orgID, id)
	if err != nil {
		return Detail{}, err
	}
	now := s.clock()
	sup, err := s.suppression(ctx, orgID, a, now, nil)
	if err != nil {
		return Detail{}, err
	}
	events, err := s.store.Events(ctx, orgID, a.ID)
	if err != nil {
		return Detail{}, fmt.Errorf("get alert %s events: %w", id, err)
	}
	var changes []StateChange
	if a.Subject.Scope == ScopeTuple {
		to := now
		if a.ClosedAt != nil {
			to = *a.ClosedAt
		}
		changes, err = s.store.StateChanges(ctx, orgID, a.Subject, a.OpenedAt, to)
		if err != nil {
			return Detail{}, fmt.Errorf("get alert %s state changes: %w", id, err)
		}
	}
	return Detail{Alert: a, Suppression: sup, Timeline: mergeTimeline(events, changes)}, nil
}

// invalidf builds an ErrInvalidInput failure carrying a message an
// operator can act on — the same helper internal/items.Service's own
// invalidf is.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}

// Acknowledge records that actor knows about alert id. The row's own
// *current* severity is what gets stored (decision 2: "stored... which
// makes 'does the ack still cover this' a comparison rather than a second
// state to maintain" — migration 00049's own words) — Store.Acknowledge
// reads it itself, inside the same statement that writes it, rather than
// this method passing along what GetAlert saw a moment ago: that value is
// already stale the instant this function has it, and a concurrent
// severity rise in the gap would otherwise land as a silent, understated
// acknowledgement, exactly what decision 2 exists to prevent (review
// finding, Important). GetAlert is still needed here, for ErrNotFound and
// the closed check below — just not for the severity. It is not a mute:
// Suppression is untouched, only Mute decides that. A closed alert cannot
// be acknowledged — there is nothing left to escalate stopping an
// acknowledgement would prevent.
func (s *Service) Acknowledge(ctx context.Context, orgID, id, actor uuid.UUID, note string) error {
	a, err := s.store.GetAlert(ctx, orgID, id)
	if err != nil {
		return err
	}
	if a.ClosedAt != nil {
		return invalidf("cannot acknowledge a closed alert")
	}
	return s.store.Acknowledge(ctx, orgID, id, actor, note, s.clock())
}

// Unacknowledge clears alert id's acknowledgement. There is no
// 'unacknowledged' alert_events kind (migration 00049's own CHECK) —
// unacknowledging writes no event, the same way ActionFlapped's own latch
// writes none (store_sql.go's Execute: "the one action kind ... with no
// event to match").
func (s *Service) Unacknowledge(ctx context.Context, orgID, id, actor uuid.UUID) error {
	return s.store.Unacknowledge(ctx, orgID, id, actor, s.clock())
}

// Snooze mutes alert id until an instant or until its severity next
// changes — never both, and never neither (design doc "Suppression":
// snooze "hangs off one open alert and can end 'when the state
// changes'" — it is always one or the other; huma cannot express "exactly
// one of" at the transport layer, so this is where that rule has to live,
// review finding, Important: until == nil && !untilChange used to reach
// the store as a silent unsnooze, reported back as success with a
// 'snoozed' event in the audit trail for an alert that was not, in fact,
// snoozed) — and never an instant already in the past, which could never
// un-mute anything.
func (s *Service) Snooze(ctx context.Context, orgID, id, actor uuid.UUID, until *time.Time, untilChange bool) error {
	if until != nil && untilChange {
		return invalidf("until and untilChange are mutually exclusive")
	}
	if until == nil && !untilChange {
		return invalidf("snooze requires either until or untilChange")
	}
	now := s.clock()
	if until != nil && until.Before(now) {
		return invalidf("until is already in the past")
	}
	return s.store.Snooze(ctx, orgID, id, actor, until, untilChange, now)
}

// Unsnooze clears alert id's snooze, bounded or unbounded.
func (s *Service) Unsnooze(ctx context.Context, orgID, id, actor uuid.UUID) error {
	return s.store.Unsnooze(ctx, orgID, id, actor, s.clock())
}
