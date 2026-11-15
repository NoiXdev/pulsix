package zfs

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// reply is what the fake runner answers for one call.
type reply struct {
	stdout, stderr []byte
	err            error
	// block waits until the context passed to Run ends, like a zpool
	// stuck on a suspended pool that the runner then stops.
	block bool
	// ignoreCtx waits until the test ends, whatever the context says: a
	// runner that broke its contract.
	ignoreCtx bool
}

// fakeRunner answers by "<name> <first argument>": "zpool list",
// "zpool status", "zfs get".
type fakeRunner struct {
	t       *testing.T
	replies map[string]reply
	release chan struct{}

	mu    sync.Mutex
	calls [][]string // name followed by the arguments
}

func newFake(t *testing.T, replies map[string]reply) *fakeRunner {
	f := &fakeRunner{t: t, replies: replies, release: make(chan struct{})}
	t.Cleanup(func() { close(f.release) })
	return f
}

func (f *fakeRunner) Run(ctx context.Context, name string, args []string) (stdout, stderr []byte, err error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.mu.Unlock()
	key := name
	if len(args) > 0 {
		key += " " + args[0]
	}
	r, ok := f.replies[key]
	if !ok {
		f.t.Errorf("unexpected call %s %q", name, args)
		return nil, nil, errors.New("unexpected call")
	}
	switch {
	case r.ignoreCtx:
		<-f.release
		return nil, nil, context.DeadlineExceeded
	case r.block:
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	return r.stdout, r.stderr, r.err
}

func (f *fakeRunner) callList() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// called reports whether a call starting with "<name> <first argument>"
// was made, and returns its argument list.
func (f *fakeRunner) called(key string) ([]string, bool) {
	for _, c := range f.callList() {
		if strings.Join(c[:min(2, len(c))], " ") == key {
			return c, true
		}
	}
	return nil, false
}

// clock is the tests' fixed now.
var clock = utc("2024-11-20 00:00:00")

func testPlugin(r runner) zfsPlugin {
	return zfsPlugin{
		hasZFS: func() (bool, error) { return true, nil },
		run:    r,
		now:    func() time.Time { return clock },
		loc:    time.UTC,
	}
}

func find(obs []checks.Observation, item, metric string) *checks.Observation {
	for i := range obs {
		if obs[i].Item == item && obs[i].Metric == metric {
			return &obs[i]
		}
	}
	return nil
}

func wantValue(t *testing.T, obs []checks.Observation, item, metric string, want float64) {
	t.Helper()
	o := find(obs, item, metric)
	if o == nil || o.Value == nil || math.Abs(*o.Value-want) > 1e-9 {
		t.Errorf("%s %s = %+v, want %v", item, metric, o, want)
	}
}

func wantLabel(t *testing.T, obs []checks.Observation, item, metric, want string) {
	t.Helper()
	o := find(obs, item, metric)
	if o == nil || o.Label == nil || *o.Label != want {
		t.Errorf("%s %s = %+v, want %q", item, metric, o, want)
	}
}

func wantErr(t *testing.T, obs []checks.Observation, item, metric, contains string) {
	t.Helper()
	o := find(obs, item, metric)
	if o == nil || o.Err == "" || !strings.Contains(o.Err, contains) {
		t.Errorf("%s %s = %+v, want an error containing %q", item, metric, o, contains)
	}
}

func days(d time.Duration) float64 { return d.Hours() / 24 }

var (
	listArgs   = []string{"zpool", "list", "-Hp", "-o", "name,health,size,free,capacity,fragmentation"}
	statusArgs = []string{"zpool", "status", "-p"}
)

// statusMetrics are the metrics read from zpool status (and zfs get).
var statusMetrics = []string{
	"zfs.errors_read", "zfs.errors_write", "zfs.errors_checksum", "zfs.data_errors",
	"zfs.devices_unhealthy", "zfs.scan", "zfs.scan_pct", "zfs.scrub_age_days",
}
