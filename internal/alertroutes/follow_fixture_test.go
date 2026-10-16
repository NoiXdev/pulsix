// The fixture every follower test runs on: one organization, one host,
// one channel and a Follower wired to them, against a real database. The
// whole point of the follower is a tick that writes the ledger and the
// outbox in ONE transaction, and neither half of that claim can be proved
// with fakes — so the enqueuer is the only fake here (it stands in for
// notify.Outbox, so a test can make the enqueue fail on demand), while
// the ledger, the routes, the rotations, the alerts, the maintenance
// windows and the advisory lock are all real.
//
// Split out of follow_test.go once that file passed the ~400-line signal:
// the fixture is shared by follow_test.go and follow_window_test.go and
// belongs to neither.
package alertroutes_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/oncall"
	"github.com/noixdev/pulsix/internal/rules"
)

type recordingEnqueuer struct {
	mu    sync.Mutex
	calls []struct {
		key      string
		channels []uuid.UUID
		count    int
		items    int
	}
	fail error
}

func (r *recordingEnqueuer) Enqueue(_ context.Context, _ pgx.Tx, _ uuid.UUID, key string,
	ev notify.Event, channels []uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.calls = append(r.calls, struct {
		key      string
		channels []uuid.UUID
		count    int
		items    int
	}{key, channels, ev.Alerts.Count, len(ev.Alerts.Items)})
	return nil
}

// fixedClock is the follower's injected clock. It never moves on its own:
// every test below pins one instant and compares maintenance windows
// against it, so a clock that advanced by itself would make the
// suppression tests depend on how long the database took. set is how a
// test moves time deliberately — the delivery-window tests
// (follow_window_test.go) hold an alert at 03:00 and then deliver it at
// 09:30 without waiting six hours.
//
// The mutex is not decoration: TestRunTicksEveryOrganizationUntilCancelled
// reads this clock from Run's own goroutine while the test body holds the
// other end.
type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fixedClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// advance moves the clock forward by d — the escalation tests' own way of
// saying "five minutes later" without sleeping, the same discipline
// set already gives the window tests their absolute instants with.
func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// logBuffer is the sink behind the follower's injected logger, so a test
// can assert on what the tick said — the only way to pin decision 9's
// "an unreadable window must be LOUD". Mutex-guarded for the same reason
// fixedClock's is.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// noGroups is the GroupReader every test here wires: the fixture's host
// belongs to no monitoring group, which is all an org-scope route needs.
// MatchRoutes' own doc comment is where the "every member, unmanaged
// included" ruling lives; this fixture simply has no groups to resolve.
type noGroups struct{}

func (noGroups) GroupsForHost(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// followEnv is one organization with one host and one channel, plus the
// follower under test. Every helper writes through the pool, never
// through the follower's own transaction, so a test's fixture is
// committed before Tick ever opens one.
type followEnv struct {
	t     *testing.T
	d     *db.DB
	clock *fixedClock

	org, host, channel uuid.UUID
	actorID            uuid.UUID // lazily set by actor()

	store      *alertroutes.Store
	rotations  *oncall.Store
	alertStore *alerts.SQLStore
	enq        *recordingEnqueuer
	log        *logBuffer
	follower   *alertroutes.Follower

	seq int
}

func newFollowEnv(t *testing.T) *followEnv {
	t.Helper()
	d := testdb.Open(t)
	org := seedOrg(t, d)
	env := &followEnv{
		t: t, d: d,
		clock:      &fixedClock{t: time.Now().UTC().Truncate(time.Second)},
		org:        org,
		host:       seedHost(t, d, org),
		channel:    seedChannel(t, d, org),
		store:      alertroutes.NewSQLStore(d),
		rotations:  oncall.NewSQLStore(d),
		alertStore: alerts.NewSQLStore(d),
		enq:        &recordingEnqueuer{},
		log:        &logBuffer{},
	}
	// Debug level, because some of what the follower must say is said
	// there: a route dropped by its own shut delivery window is ordinary
	// operation, not a fault, and a handler at the default info level
	// would silently discard the line a test is asserting on.
	env.follower = alertroutes.NewFollower(d, env.store, noGroups{}, env.rotations, env.enq,
		"https://pulsix.example", env.clock.Now,
		slog.New(slog.NewTextHandler(env.log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return env
}

// route creates one enabled route with the given selections.
func (e *followEnv) route(scope alertroutes.Scope, scopeID *uuid.UUID, severities []rules.State,
	events []alerts.EventKind, channels []uuid.UUID) uuid.UUID {
	e.t.Helper()
	return e.createRoute(alertroutes.Input{
		Enabled: true, Scope: scope, ScopeID: scopeID,
		Severities: severities, Events: events, Channels: channels,
	})
}

// createRoute is every route helper's one path into the store: it names
// the route (the only field no caller cares about) and reports its id.
func (e *followEnv) createRoute(in alertroutes.Input) uuid.UUID {
	e.t.Helper()
	e.seq++
	in.Name = fmt.Sprintf("route-%d", e.seq)
	r, err := e.store.Create(context.Background(), e.org, uuid.Must(uuid.NewV7()), in, e.clock.Now())
	if err != nil {
		e.t.Fatalf("create route: %v", err)
	}
	return r.ID
}

// openCritAlert opens one crit alert on the fixture's host and appends its
// 'opened' event — the pair the forward pass reads. Each call uses its own
// metric, because alerts_one_open_per_subject (migration 00049) permits
// exactly one open alert per subject.
func (e *followEnv) openCritAlert() uuid.UUID {
	e.t.Helper()
	return e.openAlert(rules.StateCrit)
}

// openAlert is openCritAlert generalised to any severity — the handful of
// escalation tests that need an alert to START inside a narrower route
// selection (warn-only, say) rather than crit, so a later rise out of it
// and fall back has somewhere to start from.
func (e *followEnv) openAlert(severity rules.State) uuid.UUID {
	e.t.Helper()
	e.seq++
	ctx := context.Background()
	var alertID uuid.UUID
	// severity_changed_at starts equal to opened_at — the fixture's own
	// clock, not real wall-clock now(): escalatePass folds this column
	// into LastUrgentAt (migration 00058's review, Critical 2), and a real
	// now() here would float free of every other timestamp these tests
	// drive through the fixture's injected clock.
	err := e.d.Pool().QueryRow(ctx, `
		INSERT INTO alerts (org_id, host_id, scope, check_key, item, metric, opened_at, severity, peak_severity, severity_changed_at)
		VALUES ($1, $2, 'tuple', 'cpu', '', $3, $4, $5, $5, $4)
		RETURNING id`, e.org, e.host, fmt.Sprintf("used_percent_%d", e.seq), e.clock.Now(), string(severity)).Scan(&alertID)
	if err != nil {
		e.t.Fatalf("open alert: %v", err)
	}
	if _, err := e.d.Pool().Exec(ctx, `
		INSERT INTO alert_events (org_id, alert_id, at, kind, payload)
		VALUES ($1, $2, $3, 'opened', '{}'::jsonb)`, e.org, alertID, e.clock.Now()); err != nil {
		e.t.Fatalf("open alert event: %v", err)
	}
	return alertID
}

// maintenanceWindow puts the fixture's host into a one-off maintenance
// window covering [start, end).
func (e *followEnv) maintenanceWindow(host uuid.UUID, start, end time.Time) uuid.UUID {
	e.t.Helper()
	return e.windowWithReason(host, start, end, "maintenance")
}

// silenceWindow is maintenanceWindow's own shape with reason 'silence' —
// alerts.Mute's second mute reason (mute.go's own MuteSilence), expressed
// as the identical maintenance.Window/Occurrence machinery with a
// different Reason column (that file's own doc comment explains why both
// share one mechanism). Needed to prove the escalation pass pauses for
// ALL three of alerts.Mute's reasons, not only maintenance and snooze.
func (e *followEnv) silenceWindow(host uuid.UUID, start, end time.Time) uuid.UUID {
	e.t.Helper()
	return e.windowWithReason(host, start, end, "silence")
}

func (e *followEnv) windowWithReason(host uuid.UUID, start, end time.Time, reason string) uuid.UUID {
	e.t.Helper()
	e.seq++
	id := uuid.Must(uuid.NewV7())
	if _, err := e.d.Pool().Exec(context.Background(), `
		INSERT INTO maintenance_windows (id, org_id, name, target_kind, host_id, starts_at, ends_at, reason)
		VALUES ($1, $2, $3, 'host', $4, $5, $6, $7)`,
		id, e.org, fmt.Sprintf("window-%d", e.seq), host, start, end, reason); err != nil {
		e.t.Fatalf("create %s window: %v", reason, err)
	}
	return id
}

// endWindow moves a window's end into the past, so the fixed clock is no
// longer inside it — the operator ending maintenance early, and the only
// way to leave a window with a clock that does not move.
func (e *followEnv) endWindow(window uuid.UUID) {
	e.t.Helper()
	if _, err := e.d.Pool().Exec(context.Background(),
		`UPDATE maintenance_windows SET ends_at = $2 WHERE id = $1`,
		window, e.clock.Now().Add(-time.Minute)); err != nil {
		e.t.Fatalf("end window: %v", err)
	}
}

// ledgerState reads the decision recorded for the organization's oldest
// event, or "" when the follower has decided nothing at all — which is
// what a rolled-back tick must leave behind.
func (e *followEnv) ledgerState() alertroutes.LedgerState {
	e.t.Helper()
	var state string
	err := e.d.Pool().QueryRow(context.Background(), `
		SELECT s.state FROM alert_notify_state s
		JOIN alert_events ae ON ae.id = s.alert_event_id
		WHERE s.org_id = $1 ORDER BY ae.at, ae.id LIMIT 1`, e.org).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	if err != nil {
		e.t.Fatalf("read ledger state: %v", err)
	}
	return alertroutes.LedgerState(state)
}

// count is how many messages the enqueuer has taken so far, read under
// its own lock — Run's goroutine appends to the same slice while the test
// is watching it.
func (r *recordingEnqueuer) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// deliveries counts the rows the real outbox wrote for the fixture's
// channel — the end of the line, not the follower's own bookkeeping.
func (e *followEnv) deliveries() int {
	e.t.Helper()
	var n int
	if err := e.d.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM notification_deliveries WHERE org_id = $1 AND channel_id = $2`,
		e.org, e.channel).Scan(&n); err != nil {
		e.t.Fatalf("count deliveries: %v", err)
	}
	return n
}

// routeWithWindow creates one enabled route carrying a delivery window
// and, optionally, the severities that cross it when it is closed.
func (e *followEnv) routeWithWindow(scope alertroutes.Scope, scopeID *uuid.UUID, severities []rules.State,
	events []alerts.EventKind, channels []uuid.UUID, window *alertroutes.Window,
	breakthrough []rules.State) uuid.UUID {
	e.t.Helper()
	return e.createRoute(alertroutes.Input{
		Enabled: true, Scope: scope, ScopeID: scopeID,
		Severities: severities, Events: events, Channels: channels,
		Window: window, Breakthrough: breakthrough,
	})
}

// routeWithRotation creates one enabled route whose channel comes from a
// rotation. It still carries the fixture's own channel as its fixed list —
// Validate requires one, and more usefully it makes the rotation's answer
// provable: a test that sees a DIFFERENT channel delivered has watched the
// rotation override the list rather than merely agree with it.
func (e *followEnv) routeWithRotation(scope alertroutes.Scope, scopeID *uuid.UUID, severities []rules.State,
	events []alerts.EventKind, rotationID uuid.UUID, window *alertroutes.Window,
	breakthrough []rules.State) uuid.UUID {
	e.t.Helper()
	return e.createRoute(alertroutes.Input{
		Enabled: true, Scope: scope, ScopeID: scopeID,
		Severities: severities, Events: events, Channels: []uuid.UUID{e.channel},
		Window: window, Breakthrough: breakthrough, RotationID: &rotationID,
	})
}

// secondChannel adds another notification channel to the fixture's
// organization, for the tests that have to tell two of them apart.
func (e *followEnv) secondChannel() uuid.UUID {
	e.t.Helper()
	return seedChannel(e.t, e.d, e.org)
}

// secondHost adds another host to the fixture's organization, in its own
// zone — seedHost (ledger_sql_test.go) hardcodes a single zone/host slug
// pair, so a second call in the same org collides on both; the scope test
// (TestARouteNarrowedToAnotherHostStopsEscalating) needs a host distinct
// from the fixture's own to narrow a route onto.
func (e *followEnv) secondHost() uuid.UUID {
	e.t.Helper()
	e.seq++
	ctx := context.Background()
	q := gen.New(e.d.Pool())
	slug := fmt.Sprintf("z-%d", e.seq)
	z, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: e.org, Slug: slug, Name: slug})
	if err != nil {
		e.t.Fatalf("create second zone: %v", err)
	}
	h, err := q.CreateHost(ctx, gen.CreateHostParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: e.org, ZoneID: z.ID, Name: fmt.Sprintf("h-%d", e.seq), Facts: []byte(`{}`),
	})
	if err != nil {
		e.t.Fatalf("create second host: %v", err)
	}
	return h.ID
}

// rotation creates an on-call rotation cycling channels every periodDays,
// first handover at anchor.
func (e *followEnv) rotation(periodDays int, anchor time.Time, timezone string, channels []uuid.UUID) uuid.UUID {
	e.t.Helper()
	e.seq++
	r, err := e.rotations.Create(context.Background(), e.org, uuid.Must(uuid.NewV7()), oncall.Input{
		Name: "rotation-" + uuid.Must(uuid.NewV7()).String(), PeriodDays: periodDays,
		Anchor: anchor, Timezone: timezone, Channels: channels,
	}, e.clock.Now())
	if err != nil {
		e.t.Fatalf("create rotation: %v", err)
	}
	return r.ID
}

// breakWindowTimezone writes a timezone no zoneinfo knows straight onto
// the route's row, past Validate. That is not a contrived state: a hand
// edited row, a restore from an export, or a zone a future tzdata drops
// all reach it, and the database has no CHECK that could catch it.
func (e *followEnv) breakWindowTimezone(route uuid.UUID, timezone string) {
	e.t.Helper()
	if _, err := e.d.Pool().Exec(context.Background(),
		`UPDATE alert_routes SET window_timezone = $2 WHERE id = $1`, route, timezone); err != nil {
		e.t.Fatalf("break the window timezone: %v", err)
	}
}

// logs is everything the follower has said so far.
func (e *followEnv) logs() string { return e.log.String() }

// businessHours is the window every test below reuses: deliver from 09:00
// for nine hours, so 03:00 is unambiguously outside it and 09:30 inside.
func businessHours() *alertroutes.Window {
	return &alertroutes.Window{Cron: "0 9 * * *", Duration: 9 * time.Hour, Timezone: "UTC"}
}

// The escalation chain tests' own fixture additions (v0.4d,
// follow_escalate_test.go): the fixture above was built for the forward
// pass alone, and these are exactly the handful of things the second pass
// needs on top of it, in the same direct-SQL style seedHost/seedChannel
// and openCritAlert already use for test setup that does not need to go
// through a Service.

// routeWithSteps creates one enabled, org-scope route matching every
// severity and every event kind, delivering position 0 to the fixture's
// own channel, with the given ladder attached. Every escalation test wants
// the identical route shape — one that always matches — so the clock and
// the alert's own state are what decide whether a step fires, never a
// selection this helper would otherwise have to vary per test.
func (e *followEnv) routeWithSteps(steps []alertroutes.Step) uuid.UUID {
	e.t.Helper()
	return e.routeWithStepsForSeverities([]rules.State{rules.StateCrit, rules.StateWarn, rules.StateUnknown}, steps)
}

// routeWithStepsForSeverities is routeWithSteps with an explicit severity
// selection instead of "every severity" — the severity-pause tests need a
// NARROWER set (warn-only, say) so a rise out of it and a fall back into
// it are both observable.
func (e *followEnv) routeWithStepsForSeverities(severities []rules.State, steps []alertroutes.Step) uuid.UUID {
	e.t.Helper()
	return e.createRoute(alertroutes.Input{
		Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities: severities,
		// alerts.EventKinds() minus EventEscalated: a route can never
		// select 'escalated' as a trigger (TestEventKindsMatchTheSchema's
		// own doc comment, internal/alerts/alert_test.go, explains why —
		// it would race escalatePass's own delivery of the same event
		// through the forward pass too), and Validate now rejects it
		// outright, so listing it here would fail route creation, not
		// subscribe this fixture's route to an event that does not exist.
		Events:   []alerts.EventKind{alerts.EventOpened, alerts.EventSeverityRaised, alerts.EventAcknowledged, alerts.EventAckVoided, alerts.EventSnoozed, alerts.EventUnsnoozed, alerts.EventClosed},
		Channels: []uuid.UUID{e.channel},
		Steps:    steps,
	})
}

// setRouteEnabled toggles route's own enabled flag and moves its
// updated_at to the fixture's current clock — straight SQL, not
// Store.Update, since the escalation pause/resume tests only need the one
// column (and the timestamp move that any edit carries with it), not a
// full Input round trip through a stale revision. route_updated_at is
// exactly the anchor escalate_sql.go now folds into LastUrgentAt
// (migration 00058's review, Critical 3) for precisely this case.
func (e *followEnv) setRouteEnabled(route uuid.UUID, enabled bool) {
	e.t.Helper()
	if _, err := e.d.Pool().Exec(context.Background(),
		`UPDATE alert_routes SET enabled = $2, updated_at = $3 WHERE id = $1`,
		route, enabled, e.clock.Now()); err != nil {
		e.t.Fatalf("set route enabled=%v: %v", enabled, err)
	}
}

// narrowRouteToHost re-points a host-scope route at a different host,
// straight SQL again — the escalation-pause scope test (review, Important
// 5) needs a route whose scope no longer covers the alert's own host
// without touching anything else about it.
func (e *followEnv) narrowRouteToHost(route, host uuid.UUID) {
	e.t.Helper()
	if _, err := e.d.Pool().Exec(context.Background(),
		`UPDATE alert_routes SET scope_host_id = $2, updated_at = $3 WHERE id = $1`,
		route, host, e.clock.Now()); err != nil {
		e.t.Fatalf("narrow route to host %s: %v", host, err)
	}
}

// ledgerRow reads one (alert, route, position) row of alert_escalations —
// the chain's own ledger, never the forward pass's alert_notify_state —
// reporting ok == false for a step nobody has claimed yet.
func (e *followEnv) ledgerRow(alert, route uuid.UUID, position int) (time.Time, bool) {
	e.t.Helper()
	var firedAt time.Time
	err := e.d.Pool().QueryRow(context.Background(), `
		SELECT fired_at FROM alert_escalations
		WHERE org_id = $1 AND alert_id = $2 AND route_id = $3 AND position = $4`,
		e.org, alert, route, position).Scan(&firedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false
	}
	if err != nil {
		e.t.Fatalf("read escalation ledger row: %v", err)
	}
	return firedAt, true
}

// escalatedEvent reads alertID's own 'escalated' alert_events row for
// position, decoding its payload — decision 7's own shape, {position,
// channels} (RecordEscalation, escalate_sql.go) — so a test can assert the
// timeline entry exists and names the right channels, not merely that a
// step was claimed or a message sent. ok is false when no such event
// exists at all.
func (e *followEnv) escalatedEvent(alert uuid.UUID, position int) (channels []uuid.UUID, ok bool) {
	e.t.Helper()
	rows, err := e.d.Pool().Query(context.Background(), `
		SELECT payload FROM alert_events
		WHERE org_id = $1 AND alert_id = $2 AND kind = 'escalated'`, e.org, alert)
	if err != nil {
		e.t.Fatalf("read escalated events: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			e.t.Fatalf("scan escalated event: %v", err)
		}
		var p struct {
			Position int         `json:"position"`
			Channels []uuid.UUID `json:"channels"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			e.t.Fatalf("decode escalated payload: %v", err)
		}
		if p.Position == position {
			return p.Channels, true
		}
	}
	return nil, false
}

// deliveriesTo counts the notification_deliveries rows an escalation step
// produced — deliverEscalation's own key, alert.escalation:<alert>:
// <route>:<position> (follow_escalate.go), is content-derived and never
// built from the clock, so a route+position pair identifies its own rows
// regardless of which alert sits in the middle of the key; the fixture's
// tests run one alert at a time, so that wildcard never collides with
// anything else the test wrote.
func (e *followEnv) deliveriesTo(route uuid.UUID, position int) int {
	e.t.Helper()
	// Counted off the fixture's own fake enqueuer, not notification_
	// deliveries: this fixture's enq stands in for notify.Outbox (this
	// file's own doc comment says so) precisely so a test never needs a
	// real send to pass, and nothing here writes to that table at all.
	// deliverEscalation's own key (follow_escalate.go) is
	// "alert.escalation:<alert>:<route>:<position>" — content-derived,
	// never the clock — so matching its ":<route>:<position>" suffix
	// identifies every call for this rung regardless of which alert sits
	// in the middle; the fixture's tests run one alert at a time, so that
	// wildcard never collides with anything else a test wrote.
	suffix := ":" + route.String() + ":" + strconv.Itoa(position)
	e.enq.mu.Lock()
	defer e.enq.mu.Unlock()
	n := 0
	for _, c := range e.enq.calls {
		if strings.HasSuffix(c.key, suffix) {
			n++
		}
	}
	return n
}

// actor lazily creates and caches the one user every acknowledge/snooze
// helper below needs: alert_events.actor_id and alerts.acknowledged_by
// both carry a plain FK to users(id) (migration 00049), so Acknowledge and
// Snooze need a real row to point at, the same reason internal/alerts' own
// store_sql_test.go seeds one.
func (e *followEnv) actor() uuid.UUID {
	e.t.Helper()
	if e.actorID != uuid.Nil {
		return e.actorID
	}
	id := uuid.Must(uuid.NewV7())
	u, err := gen.New(e.d.Pool()).CreateUser(context.Background(), gen.CreateUserParams{
		ID: id, Email: id.String() + "@example.test", DisplayName: "actor",
	})
	if err != nil {
		e.t.Fatalf("create user: %v", err)
	}
	e.actorID = u.ID
	return e.actorID
}

// snooze mutes alert until the given instant, through the real
// alerts.SQLStore — a snooze is one of alerts.Mute's own three reasons, and
// going through the store rather than a raw UPDATE is what proves the
// escalation pass reads the SAME column Mute already compares against,
// not a parallel fixture-only notion of "snoozed".
func (e *followEnv) snooze(alert uuid.UUID, until time.Time) {
	e.t.Helper()
	u := until
	if err := e.alertStore.Snooze(context.Background(), e.org, alert, e.actor(), &u, false, e.clock.Now()); err != nil {
		e.t.Fatalf("snooze alert: %v", err)
	}
}

// snoozeUntilChange mutes alert with an UNBOUNDED snooze — no until at
// all, only snooze_until_change — alerts.Mute's own second snooze
// candidate. Unlike a bounded snooze, this one carries no snoozed_until
// value for ChainsNeedingAttentionBase to fold into LastUrgentAt at all
// (that column stays NULL the whole time), so it is the one mute shape
// where the escalation pass's own alerts.Mute call in dueStep is the ONLY
// thing holding a chain back — a bounded snooze's own snoozed_until
// anchor would still protect it even with that call deleted, which is
// exactly why a test built on a bounded snooze alone cannot prove the
// mute gate does anything (review, Critical 1).
func (e *followEnv) snoozeUntilChange(alert uuid.UUID) {
	e.t.Helper()
	if err := e.alertStore.Snooze(context.Background(), e.org, alert, e.actor(), nil, true, e.clock.Now()); err != nil {
		e.t.Fatalf("snooze alert until change: %v", err)
	}
}

// acknowledge acks alert at its current severity, through the real store —
// decision 2's own live comparison (severity <= acknowledged_severity) is
// exactly what RecordDecision/escalatePass reads, so a raw UPDATE here
// would risk testing a fixture's idea of "acknowledged" rather than the
// column the pass actually looks at.
func (e *followEnv) acknowledge(alert uuid.UUID) {
	e.t.Helper()
	if err := e.alertStore.Acknowledge(context.Background(), e.org, alert, e.actor(), "", e.clock.Now()); err != nil {
		e.t.Fatalf("acknowledge alert: %v", err)
	}
}

// unsnooze clears alert's snooze (bounded or unbounded) through the real
// store, which writes the 'unsnoozed' event ChainsNeedingAttentionBase
// folds into LastUrgentAt (its own doc comment, alert_escalations.sql) —
// the one anchor an unbounded snooze (snoozeUntilChange) has to resume
// from, since it carries no snoozed_until column value to fold in its own
// right.
func (e *followEnv) unsnooze(alert uuid.UUID) {
	e.t.Helper()
	if err := e.alertStore.Unsnooze(context.Background(), e.org, alert, e.actor(), e.clock.Now()); err != nil {
		e.t.Fatalf("unsnooze alert: %v", err)
	}
}

// closeAlert closes alert as resolved, straight through SQL rather than
// Reconcile — the escalation pass only needs a closed alert to test
// against, not a particular path that led there, and
// ChainsNeedingAttentionBase's own `closed_at IS NULL` filter does not
// care which one produced the row.
func (e *followEnv) closeAlert(alert uuid.UUID) {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.d.Pool().Exec(ctx,
		`UPDATE alerts SET closed_at = $2, closed_reason = 'resolved' WHERE id = $1`,
		alert, e.clock.Now()); err != nil {
		e.t.Fatalf("close alert: %v", err)
	}
	if _, err := e.d.Pool().Exec(ctx, `
		INSERT INTO alert_events (org_id, alert_id, at, kind, payload)
		VALUES ($1, $2, $3, 'closed', '{}'::jsonb)`, e.org, alert, e.clock.Now()); err != nil {
		e.t.Fatalf("close alert event: %v", err)
	}
}

// raiseSeverity appends a raw severity_raised alert_event, straight to the
// table rather than through RaiseAlertSeverity — openCritAlert already
// opens at the ladder's own top (crit), so there is nowhere left for a
// REAL rise to go; what TestTheFirstDeliveryRecordsTheBase needs is only a
// second genuine event for the forward pass to decide and redeliver
// through, to prove a base claimed once does not move on a later one
// (UndecidedAlertEvents reads a.severity live off the alerts row, not off
// the event itself, so this is read as 'crit' exactly as the alert already
// is — queries/alert_routes.sql's own doc comment on that query says so).
func (e *followEnv) raiseSeverity(alert uuid.UUID) {
	e.t.Helper()
	if _, err := e.d.Pool().Exec(context.Background(), `
		INSERT INTO alert_events (org_id, alert_id, at, kind, payload)
		VALUES ($1, $2, $3, 'severity_raised', '{}'::jsonb)`, e.org, alert, e.clock.Now()); err != nil {
		e.t.Fatalf("raise severity event: %v", err)
	}
}

// setSeverity moves alert's severity through the real write path
// (alerts.SQLStore.Execute, ActionSeverity) rather than a raw event insert
// — specifically so a FALLING severity, which writes no alert_events row
// at all (raiseSeverity's own doc comment, internal/alerts/store_sql.go),
// still moves severity_changed_at exactly the way production code does.
// That column is the one anchor a fall-then-rise across a route's
// severity set has to resume a chain without a burst (migration 00058's
// review, Critical 2) — a raw UPDATE here would only prove the fixture's
// own idea of "severity changed", not the real column the pass reads.
func (e *followEnv) setSeverity(alert uuid.UUID, severity rules.State) {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.d.Begin(ctx)
	if err != nil {
		e.t.Fatalf("begin set severity: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := e.alertStore.Execute(ctx, tx, e.org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: alert, At: e.clock.Now(), Severity: severity},
	}); err != nil {
		e.t.Fatalf("set severity: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		e.t.Fatalf("commit set severity: %v", err)
	}
}

// deleteChannel removes a notification channel outright — the fixture's
// own way of putting a step's fixed channel list in the same state a
// deleted channel leaves a route's: ON DELETE CASCADE clears
// alert_route_step_channels (migration 00056) the same way it already
// clears alert_route_channels, so the step's own position survives with
// no channel left to resolve.
func (e *followEnv) deleteChannel(channel uuid.UUID) {
	e.t.Helper()
	if _, err := e.d.Pool().Exec(context.Background(),
		`DELETE FROM notification_channels WHERE id = $1`, channel); err != nil {
		e.t.Fatalf("delete channel: %v", err)
	}
}
