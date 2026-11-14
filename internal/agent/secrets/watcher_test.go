package secrets

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// watchFixture drives a Watcher with a fake clock over a real file.
type watchFixture struct {
	t     *testing.T
	path  string
	mu    sync.Mutex
	now   time.Time
	check *markerCheck
	log   bytes.Buffer
	w     *Watcher
}

func newWatchFixture(t *testing.T) *watchFixture {
	return newWatchFixtureAt(t, filepath.Join(t.TempDir(), "agent.yaml"))
}

func newWatchFixtureAt(t *testing.T, path string) *watchFixture {
	f := &watchFixture{
		t:     t,
		path:  path,
		now:   time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC),
		check: &markerCheck{},
	}
	f.w = NewWatcher(f.path, f.checkFile,
		WithClock(f.clock),
		WithLogger(slog.New(slog.NewTextHandler(&f.log, nil))))
	return f
}

func (f *watchFixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *watchFixture) checkFile(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.check.check(path)
}

// write puts content on disk with a fixed modification time, so two
// writes of the same size look alike to anything comparing size and time.
func (f *watchFixture) write(content string) {
	f.t.Helper()
	writeFile(f.t, f.path, content)
	mtime := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	if err := os.Chtimes(f.path, mtime, mtime); err != nil {
		f.t.Fatal(err)
	}
}

func (f *watchFixture) remove() {
	f.t.Helper()
	if err := os.Remove(f.path); err != nil {
		f.t.Fatal(err)
	}
}

func (f *watchFixture) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *watchFixture) checks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.check.calls
}

// expect asserts the watcher's current names and state; a state starting
// with "!" means "an error naming this text".
func (f *watchFixture) expect(names, state string) {
	f.t.Helper()
	set, got := f.w.Current()
	if gotNames := strings.Join(set.Names(), ","); gotNames != names {
		f.t.Errorf("names = %q, want %q", gotNames, names)
	}
	if want, isErr := strings.CutPrefix(state, "!"); isErr {
		if got == StateOK || got == StateAbsent || !strings.Contains(string(got), want) {
			f.t.Errorf("state = %q, want an error naming %q", got, want)
		}
		if strings.Contains(string(got), secretValue) {
			f.t.Errorf("state quotes a value: %q", got)
		}
		return
	}
	if string(got) != state {
		f.t.Errorf("state = %q, want %q", got, state)
	}
}

func TestWatcherLoadsAtStart(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  pg:\n    a: b\n")
	f.expect("pg", "ok")
	if f.checks() != 1 {
		t.Fatalf("checks = %d, want 1", f.checks())
	}
}

func TestWatcherAbsentFile(t *testing.T) {
	f := newWatchFixture(t)
	f.expect("", "absent")
}

func TestWatcherChecksAtMostOncePerMinute(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  pg:\n    a: b\n")
	f.expect("pg", "ok")
	f.write("secrets:\n  other:\n    a: b\n")
	f.advance(59 * time.Second)
	f.expect("pg", "ok")
	if f.checks() != 1 {
		t.Fatalf("within the minute: checks = %d, want 1", f.checks())
	}
	f.advance(time.Second)
	f.expect("other", "ok")
	if f.checks() != 2 {
		t.Fatalf("checks = %d, want 2", f.checks())
	}
}

// TestWatcherRereadsOnEveryLook: the file is read again every minute, even
// when its size and modification time did not change.
func TestWatcherRereadsOnEveryLook(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  aa:\n    a: b\n")
	f.expect("aa", "ok")
	f.write("secrets:\n  bb:\n    a: b\n") // same size, same mtime
	f.advance(time.Minute)
	f.expect("bb", "ok")
}

// TestWatcherRechecksProtectionWhenUnchanged: chmod and an access-list
// change leave size and mtime alone; the protection is checked every
// minute anyway, and a file that lost it drops every secret.
func TestWatcherRechecksProtectionWhenUnchanged(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  pg:\n    a: b\n")
	f.expect("pg", "ok")
	// The marker makes the check refuse.
	f.write(looseMarker + "secrets:\n  pg:\n    a: b\n")
	f.advance(time.Minute)
	f.expect("", "!mode 0644")
	if f.checks() != 2 {
		t.Fatalf("checks = %d, want 2", f.checks())
	}
	// Fixed (protection restored, nothing else changed): reloaded.
	f.write("secrets:\n  pg:\n    a: b\n")
	f.advance(time.Minute)
	f.expect("pg", "ok")
}

// TestWatcherBrokenFileDropsAllSecrets: never the last good set.
func TestWatcherBrokenFileDropsAllSecrets(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  pg:\n    a: b\n")
	f.expect("pg", "ok")
	f.write("secrets:\n  pg:\n    a: " + secretValue + ": x\n")
	f.advance(time.Minute)
	f.expect("", "!line 3: not valid YAML")
	// Still broken a minute later, nothing changed: still nothing.
	f.advance(time.Minute)
	f.expect("", "!line 3")
	f.write("secrets:\n  fixed:\n    a: b\n")
	f.advance(time.Minute)
	f.expect("fixed", "ok")
}

func TestWatcherFileAppearsAndDisappears(t *testing.T) {
	f := newWatchFixture(t)
	f.expect("", "absent")
	f.write("secrets:\n  pg:\n    a: b\n")
	f.advance(time.Minute)
	f.expect("pg", "ok")
	f.remove()
	f.advance(time.Minute)
	f.expect("", "absent")
}

func TestWatcherLogsChangesWithoutValues(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  pg:\n    password: " + secretValue + "\n")
	f.expect("pg", "ok")
	f.advance(time.Minute)
	f.expect("pg", "ok") // unchanged: no second line
	f.write("secrets:\n  pg:\n    password: " + secretValue + ": x\n")
	f.advance(time.Minute)
	f.expect("", "!line 3")
	log := f.log.String()
	if strings.Contains(log, secretValue) {
		t.Fatalf("log quotes a value: %s", log)
	}
	if n := strings.Count(log, "\n"); n != 2 {
		t.Fatalf("log has %d lines, want 2 (loaded, refused):\n%s", n, log)
	}
	if !strings.Contains(log, "names.secrets=[pg]") || !strings.Contains(log, "line 3") {
		t.Fatalf("log = %s", log)
	}
}

// TestWatcherLogsACommandChange: a change of the command names alone is
// logged, by name and without an argument.
func TestWatcherLogsACommandChange(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  pg:\n    password: x\n")
	f.expect("pg", "ok")
	f.write("secrets:\n  pg:\n    password: x\ncommands:\n  disk: [" + quoted(program()) + ", " + argValue + "]\n")
	f.advance(time.Minute)
	f.expect("pg", "ok")
	log := f.log.String()
	if strings.Contains(log, argValue) {
		t.Fatalf("log quotes an argument: %s", log)
	}
	if n := strings.Count(log, "\n"); n != 2 || !strings.Contains(log, "names.commands=[disk]") {
		t.Fatalf("log has %d lines, want 2 (loaded, commands changed):\n%s", n, log)
	}
}

// TestWatcherConcurrentReaders: the heartbeat and the scheduler read the
// watcher from their own goroutines (run with -race).
func TestWatcherConcurrentReaders(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  pg:\n    a: b\n")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				set, state := f.w.Current()
				if state != StateOK || set.Len() != 1 {
					t.Errorf("Current = %v, %q", set, state)
					return
				}
				f.advance(10 * time.Second)
			}
		}()
	}
	wg.Wait()
}

func TestNewWatcherDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	w := NewWatcher(path, func(string) error { return nil })
	if set, state := w.Current(); state != StateAbsent || set.Len() != 0 {
		t.Fatalf("Current = %v, %q", set, state)
	}
	writeFile(t, path, "secrets:\n  pg:\n    a: b\n")
	// The real clock: the next check is a minute away.
	if _, state := w.Current(); state != StateAbsent {
		t.Fatalf("re-read within the minute: %q", state)
	}
	if w.Path() != path {
		t.Fatalf("Path = %q", w.Path())
	}
}
