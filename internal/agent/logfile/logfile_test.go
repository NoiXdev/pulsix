package logfile

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func mustWrite(t *testing.T, w *Writer, p []byte) {
	t.Helper()
	n, err := w.Write(p)
	if err != nil || n != len(p) {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
}

func TestWriterRotatesAtTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	w, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	first, second := bytes.Repeat([]byte("a"), 60), bytes.Repeat([]byte("b"), 60)
	mustWrite(t, w, first)
	mustWrite(t, w, second)
	if got := readFile(t, path+".1"); !bytes.Equal(got, first) {
		t.Errorf("path.1 = %q, want first write", got)
	}
	if got := readFile(t, path); !bytes.Equal(got, second) {
		t.Errorf("path = %q, want second write", got)
	}
}

func TestWriterReplacesAnOlderRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.log")
	w, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	for _, c := range []byte("abc") {
		mustWrite(t, w, bytes.Repeat([]byte{c}, 60))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "agent.log" || names[1] != "agent.log.1" {
		t.Fatalf("files = %v, want exactly agent.log and agent.log.1", names)
	}
	if got := readFile(t, path+".1"); !bytes.Equal(got, bytes.Repeat([]byte("b"), 60)) {
		t.Errorf("path.1 = %q, want the second write", got)
	}
	if got := readFile(t, path); !bytes.Equal(got, bytes.Repeat([]byte("c"), 60)) {
		t.Errorf("path = %q, want the third write", got)
	}
}

func TestWriterCountsAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	old := bytes.Repeat([]byte("o"), 90)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	fresh := bytes.Repeat([]byte("n"), 20)
	mustWrite(t, w, fresh)
	if got := readFile(t, path+".1"); !bytes.Equal(got, old) {
		t.Errorf("path.1 = %q, want the pre-existing content", got)
	}
	if got := readFile(t, path); !bytes.Equal(got, fresh) {
		t.Errorf("path = %q, want the new write", got)
	}
}

func TestWriterCreatesTheParentDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	w, err := Open(filepath.Join(dir, "agent.log"), 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	mustWrite(t, w, []byte("hi"))
	if _, err := os.Stat(filepath.Join(dir, "agent.log")); err != nil {
		t.Fatal(err)
	}
}

func TestWriterConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	w, err := Open(path, 5000)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte("0123456789\n")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if _, err := w.Write(line); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	total := len(readFile(t, path))
	if b, err := os.ReadFile(path + ".1"); err == nil {
		total += len(b)
	}
	// 8800 bytes at a 5000-byte limit rotate once, so nothing is dropped.
	if want := 8 * 100 * len(line); total != want {
		t.Errorf("total = %d bytes, want %d", total, want)
	}
}

func TestALineLargerThanTheLimitIsStillWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	w, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	mustWrite(t, w, []byte("small\n"))
	big := bytes.Repeat([]byte("x"), 150)
	mustWrite(t, w, big)
	if got := readFile(t, path); !bytes.Equal(got, big) {
		t.Errorf("path holds %d bytes, want the whole 150-byte line", len(got))
	}
}

func TestWriteAfterAFailedReopenTriesAgain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.log")
	w, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	mustWrite(t, w, bytes.Repeat([]byte("a"), 60))
	w.openFile = func(string) (*os.File, error) { return nil, os.ErrPermission }
	if _, err := w.Write(bytes.Repeat([]byte("b"), 60)); err == nil {
		t.Fatal("want an error when the reopen fails")
	}
	w.openFile = openAppend
	fresh := bytes.Repeat([]byte("c"), 10)
	mustWrite(t, w, fresh)
	if got := readFile(t, path); !bytes.Equal(got, fresh) {
		t.Errorf("path = %q, want the write after recovery", got)
	}
}

func TestWriteAfterCloseFails(t *testing.T) {
	w, err := Open(filepath.Join(t.TempDir(), "agent.log"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("want an error writing after Close")
	}
}

// blockRotation puts a non-empty directory at path.1, which makes both the
// removal of the old rotation and the rename onto it fail — as a log tailer
// holding path.1 open does on Windows.
func blockRotation(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path+".1", "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
}

const rotationNotice = "log rotation failed"

// TestAFailedRotationKeepsTheWriterUsable: the line that triggered the
// failed rotation still lands, in the current file, after a notice that
// says why the file grows past its limit; so do the lines after it.
func TestAFailedRotationKeepsTheWriterUsable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	blockRotation(t, path)
	w, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	first, second := bytes.Repeat([]byte("a"), 60), bytes.Repeat([]byte("b"), 60)
	mustWrite(t, w, first)
	mustWrite(t, w, second) // past the limit: the rotation fails
	got := readFile(t, path)
	if !bytes.HasPrefix(got, first) || !bytes.HasSuffix(got, second) {
		t.Fatalf("path = %q, want the first write, then the second", got)
	}
	if n := bytes.Count(got, []byte(rotationNotice)); n != 1 {
		t.Errorf("path carries %d rotation notices, want 1: %q", n, got)
	}
	mustWrite(t, w, []byte("c"))
	if got := readFile(t, path); !bytes.HasSuffix(got, []byte("c")) {
		t.Errorf("path = %q, want the later write appended", got)
	}
}

// TestAFailedRotationIsRetriedAfterTheInterval: after a failed rotation the
// writer does not retry on every line, only once the retry interval has
// passed, and rotates normally once the cause is gone.
func TestAFailedRotationIsRetriedAfterTheInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	blockRotation(t, path)
	w, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	notices := func() int { return bytes.Count(readFile(t, path), []byte(rotationNotice)) }

	mustWrite(t, w, bytes.Repeat([]byte("a"), 120)) // a fresh file: no rotation
	mustWrite(t, w, []byte("b\n"))                  // fails, retry in an interval
	for i := 0; i < 5; i++ {
		now = now.Add(rotationRetryInterval / 10)
		mustWrite(t, w, []byte("b\n"))
	}
	if n := notices(); n != 1 {
		t.Fatalf("%d rotation notices within the retry interval, want 1", n)
	}

	now = now.Add(rotationRetryInterval)
	mustWrite(t, w, []byte("b\n")) // retried, fails again
	if n := notices(); n != 2 {
		t.Fatalf("%d rotation notices after the retry interval, want 2", n)
	}

	if err := os.RemoveAll(path + ".1"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(rotationRetryInterval)
	last := []byte("z\n")
	mustWrite(t, w, last) // the cause is gone: the rotation succeeds
	if got := readFile(t, path); !bytes.Equal(got, last) {
		t.Errorf("path = %q, want only the write after the rotation", got)
	}
	rotated := readFile(t, path+".1")
	if !bytes.HasPrefix(rotated, bytes.Repeat([]byte("a"), 120)) || bytes.Count(rotated, []byte("b\n")) != 7 {
		t.Errorf("path.1 = %q, want every earlier write", rotated)
	}
}
