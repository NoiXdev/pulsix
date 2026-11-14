package agent

import (
	"context"
	"slices"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/secrets"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// Local checks are the checks section of the agent's configuration file
// (backlog 3.5a). They run in the scheduler next to the manager's checks
// and follow the file on every look at it: the startup look, every
// heartbeat and the periodic look below, which keeps them following the
// file while no stream is up. A refused file has no local checks, so its
// local checks stop as its secrets go.

const (
	// localLookInterval is how often Run looks at the file on its own,
	// whether or not a stream is up. It is half the watcher's period on
	// purpose: the watcher re-reads only when a full minute has passed
	// since its last read, and a one-minute ticker's ticks arrive a few
	// microseconds early or late, so about every other tick would land
	// just under the minute and be skipped — an edit could wait two
	// minutes. Looking every 30 s costs nothing (the watcher still reads
	// at most once a minute) and applies an edit within about a minute.
	localLookInterval = 30 * time.Second
	// maxLocalCheckProblems bounds the problems a heartbeat carries, as
	// the manager keeps no more.
	maxLocalCheckProblems = 32
)

// withLocalLookTicker replaces the ticker factory of the periodic look only
// (tests: a separate fake, so the session's ticker order stays as the
// cadence tests count it). By default the look uses the loop's own.
func withLocalLookTicker(fn tickerFactory) LoopOption {
	return func(l *Loop) { l.lookTicker = fn }
}

// startLocalLook looks at the file every localLookInterval until ctx
// ends, applying its local checks; the returned channel closes when it
// has stopped.
func (l *Loop) startLocalLook(ctx context.Context) <-chan struct{} {
	factory := l.lookTicker
	if factory == nil {
		factory = l.ticker
	}
	tick := factory(localLookInterval)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C():
				l.lookAndApplyLocal()
			}
		}
	}()
	return done
}

// lookAndApplyLocal looks at the file, installs its local checks in the
// scheduler and returns what it read. It logs the keys a check from the
// manager shadows when they changed since the last time — once per
// change, not once per look.
//
// localMu is held across the whole look: the startup look, the heartbeat
// and the periodic look each call this, and without it two looks
// straddling the watcher's re-read could apply the older set last (the
// scheduler running it again until the next look, its changed checks
// restarting twice) or store an outdated shadowed list, so a later real
// change back to it would go unlogged. Nothing that holds the watcher's
// or the scheduler's mutex takes localMu, and no caller holds it across
// a stream send.
func (l *Loop) lookAndApplyLocal() (secrets.Set, secrets.State) {
	l.localMu.Lock()
	set, state := l.secrets.Current()
	local := set.LocalChecks() // fresh copies: the scheduler owns them
	specs := make([]checks.Spec, 0, len(local))
	for _, c := range local {
		specs = append(specs, checks.Spec{Key: c.Key, Plugin: c.Plugin, Params: c.Params, Interval: c.Interval})
	}
	shadowed := l.scheduler.ApplyLocal(specs)
	changed := !slices.Equal(shadowed, l.shadowed)
	l.shadowed = shadowed
	l.localMu.Unlock()
	if changed && len(shadowed) > 0 {
		l.log.Warn("local checks shadowed by checks from the manager with the same key; the manager's checks run instead",
			"keys", shadowed)
	}
	return set, state
}

// runningLocalChecks is what the heartbeat reports as running: the file's
// local checks, key order, minus the shadowed keys. Params themselves
// never travel, only their canonical JSON.
func runningLocalChecks(local []secrets.LocalCheck, shadowed []string) []*agentpb.LocalCheck {
	var out []*agentpb.LocalCheck
	for _, c := range local {
		if slices.Contains(shadowed, c.Key) {
			continue
		}
		out = append(out, &agentpb.LocalCheck{
			Key:             c.Key,
			Plugin:          c.Plugin,
			ParamsJson:      c.ParamsJSON,
			IntervalSeconds: int32(c.Interval / time.Second),
		})
	}
	return out
}

// localCheckProblems is what the heartbeat reports as problems: the file's
// refused entries (line order), then each shadowed key (key order), at
// most maxLocalCheckProblems, each safe for a proto3 string.
func localCheckProblems(fileProblems, shadowed []string) []string {
	var out []string
	add := func(p string) {
		if len(out) < maxLocalCheckProblems {
			out = append(out, reportable(p))
		}
	}
	for _, p := range fileProblems {
		add(p)
	}
	for _, key := range shadowed {
		add(`check "` + key + `": a check from the manager has the same key`)
	}
	return out
}
