package script

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCollectConcurrencyBound(t *testing.T) {
	const n = 10
	var mu sync.Mutex
	cur, peak := 0, 0
	full := make(chan struct{})
	// A bound below maxParallel never fills the gate: the waits then end
	// here, and the peak check fails, instead of the test hanging.
	gate, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := &fakeRunner{run: func(ctx context.Context, argv []string) (runResult, error) {
		mu.Lock()
		cur++
		peak = max(peak, cur)
		if cur == maxParallel {
			select {
			case <-full:
			default:
				close(full)
			}
		}
		mu.Unlock()
		select { // the first maxParallel runs wait until all of them run at once
		case <-full:
		case <-gate.Done():
		case <-ctx.Done():
		}
		mu.Lock()
		cur--
		mu.Unlock()
		return runResult{exited: true, stdout: []byte(argv[1])}, nil
	}}
	names := make([]string, n)
	argv := map[string][]string{}
	for i := range names {
		names[i] = fmt.Sprintf("c%d", i)
		argv[names[i]] = []string{prog, "out" + names[i]}
	}
	got := collect(t, newPlugin(r, progOK()), map[string]any{"commands": names}, fakeCommands{argv: argv})
	if peak != maxParallel {
		t.Errorf("at most %d runs at once, want %d", peak, maxParallel)
	}
	if len(got) != 4*n {
		t.Fatalf("got %d observations, want %d", len(got), 4*n)
	}
	for i, name := range names {
		if want := fmt.Sprintf(`%s script.output "out%s"`, name, name); got[4*i+1] != want {
			t.Errorf("observation %d = %s, want %s", 4*i+1, got[4*i+1], want)
		}
	}
}

func TestCollectStopsWhenTheAgentStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &fakeRunner{run: func(rctx context.Context, _ []string) (runResult, error) {
		cancel()
		<-rctx.Done()
		return runResult{}, rctx.Err()
	}}
	obs, err := newPlugin(r, progOK()).CollectWithCommands(ctx, map[string]any{"commands": []string{"x"}}, oneCommand())
	if obs != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("got (%v, %v), want (nil, context.Canceled)", obs, err)
	}
	obs, err = newPlugin(r, progOK()).CollectWithCommands(ctx, map[string]any{"commands": []string{"x"}}, oneCommand())
	if obs != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("on a dead context: got (%v, %v), want (nil, context.Canceled)", obs, err)
	}
}
