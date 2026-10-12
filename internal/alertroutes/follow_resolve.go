// One tick's working state, split out of follow.go so that file stays
// about the tick itself: what each channel is to be told (batches), and
// the per-tick resolution context the decisions are made in
// (resolution). Both live exactly as long as one Tick call.
package alertroutes

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/notify"
)

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
