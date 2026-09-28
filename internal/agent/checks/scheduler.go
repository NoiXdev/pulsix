// The scheduler: one timer per check, ticking its plugin on its own
// interval and handing whatever it collected to the session's sender
// (Task 8, v0.3b plan). It is the one place that turns the manager's
// resolved configuration ("Configuration delivery") into actual,
// running timers — and the one place that must NOT restart a timer just
// because a configuration push arrived: a fleet-wide push that reset
// every host's phase would make every check on every host measure at the
// same instant, over and over, which is exactly the load spike the
// per-check interval exists to avoid.
package checks

import (
	"context"
	"reflect"
	"sync"
	"time"
)

// Ticker is the periodic-tick source one scheduled check runs on — the
// same shape internal/agent's own Ticker already has, so its ticker
// factory (real or, in tests, fully controlled) can be handed to
// NewScheduler directly: a value satisfying internal/agent's Ticker
// satisfies this one too, by having the same two methods.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// Spec is one check as the agent should run it — decoded from the
// manager's wire CheckConfig, never carrying the wire types themselves,
// so this package stays free of any proto import.
type Spec struct {
	Key      string
	Plugin   string
	Params   map[string]any
	Interval time.Duration
}

// Config is the resolved configuration one CheckConfig push (or heal)
// delivers: the version the agent echoes back in its heartbeat, and the
// checks that make it up.
type Config struct {
	Version string
	Checks  []Spec
}

// Result is one Observation a plugin actually produced, stamped with the
// check key that produced it and when — Task 7's Observation plus the
// two things only the scheduler knows (the task's own dispatch note). Err
// carries straight through from the Observation that produced it: a
// plugin that could not collect one specific item/metric this tick
// reports it as its own error Result, exactly like a value or label one,
// so the session's sender ships it on the wire's "error" arm — a
// collection failure is not silently dropped, and the manager stores no
// sample for it while updating the current value's error text instead.
type Result struct {
	CheckKey string
	Item     string
	Metric   string
	Value    *float64
	Label    *string
	Err      string
	At       time.Time
}

// scheduledCheck is one running (or not-yet-started) check's state: the
// spec it was built from, its own ticker and a private stop signal that
// lets Apply retire exactly this check without touching any other's.
type scheduledCheck struct {
	spec   Spec
	ticker Ticker
	stop   chan struct{}
	// spawned records whether a goroutine actually owns this check. Only
	// a spawned check's ticker is stopped by that goroutine's own defer;
	// anything else — a check tracked before Run started, or one whose
	// plugin this build does not have (spawnLocked returns before
	// starting anything) — has to have its ticker stopped explicitly, or
	// nothing ever stops it.
	spawned bool
}

// Scheduler holds one timer per check key. NewScheduler wires it;
// Apply installs or updates the running configuration; Run drives every
// check's timer until its context ends.
type Scheduler struct {
	reg    map[string]Plugin
	now    func() time.Time
	ticker func(time.Duration) Ticker

	mu      sync.Mutex
	version string
	checks  map[string]*scheduledCheck
	running bool // Run has started: Apply may start/stop goroutines directly
	// finished records that Run has RETURNED. running is false both
	// before Run starts and after it ends, and the two mean opposite
	// things to Apply: before, a tracked check waits for Run's startup
	// loop; after, nothing will ever drive or stop it, so starting one
	// would only leak its ticker.
	finished bool
	ctx      context.Context
	sink     func([]Result)
	wg       sync.WaitGroup
}

// NewScheduler wires a Scheduler against reg (ordinarily checks.Registry(),
// fixtures in tests), an injected clock (Result.At, never a ticker's own
// reported time, so a fake clock and a fake ticker can be driven
// independently in tests) and a ticker factory (the loop's own, real or
// faked).
func NewScheduler(reg map[string]Plugin, now func() time.Time, ticker func(time.Duration) Ticker) *Scheduler {
	return &Scheduler{
		reg:    reg,
		now:    now,
		ticker: ticker,
		checks: make(map[string]*scheduledCheck),
	}
}

// Version reports the configuration version currently applied — what the
// loop echoes back in every heartbeat so the manager's convergence check
// (agentapi's processHeartbeat) can tell this agent is already up to
// date.
func (s *Scheduler) Version() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// Apply installs cfg as the running configuration. A check whose key
// disappears stops; a new key starts; an existing key whose plugin,
// params or interval changed is restarted (stopped, then started fresh,
// so ITS OWN phase resets); a key that is unchanged — same plugin, same
// params, same interval, whether or not the surrounding version changed
// — keeps its existing timer untouched. That last case is the one that
// matters: a configuration push that only edits one check's interval
// must not resynchronise every other check's phase, or a fleet-wide push
// turns into a fleet-wide load spike every time it repeats.
func (s *Scheduler) Apply(cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		// Run is one-shot and has already returned — a configuration
		// push still in flight when the process shut down. Nothing is
		// left to drive a timer, so creating one would leak it, and
		// nothing would be running this configuration, so claiming its
		// version would be a lie the heartbeat then tells.
		return
	}
	s.version = cfg.Version

	seen := make(map[string]struct{}, len(cfg.Checks))
	for _, spec := range cfg.Checks {
		seen[spec.Key] = struct{}{}
		if existing, ok := s.checks[spec.Key]; ok {
			if specsEqual(existing.spec, spec) {
				continue // unchanged: the running timer keeps its phase
			}
			s.stopLocked(spec.Key)
		}
		s.startLocked(spec)
	}
	for key := range s.checks {
		if _, ok := seen[key]; !ok {
			s.stopLocked(key)
		}
	}
}

// specsEqual compares everything about a check that a fresh timer would
// need to reflect. Key is deliberately not compared: it is already the
// map key both specs share by construction (Apply only ever calls this
// with two specs for the same key).
func specsEqual(a, b Spec) bool {
	return a.Plugin == b.Plugin && a.Interval == b.Interval && reflect.DeepEqual(a.Params, b.Params)
}

// startLocked creates spec's ticker and, if Run has already started,
// spawns its goroutine right away; otherwise Run's own startup loop
// spawns it once it begins. Called with s.mu held.
func (s *Scheduler) startLocked(spec Spec) {
	sc := &scheduledCheck{spec: spec, ticker: s.ticker(spec.Interval), stop: make(chan struct{})}
	s.checks[spec.Key] = sc
	if s.running {
		s.spawnLocked(spec.Key, sc)
	}
}

// spawnLocked starts sc's goroutine. Called with s.mu held; the goroutine
// itself runs unlocked. An unknown plugin name (a manager bug — the
// manager only ever pushes plugins its own validateParams already
// accepted) is silently skipped: the check is tracked so a later Apply
// can still add or remove it correctly, but nothing ever collects for
// it, which is the same "stay dumb, do nothing surprising" posture the
// rest of this package takes with data it cannot make sense of.
func (s *Scheduler) spawnLocked(key string, sc *scheduledCheck) {
	plugin, ok := s.reg[sc.spec.Plugin]
	if !ok {
		return // sc.spawned stays false: its ticker is nobody's but ours
	}
	sc.spawned = true
	s.wg.Add(1)
	go s.runCheck(s.ctx, key, plugin, sc, s.sink)
}

// stopLocked retires key's check: if a goroutine owns it, that goroutine
// is signalled to stop and stops its own ticker on the way out; otherwise
// the ticker is stopped directly here, because nothing else ever will.
// The condition is sc.spawned, not s.running: a check whose plugin this
// build does not have is tracked while Run is driving and still has no
// goroutine, so signalling its stop channel would leave the ticker
// running for ever. Called with s.mu held.
func (s *Scheduler) stopLocked(key string) {
	sc, ok := s.checks[key]
	if !ok {
		return
	}
	if sc.spawned {
		close(sc.stop)
	} else {
		sc.ticker.Stop()
	}
	delete(s.checks, key)
}

// runCheck is one check's whole lifetime: it fires on its own ticker,
// collects and hands whatever it got to sink, until ctx ends (the whole
// scheduler shutting down) or stop is closed (Apply retiring or
// restarting this one check).
func (s *Scheduler) runCheck(ctx context.Context, key string, plugin Plugin, sc *scheduledCheck, sink func([]Result)) {
	defer s.wg.Done()
	defer sc.ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sc.stop:
			return
		case <-sc.ticker.C():
			at := s.now()
			obs, err := plugin.Collect(ctx, sc.spec.Params)
			if len(obs) == 0 {
				_ = err // advisory, like facts.Collect and Plugin.Collect: nothing usable this tick
				continue
			}
			results := make([]Result, 0, len(obs))
			for _, o := range obs {
				results = append(results, Result{
					CheckKey: key, Item: o.Item, Metric: o.Metric,
					Value: o.Value, Label: o.Label, Err: o.Err, At: at,
				})
			}
			sink(results)
		}
	}
}

// Run drives every check's timer until ctx ends, then stops them all and
// waits for their goroutines to actually exit before returning — so a
// caller that has just received Run's return value can rely on nothing
// still being in flight. sink receives one batch per completed Collect
// call (never merged across checks): the session decides where a batch
// goes (its own sender, or the disconnect buffer).
//
// running flips to false under s.mu BEFORE wg.Wait() is ever called —
// deliberately, not merely for tidiness. A CheckConfig can arrive (and
// reach Apply -> startLocked -> spawnLocked -> wg.Add) at the exact
// instant the outer context is cancelled — production's own shutdown can
// produce this, a manager message still in flight when ctx ends — and
// sync.WaitGroup forbids a positive Add racing a Wait when the counter
// can reach zero: that is a genuine panic, not a race-detector-only
// complaint. Flipping running under the lock closes it rather than
// narrowing it: startLocked's own "if s.running" check (spawnLocked's
// only caller besides this method's own startup loop above) and this
// flip share one mutex, so the two total orders the runtime can pick
// between are both safe — either Apply's whole spawn-and-Add critical
// section completes and is visible before this flip runs (Wait, called
// only afterward, already accounts for it), or this flip completes first
// and Apply's later check of running sees false and never calls Add at
// all. There is no third, interleaved outcome a mutex can produce.
func (s *Scheduler) Run(ctx context.Context, sink func([]Result)) error {
	s.mu.Lock()
	s.ctx = ctx
	s.sink = sink
	s.running = true
	for key, sc := range s.checks {
		s.spawnLocked(key, sc)
	}
	s.mu.Unlock()

	<-ctx.Done()

	s.mu.Lock()
	s.running = false
	// finished goes up in the SAME critical section, so there is no
	// instant in which Apply sees "Run is not driving" and concludes
	// "Run has not started yet" — the one reading under which it would
	// create a ticker nothing can ever drive or stop.
	s.finished = true
	s.mu.Unlock()

	s.wg.Wait() // every spawned goroutine has returned and stopped its own ticker
	s.mu.Lock()
	// Whatever is left owns no goroutine (a check tracked before Run
	// began and cancelled before it could spawn, or one whose plugin this
	// build does not have), so its ticker is still running and nothing
	// else will stop it.
	for _, sc := range s.checks {
		if !sc.spawned {
			sc.ticker.Stop()
		}
	}
	s.checks = make(map[string]*scheduledCheck) // Run is one-shot
	s.mu.Unlock()
	return nil
}
