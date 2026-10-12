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

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/notify"
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
