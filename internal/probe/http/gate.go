package http

import (
	"context"
	"runtime"

	"github.com/noixdev/pulsix/internal/probe"
)

// errTooBusy is every JSON item's error when the probe's deadline passes
// while it waits for a JSON stage.
const errTooBusy = "the manager was too busy to read the JSON response"

// JSONGate bounds the JSON stages (decoding a body and evaluating all of a
// check's json_values entries) that run at once in the manager, and the
// assertions' parse stage (the body as JSON and as HTML, and every json
// and html assertion of a check on it; backlog 3.13b). A hostile
// 1 MiB body decodes to tens of MiB and its entries take up to a second
// of CPU, while the runner runs up to 32 probes at once inside the
// manager, next to the API: without the gate one organization could push
// the manager to gigabytes. The manager builds one gate and hands it to
// the http probe, as it does the Guard. It is not fair between
// organizations: a busy organization delays others' JSON stages up to
// their deadline, after which they fail with errTooBusy.
type JSONGate struct {
	slots chan struct{}
}

// NewJSONGate returns a gate of n slots, at least one.
func NewJSONGate(n int) *JSONGate {
	return &JSONGate{slots: make(chan struct{}, max(n, 1))}
}

// DefaultJSONStages is the manager's gate size: two stages, or one on a
// single core, so they never take more than about two cores and a few
// hundred MiB.
func DefaultJSONStages() int {
	return min(2, runtime.GOMAXPROCS(0))
}

// enter takes a slot, waiting until ctx ends; leave frees it. A free
// slot is taken even when ctx has ended, so readJSON, not the gate,
// reports a deadline that passed before the stage — at once, without
// decoding the body.
func (g *JSONGate) enter(ctx context.Context) (leave func(), ok bool) {
	leave = func() { <-g.slots }
	select {
	case g.slots <- struct{}{}:
		return leave, true
	default:
	}
	select {
	case g.slots <- struct{}{}:
		return leave, true
	case <-ctx.Done():
		return nil, false
	}
}

// stage runs read in one of the gate's slots. When ctx ends before a slot
// frees, read does not run and every entry is errTooBusy.
func (g *JSONGate) stage(ctx context.Context, entries []jsonEntry, read func() []probe.Observation) []probe.Observation {
	leave, ok := g.enter(ctx)
	if !ok {
		return failEntries(entries, errTooBusy)
	}
	defer leave()
	return read()
}
