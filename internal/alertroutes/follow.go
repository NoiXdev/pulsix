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
	"bytes"
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
func NewFollower(d *db.DB, store *Store, groups GroupReader, enq Enqueuer, publicURL string,
	now func() time.Time, log *slog.Logger) *Follower {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Follower{
		d: d, store: store, groups: groups, mutes: alerts.NewSQLStore(d), enq: enq,
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
	if _, err := f.store.ExpireClosedSuppressed(ctx, tx, orgID, now); err != nil {
		return err
	}
	routes, err := f.store.ListEnabled(ctx, tx, orgID)
	if err != nil {
		return err
	}

	// Both working sets are read before a single decision is written, so
	// they stay disjoint: a forward-pass event recorded 'suppressed' here
	// must not immediately reappear in this same tick's catch-up set,
	// where it would be re-resolved against the identical `now` for the
	// identical verdict.
	forward, err := f.store.UndecidedEvents(ctx, tx, orgID, f.batch)
	if err != nil {
		return err
	}
	caught, err := f.store.CatchUpEvents(ctx, tx, orgID, f.batch)
	if err != nil {
		return err
	}

	r := &resolution{f: f, orgID: orgID, now: now, routes: routes,
		groups:  map[uuid.UUID][]uuid.UUID{},
		windows: map[uuid.UUID][]maintenance.Occurrence{},
		alerts:  map[uuid.UUID]alerts.Alert{},
	}
	b := batches{}
	for _, ev := range forward {
		if err := f.decide(ctx, tx, r, b, ev, true); err != nil {
			return err
		}
	}
	for _, ev := range caught {
		if err := f.decide(ctx, tx, r, b, ev, false); err != nil {
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

// decide resolves one event and records its outcome. firstPass separates
// the two callers: an event the forward pass finds has no ledger row yet
// and must get one whatever the outcome — an unrouted or suppressed event
// left undecided would be re-examined by the anti-join forever — while an
// event the catch-up pass finds is already ledgered 'suppressed', so
// "still suppressed" means there is nothing to write.
func (f *Follower) decide(ctx context.Context, tx pgx.Tx, r *resolution, b batches, ev PendingEvent, firstPass bool) error {
	channels, err := r.channelsFor(ctx, ev)
	if err != nil {
		return err
	}
	if len(channels) == 0 {
		return f.store.RecordDecision(ctx, tx, r.orgID, ev.EventID, StateUnrouted, r.now)
	}
	muted, err := r.muted(ctx, ev)
	if err != nil {
		return err
	}
	if muted {
		if !firstPass {
			return nil
		}
		return f.store.RecordDecision(ctx, tx, r.orgID, ev.EventID, StateSuppressed, r.now)
	}
	b.add(channels, notify.AlertItem{
		AlertID: ev.AlertID, HostID: ev.HostID, HostName: ev.HostName,
		CheckKey: ev.CheckKey, Item: ev.Item, Metric: ev.Metric,
		Severity: string(ev.Severity), Event: string(ev.Kind), At: ev.At,
	})
	return f.store.RecordDecision(ctx, tx, r.orgID, ev.EventID, StateSent, r.now)
}

// enqueue hands one message per channel to the outbox — decision 5: a
// channel hears once per tick, with a list of what happened, not once per
// event. The key carries the tick's own second, so a retried tick that
// reaches the same channel again is deduplicated by the outbox's own
// (org_id, event_key, channel_id) uniqueness.
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
		items := b[ch]
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
		if err := f.enq.Enqueue(ctx, tx, orgID, fmt.Sprintf("alert.batch:%s:%d", ch, now.Unix()), ev, []uuid.UUID{ch}); err != nil {
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

// batches is one tick's messages: the items each channel is to be told
// about, keyed by channel id.
type batches map[uuid.UUID][]notify.AlertItem

// add appends item to every channel's bucket. Each bucket keeps its own
// copy, so enqueue can fill in a per-message URL without the copies
// aliasing.
func (b batches) add(channels []uuid.UUID, item notify.AlertItem) {
	for _, ch := range channels {
		b[ch] = append(b[ch], item)
	}
}

// channels lists the channels with something to say, sorted by their raw
// bytes so one tick's enqueues run in a reproducible order whatever the
// map's iteration order was — the same sort Channels (match.go) applies
// for the same reason.
func (b batches) channels() []uuid.UUID {
	out := make([]uuid.UUID, 0, len(b))
	for ch, items := range b {
		if len(items) > 0 {
			out = append(out, ch)
		}
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}

// resolution is one tick's resolution context: the enabled routes, the
// instant every decision is made against, and the per-tick caches that
// keep a batch from asking the same question once per event. The caches
// are safe precisely because `now` is pinned for the whole tick — the
// same argument alerts.Service.suppression's own windows cache rests on.
type resolution struct {
	f       *Follower
	orgID   uuid.UUID
	now     time.Time
	routes  []Route
	groups  map[uuid.UUID][]uuid.UUID
	windows map[uuid.UUID][]maintenance.Occurrence
	alerts  map[uuid.UUID]alerts.Alert
}

// channelsFor resolves one event to the union of channels its matching
// routes name — empty when no route wants it.
func (r *resolution) channelsFor(ctx context.Context, ev PendingEvent) ([]uuid.UUID, error) {
	hostGroups, ok := r.groups[ev.HostID]
	if !ok {
		var err error
		hostGroups, err = r.f.groups.GroupsForHost(ctx, r.orgID, ev.HostID)
		if err != nil {
			return nil, fmt.Errorf("groups for host %s: %w", ev.HostID, err)
		}
		r.groups[ev.HostID] = hostGroups
	}
	subject := Subject{HostID: ev.HostID, Severity: ev.Severity, Event: ev.Kind}
	return Channels(MatchRoutes(subject, hostGroups, r.routes)), nil
}

// muted answers whether the event's alert is muted at this tick's instant,
// through alerts.Mute — a snooze on the alert itself, an operator silence
// or a maintenance window, in that package's own order.
func (r *resolution) muted(ctx context.Context, ev PendingEvent) (bool, error) {
	a, ok := r.alerts[ev.AlertID]
	if !ok {
		got, err := r.f.mutes.GetAlert(ctx, r.orgID, ev.AlertID)
		if err != nil {
			return false, fmt.Errorf("get alert %s: %w", ev.AlertID, err)
		}
		a = got.Alert
		r.alerts[ev.AlertID] = a
	}
	windows, ok := r.windows[ev.HostID]
	if !ok {
		var err error
		windows, err = r.f.mutes.WindowsForHost(ctx, r.orgID, ev.HostID, r.now)
		if err != nil {
			return false, fmt.Errorf("windows for host %s: %w", ev.HostID, err)
		}
		r.windows[ev.HostID] = windows
	}
	return alerts.Mute(a, windows, r.now).Muted, nil
}
