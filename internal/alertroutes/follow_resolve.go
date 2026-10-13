// One tick's working state, split out of follow.go so that file stays
// about the tick itself: what each channel is to be told (batches), and
// the per-tick resolution context the decisions are made in
// (resolution). Both live exactly as long as one Tick call.
package alertroutes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/oncall"
	"github.com/noixdev/pulsix/internal/rules"
)

// batch is one channel's share of a tick: the items the message lists,
// and the ids of the events those items were decided from. The event ids
// are what the message's idempotency key is built from (key), so they are
// kept beside the items rather than derived from them — notify.AlertItem
// names an alert, not the event that moved it.
type batch struct {
	items  []notify.AlertItem
	events []uuid.UUID
}

// batches is one tick's messages, keyed by channel id.
type batches map[uuid.UUID]*batch

// add appends one decided event to every channel's bucket. Each bucket
// keeps its own copy of the item, so enqueue can fill in a per-message
// URL without the copies aliasing.
func (b batches) add(channels []uuid.UUID, eventID uuid.UUID, item notify.AlertItem) {
	for _, ch := range channels {
		cur := b[ch]
		if cur == nil {
			cur = &batch{}
			b[ch] = cur
		}
		cur.items = append(cur.items, item)
		cur.events = append(cur.events, eventID)
	}
}

// key is the message's idempotency key: the channel, and a hash over the
// sorted ids of exactly the events this message reports.
//
// Derived from the content, never from the clock. A key of the form
// alert.batch:<channel>:<tick second> collides across two ticks inside
// the same wall-clock second — two managers on their own 60 s tickers,
// phased under a second apart, with the second tick picking up events
// that committed after the first had read its working set. The outbox's
// own INSERT ... ON CONFLICT (org_id, event_key, channel_id) DO NOTHING
// would then drop that second batch with no row and NO ERROR, while its
// events commit as 'sent' in the same transaction — and the forward
// pass's anti-join never looks at a decided event again, so nobody would
// ever be told. The per-organization advisory lock does not help: those
// two ticks are sequential, not concurrent
// (TestBatchKeyIsDerivedFromItsEventsNotTheClock).
//
// Content-derived gives both halves of what the key is for: the same
// events for the same channel always hash the same, so a retried tick
// still deduplicates, and any different set of events necessarily hashes
// differently. The ids are sorted before hashing — by their raw bytes,
// the same discipline Channels (match.go) applies — so Go's map
// iteration order can never leak into a key.
func (c *batch) key(channel uuid.UUID) string {
	ids := slices.Clone(c.events)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	h := sha256.New()
	for _, id := range ids {
		h.Write(id[:])
	}
	// 16 bytes of SHA-256: far more than enough to keep two different
	// event sets apart, and short enough to stay readable in the
	// delivery log.
	return fmt.Sprintf("alert.batch:%s:%s", channel, hex.EncodeToString(h.Sum(nil)[:16]))
}

// channels lists the channels with something to say, sorted by their raw
// bytes so one tick's enqueues run in a reproducible order whatever the
// map's iteration order was — the same sort Channels (match.go) applies
// for the same reason.
func (b batches) channels() []uuid.UUID {
	out := make([]uuid.UUID, 0, len(b))
	for ch, bk := range b {
		if len(bk.items) > 0 {
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
// same argument alerts.Service.suppression's own windows cache rests on,
// and the same reason a rotation's answer is cached per rotation id
// rather than per event.
//
// tx is the tick's own transaction, carried here because resolving a
// rotation is a read (oncall.Store.Resolve) and every read a tick makes
// belongs to the one transaction its decisions commit with — follow.go's
// own file comment on why that is not negotiable.
type resolution struct {
	f       *Follower
	tx      pgx.Tx
	orgID   uuid.UUID
	now     time.Time
	routes  []Route
	groups  map[uuid.UUID][]uuid.UUID
	windows map[uuid.UUID][]maintenance.Occurrence
	alerts  map[uuid.UUID]alerts.Alert
	oncall  map[uuid.UUID]uuid.UUID
}

// routing is one event's resolved audience, already past the window gate:
// the channels to tell now, and whether some route that WOULD have told
// somebody was shut at this instant.
//
// held is what separates "nobody is configured for this" from "somebody is
// configured but not right now", which are the same empty channel list and
// two completely different ledger states — StateUnrouted is terminal and
// StateDeferred is retried. A route whose window is shut but which resolves
// to no channel at all does NOT set held: deferring an event that reaches
// nobody even when the window opens would park it in the catch-up pass for
// the life of the alert, waiting on a gate that was never the problem.
type routing struct {
	channels []uuid.UUID
	held     bool
}

// route resolves one event to the channels that may hear it at this
// instant. Routes add up (Scope's own doc comment), and each one carries
// its own window, so the gate is applied per route and the survivors are
// unioned — NOT the other way round. Two routes with complementary
// windows are the design's own answer to "page the day shift on weekdays
// and the night shift otherwise" (spec, "What this is not"), and a gate
// applied to the union would hold a Saturday alert until Monday so the
// weekday route could have it too, which is a stale page nobody can act on.
func (r *resolution) route(ctx context.Context, ev PendingEvent) (routing, error) {
	hostGroups, ok := r.groups[ev.HostID]
	if !ok {
		var err error
		hostGroups, err = r.f.groups.GroupsForHost(ctx, r.orgID, ev.HostID)
		if err != nil {
			return routing{}, fmt.Errorf("groups for host %s: %w", ev.HostID, err)
		}
		r.groups[ev.HostID] = hostGroups
	}
	subject := Subject{HostID: ev.HostID, Severity: ev.Severity, Event: ev.Kind}
	matched := MatchRoutes(subject, hostGroups, r.routes)

	var open []Route
	var held bool
	for _, rt := range matched {
		chans := r.channelsOf(ctx, rt)
		if len(chans) == 0 {
			continue
		}
		if !r.windowOpen(ctx, rt, ev.Severity) {
			held = true
			continue
		}
		// Channels unions over Route.Channels, so a rotation's answer is
		// lent to it as a one-element list rather than taught to it: the
		// route value here is this loop's own copy and never reaches the
		// store again.
		rt.Channels = chans
		open = append(open, rt)
	}
	return routing{channels: Channels(open), held: held}, nil
}

// channelsOf is where a route's channel source is decided: the fixed list
// it was configured with, or — when it names a rotation — whichever single
// channel that rotation puts on call.
//
// The instant asked about is r.now, THE TICK'S, never the event's At. An
// alert raised at 03:00 and caught up at 07:00 goes to whoever is on call
// at 07:00 (decision 8): the person who slept through it and has since
// handed over can do nothing about it, and their replacement can.
//
// A rotation that cannot name anybody — deleted out from under the route,
// an emptied cycle, a timezone zoneinfo no longer carries — contributes no
// channel and is logged. The event then falls through to StateUnrouted
// exactly as a channel-less route already does, which is what lights
// v0.4b's "reaches nobody" indicator. It is deliberately not an error that
// aborts the tick: one broken rotation would otherwise stop every other
// organization's alert in the same pass. That posture is also why this
// returns no error at all: every way it can fail ends in the same place,
// a route that names nobody and a line in the log saying so.
func (r *resolution) channelsOf(ctx context.Context, rt Route) []uuid.UUID {
	if rt.RotationID == nil {
		return rt.Channels
	}
	id := *rt.RotationID
	ch, ok := r.oncall[id]
	if !ok {
		rot, exc, err := r.f.rotations.Resolve(ctx, r.tx, r.orgID, id)
		if err != nil {
			r.f.log.WarnContext(ctx, "alert follower: rotation could not be read, route reaches nobody",
				"org", r.orgID, "route", rt.ID, "rotation", id, "err", err)
			ch = uuid.Nil
		} else if ch, err = oncall.ChannelAt(rot, exc, r.now); err != nil {
			r.f.log.WarnContext(ctx, "alert follower: rotation names nobody, route reaches nobody",
				"org", r.orgID, "route", rt.ID, "rotation", id, "err", err)
			ch = uuid.Nil
		}
		r.oncall[id] = ch
	}
	if ch == uuid.Nil {
		return nil
	}
	return []uuid.UUID{ch}
}

// windowOpen answers the fourth gate for one route: may this route deliver
// at this instant?
//
// No window means always. A severity the route lists as a breakthrough
// crosses a shut window and is therefore always open — and crosses ONLY
// this gate: a maintenance window silences a crit too, which is what a
// maintenance window is for, so suppression is checked before this is ever
// asked (follow.go's decide).
//
// A window whose cron or timezone can no longer be parsed counts as OPEN,
// and says so at warn level. Both failure directions are wrong and only one
// of them is dangerous (decision 9): a broken window that delivers is loud,
// so somebody notices and fixes it, while a broken window that holds is
// silent and is discovered at the next outage nobody heard about. That is
// also why this returns no error — there is no failure here a caller could
// do anything useful with, and handing one upwards would only invite a
// future caller to treat it as "do not deliver".
func (r *resolution) windowOpen(ctx context.Context, rt Route, severity rules.State) bool {
	if rt.Window == nil {
		return true
	}
	if slices.Contains(rt.Breakthrough, severity) {
		return true
	}
	active, err := maintenance.ActiveAt(rt.Window.ToMaintenance(rt.ID), r.now)
	if err != nil {
		r.f.log.WarnContext(ctx, "alert follower: delivery window cannot be evaluated, delivering anyway",
			"org", r.orgID, "route", rt.ID, "cron", rt.Window.Cron, "timezone", rt.Window.Timezone, "err", err)
		return true
	}
	return active
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
