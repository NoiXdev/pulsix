// The follower's own tests, against a real database: the whole point of
// this task is a tick that writes the ledger and the outbox in ONE
// transaction, and neither half of that claim can be proved with fakes.
// The enqueuer is the only fake here (it stands in for notify.Outbox, so
// a test can make the enqueue fail on demand); the ledger, the routes,
// the alerts, the maintenance windows and the advisory lock are all real.
package alertroutes_test

import (
	"bytes"
	"context"
	"crypto/rand"
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
	env.follower = alertroutes.NewFollower(d, env.store, noGroups{}, env.rotations, env.enq,
		"https://pulsix.example", env.clock.Now, slog.New(slog.NewTextHandler(env.log, nil)))
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

func TestTickCoalescesPerChannel(t *testing.T) {
	// Decision 5: one message per channel per tick, listing what
	// happened — not one message per event.
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	for range 3 {
		env.openCritAlert()
	}

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Fatalf("Enqueue called %d times, want 1 — one message per channel", len(env.enq.calls))
	}
	if env.enq.calls[0].count != 3 {
		t.Errorf("batch carried %d alerts, want 3", env.enq.calls[0].count)
	}
}

// TestTickCapsTheItemListButNotTheCount is the storm case decision 5
// names outright — "fifty hosts filling their disks" — and the cap sits
// exactly there, so the first batch that trips it is the worst possible
// one to get wrong. The message carries at most MaxBatchItems items, but
// Count stays the number of alerts the batch actually decided: that
// difference is the only thing telling a renderer the list was cut
// (notify.alertTruncationNote turns it into "… and N more").
func TestTickCapsTheItemListButNotTheCount(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	const over = alertroutes.MaxBatchItems + 7
	for range over {
		env.openCritAlert()
	}

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Fatalf("Enqueue called %d times, want 1", len(env.enq.calls))
	}
	if got := env.enq.calls[0].items; got != alertroutes.MaxBatchItems {
		t.Errorf("message listed %d items, want the cap of %d", got, alertroutes.MaxBatchItems)
	}
	if got := env.enq.calls[0].count; got != over {
		t.Errorf("batch count = %d, want %d — the number before the cut, not after it", got, over)
	}
}

func TestTickIsIdempotent(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	env.openCritAlert()

	for range 2 {
		if err := env.follower.Tick(context.Background(), env.org); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	if len(env.enq.calls) != 1 {
		t.Errorf("Enqueue called %d times across two ticks, want 1", len(env.enq.calls))
	}
}

func TestSuppressedIsCaughtUpWhenTheWindowEnds(t *testing.T) {
	// Decision 4. The alert opened inside a maintenance window and is
	// still open after it — the case that must not be swallowed.
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	window := env.maintenanceWindow(env.host, env.clock.Now().Add(-time.Hour), env.clock.Now().Add(time.Hour))
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick during maintenance: %v", err)
	}
	if len(env.enq.calls) != 0 {
		t.Fatalf("a suppressed alert was delivered: %d calls", len(env.enq.calls))
	}

	env.endWindow(window)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick after maintenance: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Errorf("Enqueue called %d times after the window ended, want 1", len(env.enq.calls))
	}
}

func TestUnmatchedEventIsRecordedNotLeftPending(t *testing.T) {
	// Review Focus 3. No route wants this kind, so nothing is sent — but
	// the event must be decided, or the anti-join re-examines it forever.
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventClosed}, []uuid.UUID{env.channel})
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 0 {
		t.Errorf("Enqueue called %d times, want 0", len(env.enq.calls))
	}
	if got := env.ledgerState(); got != alertroutes.StateUnrouted {
		t.Errorf("ledger state = %q, want %q", got, alertroutes.StateUnrouted)
	}
}

func TestTickFailureLeavesEverythingUndecided(t *testing.T) {
	// Review Focus 5 and the transaction rule from the spec's "The
	// follower": recording `sent` without the delivery existing would
	// drop an alert silently and forever, because the anti-join never
	// looks at a decided event again.
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	env.openCritAlert()
	env.enq.fail = errors.New("channel vanished mid-tick")

	if err := env.follower.Tick(context.Background(), env.org); err == nil {
		t.Fatal("Tick() = nil, want the enqueue failure reported")
	}
	if got := env.ledgerState(); got != "" {
		t.Errorf("ledger state = %q after a failed tick, want no row at all", got)
	}

	env.enq.fail = nil
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick after recovery: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Errorf("Enqueue called %d times after recovery, want 1 — the event was not lost", len(env.enq.calls))
	}
}

func TestConcurrentTicksSendOnce(t *testing.T) {
	// Review Focus 4. Two managers, one organization, one message.
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	env.openCritAlert()

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = env.follower.Tick(context.Background(), env.org)
		}()
	}
	wg.Wait()
	if len(env.enq.calls) != 1 {
		t.Errorf("Enqueue called %d times across four concurrent ticks, want 1", len(env.enq.calls))
	}
}

// TestRunTicksEveryOrganizationUntilCancelled covers the loop itself: Run
// finds the organizations on its own — nothing hands it a tenant list —
// ticks each of them, and returns when its context is cancelled, which is
// precisely what internal/app's startLoop relies on to shut the follower
// down before it closes the pool.
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

// TestBatchKeyIsDerivedFromItsEventsNotTheClock is the review's own
// scenario, with the real outbox behind it rather than the recording
// fake: two managers' ticks land inside the same wall-clock second — the
// injected clock is frozen, so "the same second" is exact rather than
// hoped for — with new events committed in between. A key built from the
// tick's second would be identical for both, and
// InsertNotificationDelivery's ON CONFLICT (org_id, event_key,
// channel_id) DO NOTHING would then swallow the second batch WITHOUT AN
// ERROR while its events commit as 'sent' in the same transaction: the
// anti-join never looks at them again and nobody is ever told. The
// advisory lock cannot help — the two ticks are sequential, not
// concurrent.
func TestBatchKeyIsDerivedFromItsEventsNotTheClock(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	outbox := notify.NewOutbox(env.d, "https://pulsix.example", time.Now, rand.Reader, slog.Default())
	follower := alertroutes.NewFollower(env.d, env.store, noGroups{}, env.rotations, outbox,
		"https://pulsix.example", env.clock.Now, slog.Default())
	ctx := context.Background()

	env.openCritAlert()
	if err := follower.Tick(ctx, env.org); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	// A second manager's tick, the same second, over events that only
	// committed after the first tick had already read its working set.
	env.openCritAlert()
	if err := follower.Tick(ctx, env.org); err != nil {
		t.Fatalf("second Tick: %v", err)
	}

	if got := env.deliveries(); got != 2 {
		t.Errorf("notification_deliveries = %d, want 2 — the second tick's batch was swallowed by the first tick's key", got)
	}
}

func TestRunTicksEveryOrganizationUntilCancelled(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	env.openCritAlert()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.follower.Run(ctx, 10*time.Millisecond)
	}()

	deadline := time.Now().Add(30 * time.Second)
	for env.enq.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Run never delivered the pending alert")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
