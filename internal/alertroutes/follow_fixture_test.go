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
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db"
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

	store     *alertroutes.Store
	rotations *oncall.Store
	enq       *recordingEnqueuer
	log       *logBuffer
	follower  *alertroutes.Follower

	seq int
}

func newFollowEnv(t *testing.T) *followEnv {
	t.Helper()
	d := testdb.Open(t)
	org := seedOrg(t, d)
	env := &followEnv{
		t: t, d: d,
		clock:     &fixedClock{t: time.Now().UTC().Truncate(time.Second)},
		org:       org,
		host:      seedHost(t, d, org),
		channel:   seedChannel(t, d, org),
		store:     alertroutes.NewSQLStore(d),
		rotations: oncall.NewSQLStore(d),
		enq:       &recordingEnqueuer{},
		log:       &logBuffer{},
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
	e.seq++
	ctx := context.Background()
	var alertID uuid.UUID
	err := e.d.Pool().QueryRow(ctx, `
		INSERT INTO alerts (org_id, host_id, scope, check_key, item, metric, opened_at, severity, peak_severity)
		VALUES ($1, $2, 'tuple', 'cpu', '', $3, $4, 'crit', 'crit')
		RETURNING id`, e.org, e.host, fmt.Sprintf("used_percent_%d", e.seq), e.clock.Now()).Scan(&alertID)
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
	e.seq++
	id := uuid.Must(uuid.NewV7())
	if _, err := e.d.Pool().Exec(context.Background(), `
		INSERT INTO maintenance_windows (id, org_id, name, target_kind, host_id, starts_at, ends_at, reason)
		VALUES ($1, $2, $3, 'host', $4, $5, $6, 'maintenance')`,
		id, e.org, fmt.Sprintf("window-%d", e.seq), host, start, end); err != nil {
		e.t.Fatalf("create maintenance window: %v", err)
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
