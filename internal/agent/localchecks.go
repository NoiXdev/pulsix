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
// heartbeat and the minute look below, which keeps them following the
// file while no stream is up. A refused file has no local checks, so its
// local checks stop as its secrets go.

const (
	// localLookInterval is how often Run looks at the file on its own,
	// whether or not a stream is up: the watcher reads it at most this
	// often anyway.
	localLookInterval = time.Minute
	// maxLocalCheckProblems bounds the problems a heartbeat carries, as
	// the manager keeps no more.
	maxLocalCheckProblems = 32
)

// withLocalLookTicker replaces the ticker factory of the minute look only
// (tests: a separate fake, so the session's ticker order stays as the
// cadence tests count it). By default the look uses the loop's own.
func withLocalLookTicker(fn tickerFactory) LoopOption {
	return func(l *Loop) { l.lookTicker = fn }
}

// startLocalLook looks at the file once a minute until ctx ends, applying
// its local checks; the returned channel closes when it has stopped.
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
				set, _ := l.secrets.Current()
				l.applyLocal(set)
			}
		}
	}()
	return done
}

// applyLocal installs set's local checks in the scheduler and logs the
// keys a check from the manager shadows when they changed since the last
// time — once per change, not once per look.
func (l *Loop) applyLocal(set secrets.Set) {
	local := set.LocalChecks() // fresh copies: the scheduler owns them
	specs := make([]checks.Spec, 0, len(local))
	for _, c := range local {
		specs = append(specs, checks.Spec{Key: c.Key, Plugin: c.Plugin, Params: c.Params, Interval: c.Interval})
	}
	shadowed := l.scheduler.ApplyLocal(specs)

	l.localMu.Lock()
	changed := !slices.Equal(shadowed, l.shadowed)
	l.shadowed = shadowed
	l.localMu.Unlock()
	if changed && len(shadowed) > 0 {
		l.log.Warn("local checks shadowed by checks from the manager with the same key; the manager's run instead",
			"keys", shadowed)
	}
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
