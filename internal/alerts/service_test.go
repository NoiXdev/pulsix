// Service's own tests run against fakeStore below, an in-memory
// alerts.Store — no database, the same split Reconcile (reconcile_test.go)
// and Mute (mute_test.go) already keep from their own executor. Each test
// is named after the one behaviour it exists to pin; see each test's own
// comment for which bug it would catch if that behaviour went missing.
package alerts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/rules"
)

// ctx is the bare, unscoped context every test below hands Service — none
// of these calls go through db.WithOrg or any other ctx-based org scope,
// since fakeStore reads its org argument directly like the real
// SQLStore's own statements do.
var ctx = context.Background()

// ackCall and snoozeCall record exactly the arguments fakeStore's own
// Acknowledge/Snooze were called with — not the alert they mutate — so a
// test can assert Service passed the right actor even if the fake's own
// bookkeeping happened to produce the right alert by accident. ackCall
// carries no severity: Service no longer passes one (review finding,
// Important — see fakeStore.Acknowledge's own comment for why).
type ackCall struct {
	id, actor uuid.UUID
	note      string
	at        time.Time
}

type snoozeCall struct {
	id, actor   uuid.UUID
	until       *time.Time
	untilChange bool
	at          time.Time
}

type changesCall struct {
	subject  alerts.Subject
	from, to time.Time
}

// fakeStore is an in-memory alerts.Store. Every read method serves
// exactly what a test seeded via its own maps; every write method both
// mutates that same state (so a Get after an Acknowledge sees the result)
// and appends to a *Calls slice (so a test can pin the exact arguments
// Service passed, not merely the fake's own resulting state).
type fakeStore struct {
	open       map[uuid.UUID][]alerts.Alert // hostID -> OpenForHost's own answer
	openErr    error
	executed   [][]alerts.Action // one slice per Execute call
	executeErr error
	sweptN     int
	sweepErr   error

	alerts  map[uuid.UUID]alerts.Alert
	getErr  error
	listErr error
	// hostNames is keyed by host id, mirroring the store's own join
	// (GetAlert/ListAlerts's SQL now names hosts.name) — absent from a
	// test's own setup reads as "", the same default a host without a
	// name could never actually have in Postgres but which lets most
	// tests here ignore HostName entirely.
	hostNames map[uuid.UUID]string

	events    map[uuid.UUID][]alerts.Event
	eventsErr error

	changes      map[alerts.Subject][]alerts.StateChange
	changesCalls []changesCall
	changesErr   error

	windows     map[uuid.UUID][]maintenance.Occurrence
	windowCalls []uuid.UUID // every hostID WindowsForHost was called with, in order — TestListSharesWindowsForHostAcrossAlertsOnTheSameHost's own evidence
	windowsErr  error

	ackCalls      []ackCall
	ackErr        error
	unackCalls    []uuid.UUID
	unackErr      error
	snoozeCalls   []snoozeCall
	snoozeErr     error
	unsnoozeCalls []uuid.UUID
	unsnoozeErr   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		open:      map[uuid.UUID][]alerts.Alert{},
		alerts:    map[uuid.UUID]alerts.Alert{},
		events:    map[uuid.UUID][]alerts.Event{},
		changes:   map[alerts.Subject][]alerts.StateChange{},
		windows:   map[uuid.UUID][]maintenance.Occurrence{},
		hostNames: map[uuid.UUID]string{},
	}
}

func (f *fakeStore) OpenForHost(_ context.Context, _ pgx.Tx, _, hostID uuid.UUID) ([]alerts.Alert, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.open[hostID], nil
}

func (f *fakeStore) Execute(_ context.Context, _ pgx.Tx, _ uuid.UUID, actions []alerts.Action) error {
	if f.executeErr != nil {
		return f.executeErr
	}
	f.executed = append(f.executed, actions)
	return nil
}

func (f *fakeStore) Sweep(_ context.Context, _ time.Time) (int, error) {
	return f.sweptN, f.sweepErr
}

func (f *fakeStore) GetAlert(_ context.Context, _, id uuid.UUID) (alerts.AlertWithHost, error) {
	if f.getErr != nil {
		return alerts.AlertWithHost{}, f.getErr
	}
	a, ok := f.alerts[id]
	if !ok {
		return alerts.AlertWithHost{}, alerts.ErrNotFound
	}
	return alerts.AlertWithHost{Alert: a, HostName: f.hostNames[a.Subject.HostID]}, nil
}

func (f *fakeStore) ListAlerts(_ context.Context, _ uuid.UUID, filter alerts.Filter) ([]alerts.AlertWithHost, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []alerts.AlertWithHost
	for _, a := range f.alerts {
		if filter.Open != nil && *filter.Open != (a.ClosedAt == nil) {
			continue
		}
		if filter.Severity != "" && filter.Severity != a.Severity {
			continue
		}
		if filter.HostID != nil && *filter.HostID != a.Subject.HostID {
			continue
		}
		if filter.Scope != "" && filter.Scope != a.Subject.Scope {
			continue
		}
		// Coverage, not mere presence (review finding, Important, carried
		// into the fake so it keeps agreeing with the real SQL's own fix):
		// an ack given at warn does not cover a later rise to crit.
		covered := a.AckAt != nil && rules.StateRank(a.Severity) <= rules.StateRank(a.AckSeverity)
		if filter.Acknowledged != nil && *filter.Acknowledged != covered {
			continue
		}
		out = append(out, alerts.AlertWithHost{Alert: a, HostName: f.hostNames[a.Subject.HostID]})
	}
	return out, nil
}

func (f *fakeStore) Events(_ context.Context, _, alertID uuid.UUID) ([]alerts.Event, error) {
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	return f.events[alertID], nil
}

func (f *fakeStore) StateChanges(_ context.Context, _ uuid.UUID, subject alerts.Subject, from, to time.Time) ([]alerts.StateChange, error) {
	f.changesCalls = append(f.changesCalls, changesCall{subject: subject, from: from, to: to})
	if f.changesErr != nil {
		return nil, f.changesErr
	}
	return f.changes[subject], nil
}

func (f *fakeStore) WindowsForHost(_ context.Context, _, hostID uuid.UUID, _ time.Time) ([]maintenance.Occurrence, error) {
	f.windowCalls = append(f.windowCalls, hostID)
	if f.windowsErr != nil {
		return nil, f.windowsErr
	}
	return f.windows[hostID], nil
}

// Acknowledge mirrors AcknowledgeAlert's own real-SQL shape after the
// review fix: acknowledged_severity comes from the row's own *current*
// Severity, read here — at the moment this runs, not from an argument
// Service passed in — the same thing `SET acknowledged_severity =
// severity` (unqualified, the pre-update value of the row) does atomically
// in Postgres. Service has no severity parameter to give this method
// anymore; a test that wants to prove the race is closed mutates
// f.alerts[id].Severity before calling Acknowledge and asserts the stored
// AckSeverity followed it (TestAcknowledgeStoresTheRowsLiveSeverity).
func (f *fakeStore) Acknowledge(_ context.Context, _, id, actor uuid.UUID, note string, at time.Time) error {
	if f.ackErr != nil {
		return f.ackErr
	}
	f.ackCalls = append(f.ackCalls, ackCall{id: id, actor: actor, note: note, at: at})
	a := f.alerts[id]
	a.AckAt, a.AckBy, a.AckSeverity, a.AckNote = &at, &actor, a.Severity, note
	f.alerts[id] = a
	return nil
}

func (f *fakeStore) Unacknowledge(_ context.Context, _, id, actor uuid.UUID, _ time.Time) error {
	if f.unackErr != nil {
		return f.unackErr
	}
	f.unackCalls = append(f.unackCalls, id)
	_ = actor
	a := f.alerts[id]
	a.AckAt, a.AckBy, a.AckSeverity, a.AckNote = nil, nil, "", ""
	f.alerts[id] = a
	return nil
}

func (f *fakeStore) Snooze(_ context.Context, _, id, actor uuid.UUID, until *time.Time, untilChange bool, at time.Time) error {
	if f.snoozeErr != nil {
		return f.snoozeErr
	}
	f.snoozeCalls = append(f.snoozeCalls, snoozeCall{id: id, actor: actor, until: until, untilChange: untilChange, at: at})
	a := f.alerts[id]
	a.SnoozedUntil, a.SnoozeUntilChange = until, untilChange
	f.alerts[id] = a
	return nil
}

func (f *fakeStore) Unsnooze(_ context.Context, _, id, actor uuid.UUID, _ time.Time) error {
	if f.unsnoozeErr != nil {
		return f.unsnoozeErr
	}
	f.unsnoozeCalls = append(f.unsnoozeCalls, id)
	_ = actor
	a := f.alerts[id]
	a.SnoozedUntil, a.SnoozeUntilChange = nil, false
	f.alerts[id] = a
	return nil
}

// clockAt returns a fixed clock — the shape every test below passes to
// NewService so Acknowledge/Snooze/Get stamp and compare against a known
// instant instead of the real time.Now.
func clockAt(now time.Time) func() time.Time { return func() time.Time { return now } }

func TestAcknowledgeRecordsActorAndSeverity(t *testing.T) {
	store := newFakeStore()
	// Severity is crit, deliberately not the zero value and not a value
	// AckSeverity could already carry by construction — a test that used
	// AckSeverity's own zero value here could not tell "the current
	// severity" apart from "nothing was passed at all".
	a := openAlert(tupleSubject("disk", "", "used"), rules.StateCrit, t0)
	store.alerts[a.ID] = a
	now := t0.Add(time.Hour)
	svc := alerts.NewService(store, clockAt(now))

	actor := uuid.Must(uuid.NewV7())
	if err := svc.Acknowledge(ctx, orgID, a.ID, actor, "on it"); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if len(store.ackCalls) != 1 {
		t.Fatalf("store.Acknowledge called %d times, want 1", len(store.ackCalls))
	}
	call := store.ackCalls[0]
	if call.actor != actor {
		t.Errorf("stored actor = %s, want %s", call.actor, actor)
	}
	if call.note != "on it" {
		t.Errorf("stored note = %q, want %q", call.note, "on it")
	}
	if !call.at.Equal(now) {
		t.Errorf("stored at = %v, want the service's own clock %v", call.at, now)
	}
	// AckSeverity on the stored row is the alert's own current severity —
	// Service no longer passes one at all (see fakeStore.Acknowledge's own
	// comment); this is the only place left that proves it landed.
	if got := store.alerts[a.ID].AckSeverity; got != rules.StateCrit {
		t.Errorf("alerts[id].AckSeverity = %q, want %q", got, rules.StateCrit)
	}
}

// TestAcknowledgeStoresTheRowsLiveSeverity is Finding 1's own regression
// test: it proves Acknowledge captures severity at the moment the store
// writes, not whatever Service's own GetAlert saw one round trip earlier.
// fakeStore.Acknowledge reads f.alerts[id].Severity itself (mirroring
// AcknowledgeAlert's `SET acknowledged_severity = severity`); mutating the
// stored alert's severity directly, bypassing Service entirely, is the
// closest a fake without real transactions can come to simulating "the
// evaluator raised this alert in the gap between GetAlert and the write"
// — an implementation that threaded Service's own earlier read through
// again would get this wrong.
func TestAcknowledgeStoresTheRowsLiveSeverity(t *testing.T) {
	store := newFakeStore()
	a := openAlert(tupleSubject("disk", "", "used"), rules.StateWarn, t0)
	store.alerts[a.ID] = a
	svc := alerts.NewService(store, clockAt(t0.Add(time.Hour)))

	// Simulate a concurrent severity rise landing between Service's own
	// GetAlert and the moment the store actually writes: mutate the row
	// out from under Acknowledge before calling it.
	raised := a
	raised.Severity = rules.StateCrit
	store.alerts[a.ID] = raised

	if err := svc.Acknowledge(ctx, orgID, a.ID, uuid.Must(uuid.NewV7()), "late"); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if got := store.alerts[a.ID].AckSeverity; got != rules.StateCrit {
		t.Fatalf("AckSeverity = %q, want %q (the row's own severity as of the write, not warn)", got, rules.StateCrit)
	}
}

func TestAcknowledgeRejectsAClosedAlert(t *testing.T) {
	store := newFakeStore()
	a := openAlert(tupleSubject("disk", "", "used"), rules.StateWarn, t0)
	closedAt := t0.Add(time.Hour)
	a.ClosedAt, a.ClosedReason = &closedAt, alerts.Resolved
	store.alerts[a.ID] = a
	svc := alerts.NewService(store, clockAt(t0.Add(2*time.Hour)))

	err := svc.Acknowledge(ctx, orgID, a.ID, uuid.Must(uuid.NewV7()), "too late")
	if !errors.Is(err, alerts.ErrInvalidInput) {
		t.Fatalf("Acknowledge on a closed alert: err = %v, want ErrInvalidInput", err)
	}
	if len(store.ackCalls) != 0 {
		t.Errorf("store.Acknowledge called %d times, want 0 — the rejection must happen before any write", len(store.ackCalls))
	}
}

func TestSnoozeRejectsBothUntilAndUntilChange(t *testing.T) {
	store := newFakeStore()
	svc := alerts.NewService(store, clockAt(t0))
	until := t0.Add(time.Hour)

	err := svc.Snooze(ctx, orgID, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), &until, true)
	if !errors.Is(err, alerts.ErrInvalidInput) {
		t.Fatalf("Snooze(until, untilChange=true): err = %v, want ErrInvalidInput", err)
	}
	if len(store.snoozeCalls) != 0 {
		t.Errorf("store.Snooze called %d times, want 0", len(store.snoozeCalls))
	}
}

func TestSnoozeRejectsAnUntilInThePast(t *testing.T) {
	store := newFakeStore()
	now := t0.Add(time.Hour)
	svc := alerts.NewService(store, clockAt(now))
	past := now.Add(-time.Minute)

	err := svc.Snooze(ctx, orgID, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), &past, false)
	if !errors.Is(err, alerts.ErrInvalidInput) {
		t.Fatalf("Snooze(until in the past): err = %v, want ErrInvalidInput", err)
	}
	if len(store.snoozeCalls) != 0 {
		t.Errorf("store.Snooze called %d times, want 0", len(store.snoozeCalls))
	}
}

// TestSnoozeRejectsNeitherUntilNorUntilChange is Finding 2's own
// regression test: Snooze(nil, false) used to reach the store as a silent
// unsnooze (SnoozeAlert sets both columns to their cleared values) while
// still writing a 'snoozed' event — the caller sees success and the audit
// trail claims a mute that never happened. huma cannot express "exactly
// one of" at the transport layer, so the rejection has to live here.
func TestSnoozeRejectsNeitherUntilNorUntilChange(t *testing.T) {
	store := newFakeStore()
	svc := alerts.NewService(store, clockAt(t0))

	err := svc.Snooze(ctx, orgID, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), nil, false)
	if !errors.Is(err, alerts.ErrInvalidInput) {
		t.Fatalf("Snooze(nil, false): err = %v, want ErrInvalidInput", err)
	}
	if len(store.snoozeCalls) != 0 {
		t.Errorf("store.Snooze called %d times, want 0", len(store.snoozeCalls))
	}
}

// TestSnoozeAcceptsAFutureUntil is the positive counterpart to the
// rejection tests above: without it, an implementation that rejected
// every Snooze call unconditionally would also pass them.
func TestSnoozeAcceptsAFutureUntil(t *testing.T) {
	store := newFakeStore()
	a := openAlert(tupleSubject("cpu", "", "used"), rules.StateWarn, t0)
	store.alerts[a.ID] = a
	now := t0.Add(time.Hour)
	svc := alerts.NewService(store, clockAt(now))
	until := now.Add(30 * time.Minute)
	actor := uuid.Must(uuid.NewV7())

	if err := svc.Snooze(ctx, orgID, a.ID, actor, &until, false); err != nil {
		t.Fatalf("Snooze: %v", err)
	}
	if len(store.snoozeCalls) != 1 {
		t.Fatalf("store.Snooze called %d times, want 1", len(store.snoozeCalls))
	}
	call := store.snoozeCalls[0]
	if call.until == nil || !call.until.Equal(until) {
		t.Errorf("stored until = %v, want %v", call.until, until)
	}
	if call.untilChange {
		t.Error("stored untilChange = true, want false")
	}
}

// TestGetInterleavesEventsAndStateChanges is the merge's own test: an
// 'ack_voided' event and the severity_raised state change that caused it
// share the exact same instant (raiseSeverity, store_sql.go, stamps both
// with the same action's At) — if mergeTimeline let the event sort first
// on a tie, this timeline would read "acknowledged, then went critical"
// when the truth is the reverse.
func TestGetInterleavesEventsAndStateChanges(t *testing.T) {
	store := newFakeStore()
	sub := tupleSubject("disk", "", "used")
	a := openAlert(sub, rules.StateCrit, t0)
	closedAt := t0.Add(2 * time.Hour)
	a.ClosedAt, a.ClosedReason = &closedAt, alerts.Resolved
	store.alerts[a.ID] = a

	tie := t0.Add(90 * time.Minute)
	store.events[a.ID] = []alerts.Event{
		{ID: uuid.Must(uuid.NewV7()), At: t0, Kind: "opened"},
		{ID: uuid.Must(uuid.NewV7()), At: tie, Kind: "ack_voided"},
	}
	store.changes[sub] = []alerts.StateChange{
		{At: tie, FromState: rules.StateWarn, ToState: rules.StateCrit},
	}

	svc := alerts.NewService(store, clockAt(closedAt.Add(time.Hour)))
	detail, err := svc.Get(ctx, orgID, a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detail.Timeline) != 3 {
		t.Fatalf("len(Timeline) = %d, want 3: %+v", len(detail.Timeline), detail.Timeline)
	}
	if detail.Timeline[0].Kind != alerts.TimelineEvent || !detail.Timeline[0].At.Equal(t0) {
		t.Errorf("Timeline[0] = %+v, want the opened event at t0", detail.Timeline[0])
	}
	if detail.Timeline[1].Kind != alerts.TimelineStateChange || !detail.Timeline[1].At.Equal(tie) {
		t.Errorf("Timeline[1] = %+v, want the state change at the tied instant (it must sort before the event it caused)", detail.Timeline[1])
	}
	if detail.Timeline[2].Kind != alerts.TimelineEvent || !detail.Timeline[2].At.Equal(tie) {
		t.Errorf("Timeline[2] = %+v, want the ack_voided event at the tied instant, after the state change", detail.Timeline[2])
	}

	// StateChanges must have been asked for exactly [OpenedAt, ClosedAt].
	if len(store.changesCalls) != 1 {
		t.Fatalf("StateChanges called %d times, want 1", len(store.changesCalls))
	}
	if got := store.changesCalls[0]; !got.from.Equal(a.OpenedAt) || !got.to.Equal(closedAt) {
		t.Errorf("StateChanges called with [%v, %v], want [%v, %v]", got.from, got.to, a.OpenedAt, closedAt)
	}
}

// TestGetOnAHostAlertReturnsEventsOnly is the brief's own named case: a
// scope='host' alert has no check_state_changes history by construction
// (check_host_states is a roll-up with no change log), so Get must return
// its own events, unchanged, and must not error — and must not even ask
// the store for state changes, rather than asking and happening to get
// none back.
func TestGetOnAHostAlertReturnsEventsOnly(t *testing.T) {
	store := newFakeStore()
	a := openAlert(hostSubject(), rules.StateUnknown, t0)
	store.alerts[a.ID] = a
	store.events[a.ID] = []alerts.Event{{ID: uuid.Must(uuid.NewV7()), At: t0, Kind: "opened"}}

	svc := alerts.NewService(store, clockAt(t0.Add(time.Hour)))
	detail, err := svc.Get(ctx, orgID, a.ID)
	if err != nil {
		t.Fatalf("Get on a host-scope alert: %v", err)
	}
	if len(detail.Timeline) != 1 || detail.Timeline[0].Kind != alerts.TimelineEvent {
		t.Fatalf("Timeline = %+v, want exactly the one event", detail.Timeline)
	}
	if len(store.changesCalls) != 0 {
		t.Errorf("StateChanges called %d times, want 0 — a host-scope alert must never ask for tuple state changes", len(store.changesCalls))
	}
}

// TestApplyComposesOpenReconcileExecute pins Apply's own job description:
// load what's open, hand it to Reconcile, execute whatever comes back —
// and nothing at all when Reconcile has no actions, so a quiet tick does
// not touch the store a second time for no reason.
func TestApplyComposesOpenReconcileExecute(t *testing.T) {
	store := newFakeStore()
	svc := alerts.NewService(store, clockAt(t0))

	in := input(stateAt(tupleSubject("cpu", "", "used"), rules.StateWarn, t0))
	if err := svc.Apply(ctx, nil, in); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(store.executed) != 1 {
		t.Fatalf("Execute called %d times, want 1", len(store.executed))
	}
	acts := store.executed[0]
	if len(acts) != 1 || acts[0].Kind != alerts.ActionOpen {
		t.Fatalf("executed actions = %+v, want exactly one ActionOpen", acts)
	}

	// A quiet tick (no states, nothing open) decides nothing — Execute
	// must not be called a second time.
	if err := svc.Apply(ctx, nil, input()); err != nil {
		t.Fatalf("Apply (quiet tick): %v", err)
	}
	if len(store.executed) != 1 {
		t.Errorf("Execute called %d times after a quiet tick, want still 1 (no-op actions must not reach the store)", len(store.executed))
	}
}

func TestSweepPropagatesStoreError(t *testing.T) {
	store := newFakeStore()
	store.sweepErr = errors.New("boom")
	svc := alerts.NewService(store, clockAt(t0))

	if err := svc.Sweep(ctx, t0); !errors.Is(err, store.sweepErr) {
		t.Fatalf("Sweep: err = %v, want %v", err, store.sweepErr)
	}
}

func TestUnacknowledgeClearsTheAcknowledgement(t *testing.T) {
	store := newFakeStore()
	a := openAlert(tupleSubject("cpu", "", "used"), rules.StateWarn, t0)
	ackAt, ackBy := t0, uuid.Must(uuid.NewV7())
	a.AckAt, a.AckBy, a.AckSeverity = &ackAt, &ackBy, rules.StateWarn
	store.alerts[a.ID] = a
	svc := alerts.NewService(store, clockAt(t0.Add(time.Hour)))

	actor := uuid.Must(uuid.NewV7())
	if err := svc.Unacknowledge(ctx, orgID, a.ID, actor); err != nil {
		t.Fatalf("Unacknowledge: %v", err)
	}
	if len(store.unackCalls) != 1 || store.unackCalls[0] != a.ID {
		t.Fatalf("store.Unacknowledge calls = %v, want exactly [%s]", store.unackCalls, a.ID)
	}
	if got := store.alerts[a.ID].AckSeverity; got != "" {
		t.Errorf("AckSeverity after Unacknowledge = %q, want \"\"", got)
	}
}

func TestUnsnoozeClearsTheSnooze(t *testing.T) {
	store := newFakeStore()
	a := openAlert(tupleSubject("cpu", "", "used"), rules.StateWarn, t0)
	a.SnoozeUntilChange = true
	store.alerts[a.ID] = a
	svc := alerts.NewService(store, clockAt(t0.Add(time.Hour)))

	actor := uuid.Must(uuid.NewV7())
	if err := svc.Unsnooze(ctx, orgID, a.ID, actor); err != nil {
		t.Fatalf("Unsnooze: %v", err)
	}
	if len(store.unsnoozeCalls) != 1 || store.unsnoozeCalls[0] != a.ID {
		t.Fatalf("store.Unsnooze calls = %v, want exactly [%s]", store.unsnoozeCalls, a.ID)
	}
	if got := store.alerts[a.ID]; got.SnoozeUntilChange {
		t.Error("SnoozeUntilChange after Unsnooze = true, want false")
	}
}

// TestListLabelsMutedAlertsAndFilters pins View's own reason for being:
// the list must label "in maintenance" without a second round trip, and
// Filter.Muted (no SQL column for it) must narrow what List returns.
func TestListLabelsMutedAlertsAndFilters(t *testing.T) {
	store := newFakeStore()
	quietHost, mutedHost := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	quiet := openAlert(alerts.Subject{HostID: quietHost, Scope: alerts.ScopeTuple, CheckKey: "cpu", Metric: "used"}, rules.StateWarn, t0)
	muted := openAlert(alerts.Subject{HostID: mutedHost, Scope: alerts.ScopeTuple, CheckKey: "cpu", Metric: "used"}, rules.StateWarn, t0)
	store.alerts[quiet.ID] = quiet
	store.alerts[muted.ID] = muted

	now := t0.Add(time.Hour)
	windowID := uuid.Must(uuid.NewV7())
	store.windows[mutedHost] = []maintenance.Occurrence{
		{WindowID: windowID, Reason: maintenance.ReasonMaintenance, Start: t0, End: now.Add(time.Hour)},
	}

	svc := alerts.NewService(store, clockAt(now))

	all, err := svc.List(ctx, orgID, alerts.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List (unfiltered) returned %d views, want 2", len(all))
	}
	for _, v := range all {
		wantMuted := v.Subject.HostID == mutedHost
		if v.Suppression.Muted != wantMuted {
			t.Errorf("view for host %s: Suppression.Muted = %v, want %v", v.Subject.HostID, v.Suppression.Muted, wantMuted)
		}
	}

	mutedOnly := true
	filtered, err := svc.List(ctx, orgID, alerts.Filter{Muted: &mutedOnly})
	if err != nil {
		t.Fatalf("List (muted only): %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != muted.ID {
		t.Fatalf("List (muted only) = %+v, want exactly the alert on the muted host", filtered)
	}
}

// TestListSharesWindowsForHostAcrossAlertsOnTheSameHost is Finding 3's own
// regression test: List used to call WindowsForHost once per alert, so an
// organization with 200 open alerts on 50 hosts ran 200 recursive CTEs
// instead of 50. Two alerts on the identical host must produce exactly
// one WindowsForHost call for that host, not two.
func TestListSharesWindowsForHostAcrossAlertsOnTheSameHost(t *testing.T) {
	store := newFakeStore()
	host := uuid.Must(uuid.NewV7())
	a := openAlert(alerts.Subject{HostID: host, Scope: alerts.ScopeTuple, CheckKey: "cpu", Metric: "used"}, rules.StateWarn, t0)
	b := openAlert(alerts.Subject{HostID: host, Scope: alerts.ScopeTuple, CheckKey: "disk", Metric: "used"}, rules.StateWarn, t0)
	store.alerts[a.ID] = a
	store.alerts[b.ID] = b
	svc := alerts.NewService(store, clockAt(t0.Add(time.Hour)))

	views, err := svc.List(ctx, orgID, alerts.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("List returned %d views, want 2", len(views))
	}
	if len(store.windowCalls) != 1 {
		t.Fatalf("WindowsForHost called %d times, want 1 for two alerts sharing one host: %v", len(store.windowCalls), store.windowCalls)
	}
	if store.windowCalls[0] != host {
		t.Errorf("WindowsForHost called with host %s, want %s", store.windowCalls[0], host)
	}
}
