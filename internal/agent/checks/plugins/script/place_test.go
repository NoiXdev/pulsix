package script

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestCollectJudgesWhereAMissingArgumentLives: an absolute argument that
// names no regular file is still judged by where it lives, so a script
// that is missing at the check cannot appear in a directory others can
// write before the run. Its path is echoed only with a refusal of the
// place the checker inspected; a place that could not be checked names no
// argument.
func TestCollectJudgesWhereAMissingArgumentLives(t *testing.T) {
	missing := abs("home", "u", "x.py")
	tests := []struct {
		name   string
		args   []string
		places map[string]error
		want   []string // nil: the command ran
	}{
		{
			name: "missing file in a user-owned directory", args: []string{missing},
			places: map[string]error{missing: refuse("its directory is owned by uid 1000, not by root")},
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
			name: "place could not be checked", args: []string{abs("srv", marker)},
			places: map[string]error{abs("srv", marker): errors.New("stat " + abs("srv", marker) + ": permission denied")},
			want:   []string{`x script.status "unknown"`, `x script.output "` + textUncheckedArgument + `"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := exitedWith(0, "OK")
			c := &fakeChecker{files: map[string]error{prog: nil}, places: tt.places}
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
			var abss []string
			for _, a := range tt.args {
				if a[0] != '-' {
					abss = append(abss, a)
				}
			}
			if fmt.Sprint(c.placed) != fmt.Sprint(abss) {
				t.Errorf("places judged %q, want %q", c.placed, abss)
			}
		})
	}
}

// TestCollectJudgesNoPlaceForAProgramOrAFile: a program that names no
// file is reported as such, and an argument naming a file is judged by
// Check alone.
func TestCollectJudgesNoPlaceForAProgramOrAFile(t *testing.T) {
	c := &fakeChecker{files: map[string]error{abs("opt", "x.py"): nil}}
	collect(t, newPlugin(exitedWith(0, ""), c), map[string]any{"commands": []string{"x"}}, oneCommand(abs("opt", "y.py")))
	if len(c.placed) != 0 {
		t.Errorf("places judged %q for a missing program", c.placed)
	}
	c = &fakeChecker{files: map[string]error{prog: nil, abs("opt", "x.py"): nil}}
	collect(t, newPlugin(exitedWith(0, ""), c), map[string]any{"commands": []string{"x"}}, oneCommand(abs("opt", "x.py")))
	if len(c.placed) != 0 {
		t.Errorf("places judged %q for a file", c.placed)
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
