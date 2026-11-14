package script

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// TestCollectArgumentsThatNameNoFile: an absolute argument that names no
// regular file (a missing script, the root, a device, a mount point, a
// URL path) is left as it is once the checker passed the directories on
// its way (errNotFile); where one is refused, the argument is echoed with
// the reason, and one the checker could not check names no argument.
func TestCollectArgumentsThatNameNoFile(t *testing.T) {
	missing := abs("home", "u", "x.py")
	tests := []struct {
		name  string
		args  []string
		files map[string]error
		want  []string // nil: the command ran
	}{
		{
			name: "missing file in a user-owned directory", args: []string{missing},
			files: map[string]error{missing: refuse("its directory is owned by uid 1000, not by root")},
			want: []string{`x script.status "unknown"`,
				`x script.output "` + missing + `: its directory is owned by uid 1000, not by root"`},
		},
		{
			name: "missing file under a root-owned directory", args: []string{"-f", abs("opt", "gone.py")},
		},
		{
			name: "the root, a device, a mount point and a URL path", args: []string{"-p", testRoot, abs("dev", "sda"), abs("mnt", "nfs"), "-u", abs("health")},
		},
		{
			name: "could not be checked", args: []string{abs("srv", marker)},
			files: map[string]error{abs("srv", marker): errors.New("lstat " + abs("srv", marker) + ": permission denied")},
			want:  []string{`x script.status "unknown"`, `x script.output "` + textUncheckedArgument + `"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := exitedWith(0, "OK")
			files := map[string]error{prog: nil}
			maps.Copy(files, tt.files)
			c := &fakeChecker{files: files}
			got := collect(t, newPlugin(r, c), map[string]any{"commands": []string{"x"}}, oneCommand(tt.args...))
			if tt.want != nil {
				equalLines(t, got, tt.want)
				if len(r.called()) != 0 {
					t.Error("the command ran")
				}
				return
			}
			if got[0] != `x script.status "ok"` {
				t.Fatalf("status = %s, want ok", got[0])
			}
			want := []string{prog}
			for _, a := range tt.args {
				if a[0] != '-' {
					want = append(want, a)
				}
			}
			if fmt.Sprint(c.checked) != fmt.Sprint(want) {
				t.Errorf("checked %q, want %q", c.checked, want)
			}
			if calls := r.called(); len(calls) != 1 || !slices.Equal(calls[0], append([]string{prog}, tt.args...)) {
				t.Errorf("ran %q, want the arguments unchanged", calls)
			}
		})
	}
}

// TestCollectBoundsFileChecksByTheTimeout: a check stuck on a hung mount
// ends the round within timeout_seconds; while it has not returned, later
// rounds report the command busy instead of starting another; once it
// returns, the command runs again.
func TestCollectBoundsFileChecksByTheTimeout(t *testing.T) {
	release := make(chan struct{})
	var once bool
	c := &fakeChecker{files: map[string]error{prog: nil}}
	c.check = func(string) {
		c.mu.Lock()
		first := !once
		once = true
		c.mu.Unlock()
		if first {
			<-release
		}
	}
	// Without the bound the round would wait for the check forever: the
	// guard unblocks it, and the round's length fails the test.
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	guard := time.AfterFunc(5*time.Second, unblock)
	defer guard.Stop()
	p := newPlugin(exitedWith(0, "OK"), c)
	params := map[string]any{"commands": []string{"x"}, "timeout_seconds": 1}

	start := time.Now()
	got := collect(t, p, params, oneCommand())
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the round took %s, want about 1s", d)
	}
	equalLines(t, got, []string{`x script.status "unknown"`, `x script.output "checking the files took longer than 1s"`})

	equalLines(t, collect(t, p, params, oneCommand()), []string{`x script.status "unknown"`, `x script.output "` + textBusy + `"`})

	unblock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got = collect(t, p, params, oneCommand())
		if got[0] == `x script.status "ok"` {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %q after the check returned", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestCollectFileCheckPanicIsReported: a checker panicking in the check's
// own goroutine is reported, not a crash of the agent.
func TestCollectFileCheckPanicIsReported(t *testing.T) {
	c := checkerFunc(func(path string) error { panic("check " + path + " " + marker) })
	obs, err := newPlugin(exitedWith(0, ""), c).CollectWithCommands(context.Background(), map[string]any{"commands": []string{"x"}}, oneCommand())
	assertNoMarker(t, obs, err)
	equalLines(t, render(obs), []string{`x script.status "unknown"`, `x script.output "` + textInternal + `"`})
}

// TestRunOneAgentStoppingDuringFileChecks: when the agent stops while the
// files are checked, the command reports nothing (the round is dropped),
// not that its checks took too long.
func TestRunOneAgentStoppingDuringFileChecks(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	c := &fakeChecker{files: map[string]error{prog: nil}, check: func(string) {
		cancel()
		<-release
	}}
	opts, problem := parseOptions(map[string]any{"timeout_seconds": 30})
	if problem != "" {
		t.Fatal(problem)
	}
	done := make(chan []checks.Observation, 1)
	go func() { done <- newPlugin(exitedWith(0, "OK"), c).runOne(ctx, "x", oneCommand(), opts) }()
	select {
	case obs := <-done:
		if obs != nil {
			t.Errorf("runOne = %q, want no observations", render(obs))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runOne did not return when the agent stopped")
	}
}
