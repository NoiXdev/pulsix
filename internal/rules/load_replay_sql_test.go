package rules_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// The replay guard (backlog 3.118). An agent back from a gap replays its
// buffer in chunks of 200 before it sends anything live, and one item's
// metrics can be split across two chunks. Every test here anchors its
// readings and the ticker's clock on the agent's online_since as the
// database stored it, so no comparison crosses the Go and database clocks.

// replayFixture is one server host whose agent came back online at since.
type replayFixture struct {
	d          *db.DB
	org, host  uuid.UUID
	since      time.Time
	up, dns    rules.Tuple
	checks     fixedChecks
	sink       *fakeSink
	storeTuple func(tuple rules.Tuple, at time.Time)
}

func newReplayFixture(t *testing.T, name, onlineFor string) *replayFixture {
	t.Helper()
	d := testdb.Open(t)
	org := seedOrg(t, d, name)
	host := seedHost(t, d, org, "h")
	_, online, _ := insertAgent(t, d, org, host, agentRow{lastSeen: "5 seconds", online: onlineFor})
	f := &replayFixture{
		d: d, org: org, host: host, since: online.UTC().Truncate(time.Microsecond),
		up:     rules.Tuple{CheckKey: "web", Metric: "http.up"},
		dns:    rules.Tuple{CheckKey: "web", Metric: "http.dns_ms"},
		checks: fixedChecks{intervals: map[string]time.Duration{"web": time.Minute}},
		sink:   &fakeSink{},
	}
	f.storeTuple = func(tuple rules.Tuple, at time.Time) {
		insertCurrent(t, d, org, host, tuple, ptr(1.0), nil, at, "")
	}
	// http.dns_ms sat at crit before the gap, with its alert open.
	insertCheckState(t, d, org, host, stateRow{tuple: f.dns, state: rules.StateCrit, since: f.since.Add(-2 * time.Hour), evaluatedAt: f.since.Add(-40 * time.Minute)})
	return f
}

// tick evaluates the host once at now and returns what the alert sink saw.
func (f *replayFixture) tick(t *testing.T, now time.Time) rules.AlertInput {
	t.Helper()
	markDirty(t, f.d, f.org, f.host)
	store := rules.NewEvalSQLStore(f.d, f.checks, items.NewSQLStore(f.d), testAgentWindow, discard())
	tk := rules.NewTicker(store, f.sink,
		rules.WithTickerClock(func() time.Time { return now }), rules.WithTickerLogger(discard()))
	before := len(f.sink.calls)
	if n, err := tk.Tick(context.Background()); err != nil || n != 1 {
		t.Fatalf("Tick = %d, %v; want one host evaluated", n, err)
	}
	if len(f.sink.calls) != before+1 {
		t.Fatalf("sink called %d times, want once more", len(f.sink.calls)-before)
	}
	return f.sink.calls[before]
}

func (f *replayFixture) storedState(t *testing.T, tuple rules.Tuple) rules.State {
	t.Helper()
	var state string
	if err := f.d.Pool().QueryRow(context.Background(),
		`SELECT state FROM check_states WHERE host_id = $1 AND check_key = $2 AND item = $3 AND metric = $4`,
		f.host, tuple.CheckKey, tuple.Item, tuple.Metric).Scan(&state); err != nil {
		t.Fatalf("read state of %+v: %v", tuple, err)
	}
	return rules.State(state)
}

func judged(in rules.AlertInput, tuple rules.Tuple) bool {
	for _, st := range in.States {
		if st.Tuple == tuple {
			return true
		}
	}
	return false
}

// TestASplitReplayClosesNoAlertAsStale: the first chunk carried http.up
// from the item's first collection in the ring, fifteen minutes old; its
// http.dns_ms is still in the second chunk, so the stored one is the last
// from before the gap, forty minutes old. By the rule alone it trails its
// sibling by far more than three intervals and would vanish — reported
// unmeasured, its open crit alert closed as stale, only to reopen with the
// next chunk. While the item's newest reading predates the agent's online
// period the verdict is held instead: no state, no unmeasured report, the
// stored crit untouched. The second chunk then brings it back to be judged.
//
// Held, not merely kept from vanishing: a client measures staleness from
// the online period's start, so judged, the old reading would count as
// fresh. Both kinds are run.
func TestASplitReplayClosesNoAlertAsStale(t *testing.T) {
	for _, kind := range []string{"server", "client"} {
		t.Run(kind, func(t *testing.T) {
			f := newReplayFixture(t, "replay-split-"+kind, "20 seconds")
			setKind(t, f.d, f.host, kind)
			now := f.since.Add(5 * time.Second)
			f.storeTuple(f.up, f.since.Add(-15*time.Minute))
			f.storeTuple(f.dns, f.since.Add(-40*time.Minute))

			got := f.tick(t, now)
			if len(got.Unmeasured) != 0 {
				t.Fatalf("between the chunks: unmeasured = %+v, want none", got.Unmeasured)
			}
			if judged(got, f.dns) {
				t.Fatalf("between the chunks: states = %+v, want http.dns_ms held, not judged", got.States)
			}
			if !judged(got, f.up) {
				t.Fatalf("between the chunks: states = %+v, want http.up judged", got.States)
			}
			if st := f.storedState(t, f.dns); st != rules.StateCrit {
				t.Fatalf("between the chunks: stored http.dns_ms = %v, want its crit untouched", st)
			}

			// The second chunk: the same collection's http.dns_ms.
			f.storeTuple(f.dns, f.since.Add(-15*time.Minute))
			got = f.tick(t, now.Add(time.Second))
			if len(got.Unmeasured) != 0 {
				t.Fatalf("after the second chunk: unmeasured = %+v, want none", got.Unmeasured)
			}
			if !judged(got, f.dns) {
				t.Fatalf("after the second chunk: states = %+v, want http.dns_ms judged again", got.States)
			}
		})
	}
}

// TestTheReplayGuardEndsWithTheItemsFirstLiveReading: once the item reports
// a reading of the agent's online period, the replay is over (the agent
// sends nothing live before its buffer is through) and a metric that
// really stopped is judged vanished as always.
func TestTheReplayGuardEndsWithTheItemsFirstLiveReading(t *testing.T) {
	f := newReplayFixture(t, "replay-done", "2 minutes")
	now := f.since.Add(2 * time.Minute)
	f.storeTuple(f.up, f.since.Add(90*time.Second))
	f.storeTuple(f.dns, f.since.Add(-40*time.Minute))

	got := f.tick(t, now)
	if len(got.Unmeasured) != 1 || got.Unmeasured[0] != f.dns {
		t.Fatalf("unmeasured = %+v, want only %+v", got.Unmeasured, f.dns)
	}
	if judged(got, f.dns) {
		t.Fatalf("states = %+v, want no new state for the vanished tuple", got.States)
	}
}

// TestAVanishedMetricWithoutAReplayStillCloses: an agent online for hours,
// whose item keeps reporting while one metric stopped ten minutes ago, is
// exactly backlog 3.88's case and is judged exactly as before.
func TestAVanishedMetricWithoutAReplayStillCloses(t *testing.T) {
	f := newReplayFixture(t, "replay-none", "2 hours")
	now := f.since.Add(2 * time.Hour)
	f.storeTuple(f.up, now.Add(-10*time.Second))
	f.storeTuple(f.dns, now.Add(-10*time.Minute))

	got := f.tick(t, now)
	if len(got.Unmeasured) != 1 || got.Unmeasured[0] != f.dns {
		t.Fatalf("unmeasured = %+v, want only %+v", got.Unmeasured, f.dns)
	}
}
