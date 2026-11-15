package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
)

// TestJSONGateBoundsTheStagesAtOnce: however many probes reach their JSON
// stage together, at most the gate's capacity run it; the others wait and
// take a slot as one frees.
func TestJSONGateBoundsTheStagesAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const stages = 5
		gate := NewJSONGate(2)
		release := make(chan struct{})
		// inside only grows until a release, so what Wait finds is the
		// most that ran at once.
		var inside atomic.Int32
		results := make(chan []probe.Observation, stages)
		for range stages {
			go func() {
				results <- gate.stage(context.Background(), []jsonEntry{{name: "a"}}, func() []probe.Observation {
					inside.Add(1)
					<-release
					inside.Add(-1)
					return []probe.Observation{jsonText("a", "read")}
				})
			}()
		}
		for done := range stages {
			synctest.Wait()
			if got, want := inside.Load(), int32(min(2, stages-done)); got != want {
				t.Fatalf("after %d stages finished: %d run at once, want %d", done, got, want)
			}
			release <- struct{}{}
		}
		for range stages {
			obs := <-results
			if len(obs) != 1 || obs[0].Label == nil || *obs[0].Label != "read" {
				t.Fatalf("a stage's result was %+v, want its own", obs)
			}
		}
	})
}

// TestJSONGateWaitEndsAtTheDeadline: a probe whose deadline passes while
// it waits for a slot reads no JSON; every entry says the manager was too
// busy.
func TestJSONGateWaitEndsAtTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := NewJSONGate(1)
		release := make(chan struct{})
		go gate.stage(context.Background(), nil, func() []probe.Observation {
			<-release
			return nil
		})
		synctest.Wait()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		start := time.Now()
		ran := false
		obs := gate.stage(ctx, []jsonEntry{{name: "a"}, {name: "b"}}, func() []probe.Observation {
			ran = true
			return nil
		})
		if ran {
			t.Fatal("the stage ran without a slot")
		}
		if waited := time.Since(start); waited != 3*time.Second {
			t.Fatalf("waited %v, want the deadline's 3s", waited)
		}
		if len(obs) != 2 || obs[0].Item != "a" || obs[1].Item != "b" || obs[0].Err != errTooBusy || obs[1].Err != errTooBusy {
			t.Fatalf("observations %+v, want %q on both entries", obs, errTooBusy)
		}
		close(release)
	})
}

func TestJSONGateCapacity(t *testing.T) {
	if got, want := DefaultJSONStages(), min(2, runtime.GOMAXPROCS(0)); got != want {
		t.Fatalf("DefaultJSONStages() = %d, want %d", got, want)
	}
	for n, want := range map[int]int{-1: 1, 0: 1, 1: 1, 2: 2} {
		if got := cap(NewJSONGate(n).slots); got != want {
			t.Errorf("NewJSONGate(%d) holds %d, want %d", n, got, want)
		}
	}
}

// TestHTTPJSONWaitsForTheGateUntilTheDeadline: through the probe, a
// response whose JSON stage cannot get a slot before the probe's deadline
// still reports up, status and time; only the JSON items fail.
func TestHTTPJSONWaitsForTheGateUntilTheDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"a":1}`))
	}))
	t.Cleanup(srv.Close)
	gate := NewJSONGate(1)
	leave, ok := gate.enter(context.Background())
	if !ok {
		t.Fatal("an empty gate refused a slot")
	}
	defer leave()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	params := map[string]any{"url": srv.URL, "json_values": []any{"a=$.a"}}
	obs := probeHTTP(ctx, loopbackOnly(), gate, params, "", nil, nil)
	got := map[string]probe.Observation{}
	for _, o := range obs {
		got[o.Metric+"/"+o.Item] = o
	}
	for _, m := range []string{"http.up/", "http.status_code/", "http.response_ms/"} {
		if got[m].Value == nil {
			t.Fatalf("%s = %+v, want a value", m, got[m])
		}
	}
	if o := got["http.json.value/a"]; o.Err != errTooBusy {
		t.Fatalf("item a = %+v, want %q", o, errTooBusy)
	}
}

// entriesOf parses name=path entries the way the probe does.
func entriesOf(t *testing.T, list ...string) []jsonEntry {
	t.Helper()
	params := map[string]any{"json_values": []any{}}
	for _, s := range list {
		params["json_values"] = append(params["json_values"].([]any), s)
	}
	entries := jsonEntries(params)
	if len(entries) != len(list) {
		t.Fatalf("parsed %d of %d entries", len(entries), len(list))
	}
	return entries
}

// wantItems checks each entry's observation in order: an error when the
// want starts with "!", else a number.
func wantItems(t *testing.T, obs []probe.Observation, want ...string) {
	t.Helper()
	if len(obs) != len(want) {
		t.Fatalf("%d observations, want %d: %+v", len(obs), len(want), obs)
	}
	for i, w := range want {
		o := obs[i]
		if msg, isErr := strings.CutPrefix(w, "!"); isErr {
			if o.Err != msg {
				t.Errorf("item %s = %+v, want error %q", o.Item, o, msg)
			}
		} else if o.Err != "" || o.Value == nil {
			t.Errorf("item %s = %+v, want a value", o.Item, o)
		}
	}
}

// TestJSONEntriesShareTheCheckBudget: each entry may visit at most its
// own budget, and all of a check's entries together at most the check's;
// whichever runs out first, that entry selected too much, and so does
// every entry after the check's budget is spent.
func TestJSONEntriesShareTheCheckBudget(t *testing.T) {
	body := []byte("[" + strings.Repeat("0,", 999) + "0]")
	many := "!" + "the path matched 1000 values"
	b := jsonBudgets{perPath: 1_500, perCheck: 2_500}
	obs := readJSON(context.Background(), entriesOf(t, "a=$[*]", "b=$[*]", "c=$[*]", "d=$[0]"), body, b)
	// a and b use 1000 each; c gets the 500 left and runs out; d finds
	// nothing left.
	wantItems(t, obs, many, many, "!"+errTooMuch, "!"+errTooMuch)

	b = jsonBudgets{perPath: 999, perCheck: 2_500}
	obs = readJSON(context.Background(), entriesOf(t, "a=$[*]", "b=$[0]"), body, b)
	wantItems(t, obs, "!"+errTooMuch, "0")
}

// endingCtx is a context whose Err is nil for its first live calls and
// DeadlineExceeded from then on, so a test ends an evaluation part way
// through without a clock.
type endingCtx struct {
	context.Context
	live, calls int
}

func (c *endingCtx) Err() error {
	c.calls++
	if c.calls > c.live {
		return context.DeadlineExceeded
	}
	return nil
}

// TestJSONEntriesStopWhenTheProbeRunsOutOfTime: once the probe's deadline
// passes mid-evaluation, that entry and every entry after it ran out of
// time; the entries before keep their values.
func TestJSONEntriesStopWhenTheProbeRunsOutOfTime(t *testing.T) {
	body := []byte("[" + strings.Repeat("0,", 69_999) + "0]")
	// a looks once (its start), b at its start and at its 65 536th visit,
	// which is the look that sees the deadline.
	ctx := &endingCtx{Context: context.Background(), live: 2}
	obs := readJSON(ctx, entriesOf(t, "a=$[0]", "b=$[*]", "c=$[0]", "d=$[*"), body, defaultJSONBudgets)
	wantItems(t, obs, "0", "!"+errOutOfTime, "!"+errOutOfTime, "!"+errNotAllowed)
}
