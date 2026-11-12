package secrets

import (
	"bytes"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeInfo is the part of a FileInfo the watcher compares.
type fakeInfo struct {
	size  int64
	mtime time.Time
	mode  fs.FileMode
}

func (f fakeInfo) Name() string       { return "agent.yaml" }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return f.mtime }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

// watchFixture drives a Watcher with a fake clock and a fake stat over a
// real file: the test decides what the stat reports, so it can change the
// file's content without the watcher noticing, or the other way round.
type watchFixture struct {
	t     *testing.T
	path  string
	mu    sync.Mutex
	now   time.Time
	info  *fakeInfo // nil: the file does not exist
	stats int
	check *markerCheck
	log   bytes.Buffer
	w     *Watcher
}

func newWatchFixture(t *testing.T) *watchFixture {
	f := &watchFixture{
		t:     t,
		path:  filepath.Join(t.TempDir(), "agent.yaml"),
		now:   time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC),
		check: &markerCheck{},
	}
	f.w = NewWatcher(f.path, f.checkFile,
		WithClock(f.clock),
		WithStat(f.stat),
		WithLogger(slog.New(slog.NewTextHandler(&f.log, nil))))
	return f
}

func (f *watchFixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *watchFixture) stat(path string) (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats++
	if path != f.path {
		f.t.Errorf("stat(%q), want %q", path, f.path)
	}
	if f.info == nil {
		return nil, &fs.PathError{Op: "lstat", Path: path, Err: fs.ErrNotExist}
	}
	return *f.info, nil
}

func (f *watchFixture) checkFile(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.check.check(path)
}

// write puts content on disk and reports the given size and mtime offset
// (seconds after the fixture's start) through the fake stat.
func (f *watchFixture) write(content string, size int64, mtime int) {
	f.t.Helper()
	writeFile(f.t, f.path, content)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info = &fakeInfo{size: size, mtime: time.Date(2026, 11, 12, 9, 0, mtime, 0, time.UTC), mode: 0o600}
}

func (f *watchFixture) remove() {
	f.t.Helper()
	if err := os.Remove(f.path); err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info = nil
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

func (f *watchFixture) statCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats
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
	f.write("secrets:\n  pg:\n    a: b\n", 20, 0)
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
	f.write("secrets:\n  pg:\n    a: b\n", 20, 0)
	f.expect("pg", "ok")
	f.write("secrets:\n  other:\n    a: b\n", 25, 5)
	f.advance(59 * time.Second)
	f.expect("pg", "ok")
	if f.statCalls() != 1 || f.checks() != 1 {
		t.Fatalf("within the minute: stats = %d, checks = %d; want 1 and 1", f.statCalls(), f.checks())
	}
	f.advance(time.Second)
	f.expect("other", "ok")
	if f.statCalls() != 2 {
		t.Fatalf("stats = %d, want 2", f.statCalls())
	}
}

func TestWatcherRereadsOnlyWhenTheFileChanged(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  pg:\n    a: b\n", 20, 0)
	f.expect("pg", "ok")

	// Content changed on disk, but size and mtime as before: not re-read.
	f.write("secrets:\n  xx:\n    a: b\n", 20, 0)
	f.advance(time.Minute)
	f.expect("pg", "ok")

	// The modification time changed: re-read.
	f.write("secrets:\n  mt:\n    a: b\n", 20, 1)
	f.advance(time.Minute)
	f.expect("mt", "ok")

	// Only the size changed: re-read.
	f.write("secrets:\n  sz:\n    a: b\n", 21, 1)
	f.advance(time.Minute)
	f.expect("sz", "ok")
}

// TestWatcherRechecksProtectionWhenUnchanged: chmod and an access-list
// change leave size and mtime alone; the protection is checked every
// minute anyway, and a file that lost it drops every secret.
func TestWatcherRechecksProtectionWhenUnchanged(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  pg:\n    a: b\n", 20, 0)
	f.expect("pg", "ok")
	// The marker makes the check refuse; the stat reports no change.
	f.write(looseMarker+"secrets:\n  pg:\n    a: b\n", 20, 0)
	f.advance(time.Minute)
	f.expect("", "!mode 0644")
	if f.checks() != 2 {
		t.Fatalf("checks = %d, want 2", f.checks())
	}
	// Fixed (protection restored, nothing else changed): reloaded.
	f.write("secrets:\n  pg:\n    a: b\n", 20, 0)
	f.advance(time.Minute)
	f.expect("pg", "ok")
}

// TestWatcherBrokenFileDropsAllSecrets: never the last good set.
func TestWatcherBrokenFileDropsAllSecrets(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  pg:\n    a: b\n", 20, 0)
	f.expect("pg", "ok")
	f.write("secrets:\n  pg:\n    a: "+secretValue+": x\n", 30, 1)
	f.advance(time.Minute)
	f.expect("", "!line 3: not valid YAML")
	// Still broken a minute later, nothing changed: still nothing.
	f.advance(time.Minute)
	f.expect("", "!line 3")
	f.write("secrets:\n  fixed:\n    a: b\n", 31, 2)
	f.advance(time.Minute)
	f.expect("fixed", "ok")
}

func TestWatcherFileAppearsAndDisappears(t *testing.T) {
	f := newWatchFixture(t)
	f.expect("", "absent")
	f.write("secrets:\n  pg:\n    a: b\n", 20, 0)
	f.advance(time.Minute)
	f.expect("pg", "ok")
	f.remove()
	f.advance(time.Minute)
	f.expect("", "absent")
}

func TestWatcherLogsChangesWithoutValues(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  pg:\n    password: "+secretValue+"\n", 20, 0)
	f.expect("pg", "ok")
	f.advance(time.Minute)
	f.expect("pg", "ok") // unchanged: no second line
	f.write("secrets:\n  pg:\n    password: "+secretValue+": x\n", 30, 1)
	f.advance(time.Minute)
	f.expect("", "!line 3")
	log := f.log.String()
	if strings.Contains(log, secretValue) {
		t.Fatalf("log quotes a value: %s", log)
	}
	if n := strings.Count(log, "\n"); n != 2 {
		t.Fatalf("log has %d lines, want 2 (loaded, refused):\n%s", n, log)
	}
	if !strings.Contains(log, "names=[pg]") || !strings.Contains(log, "line 3") {
		t.Fatalf("log = %s", log)
	}
}

// TestWatcherConcurrentReaders: the heartbeat and the scheduler read the
// watcher from their own goroutines (run with -race).
func TestWatcherConcurrentReaders(t *testing.T) {
	f := newWatchFixture(t)
	f.write("secrets:\n  pg:\n    a: b\n", 20, 0)
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
