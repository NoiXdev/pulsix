package checks

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// The agent clock story, in one place.
//
// Two thresholds in this milestone disagree by a factor of twenty, on
// purpose. Ingest accepts a reading up to maxPast (1 hour) old, because a
// reconnecting agent replays a bounded ring and a sample from twenty
// minutes ago is real data. The host page calls a reading stale after
// three of the check's own intervals — 180 s for the 60 s seed checks.
// Between those two numbers sits a host whose clock is four minutes
// behind the manager's: every reading it sends is accepted, stored, and
// then permanently badged amber, while the agent is connected, punctual
// and completely healthy. Multi-minute skew on a host without working NTP
// is ordinary.
//
// Widening the staleness threshold to cover it would hide the skew
// instead of naming it, and would blind the page to the thing staleness
// is for. What is needed is the difference between the two causes, since
// the remedies share nothing: "this host's clock disagrees with the
// manager's" is fixed with NTP, "this host stopped reporting" is fixed by
// looking at the agent.
//
// The manager can measure the first exactly, and only at one moment: when
// a batch arrives, it holds the agent's own timestamps next to its own
// clock. Nothing stores that comparison — check_current keeps the agent's
// timestamp and no arrival time — so it is kept here, in the process that
// made it, for as long as it is worth anything.
//
// What that costs in honesty, stated rather than hidden:
//
//   - It is per manager process. An agent's stream lives on exactly one
//     manager, so the measurement exists on that one; a GUI request
//     answered by a different manager reports no offset rather than a
//     wrong one, and the host page says nothing instead of something
//     false. Same lifetime as the stream registry next door.
//   - It is a fresh measurement only while results keep arriving. A
//     disconnected agent's last offset stays until it is pruned; the read
//     side carries the measurement's own timestamp so the GUI can say
//     when, rather than implying now.
//   - A reconnect's replay can overstate it for one moment: the offset is
//     taken from the NEWEST reading in the batch, so a chunk of old
//     replayed results measures as old. The agent sends its buffer
//     newest-chunk-last and then live results within one interval, so the
//     estimate corrects itself on the next batch.

// offsetRetention is how long a measurement stays in the map after the
// host stops reporting. An hour is deliberately longer than any GUI
// session cares about and short enough that a decommissioned fleet does
// not live in memory for the life of the process.
const offsetRetention = time.Hour

// offsetPruneInterval is how often the map is swept. The sweep is O(hosts
// that reported in the last hour) and runs at most this often, so it
// costs nothing at the per-batch rate a fleet actually ingests at.
const offsetPruneInterval = time.Minute

// ClockOffset is how far a host's clock ran behind the manager's when its
// most recent readings were ingested.
type ClockOffset struct {
	// Offset is the manager's clock minus the newest reading's own
	// timestamp: POSITIVE means the host's clock is behind the manager's
	// (its readings look older than they are, which is what makes them
	// stale), negative means it runs ahead. It includes the delivery
	// latency of one batch, which is milliseconds against the minutes
	// this exists to name.
	Offset time.Duration
	// At is when the measurement was taken, on the manager's clock.
	At time.Time
}

// clockOffsets remembers the last measurement per host. It is the one
// piece of mutable state in checks.Service and is owned by it — never
// package-level, never shared between services.
type clockOffsets struct {
	mu        sync.Mutex
	byHost    map[uuid.UUID]ClockOffset
	lastPrune time.Time
}

func newClockOffsets() *clockOffsets {
	return &clockOffsets{byHost: map[uuid.UUID]ClockOffset{}}
}

// note records what obs says about each host's clock, measured against
// now. Only the newest reading per host counts: it is the one whose age
// is closest to pure clock difference, since everything older in the same
// batch may simply have been collected earlier or replayed.
func (c *clockOffsets) note(obs []Observation, now time.Time) {
	newest := make(map[uuid.UUID]time.Time, 1)
	for _, o := range obs {
		if at, ok := newest[o.HostID]; !ok || o.At.After(at) {
			newest[o.HostID] = o.At
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for host, at := range newest {
		c.byHost[host] = ClockOffset{Offset: now.Sub(at), At: now}
	}
	if now.Sub(c.lastPrune) < offsetPruneInterval {
		return
	}
	c.lastPrune = now
	for host, m := range c.byHost {
		if now.Sub(m.At) > offsetRetention {
			delete(c.byHost, host)
		}
	}
}

// lookup returns the last measurement for hostID, if this process has one.
func (c *clockOffsets) lookup(hostID uuid.UUID) (ClockOffset, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.byHost[hostID]
	return m, ok
}

// ClockOffset reports how far hostID's clock ran behind this manager's
// when its last readings arrived here, and whether there is a measurement
// at all. The host page renders it to tell a skewed clock apart from a
// host that stopped reporting — see this file's own doc comment for what
// the answer is and is not worth.
func (s *Service) ClockOffset(hostID uuid.UUID) (ClockOffset, bool) {
	return s.offsets.lookup(hostID)
}
