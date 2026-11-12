//go:build !windows

package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// modeCheck refuses a file that anyone but its owner may read or write,
// like the agent's own rule does on Unix.
func modeCheck(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s is accessible to mode %04o", path, perm)
	}
	return nil
}

// TestWatcherNoticesAModeOnlyChange: chmod changes neither size, content
// nor modification time; the next look still refuses the file, and takes
// it back once it is private again.
func TestWatcherNoticesAModeOnlyChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	writeFile(t, path, "secrets:\n  pg:\n    password: "+secretValue+"\n")
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	w := NewWatcher(path, modeCheck, WithClock(func() time.Time { return now }))
	if set, state := w.Current(); state != StateOK || set.Len() != 1 {
		t.Fatalf("Current = %v, %q", set, state)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if set, state := w.Current(); state == StateOK || set.Len() != 0 {
		t.Fatalf("after chmod 0644: Current = %v, %q", set, state)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if set, state := w.Current(); state != StateOK || set.Len() != 1 {
		t.Fatalf("after chmod 0600: Current = %v, %q", set, state)
	}
}

// TestWatcherReportsAStatErrorAsAnError: only a file that does not exist
// is absent. A path the agent cannot even look at (here: a directory it
// may not search) is an error, also after a look that found no file.
func TestWatcherReportsAStatErrorAsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root searches every directory")
	}
	dir := filepath.Join(t.TempDir(), "conf")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // #nosec G302 -- test dir
	f := newWatchFixtureAt(t, filepath.Join(dir, "agent.yaml"))
	f.expect("", "absent")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	f.expect("", "!permission denied")
}
