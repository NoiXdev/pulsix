package rules_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/rules"
)

// The ticker's own tests run entirely against a fake EvalStore: what is
// under test here is the orchestration — claim, load, ask for exactly the
// windows the rules need, evaluate, apply — and the tick discipline around
// it, never any SQL. The store's own behaviour (the claim, the window
// statement, the writes) is pinned by due_sql_test.go against a real
// database.

var tickEpoch = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// The hosts the fake store hands out, fixed so a failure names a host
// rather than a fresh uuid.
var (
	hostA = uuid.MustParse("aaaaaaaa-0000-4000-8000-000000000001")
	hostB = uuid.MustParse("bbbbbbbb-0000-4000-8000-000000000002")
	hostC = uuid.MustParse("cccccccc-0000-4000-8000-000000000003")
	tickO = uuid.MustParse("00000000-0000-4000-8000-0000000000ff")
)

func due(host uuid.UUID) rules.Due { return rules.Due{OrgID: tickO, HostID: host} }

// fakeEvalStore claims a fixed list of hosts and records everything the
// ticker asked it to do. ProcessDue counts the hosts fn returned nil for,
// exactly as the SQL store's contract says it must.
type fakeEvalStore struct {
	mu sync.Mutex

	due      []rules.Due
	load     rules.HostLoad
	windows  map[rules.WindowRequest]float64
	claimErr error
	loadErr  map[uuid.UUID]error
	applyErr map[uuid.UUID]error

	limits   []int
	nows     []time.Time
	loaded   []uuid.UUID
	reqs     map[uuid.UUID][]rules.WindowRequest
	windowAt map[uuid.UUID]time.Time
	applied  map[uuid.UUID]rules.Result
}

func newFakeEvalStore(load rules.HostLoad, dues ...rules.Due) *fakeEvalStore {
	return &fakeEvalStore{
		due: dues, load: load,
		loadErr: map[uuid.UUID]error{}, applyErr: map[uuid.UUID]error{},
		reqs: map[uuid.UUID][]rules.WindowRequest{}, windowAt: map[uuid.UUID]time.Time{},
		applied: map[uuid.UUID]rules.Result{},
	}
}

func (f *fakeEvalStore) ProcessDue(ctx context.Context, now time.Time, limit int, fn func(context.Context, rules.Due) error) (int, error) {
	f.mu.Lock()
	f.limits = append(f.limits, limit)
	f.nows = append(f.nows, now)
	claimErr, dues := f.claimErr, f.due
	f.mu.Unlock()
	if claimErr != nil {
		return 0, claimErr
	}
	n := 0
	for i, d := range dues {
		if i >= limit {
			break
		}
		if err := fn(ctx, d); err != nil {
			continue // the failed host's own transaction rolled back
		}
		n++
	}
	return n, nil
}

func (f *fakeEvalStore) Load(_ context.Context, d rules.Due) (rules.HostLoad, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loaded = append(f.loaded, d.HostID)
	if err := f.loadErr[d.HostID]; err != nil {
		return rules.HostLoad{}, err
	}
	return f.load, nil
}

func (f *fakeEvalStore) Windows(_ context.Context, d rules.Due, reqs []rules.WindowRequest, now time.Time) (map[rules.WindowRequest]float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs[d.HostID] = reqs
	f.windowAt[d.HostID] = now
	return f.windows, nil
}

func (f *fakeEvalStore) Apply(_ context.Context, d rules.Due, res rules.Result) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.applyErr[d.HostID]; err != nil {
		return err
	}
	f.applied[d.HostID] = res
	return nil
}

func (f *fakeEvalStore) appliedFor(host uuid.UUID) (rules.Result, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, ok := f.applied[host]
	return res, ok
}

// cpuLoad is one host's inputs: cpu.used_pct at 95, previously ok, judged
// by a crit rule at 90. Evaluating it yields crit and one change.
func cpuLoad() rules.HostLoad {
	r := evalRule(critID, cpuT, rules.SeverityCrit, compare(">", metric("cpu.used_pct"), number(90)))
	return rules.HostLoad{
		Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, tickEpoch, time.Minute)},
		Effective: []rules.Effective{eff(r, cpuT)},
		Previous:  prev(was(cpuT, rules.StateOK, tickEpoch.Add(-time.Hour))),
		Changes30: map[rules.Tuple]int{},
	}
}

// elapsedClock is a clock whose first reading is start and whose every later
// reading is start+elapsed, so a test states how long a tick took without
// sleeping and without depending on how often the ticker reads the clock.
type elapsedClock struct {
	mu      sync.Mutex
	start   time.Time
	elapsed time.Duration
	reads   int
}

func (c *elapsedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	if c.reads == 1 {
		return c.start
	}
	return c.start.Add(c.elapsed)
}

// quietLogger returns a logger writing into buf, so a test can assert what
// was logged and nothing reaches the test's own output.
func quietLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// TestTickEvaluatesEveryClaimedHost is the brief's batch of three, one of
// which fails: the other two are still applied, and the count Tick reports
// is the number of hosts that actually got through — not the number
// claimed.
func TestTickEvaluatesEveryClaimedHost(t *testing.T) {
	store := newFakeEvalStore(cpuLoad(), due(hostA), due(hostB), due(hostC))
	store.loadErr[hostB] = errors.New("boom")
	log, buf := quietLogger()

	tk := rules.NewTicker(store, rules.WithTickerClock(func() time.Time { return tickEpoch }), rules.WithTickerLogger(log))
	n, err := tk.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 2 {
		t.Fatalf("Tick evaluated %d hosts, want 2 (the third failed)", n)
	}
	for _, host := range []uuid.UUID{hostA, hostC} {
		res, ok := store.appliedFor(host)
		if !ok {
			t.Fatalf("host %s was claimed but never applied", host)
		}
		if len(res.States) != 1 || res.States[0].State != rules.StateCrit {
			t.Fatalf("host %s: states = %+v, want one crit", host, res.States)
		}
		if len(res.Changes) != 1 {
			t.Fatalf("host %s: changes = %+v, want exactly one", host, res.Changes)
		}
		if res.Host.State != rules.StateCrit {
			t.Fatalf("host %s: roll-up = %s, want crit", host, res.Host.State)
		}
	}
	if _, ok := store.appliedFor(hostB); ok {
		t.Fatal("the host whose load failed was applied anyway")
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("the failed host was not logged; log was %q", buf.String())
	}
}

// TestTickStampsEveryHostWithTheInjectedClock pins that the pure
// function's "now" is the ticker's own clock reading, taken once per
// batch, and that the window fetch is anchored to the same moment — a
// window measured from a later instant than the state it decides would
// judge one tuple against two different presents.
func TestTickStampsEveryHostWithTheInjectedClock(t *testing.T) {
	store := newFakeEvalStore(cpuLoad(), due(hostA))
	tk := rules.NewTicker(store, rules.WithTickerClock(func() time.Time { return tickEpoch }))
	if _, err := tk.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	res, _ := store.appliedFor(hostA)
	if !res.States[0].EvaluatedAt.Equal(tickEpoch) {
		t.Fatalf("EvaluatedAt = %v, want the clock's %v", res.States[0].EvaluatedAt, tickEpoch)
	}
	if len(store.nows) != 1 || !store.nows[0].Equal(tickEpoch) {
		t.Fatalf("ProcessDue saw now = %v, want [%v]", store.nows, tickEpoch)
	}
}

// TestTickFetchesExactlyTheWindowsTheRulesAskFor is the round trip the
// ticker exists to keep in step: WindowRequests is computed from the
// loaded snapshot, the store is asked for precisely those, and the answer
// reaches the evaluation. A ticker that skipped the fetch would leave the
// rule unmatched — a window with no value is a non-match — so the crit
// below is only reachable through the store's answer.
func TestTickFetchesExactlyTheWindowsTheRulesAskFor(t *testing.T) {
	r := evalRule(critID, cpuT, rules.SeverityCrit, compare(">", window("cpu.used_pct", "avg", 600), number(90)))
	load := rules.HostLoad{
		Tuples:    []rules.TupleSnapshot{snap(cpuT, 10, tickEpoch, time.Minute)},
		Effective: []rules.Effective{eff(r, cpuT)},
	}
	want := rules.WindowRequest{Key: rules.ItemKey{CheckKey: "cpu"}, Metric: "cpu.used_pct", Agg: "avg", Seconds: 600}
	store := newFakeEvalStore(load, due(hostA))
	store.windows = map[rules.WindowRequest]float64{want: 95}

	tk := rules.NewTicker(store, rules.WithTickerClock(func() time.Time { return tickEpoch }))
	if _, err := tk.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	store.mu.Lock()
	reqs, at := store.reqs[hostA], store.windowAt[hostA]
	store.mu.Unlock()
	if len(reqs) != 1 || reqs[0] != want {
		t.Fatalf("Windows asked for %+v, want exactly [%+v]", reqs, want)
	}
	if !at.Equal(tickEpoch) {
		t.Fatalf("Windows anchored at %v, want the tick's %v", at, tickEpoch)
	}
	res, _ := store.appliedFor(hostA)
	if res.States[0].State != rules.StateCrit {
		t.Fatalf("state = %s, want crit — the fetched window never reached the evaluation", res.States[0].State)
	}
}

// TestTickSkipsTheWindowFetchWhenNoRuleNeedsOne: no windowed rule, no
// round trip. The hot path of a fleet whose rules are all thresholds must
// not pay for a statement with an empty VALUES list.
func TestTickSkipsTheWindowFetchWhenNoRuleNeedsOne(t *testing.T) {
	store := newFakeEvalStore(cpuLoad(), due(hostA))
	tk := rules.NewTicker(store, rules.WithTickerClock(func() time.Time { return tickEpoch }))
	if _, err := tk.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.reqs) != 0 {
		t.Fatalf("Windows was called with %+v, want no call at all", store.reqs)
	}
}

// TestTickReportsAClaimFailure: a failing claim is the batch's error, not
// a silent zero.
func TestTickReportsAClaimFailure(t *testing.T) {
	store := newFakeEvalStore(cpuLoad(), due(hostA))
	store.claimErr = errors.New("claim failed")
	tk := rules.NewTicker(store, rules.WithTickerClock(func() time.Time { return tickEpoch }))
	n, err := tk.Tick(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claim failed") {
		t.Fatalf("Tick error = %v, want the claim's own", err)
	}
	if n != 0 {
		t.Fatalf("Tick = %d, want 0", n)
	}
}

// TestTickDefaults pins the two documented defaults: a batch of 200 and a
// 15-second interval (the latter observable through what counts as an
// overrun).
func TestTickDefaults(t *testing.T) {
	store := newFakeEvalStore(cpuLoad(), due(hostA))
	clock := &elapsedClock{start: tickEpoch, elapsed: 14 * time.Second}
	tk := rules.NewTicker(store, rules.WithTickerClock(clock.now))
	if _, err := tk.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(store.limits) != 1 || store.limits[0] != 200 {
		t.Fatalf("ProcessDue limits = %v, want [200]", store.limits)
	}
	if got := tk.Overruns(); got != 0 {
		t.Fatalf("overruns = %d after a 14 s tick under the 15 s default, want 0", got)
	}
}

// TestTickCountsAndLogsAnOverrunOnce is backlog 2.60's lesson: a tick
// longer than its interval is counted and logged — once per occurrence,
// with the batch size and the elapsed time — and Tick still returns after
// its one batch rather than re-entering to catch up.
func TestTickCountsAndLogsAnOverrunOnce(t *testing.T) {
	store := newFakeEvalStore(cpuLoad(), due(hostA), due(hostB))
	clock := &elapsedClock{start: tickEpoch, elapsed: 40 * time.Second}
	log, buf := quietLogger()
	tk := rules.NewTicker(store,
		rules.WithTickerClock(clock.now), rules.WithInterval(15*time.Second),
		rules.WithBatch(2), rules.WithTickerLogger(log))

	n, err := tk.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 2 {
		t.Fatalf("Tick = %d, want 2", n)
	}
	if got := tk.Overruns(); got != 1 {
		t.Fatalf("overruns = %d, want 1", got)
	}
	if got := strings.Count(buf.String(), "evaluation tick overran"); got != 1 {
		t.Fatalf("overrun logged %d times, want exactly 1; log was %q", got, buf.String())
	}
	line := buf.String()
	for _, want := range []string{"hosts=2", "elapsed=40s"} {
		if !strings.Contains(line, want) {
			t.Fatalf("overrun line %q does not carry %q", line, want)
		}
	}
	if len(store.limits) != 1 {
		t.Fatalf("ProcessDue was called %d times in one Tick, want exactly 1 (no catch-up loop)", len(store.limits))
	}
}

// TestRunTicksUntilItsContextEnds: Run keeps ticking on its interval and
// returns promptly when the context is cancelled, which is what makes
// app.Close's stop wait bounded.
func TestRunTicksUntilItsContextEnds(t *testing.T) {
	store := newFakeEvalStore(cpuLoad(), due(hostA))
	tk := rules.NewTicker(store,
		rules.WithTickerClock(func() time.Time { return tickEpoch }),
		rules.WithInterval(2*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	errC := make(chan error, 1)
	go func() { errC <- tk.Run(ctx) }()

	// Wait until at least two batches have been claimed, so "it keeps
	// ticking" is observed rather than assumed.
	deadline := time.After(2 * time.Second)
	for {
		store.mu.Lock()
		ticks := len(store.limits)
		store.mu.Unlock()
		if ticks >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("Run produced %d ticks in 2 s, want at least 2", ticks)
		case <-time.After(time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-errC:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2 s of its context being cancelled")
	}
}

// TestRunLogsATickFailureAndKeepsGoing: one failed claim must not end the
// loop — the next interval tries again.
func TestRunLogsATickFailureAndKeepsGoing(t *testing.T) {
	store := newFakeEvalStore(cpuLoad(), due(hostA))
	store.claimErr = errors.New("claim failed")
	log, buf := quietLogger()
	tk := rules.NewTicker(store,
		rules.WithTickerClock(func() time.Time { return tickEpoch }),
		rules.WithInterval(2*time.Millisecond), rules.WithTickerLogger(log))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errC := make(chan error, 1)
	go func() { errC <- tk.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		store.mu.Lock()
		ticks := len(store.limits)
		store.mu.Unlock()
		if ticks >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("Run stopped after %d failing ticks, want it to keep going", ticks)
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-errC
	if !strings.Contains(buf.String(), "claim failed") {
		t.Fatalf("the failing tick was not logged; log was %q", buf.String())
	}
}

// panicStore panics on its first claim and behaves on every later one.
type panicStore struct {
	*fakeEvalStore
	panicked bool
}

func (p *panicStore) ProcessDue(ctx context.Context, now time.Time, limit int, fn func(context.Context, rules.Due) error) (int, error) {
	p.mu.Lock()
	first := !p.panicked
	p.panicked = true
	p.mu.Unlock()
	if first {
		panic("a rule tree exploded")
	}
	return p.fakeEvalStore.ProcessDue(ctx, now, limit, fn)
}

// TestRunSurvivesAPanickingTick: a bug anywhere below the ticker costs one
// tick, never the ticker's goroutine — and with it the manager process.
func TestRunSurvivesAPanickingTick(t *testing.T) {
	store := &panicStore{fakeEvalStore: newFakeEvalStore(cpuLoad(), due(hostA))}
	log, buf := quietLogger()
	tk := rules.NewTicker(store,
		rules.WithTickerClock(func() time.Time { return tickEpoch }),
		rules.WithInterval(2*time.Millisecond), rules.WithTickerLogger(log))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errC := make(chan error, 1)
	go func() { errC <- tk.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		if _, ok := store.appliedFor(hostA); ok {
			break // a later tick ran to completion, so the panic cost only its own
		}
		select {
		case <-deadline:
			t.Fatal("Run never produced a completed tick after the panicking one")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-errC
	if !strings.Contains(buf.String(), "evaluation tick panicked") {
		t.Fatalf("the panic was not logged; log was %q", buf.String())
	}
}
