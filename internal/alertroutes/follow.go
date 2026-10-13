// The follower (spec docs/superpowers/specs/2026-10-12-v0.4b-alert-routes-
// design.md, "The follower"): the ticker that walks alert_events, decides
// who hears about each event, and hands one batch per channel to the
// notification outbox. Everything else in this package is a part it uses —
// Route and Validate (route.go), MatchRoutes and Channels (match.go), the
// route store (store_sql.go) and the ledger (ledger_sql.go).
//
// The one invariant the whole feature rests on: THE LEDGER WRITE AND THE
// ENQUEUE SHARE ONE TRANSACTION. UndecidedEvents is an anti-join, so a
// decided event is never looked at again; recording an event as 'sent'
// and then failing before its delivery row exists would drop an alert
// silently and permanently, with no retry, no log and no symptom but a
// person who was never told. Tick therefore opens exactly one
// transaction, runs every read, every RecordDecision and every Enqueue on
// it, and commits once at the end — or commits nothing, leaving every
// event undecided for the next tick to find again.
package alertroutes

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/oncall"
)

const (
	// DefaultFollowInterval is one tick per minute. The coalescing window
	// is the tick itself (decision 5), so this is also how long an alert
	// can wait before it is reported and how wide a batch can get.
	DefaultFollowInterval = time.Minute
	// DefaultFollowBatch bounds how many events one pass claims, in the
	// same spirit as rules.DefaultEvalBatch: a manager that has fallen
	// behind catches up over several ticks rather than in one pass long
	// enough to matter to everything else sharing the pool.
	DefaultFollowBatch = 500
	// MaxBatchItems bounds how many alerts one message lists.
	// notify.AlertBatch.Count is the number before truncation precisely so
	// a reader can tell a complete list from a capped one — the same
	// promise notify.MaxDigestSchedules already makes for the digest.
	MaxBatchItems = 50
	// followLockKeyPrefix namespaces this lock's key within hashtext's
	// output space, exactly as sshconf.chainLockKeyPrefix does for the
	// jump-host chain lock: without a distinct prefix,
	// hashtext(orgID.String()) would hash two unrelated per-organization
	// locks to the same key and serialize them against each other.
	followLockKeyPrefix = "alertroutes-follow:"
)

// Enqueuer is the delivery port: notify.Outbox.Enqueue, and nothing else
// of that package. It takes the caller's transaction because the ledger
// write and the enqueue must commit together (this file's own doc comment
// says why). Enqueue already opens a savepoint nested inside tx for its
// own statements, so a notification failure rolls back only the
// notification — but the error it returns still aborts this tick, which is
// what keeps a recorded 'sent' from outliving its message.
type Enqueuer interface {
	Enqueue(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, key string, ev notify.Event, channels []uuid.UUID) error
}

// RotationReader is the slice of internal/oncall the follower needs: given
// a rotation id, the cycle and the stand-ins covering it. The arithmetic
// that turns those into "who is on call at this instant" stays in that
// package (oncall.ChannelAt) — this port is only how the rows are read.
//
// It takes the caller's transaction for the same reason every other read
// in a tick does: the rotation's answer, the routes it is resolved
// against, and the ledger row that records the outcome all belong to one
// snapshot, so a handover committed halfway through a tick cannot split a
// batch between two people.
//
// Declared here rather than taken as *oncall.Store so a unit test can
// stand one in, the same shape GroupReader and Enqueuer already take.
type RotationReader interface {
	Resolve(ctx context.Context, tx pgx.Tx, orgID, rotationID uuid.UUID) (oncall.Rotation, []oncall.Exception, error)
}

// muteSource is the slice of internal/alerts the follower needs to answer
// "is this alert muted right now": the alert itself, for its own snooze
// fields, and the maintenance windows covering its host. Both halves go
// to alerts.Mute, which is the single place the three mute reasons are
// compared in their one strict order (alerts/mute.go) — re-deciding any
// of that here would be exactly the divergence backlog 4.30 had to settle
// once already.
//
// It is not a constructor argument: the follower builds it from the same
// *db.DB it is handed, because there is only one implementation that can
// be correct (alerts.SQLStore, whose occurrenceAt is unexported) and an
// injection point here would invite a second, hand-rolled suppression
// answer. Both reads run on the pool rather than on the tick's
// transaction, which is sound because the tick writes neither table.
type muteSource interface {
	GetAlert(ctx context.Context, orgID, id uuid.UUID) (alerts.AlertWithHost, error)
	WindowsForHost(ctx context.Context, orgID, hostID uuid.UUID, now time.Time) ([]maintenance.Occurrence, error)
}

// Follower is the ticker. One per manager; the per-organization advisory
// lock (tryLock) is what makes several managers safe.
type Follower struct {
	d         *db.DB
	store     *Store
	groups    GroupReader
	rotations RotationReader
	mutes     muteSource
	enq       Enqueuer
	publicURL string
	now       func() time.Time
	log       *slog.Logger
	batch     int
}

// NewFollower wires the follower. groups is a GroupReader, not this
// package's own Service: the follower needs host→group membership and the
// enabled routes, has no use for route CRUD, and must NOT reuse
// Service.ForHost, which deliberately ignores a route's severity and
// event selections (its own doc comment says why) and would therefore
// route events no route actually asked for.
func NewFollower(d *db.DB, store *Store, groups GroupReader, rotations RotationReader, enq Enqueuer,
	publicURL string, now func() time.Time, log *slog.Logger) *Follower {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Follower{
		d: d, store: store, groups: groups, rotations: rotations, mutes: alerts.NewSQLStore(d), enq: enq,
		publicURL: publicURL, now: now, log: log, batch: DefaultFollowBatch,
	}
}

// Run ticks every interval until ctx is cancelled, walking every
// organization each time. One organization's failure is logged and the
// pass moves on, so a single bad tenant cannot stop the rest — the same
// posture schedule.Retest takes for its own per-organization loop.
func (f *Follower) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultFollowInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.pass(ctx)
		}
	}
}

// pass is one sweep over every organization.
func (f *Follower) pass(ctx context.Context) {
	orgs, err := f.store.ListOrgIDs(ctx)
	if err != nil {
		f.log.ErrorContext(ctx, "alert follower: list organizations", "err", err)
		return
	}
	for _, org := range orgs {
		if ctx.Err() != nil {
			return
		}
		if err := f.Tick(ctx, org); err != nil {
			f.log.ErrorContext(ctx, "alert follower: tick failed", "org", org, "err", err)
		}
	}
}

// Tick runs one organization's pass inside one transaction: take the
// per-organization advisory lock, retire suppressed rows whose alert has
// closed, read both passes' working sets, decide every event, and enqueue
// one batch per channel. A lock this tick did not get means another
// manager is already doing this organization's work, which is not an
// error — it returns nil and the other manager's commit is the one that
// counts.
func (f *Follower) Tick(ctx context.Context, orgID uuid.UUID) error {
	tx, err := f.d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin alert follow tick: %w", err)
	}
	// Rollback on every path that does not reach Commit, on a context the
	// caller's cancellation cannot take away; after a successful Commit it
	// is a no-op that reports ErrTxClosed.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	locked, err := tryLock(ctx, tx, orgID)
	if err != nil {
		return err
	}
	if !locked {
		return nil
	}

	now := f.now()
	retired, err := f.store.ExpireClosedHeld(ctx, tx, orgID, now)
	if err != nil {
		return err
	}
	if retired > 0 {
		// Debug, not info: retiring rows is routine bookkeeping, and on a
		// tick that retires nothing — the overwhelming majority — there is
		// nothing to say at all. It is logged rather than dropped because
		// a sudden large count is the visible shape of "a maintenance
		// window swallowed a lot of alerts", which is otherwise a silent
		// fact about rows nobody will ever look at again.
		f.log.DebugContext(ctx, "alert follower: retired held rows whose alert closed",
			"org", orgID, "rows", retired)
	}
	routes, err := f.store.ListEnabled(ctx, tx, orgID)
	if err != nil {
		return err
	}

	// Both working sets are read before a single decision is written, so
	// they stay disjoint: a forward-pass event recorded 'suppressed' or
	// 'deferred' here must not immediately reappear in this same tick's
	// catch-up set, where it would be re-resolved against the identical
	// `now` for the identical verdict.
	forward, err := f.store.UndecidedEvents(ctx, tx, orgID, f.batch)
	if err != nil {
		return err
	}
	caught, err := f.store.CatchUpEvents(ctx, tx, orgID, f.batch)
	if err != nil {
		return err
	}

	r := &resolution{f: f, tx: tx, orgID: orgID, now: now, routes: routes,
		groups:  map[uuid.UUID][]uuid.UUID{},
		windows: map[uuid.UUID][]maintenance.Occurrence{},
		alerts:  map[uuid.UUID]alerts.Alert{},
		oncall:  map[uuid.UUID]uuid.UUID{},
		gates:   map[uuid.UUID]bool{},
	}
	// The two passes run through the same decide: a caught-up event is
	// re-decided from scratch against this tick's instant, exactly as a
	// fresh one is, and the state it was found in only decides whether the
	// verdict is worth writing (decide's own record).
	b := batches{}
	for _, ev := range slices.Concat(forward, caught) {
		if err := f.decide(ctx, tx, r, b, ev); err != nil {
			return err
		}
	}
	if err := f.enqueue(ctx, tx, orgID, b, now); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit alert follow tick: %w", err)
	}
	return nil
}

// decide resolves one event through four gates, in this order, and records
// the outcome. The order is the decision, not an implementation detail:
//
//  1. ROUTING. No route wants this event, or every route that does names
//     nobody — a channel-less route, or a rotation that resolves to no
//     channel. StateUnrouted, and v0.4b's "reaches nobody" indicator says
//     so. Terminal: nothing about a later instant can change it.
//  2. SUPPRESSION. The alert is muted — snoozed, silenced, or its host is
//     in maintenance — through alerts.Mute's own strict order.
//     StateSuppressed, and the catch-up pass retries it.
//  3. THE WINDOW. Every route that would have told somebody is outside its
//     delivery window. StateDeferred, and the catch-up pass retries it.
//  4. Otherwise it goes out, and StateSent.
//
// Suppression is asked BEFORE the window, and that is the whole of why a
// breakthrough severity cannot cross it: breakthrough is a property of the
// window gate alone (resolution.windowOpen), so an event that never reaches
// gate 3 never gets to use it. A maintenance window silences a crit too —
// that is what a maintenance window is for, and if a severity could cross
// it, maintenance would stop being a promise and become a request
// (TestMaintenanceSilencesEvenABreakthroughSeverity).
//
// The same ordering settles which reason the ledger records when an event
// trips both gates: 'suppressed', never 'deferred'. That the host is in
// maintenance is the first thing an operator needs to know; the time of day
// is the second.
func (f *Follower) decide(ctx context.Context, tx pgx.Tx, r *resolution, b batches, ev PendingEvent) error {
	routed, err := r.route(ctx, ev)
	if err != nil {
		return err
	}
	if len(routed.channels) == 0 && !routed.held {
		return f.record(ctx, tx, r, ev, StateUnrouted)
	}
	muted, err := r.muted(ctx, ev)
	if err != nil {
		return err
	}
	if muted {
		return f.record(ctx, tx, r, ev, StateSuppressed)
	}
	if len(routed.channels) == 0 {
		return f.record(ctx, tx, r, ev, StateDeferred)
	}
	b.add(routed.channels, ev.EventID, notify.AlertItem{
		AlertID: ev.AlertID, HostID: ev.HostID, HostName: ev.HostName,
		CheckKey: ev.CheckKey, Item: ev.Item, Metric: ev.Metric,
		Severity: string(ev.Severity), Event: string(ev.Kind), At: ev.At,
	})
	return f.record(ctx, tx, r, ev, StateSent)
}

// record writes st unless the ledger already says exactly that. Only the
// catch-up pass can hit the second case: the forward pass finds events with
// no ledger row at all, whose PendingEvent.Ledger is empty and therefore
// differs from every state there is.
//
// Skipping the no-op write is not an optimisation. decided_at means "when
// this state became true" — the reading ExpireClosedHeld's own @now already
// takes — so rewriting 'suppressed' over 'suppressed' every minute would
// turn "held since 03:00" into "held since a moment ago" and erase the one
// fact the column carries. What it must NOT skip is a change of reason: an
// event held for a shut window the moment a maintenance window opens over
// it moves from 'deferred' to 'suppressed', which the comparison catches
// because the two differ.
func (f *Follower) record(ctx context.Context, tx pgx.Tx, r *resolution, ev PendingEvent, st LedgerState) error {
	if !writeNeeded(st, ev.Ledger) {
		return nil
	}
	return f.store.RecordDecision(ctx, tx, r.orgID, ev.EventID, st, r.now)
}

// writeNeeded reports whether a verdict of st is worth writing when the
// ledger already holds found.
//
// The second clause is a guard, not a case: StateSent is never skipped,
// whatever the ledger says. It is unreachable today — HeldAlertEvents
// returns only 'suppressed' and 'deferred', so found can never be 'sent'
// — and that is precisely the problem it answers. Without it, the safety
// of this function rests on a WHERE clause in another file: widen that
// clause by one state and an event is enqueued with no ledger write,
// which is 'sent' without the delivery that reports it, in the one place
// the whole feature exists to keep that from happening. Three words here
// make the guarantee local to the code that depends on it
// (TestWriteNeededNeverSkipsSent).
func writeNeeded(st, found LedgerState) bool {
	return st != found || st == StateSent
}

// enqueue hands one message per channel to the outbox — decision 5: a
// channel hears once per tick, with a list of what happened, not once per
// event. The key is built from the batch's own events (batch.key, which
// documents why it must not be built from the clock), so the outbox's own
// (org_id, event_key, channel_id) uniqueness deduplicates a retried tick
// without ever swallowing a different batch.
func (f *Follower) enqueue(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, b batches, now time.Time) error {
	channels := b.channels()
	if len(channels) == 0 {
		return nil
	}
	slug, name, err := f.store.OrgContext(ctx, tx, orgID)
	if err != nil {
		return err
	}
	base := f.publicURL + "/o/" + slug
	for _, ch := range channels {
		items := b[ch].items
		count := len(items)
		for i := range items {
			// The Alerts page filtered to the host, the closest thing the
			// SPA has to a per-alert address (router.tsx's alertsRoute
			// carries `?host=`, there is no /alerts/{id} page).
			items[i].URL = base + "/alerts?host=" + items[i].HostID.String()
		}
		if len(items) > MaxBatchItems {
			items = items[:MaxBatchItems]
		}
		ev := notify.Event{
			Kind: notify.EventAlertBatch, At: now,
			Org:    notify.OrgRef{ID: orgID, Slug: slug, Name: name},
			Alerts: &notify.AlertBatch{Count: count, Items: items},
			URL:    base + "/alerts",
		}
		if err := f.enq.Enqueue(ctx, tx, orgID, b[ch].key(ch), ev, []uuid.UUID{ch}); err != nil {
			return fmt.Errorf("enqueue alert batch for channel %s: %w", ch, err)
		}
	}
	return nil
}

// tryLock takes orgID's follow lock for the rest of tx. Transaction-scoped
// (pg_try_advisory_xact_lock), not session-scoped like
// schedule.AdvisoryLocker's: the lock's life is exactly this tick's
// transaction, so there is no release path to get wrong and a manager
// that dies mid-tick drops both at once. The key is hashtext over a
// prefixed organization id, the convention sshconf.WithChainLock already
// follows for its own per-organization lock.
func tryLock(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (bool, error) {
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext($1))`,
		followLockKeyPrefix+orgID.String()).Scan(&locked); err != nil {
		return false, fmt.Errorf("acquire alert follow lock: %w", err)
	}
	return locked, nil
}
