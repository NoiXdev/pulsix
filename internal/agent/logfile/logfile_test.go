package logfile

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
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

func TestAFailedRotationKeepsTheWriterUsable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	// A non-empty directory at path.1 makes both Remove and Rename fail.
	if err := os.MkdirAll(filepath.Join(path+".1", "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	mustWrite(t, w, bytes.Repeat([]byte("a"), 60))
	if _, err := w.Write(bytes.Repeat([]byte("b"), 60)); err == nil {
		t.Fatal("want an error when rotation fails")
	}
	mustWrite(t, w, []byte("c"))
	if got := readFile(t, path); !bytes.HasSuffix(got, []byte("c")) {
		t.Errorf("path = %q, want the later write appended", got)
	}
}
