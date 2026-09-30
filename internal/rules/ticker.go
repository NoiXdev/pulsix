// This file is the engine room of v0.3b-2: what wakes an evaluation.
//
// Evaluation is triggered by a ticker, not by the ingest path (the spec's
// "Trigger"). The ingest path is the hot path — roughly 7 000 samples a
// second across a fleet — and it contributes exactly one single-row upsert
// per batch, marking the host dirty; everything else happens here, on a
// 15-second tick that claims dirty-or-due hosts. One code path then covers
// thresholds, duration, missing data and windows alike, and `for_duration`
// and `unknown` need a timed path whatever else is true, so this way there
// is one engine rather than two.
//
// The Ticker itself is a pure orchestrator: claim, load, fetch the windows
// the loaded rules ask for, evaluate, apply. It holds no SQL — EvalStore
// does — and no clock of its own beyond the injected one, which is the
// single "now" every host in a batch is judged against.
package rules

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// DefaultEvalInterval and DefaultEvalBatch are the tick's own defaults: 15
// seconds (the spec's figure) and 200 hosts per batch. The batch bounds
// one transaction, in the same spirit as internal/schedules' own batchSize
// — a manager that has fallen behind catches up over several ticks rather
// than in one transaction long enough to matter to everything else sharing
// the pool.
const (
	DefaultEvalInterval = 15 * time.Second
	DefaultEvalBatch    = 200
)

// Due is one host the ticker has claimed: which organization's row-level
// scope its statements belong to, and which host to evaluate.
type Due struct {
	OrgID, HostID uuid.UUID
}

// HostLoad is everything one host's evaluation reads, gathered before the
// pure function runs. It is Snapshot minus Now and Windows: the moment is
// the ticker's own clock reading, and the window aggregates are fetched in
// a second round trip that depends on what the loaded rules turn out to
// ask for (WindowRequests).
//
// PreviousHost is the roll-up the last evaluation wrote, or nil for a host
// that has never been evaluated. Snapshot.PreviousHost documents nil as a
// supported input with a quiet degradation behind it — which is exactly
// why a loader must fill this field rather than lean on that fallback.
type HostLoad struct {
	Tuples       []TupleSnapshot
	Effective    []Effective
	Facts        map[string]string
	Previous     map[Tuple]TupleState
	Changes30    map[Tuple]int
	PreviousHost *HostState
}

// EvalStore is the ticker's store: the claim, the three reads one
// evaluation needs and the write-back. Declared here, next to its only
// consumer, and satisfied by EvalSQLStore (due_sql.go).
//
// Load, Windows and Apply are called from inside fn, on the context
// ProcessDue hands it, and the SQL implementation runs them inside that
// host's own transaction — so a host's reads and its write-back are one
// atomic unit, and a failure anywhere in it leaves the host exactly as it
// was, still due.
type EvalStore interface {
	// ProcessDue claims up to limit cursors that are dirty or due and runs
	// fn for each inside its own transaction, holding that cursor's row
	// lock. fn's error rolls that host back and leaves its cursor
	// untouched; the other hosts are unaffected. It returns the number of
	// hosts fn returned nil for — the hosts actually evaluated, not the
	// hosts claimed.
	ProcessDue(ctx context.Context, now time.Time, limit int, fn func(ctx context.Context, d Due) error) (int, error)
	// Load gathers one host's inputs: its tuples with the intervals that
	// define staleness, its effective rules, its facts, its previous
	// states, its flapping counts and its previous roll-up.
	Load(ctx context.Context, d Due) (HostLoad, error)
	// Windows fetches the requested aggregates in one round trip, measured
	// back from now. A request whose window holds no sample is simply
	// absent from the result — a missing window is a non-match, never a
	// zero (the spec's "A missing value is not a false comparison").
	Windows(ctx context.Context, d Due, reqs []WindowRequest, now time.Time) (map[WindowRequest]float64, error)
	// Apply writes the evaluation's states, changes and roll-up and
	// advances the host's cursor.
	Apply(ctx context.Context, d Due, res Result) error
}

// Ticker evaluates dirty-or-due hosts on a fixed interval. Several
// managers may each run one: the claim's FOR UPDATE SKIP LOCKED
// distributes hosts across them without any claim protocol of its own.
type Ticker struct {
	store    EvalStore
	now      func() time.Time
	interval time.Duration
	batch    int
	log      *slog.Logger
	overruns atomic.Int64
}

// TickerOption configures a Ticker at construction.
type TickerOption func(*Ticker)

// WithTickerClock injects the clock every evaluation in a batch is stamped
// with. Note what it does NOT influence: the claim's own dirty/due
// comparisons run against the database's now() rather than any manager's
// wall clock (backlog 2.80), so two managers with skewed clocks still
// agree on which hosts are due.
func WithTickerClock(now func() time.Time) TickerOption {
	return func(t *Ticker) {
		if now != nil {
			t.now = now
		}
	}
}

// WithInterval sets the tick interval (default DefaultEvalInterval). It is
// also the threshold an overrun is measured against.
func WithInterval(d time.Duration) TickerOption {
	return func(t *Ticker) {
		if d > 0 {
			t.interval = d
		}
	}
}

// WithBatch sets how many hosts one tick claims (default DefaultEvalBatch).
func WithBatch(n int) TickerOption {
	return func(t *Ticker) {
		if n > 0 {
			t.batch = n
		}
	}
}

// WithTickerLogger sets the logger (default slog.Default()).
func WithTickerLogger(l *slog.Logger) TickerOption {
	return func(t *Ticker) {
		if l != nil {
			t.log = l
		}
	}
}

// NewTicker builds the ticker on store, with every option defaulted.
func NewTicker(store EvalStore, opts ...TickerOption) *Ticker {
	t := &Ticker{
		store:    store,
		now:      time.Now,
		interval: DefaultEvalInterval,
		batch:    DefaultEvalBatch,
		log:      slog.Default(),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Overruns is how many ticks have taken longer than the interval since this
// ticker was built. Pulsix has no metrics endpoint yet (backlog 3.27), so
// this counter is read by a test and by whoever is holding the ticker;
// every overrun is also logged as it happens.
func (t *Ticker) Overruns() int64 { return t.overruns.Load() }

// Tick processes one batch and reports how many hosts it evaluated.
//
// It deliberately does not loop until the batch comes back short, the way
// internal/schedules' own Tick does: a manager that has fallen behind must
// take the delay rather than build a backlog of re-entered ticks, which is
// what backlog 2.60 records the cost of. The next tick simply takes the
// oldest next_due_at first, so nothing is lost by returning early — only
// deferred.
func (t *Ticker) Tick(ctx context.Context) (int, error) {
	// One clock reading for the whole batch: every host in it is judged
	// against the same present, so two hosts evaluated in one tick can
	// never disagree about what "now" was.
	now := t.now()
	n, err := t.store.ProcessDue(ctx, now, t.batch, func(ctx context.Context, d Due) error {
		return t.evaluate(ctx, d, now)
	})
	if elapsed := t.now().Sub(now); elapsed > t.interval {
		t.overruns.Add(1)
		// Once per occurrence, with both numbers that say what to do about
		// it: a large batch means the fleet outgrew the interval, a small
		// one means a single host's evaluation is slow.
		t.log.WarnContext(ctx, "evaluation tick overran its interval; the next tick takes the oldest hosts first",
			"hosts", n, "batch", t.batch, "elapsed", elapsed, "interval", t.interval)
	}
	return n, err
}

// evaluate is one host: load, fetch the windows its rules ask for,
// evaluate, apply. Every error is returned, which is what rolls that one
// host back and leaves it due — and logged here, where the host is still
// in hand to name.
func (t *Ticker) evaluate(ctx context.Context, d Due, now time.Time) error {
	load, err := t.store.Load(ctx, d)
	if err != nil {
		t.log.ErrorContext(ctx, "load host for evaluation; leaving it due for the next tick",
			"org", d.OrgID, "host", d.HostID, "err", err)
		return err
	}
	snap := Snapshot{
		Now:          now,
		Tuples:       load.Tuples,
		Effective:    load.Effective,
		Facts:        load.Facts,
		Previous:     load.Previous,
		Changes30:    load.Changes30,
		PreviousHost: load.PreviousHost,
	}
	// WindowRequests reads only Tuples and Effective, so it is computed on
	// the snapshot while Windows is still nil and fills that field from its
	// own result. A host whose rules name no window costs no round trip at
	// all.
	if reqs := WindowRequests(snap); len(reqs) > 0 {
		windows, err := t.store.Windows(ctx, d, reqs, now)
		if err != nil {
			t.log.ErrorContext(ctx, "fetch window aggregates; leaving the host due for the next tick",
				"org", d.OrgID, "host", d.HostID, "windows", len(reqs), "err", err)
			return err
		}
		snap.Windows = windows
	}
	if err := t.store.Apply(ctx, d, Evaluate(snap)); err != nil {
		t.log.ErrorContext(ctx, "apply evaluation; leaving the host due for the next tick",
			"org", d.OrgID, "host", d.HostID, "err", err)
		return err
	}
	return nil
}

// Run ticks until ctx ends, returning ctx's own error. The first tick
// happens immediately — a manager that has just started should not leave
// a fleet unjudged for an interval — and every later one waits the full
// interval, including after an overrun (backlog 2.60: a ticker that
// re-enters immediately turns a slow batch into a busy loop).
func (t *Ticker) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		t.tickSafely(ctx)
		timer.Reset(t.interval)
	}
}

// tickSafely runs one Tick, recovering any panic that escapes it. A bug
// anywhere below — in a rule tree, in a store's row loop — must cost one
// tick, not the ticker's goroutine and with it the manager process. A
// clean shutdown surfaces as ctx.Err() on the tick's own error and is not
// worth an ERROR line on every stop.
func (t *Ticker) tickSafely(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			t.log.ErrorContext(ctx, "evaluation tick panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if _, err := t.Tick(ctx); err != nil && ctx.Err() == nil {
		t.log.ErrorContext(ctx, "evaluation tick failed", "err", err)
	}
}
