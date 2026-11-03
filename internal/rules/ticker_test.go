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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// stubTx is a pgx.Tx that is never actually used: these orchestration
// tests exercise fakeEvalStore, not EvalSQLStore, so nothing ever calls a
// method on it — it exists only so fakeEvalStore.ProcessDue has something
// to bind under the claimed-transaction context key (via
// rules.ContextWithClaimedTx, export_test.go's own seam) the way
// EvalSQLStore's real evaluateOne does, which is what lets Ticker.evaluate
// reach the sink at all.
//
// It implements the interface rather than embedding a nil pgx.Tx
// (backlog 4.32). "Never actually used" is a property of today's tests,
// not of the type: the first orchestration test that does reach for the
// transaction would, with an embedded nil, fail as a nil-pointer
// dereference inside pgx, with a stack that says nothing about why.
// Every method below panics by name instead, and the message names the
// decision the author then has to make — a real transaction through
// testdb, or a real behaviour on this stub.
//
// A panic rather than t.Fatalf so the zero value stays usable:
// threading a *testing.T through would mean changing
// newFakeEvalStore's signature and every one of its call sites, for no
// gain over a message that already says everything.
type stubTx struct{}

func (s stubTx) unused(method string) {
	panic("stubTx." + method + " was called: these orchestration tests bind a transaction only so the " +
		"claimed-tx context key is populated, and this stub can execute nothing. Either the code under " +
		"test now needs a real transaction (use testdb) or this stub needs a real behaviour.")
}

func (s stubTx) Begin(context.Context) (pgx.Tx, error) { s.unused("Begin"); return nil, nil }
func (s stubTx) Commit(context.Context) error          { s.unused("Commit"); return nil }
func (s stubTx) Rollback(context.Context) error        { s.unused("Rollback"); return nil }
func (s stubTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	s.unused("CopyFrom")
	return 0, nil
}
func (s stubTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	s.unused("SendBatch")
	return nil
}
func (s stubTx) LargeObjects() pgx.LargeObjects { s.unused("LargeObjects"); return pgx.LargeObjects{} }
func (s stubTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	s.unused("Prepare")
	return nil, nil
}
func (s stubTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	s.unused("Exec")
	return pgconn.CommandTag{}, nil
}
func (s stubTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	s.unused("Query")
	return nil, nil
}
func (s stubTx) QueryRow(context.Context, string, ...any) pgx.Row { s.unused("QueryRow"); return nil }
func (s stubTx) Conn() *pgx.Conn                                  { s.unused("Conn"); return nil }

// fakeSink is the rules.AlertSink test double. Apply records exactly what
// it was called with, in order — before checking applyErr, not after, so a
// failure-path test can still assert what the sink was reached with rather
// than inferring it from a log line — and Sweep counts its own calls. Both
// can be made to fail, for the tests that exercise the two kinds of "leave
// it due".
type fakeSink struct {
	mu sync.Mutex

	calls    []rules.AlertInput
	applyErr error
	// onApply, when set, runs on every Apply call with the tx it was
	// handed, before applyErr is consulted. TestEvaluateFeedsTheAlertSink-
	// InTheHostTransaction (due_sql_test.go) is the one test that sets it:
	// it reads the row straight through tx to prove the sink is on the
	// host's own still-uncommitted transaction, not merely that *some*
	// pgx.Tx value — nil included, since this type discards its argument
	// entirely otherwise — was passed. An error here fails Apply the same
	// way applyErr does.
	onApply func(ctx context.Context, tx pgx.Tx, in rules.AlertInput) error

	sweeps   int
	sweepErr error
}

func (s *fakeSink) Apply(ctx context.Context, tx pgx.Tx, in rules.AlertInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, in)
	if s.onApply != nil {
		if err := s.onApply(ctx, tx, in); err != nil {
			return err
		}
	}
	return s.applyErr
}

func (s *fakeSink) Sweep(_ context.Context, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweeps++
	return s.sweepErr
}

// fakeEvalStore claims a fixed list of hosts and records everything the
// ticker asked it to do. ProcessDue counts the hosts fn returned nil for,
// exactly as the SQL store's contract says it must. It also binds a stub
// claimed-transaction to fn's own context (see stubTx) — the one piece of
// EvalSQLStore.evaluateOne's real behaviour these orchestration tests
// still need, now that Ticker.evaluate reaches for it after every
// successful store.Apply.
type fakeEvalStore struct {
	mu sync.Mutex

	due      []rules.Due
	load     rules.HostLoad
	windows  map[rules.WindowRequest]float64
	claimErr error
	loadErr  map[uuid.UUID]error
	// recordLoadDeadline makes Load count the calls whose context carried
	// a deadline — the batch's own, threaded down to the per-host work.
	recordLoadDeadline bool
	loadDeadlines      int
	applyErr           map[uuid.UUID]error

	limits    []int
	nows      []time.Time
	deadlines []time.Time
	loaded    []uuid.UUID
	reqs      map[uuid.UUID][]rules.WindowRequest
	windowAt  map[uuid.UUID]time.Time
	applied   map[uuid.UUID]rules.Result
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
	if deadline, ok := ctx.Deadline(); ok {
		f.deadlines = append(f.deadlines, deadline)
	}
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
		if err := fn(rules.ContextWithClaimedTx(ctx, stubTx{}), d); err != nil {
			continue // the failed host's own transaction rolled back
		}
		n++
	}
	return n, nil
}

func (f *fakeEvalStore) Load(ctx context.Context, d rules.Due) (rules.HostLoad, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loaded = append(f.loaded, d.HostID)
	if _, ok := ctx.Deadline(); ok && f.recordLoadDeadline {
		f.loadDeadlines++
	}
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

	tk := rules.NewTicker(store, &fakeSink{}, rules.WithTickerClock(func() time.Time { return tickEpoch }), rules.WithTickerLogger(log))
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
	tk := rules.NewTicker(store, &fakeSink{}, rules.WithTickerClock(func() time.Time { return tickEpoch }))
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

	tk := rules.NewTicker(store, &fakeSink{}, rules.WithTickerClock(func() time.Time { return tickEpoch }))
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
	tk := rules.NewTicker(store, &fakeSink{}, rules.WithTickerClock(func() time.Time { return tickEpoch }))
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
	tk := rules.NewTicker(store, &fakeSink{}, rules.WithTickerClock(func() time.Time { return tickEpoch }))
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
	tk := rules.NewTicker(store, &fakeSink{}, rules.WithTickerClock(clock.now))
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
	tk := rules.NewTicker(store, &fakeSink{},
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
	tk := rules.NewTicker(store, &fakeSink{},
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
	tk := rules.NewTicker(store, &fakeSink{},
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
	if err := <-errC; !errors.Is(err, context.Canceled) {
		t.Errorf("Run after its context was cancelled = %v, want context.Canceled", err)
	}
	if !strings.Contains(buf.String(), "claim failed") {
		t.Fatalf("the failing tick was not logged; log was %q", buf.String())
	}
}

// panicStore panics on its first claim and behaves on every later one.
// TestTickLogsWhatBecomesOfAFailedHost: a host whose evaluation fails is
// held out of the claim for a while (EvalSQLStore.evaluateOne), not left due
// for the next tick; only a failure in a batch whose context is done leaves
// the host due as it is. The log line says which of the two happened.
func TestTickLogsWhatBecomesOfAFailedHost(t *testing.T) {
	t.Run("host failure", func(t *testing.T) {
		store := newFakeEvalStore(cpuLoad(), due(hostA))
		store.loadErr[hostA] = errors.New("boom")
		log, buf := quietLogger()
		tk := rules.NewTicker(store, &fakeSink{}, rules.WithTickerClock(func() time.Time { return tickEpoch }), rules.WithTickerLogger(log))
		if _, err := tk.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if !strings.Contains(buf.String(), "held out of the claim for a while, then retried") || strings.Contains(buf.String(), "next tick") {
			t.Fatalf("want the failed host logged as held out of the claim; log was %q", buf.String())
		}
	})
	t.Run("batch cut short", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		store := &cancelOnLoadStore{fakeEvalStore: newFakeEvalStore(cpuLoad(), due(hostA)), cancel: cancel}
		log, buf := quietLogger()
		tk := rules.NewTicker(store, &fakeSink{}, rules.WithTickerClock(func() time.Time { return tickEpoch }), rules.WithTickerLogger(log))
		_, _ = tk.Tick(ctx)
		if !strings.Contains(buf.String(), "batch cut short; the host stays due for the next tick") || strings.Contains(buf.String(), "held out of the claim") {
			t.Fatalf("want the host logged as still due; log was %q", buf.String())
		}
	})
}

// cancelOnLoadStore cancels the tick's context from inside Load and fails
// that load, as a batch deadline firing mid-host does.
type cancelOnLoadStore struct {
	*fakeEvalStore
	cancel context.CancelFunc
}

func (c *cancelOnLoadStore) Load(ctx context.Context, d rules.Due) (rules.HostLoad, error) {
	c.cancel()
	<-ctx.Done()
	return rules.HostLoad{}, ctx.Err()
}

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
	tk := rules.NewTicker(store, &fakeSink{},
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
	if err := <-errC; !errors.Is(err, context.Canceled) {
		t.Errorf("Run after its context was cancelled = %v, want context.Canceled", err)
	}
	if !strings.Contains(buf.String(), "evaluation tick panicked") {
		t.Fatalf("the panic was not logged; log was %q", buf.String())
	}
}

// TestTickBoundsItsBatchWithADeadline is the bound that makes this claim
// protocol survivable (see batchDeadlineFactor): every batch runs under
// batchDeadlineFactor x the interval, never under less than the floor. A
// batch without one, blocked acquiring a second pool connection while
// holding the first and the batch's row locks, would never end on its own
// — this manager would stop evaluating, and every other manager would
// SKIP LOCKED past the rows it holds.
func TestTickBoundsItsBatchWithADeadline(t *testing.T) {
	cases := []struct {
		name     string
		interval time.Duration
		want     time.Duration
	}{
		{name: "four times the interval", interval: time.Hour, want: 4 * time.Hour},
		{name: "never under the floor", interval: time.Millisecond, want: 10 * time.Second},
		// The shipped default: 4 x 15 s, comfortably over the floor.
		{name: "the default interval", interval: 0, want: 60 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeEvalStore(cpuLoad(), due(hostA))
			opts := []rules.TickerOption{rules.WithTickerClock(func() time.Time { return tickEpoch })}
			if tc.interval > 0 {
				opts = append(opts, rules.WithInterval(tc.interval))
			}
			tk := rules.NewTicker(store, &fakeSink{}, opts...)

			before := time.Now()
			if _, err := tk.Tick(context.Background()); err != nil {
				t.Fatalf("Tick: %v", err)
			}
			if len(store.deadlines) != 1 {
				t.Fatalf("ProcessDue ran with %d deadlines, want exactly 1 — an unbounded batch can hang for ever", len(store.deadlines))
			}
			got := store.deadlines[0].Sub(before)
			if got < tc.want-5*time.Second || got > tc.want+5*time.Second {
				t.Fatalf("batch deadline = %v from the call, want about %v", got, tc.want)
			}
		})
	}
}

// TestTickPassesTheBatchDeadlineDownToEveryHost: the deadline has to reach
// the per-host work, since that is where the blocking happens (Load calls
// into the check pipeline, which acquires a pool connection of its own).
func TestTickPassesTheBatchDeadlineDownToEveryHost(t *testing.T) {
	store := newFakeEvalStore(cpuLoad(), due(hostA))
	store.recordLoadDeadline = true
	tk := rules.NewTicker(store, &fakeSink{}, rules.WithTickerClock(func() time.Time { return tickEpoch }))
	if _, err := tk.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.loadDeadlines != 1 {
		t.Fatalf("Load ran with a deadline %d times, want 1", store.loadDeadlines)
	}
}

// TestNewTickerRefusesANilSink pins the constructor's own refusal. The
// alerts sink is a required positional argument, not a TickerOption — the
// same ruling internal/items' own required arguments got in v0.3b-3 — so
// that a forgotten wire is a panic at construction, or better, a compile
// error from the missing parameter, rather than half the alerting silently
// never running. No store, no database, no evaluation is needed to prove
// it: the panic belongs at construction, before any of that runs.
func TestNewTickerRefusesANilSink(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewTicker(store, nil) returned a ticker, want a panic at construction")
		}
	}()
	rules.NewTicker(newFakeEvalStore(cpuLoad()), nil)
}

// bareEvalStore calls fn without ever binding a claimed transaction to its
// context — exactly the mistake Ticker.evaluate's own claimTx(ctx) check
// exists to catch. fakeEvalStore (used everywhere else in this file) binds
// a stub one unconditionally via rules.ContextWithClaimedTx precisely so
// the orchestration tests above don't have to care about this at all; this
// type exists solely so one test still exercises the branch they all now
// skip.
type bareEvalStore struct{ load rules.HostLoad }

func (b bareEvalStore) ProcessDue(ctx context.Context, _ time.Time, _ int, fn func(context.Context, rules.Due) error) (int, error) {
	if err := fn(ctx, due(hostA)); err != nil {
		return 0, nil
	}
	return 1, nil
}

func (b bareEvalStore) Load(context.Context, rules.Due) (rules.HostLoad, error) { return b.load, nil }

func (b bareEvalStore) Windows(context.Context, rules.Due, []rules.WindowRequest, time.Time) (map[rules.WindowRequest]float64, error) {
	return nil, nil
}

func (b bareEvalStore) Apply(context.Context, rules.Due, rules.Result) error { return nil }

// TestEvaluateRefusesToApplyAlertsOutsideAClaim: without a claimed
// transaction on the context, evaluate must refuse to call the sink at all
// — falling back to an unclaimed connection would let the sink write
// outside the host's atomicity, the same hazard Apply itself already
// refuses (errNoClaimTx). The host is left due, and the refusal is logged
// rather than silent, since nothing else would ever say why this host
// stopped advancing.
func TestEvaluateRefusesToApplyAlertsOutsideAClaim(t *testing.T) {
	store := bareEvalStore{load: cpuLoad()}
	sink := &fakeSink{}
	log, buf := quietLogger()
	tk := rules.NewTicker(store, sink, rules.WithTickerClock(func() time.Time { return tickEpoch }), rules.WithTickerLogger(log))

	n, err := tk.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 0 {
		t.Fatalf("Tick evaluated %d hosts, want 0 — no transaction was ever claimed", n)
	}
	if len(sink.calls) != 0 {
		t.Fatal("the sink was reached even though no transaction was claimed")
	}
	if !strings.Contains(buf.String(), "apply alerts outside a claimed host's transaction") {
		t.Fatalf("the refusal was not logged; log was %q", buf.String())
	}
}

// TestTickCallsSweepOnce is the sweep half of the wiring: Sweep runs
// exactly once per Tick, independent of how many hosts were due —
// including a tick that claimed none at all, which is precisely the case
// a per-host sweep would miss, since the sweep is driven by open alerts
// rather than by a due queue and must answer for subjects no host's
// evaluation reaches this tick. A ticker that called Sweep once per
// evaluated host, or skipped it on an empty batch, would both pass every
// other test in this file and still fail this one.
func TestTickCallsSweepOnce(t *testing.T) {
	t.Run("hosts due", func(t *testing.T) {
		store := newFakeEvalStore(cpuLoad(), due(hostA), due(hostB))
		sink := &fakeSink{}
		tk := rules.NewTicker(store, sink, rules.WithTickerClock(func() time.Time { return tickEpoch }))
		if _, err := tk.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if sink.sweeps != 1 {
			t.Fatalf("Sweep ran %d times for a batch of 2 hosts, want exactly 1", sink.sweeps)
		}
	})
	t.Run("no hosts due", func(t *testing.T) {
		store := newFakeEvalStore(cpuLoad()) // claims nothing
		sink := &fakeSink{}
		tk := rules.NewTicker(store, sink, rules.WithTickerClock(func() time.Time { return tickEpoch }))
		if _, err := tk.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if sink.sweeps != 1 {
			t.Fatalf("Sweep ran %d times on a tick with nothing due, want exactly 1 — this is precisely the tick an orphaned host needs sweeping on", sink.sweeps)
		}
	})
}

// TestSweepErrorDoesNotFailTheTick: a sweep that could not run is a
// delayed cleanup, not a reason to leave every evaluated host due again —
// so its error is logged and Tick's own result is unaffected.
func TestSweepErrorDoesNotFailTheTick(t *testing.T) {
	store := newFakeEvalStore(cpuLoad(), due(hostA))
	sink := &fakeSink{sweepErr: errors.New("sweep boom")}
	log, buf := quietLogger()
	tk := rules.NewTicker(store, sink,
		rules.WithTickerClock(func() time.Time { return tickEpoch }), rules.WithTickerLogger(log))

	n, err := tk.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick returned %v, want nil — a sweep failure must not fail the tick", err)
	}
	if n != 1 {
		t.Fatalf("Tick evaluated %d hosts, want 1 — unaffected by the sweep failure", n)
	}
	if !strings.Contains(buf.String(), "sweep boom") {
		t.Fatalf("the sweep failure was not logged; log was %q", buf.String())
	}
}

// TestTickFeedsTheAlertSinkPerEvaluatedHost is the fake-store half of the
// task's own test: the sink is called once per successfully-applied host,
// with the post-evaluation states and roll-up the store itself just wrote
// — not a diff, and not called at all for the host whose Load failed. The
// real per-host-transaction half of this claim — that the sink runs on the
// same pgx.Tx the store write just committed through — can only be shown
// against the real EvalSQLStore, so it lives in due_sql_test.go as
// TestEvaluateFeedsTheAlertSinkInTheHostTransaction.
func TestTickFeedsTheAlertSinkPerEvaluatedHost(t *testing.T) {
	store := newFakeEvalStore(cpuLoad(), due(hostA), due(hostB))
	store.loadErr[hostB] = errors.New("boom")
	sink := &fakeSink{}
	tk := rules.NewTicker(store, sink, rules.WithTickerClock(func() time.Time { return tickEpoch }))
	if _, err := tk.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(sink.calls) != 1 {
		t.Fatalf("sink called %d times, want once per evaluated host", len(sink.calls))
	}
	got := sink.calls[0]
	if got.OrgID != tickO || got.HostID != hostA {
		t.Fatalf("sink saw org=%s host=%s, want the applied host org=%s host=%s", got.OrgID, got.HostID, tickO, hostA)
	}
	if got.Host.State != rules.StateCrit || len(got.States) == 0 {
		t.Fatalf("the sink got %+v, want the post-evaluation states and roll-up", got)
	}
	if len(got.Changes) != 1 {
		t.Fatalf("sink changes = %+v, want exactly one", got.Changes)
	}
	if !got.At.Equal(tickEpoch) {
		t.Fatalf("sink saw At=%v, want the tick's own now=%v", got.At, tickEpoch)
	}
}

// TestSinkErrorLeavesTheHostDue (fake-store half): a sink failure must
// fail the whole host's evaluation, exactly like a store.Apply failure
// does — ProcessDue's own contract ("fn's error rolls that host back")
// does not distinguish which step inside fn returned it. The transactional
// half of that claim (check_states actually unwritten, the cursor actually
// untouched) needs the real store, and lives in due_sql_test.go.
func TestSinkErrorLeavesTheHostDue(t *testing.T) {
	store := newFakeEvalStore(cpuLoad(), due(hostA))
	sink := &fakeSink{applyErr: errors.New("sink boom")}
	log, buf := quietLogger()
	tk := rules.NewTicker(store, sink, rules.WithTickerClock(func() time.Time { return tickEpoch }), rules.WithTickerLogger(log))

	n, err := tk.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 0 {
		t.Fatalf("Tick evaluated %d hosts, want 0 — the sink failed", n)
	}
	if _, ok := store.appliedFor(hostA); !ok {
		t.Fatal("store.Apply was never called — this test wants the sink, not the store, to be the failing step")
	}
	if !strings.Contains(buf.String(), "sink boom") {
		t.Fatalf("the sink failure was not logged; log was %q", buf.String())
	}
}
