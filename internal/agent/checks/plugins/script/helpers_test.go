package script

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// fakeRunner answers Run through run, which a test sets per case.
type fakeRunner struct {
	run func(ctx context.Context, argv []string) (runResult, error)

	mu    sync.Mutex
	calls [][]string
}

func (f *fakeRunner) Run(ctx context.Context, argv []string) (runResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), argv...))
	f.mu.Unlock()
	return f.run(ctx, argv)
}

func (f *fakeRunner) called() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

// exitedWith is a runner that ends every run with code and stdout.
func exitedWith(code int, stdout string) *fakeRunner {
	return &fakeRunner{run: func(context.Context, []string) (runResult, error) {
		return runResult{stdout: []byte(stdout), exitCode: code, exited: true, duration: durationOfRun}, nil
	}}
}

// durationOfRun is the fake runs' wall time: 0.25 s.
const durationOfRun = 250_000_000

// fakeChecker answers Check per path: a listed path gets its error (nil
// accepts it), any other path names no file whose place passes
// (errNotFile). check, when set, runs ahead of every Check (a test
// blocking the checker).
type fakeChecker struct {
	files map[string]error
	check func(path string)

	mu      sync.Mutex
	checked []string
}

func (f *fakeChecker) Check(path string) error {
	if f.check != nil {
		f.check(path)
	}
	f.mu.Lock()
	f.checked = append(f.checked, path)
	f.mu.Unlock()
	if err, ok := f.files[path]; ok {
		return err
	}
	return errNotFile
}

// fakeCommands is the agent.yaml lookup.
type fakeCommands struct {
	argv    map[string][]string
	problem string
}

func (f fakeCommands) Command(name string) ([]string, bool, string) {
	argv, ok := f.argv[name]
	if !ok {
		return nil, false, f.problem
	}
	return append([]string(nil), argv...), true, ""
}

// prog is the programs' path in the tests, absolute on every platform.
var prog = abs("usr", "lib", "nagios", "plugins", "check_x")

// abs is elem joined by slashes under the platform's root (testRoot):
// absolute on every platform, and quoted (render) without escapes.
func abs(elem ...string) string {
	return testRoot + strings.Join(elem, "/")
}

// oneCommand defines command "x" as prog with args.
func oneCommand(args ...string) fakeCommands {
	return fakeCommands{argv: map[string][]string{"x": append([]string{prog}, args...)}}
}

// progOK is a checker that accepts prog.
func progOK() *fakeChecker { return &fakeChecker{files: map[string]error{prog: nil}} }

// render turns observations into comparable lines: `item metric "label"`,
// `item metric 1.5` or `item metric !error`.
func render(obs []checks.Observation) []string {
	out := make([]string, 0, len(obs))
	for _, o := range obs {
		s := o.Item + " " + o.Metric + " "
		switch {
		case o.Err != "":
			s += "!" + o.Err
		case o.Label != nil:
			s += strconv.Quote(*o.Label)
		case o.Value != nil:
			s += strconv.FormatFloat(*o.Value, 'f', -1, 64)
		}
		out = append(out, s)
	}
	return out
}

func collect(t *testing.T, p *scriptPlugin, params map[string]any, cmds checks.Commands) []string {
	t.Helper()
	obs, _ := p.CollectWithCommands(context.Background(), params, cmds)
	return render(obs)
}

func equalLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d observations, want %d:\ngot  %q\nwant %q", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("observation %d:\ngot  %s\nwant %s", i, got[i], want[i])
		}
	}
}
