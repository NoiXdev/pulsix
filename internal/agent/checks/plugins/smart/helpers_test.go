package smart

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// fixture reads a recorded smartctl output from testdata/.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

// reply is what the fake runner answers for one smartctl invocation.
type reply struct {
	stdout []byte
	exit   int
	err    error
	// hang blocks until the context passed to Run ends, like a smartctl
	// stuck on a device that the runner then kills.
	hang bool
	// ignoreCtx blocks until the test ends, whatever the context says: a
	// runner that broke its contract (Run must return once ctx is done).
	ignoreCtx bool
	delay     time.Duration
}

// fakeRunner answers by invocation key: "scan" for the scan, otherwise the
// device argument, followed by " -d <type>" when a type was passed.
type fakeRunner struct {
	t       *testing.T
	replies map[string]reply
	release chan struct{}

	mu          sync.Mutex
	calls       [][]string
	inFlight    int
	maxInFlight int
}

func newFakeRunner(t *testing.T, replies map[string]reply) *fakeRunner {
	f := &fakeRunner{t: t, replies: replies, release: make(chan struct{})}
	t.Cleanup(func() { close(f.release) })
	return f
}

func callKey(args []string) string {
	if slices.Contains(args, "--scan") {
		return "scan"
	}
	key := args[len(args)-1]
	if i := slices.Index(args, "-d"); i >= 0 {
		key += " -d " + args[i+1]
	}
	return key
}

func (f *fakeRunner) Run(ctx context.Context, args []string) (runResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(args))
	f.inFlight++
	f.maxInFlight = max(f.maxInFlight, f.inFlight)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	r, ok := f.replies[callKey(args)]
	if !ok {
		f.t.Errorf("unexpected smartctl call %q", args)
		return runResult{}, context.Canceled
	}
	switch {
	case r.ignoreCtx:
		<-f.release
		return runResult{}, context.DeadlineExceeded
	case r.hang:
		<-ctx.Done()
		return runResult{}, ctx.Err()
	case r.delay > 0:
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return runResult{}, ctx.Err()
		}
	}
	return runResult{stdout: r.stdout, exitCode: r.exit}, r.err
}

func (f *fakeRunner) callArgs() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// plugin builds the smart plugin around f with the default bounds.
func plugin(f runner) *smartPlugin {
	return &smartPlugin{smartctl: func() (runner, error) { return f, nil }}
}

func findObs(t *testing.T, obs []checks.Observation, item, metric string) checks.Observation {
	t.Helper()
	for _, o := range obs {
		if o.Item == item && o.Metric == metric {
			return o
		}
	}
	t.Fatalf("no observation for item %q metric %q in %+v", item, metric, obs)
	return checks.Observation{}
}

func value(t *testing.T, obs []checks.Observation, item, metric string) float64 {
	t.Helper()
	o := findObs(t, obs, item, metric)
	if o.Value == nil {
		t.Fatalf("%s %s: no value (err %q)", item, metric, o.Err)
	}
	return *o.Value
}

// label is item's metric label, failing the test when it carries none.
func label(t *testing.T, obs []checks.Observation, item, metric string) string {
	t.Helper()
	o := findObs(t, obs, item, metric)
	if o.Label == nil {
		t.Fatalf("%s %s: no label (value %v, err %q)", item, metric, o.Value, o.Err)
	}
	return *o.Label
}

func itemObs(obs []checks.Observation, item string) []checks.Observation {
	var out []checks.Observation
	for _, o := range obs {
		if o.Item == item {
			out = append(out, o)
		}
	}
	return out
}

// items lists the device items reported, sorted; the host-level item ""
// (smart.devices) is left out.
func items(obs []checks.Observation) []string {
	var out []string
	for _, o := range obs {
		if o.Item != "" && !slices.Contains(out, o.Item) {
			out = append(out, o.Item)
		}
	}
	slices.Sort(out)
	return out
}

// onlyErr asserts that item has exactly one observation: an error on
// smart.passed containing want.
func onlyErr(t *testing.T, obs []checks.Observation, item, want string) {
	t.Helper()
	got := itemObs(obs, item)
	if len(got) != 1 || got[0].Metric != "smart.passed" || got[0].Err == "" {
		t.Fatalf("%s: observations = %+v, want one error on smart.passed", item, got)
	}
	if !strings.Contains(got[0].Err, want) {
		t.Fatalf("%s: error %q does not contain %q", item, got[0].Err, want)
	}
}

// hostErr asserts that the only observation on item "" is an error on
// smart.devices containing want.
func hostErr(t *testing.T, obs []checks.Observation, want string) {
	t.Helper()
	got := itemObs(obs, "")
	if len(got) != 1 || got[0].Metric != "smart.devices" || got[0].Err == "" || got[0].Value != nil {
		t.Fatalf("item \"\": observations = %+v, want one error on smart.devices", got)
	}
	if !strings.Contains(got[0].Err, want) {
		t.Fatalf("item \"\": error %q does not contain %q", got[0].Err, want)
	}
}

// deviceCount asserts that item "" carries exactly smart.devices = want.
func deviceCount(t *testing.T, obs []checks.Observation, want float64) {
	t.Helper()
	got := itemObs(obs, "")
	if len(got) != 1 || got[0].Metric != "smart.devices" || got[0].Value == nil || *got[0].Value != want {
		t.Fatalf("item \"\": observations = %+v, want smart.devices = %v", got, want)
	}
}

// encodesAsCheckResult fails the test when o could not travel as the
// CheckResult it is sent as.
func encodesAsCheckResult(t *testing.T, o checks.Observation) {
	t.Helper()
	r := &agentpb.CheckResult{CheckKey: "smart", Item: o.Item, Metric: o.Metric}
	switch {
	case o.Value != nil:
		r.Observation = &agentpb.CheckResult_Value{Value: *o.Value}
	case o.Label != nil:
		r.Observation = &agentpb.CheckResult_Label{Label: *o.Label}
	default:
		r.Observation = &agentpb.CheckResult_Error{Error: o.Err}
	}
	if _, err := proto.Marshal(r); err != nil {
		t.Fatalf("observation %q/%q does not encode: %v", o.Item, o.Metric, err)
	}
	if !checks.ReportableItem(o.Item) {
		t.Fatalf("item %q breaks the item contract", o.Item)
	}
}
