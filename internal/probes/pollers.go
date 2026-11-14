package probes

import (
	"context"
	"sync"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/snmp"
)

// pluginSNMP is the one plugin polled through a Poller: its rates need the
// previous poll's counters.
const pluginSNMP = "snmp"

// skippedLogEvery is how often a check's skipped interfaces are logged at
// most.
const skippedLogEvery = time.Hour

// msgInterfacesSkipped is the log message for a device that has more
// interfaces than the probe reports.
const msgInterfacesSkipped = "probe runner: the device has more than 128 interfaces; only the 128 with the lowest index are reported"

// snmpPoller is the production Poller: snmp's, polling with the
// manager's guard.
type snmpPoller struct {
	p *snmp.Poller
	g *probe.Guard
}

func (s snmpPoller) Poll(ctx context.Context, params map[string]any, address string, secret map[string]string) ([]probe.Observation, snmp.Info) {
	return s.p.Poll(ctx, s.g, params, address, secret)
}

// pollerFor returns slot s's poller, building it on the slot's first
// poll, or nil for a plugin without one. Run goroutine only. The refresh
// drops a poller when its slot goes or its target changes, and a lost lock
// drops them all; a probe still running keeps the one it was handed, and
// the slot's next probe (never concurrent with it) starts on a new one.
func (r *Runner) pollerFor(s slot, t target) Poller {
	if t.check.Plugin != pluginSNMP {
		return nil
	}
	p, ok := r.pollers[s]
	if !ok {
		p = r.newPoller()
		r.pollers[s] = p
	}
	return p
}

// logSkipped logs that the slot's device has more interfaces than are
// reported, at most once per skippedLogEvery per check.
func (r *Runner) logSkipped(ctx context.Context, s slot, t target) {
	if r.skipped.allow(s, r.clock()) {
		r.log.WarnContext(ctx, msgInterfacesSkipped, "org", t.org, "host", s.host, "check", s.key)
	}
}

// throttle allows one event per key and period. It forgets a key once its
// period is over, so it holds only the keys allowed within the last one.
// Safe for concurrent use.
type throttle struct {
	every time.Duration
	mu    sync.Mutex
	last  map[slot]time.Time
}

func newThrottle(every time.Duration) *throttle {
	return &throttle{every: every, last: map[slot]time.Time{}}
}

// allow reports whether s's event at now may happen: when none did within
// the period before now. A clock that went backwards counts as within it.
func (t *throttle) allow(s slot, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, at := range t.last {
		if now.Sub(at) >= t.every {
			delete(t.last, k)
		}
	}
	if _, ok := t.last[s]; ok {
		return false
	}
	t.last[s] = now
	return true
}
