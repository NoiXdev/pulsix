package backup_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/backup"
)

// backupFile is a header followed by a payload, shaped like a real backup
// as far as a storage back end can tell.
func backupFile(t *testing.T, at time.Time, kind, payload string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := backup.WriteHeader(&buf, backup.Header{CreatedAt: at, Kind: kind, PulsixVersion: "test", SchemaVersion: 74}); err != nil {
		t.Fatal(err)
	}
	buf.WriteString(payload)
	return buf.Bytes()
}

// exerciseStorage is the contract every back end has to meet.
func exerciseStorage(t *testing.T, s backup.Storage) {
	t.Helper()
	ctx := context.Background()

	older := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	olderName := backup.ObjectName(older, "scheduled")
	newerName := backup.ObjectName(newer, "manual")
	olderData := backupFile(t, older, "scheduled", "older payload")
	newerData := backupFile(t, newer, "manual", strings.Repeat("n", 100_000))

	// Stored in the "wrong" order on purpose.
	n, err := s.Put(ctx, newerName, bytes.NewReader(newerData))
	if err != nil || n != int64(len(newerData)) {
		t.Fatalf("Put newer = %d, %v", n, err)
	}
	if n, err = s.Put(ctx, olderName, bytes.NewReader(olderData)); err != nil || n != int64(len(olderData)) {
		t.Fatalf("Put older = %d, %v", n, err)
	}

	objs, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 || objs[0].Name != newerName || objs[1].Name != olderName {
		t.Fatalf("List = %+v, want newest first", objs)
	}
	for i, want := range []struct {
		kind string
		at   time.Time
		size int
	}{{"manual", newer, len(newerData)}, {"scheduled", older, len(olderData)}} {
		o := objs[i]
		if o.HeaderErr != nil || o.Header.Kind != want.kind || !o.Header.CreatedAt.Equal(want.at) || o.Header.SchemaVersion != 74 || o.Size != int64(want.size) {
			t.Errorf("object %d = %+v, want kind %s at %v size %d", i, o, want.kind, want.at, want.size)
		}
	}

	rc, err := s.Open(ctx, newerName)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Equal(got, newerData) {
		t.Fatalf("Open returned %d bytes (err %v), want %d", len(got), err, len(newerData))
	}

	if _, err := s.Open(ctx, "pulsix-missing.pxbak"); !errors.Is(err, backup.ErrObjectNotFound) {
		t.Fatalf("Open missing = %v, want ErrObjectNotFound", err)
	}

	if err := s.Delete(ctx, olderName); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, olderName); err != nil {
		t.Fatalf("Delete of a missing object = %v, want nil", err)
	}
	objs, err = s.List(ctx)
	if err != nil || len(objs) != 1 || objs[0].Name != newerName {
		t.Fatalf("List after delete = %+v, %v", objs, err)
	}
	if err := s.Delete(ctx, newerName); err != nil {
		t.Fatal(err)
	}

	// A file that is not a backup is still listed, with the reason.
	if _, err := s.Put(ctx, "pulsix-20261103T030000Z-scheduled.pxbak", strings.NewReader("garbage")); err != nil {
		t.Fatal(err)
	}
	objs, err = s.List(ctx)
	if err != nil || len(objs) != 1 {
		t.Fatalf("List = %+v, %v", objs, err)
	}
	if !errors.Is(objs[0].HeaderErr, backup.ErrNotABackup) {
		t.Fatalf("HeaderErr = %v, want ErrNotABackup", objs[0].HeaderErr)
	}
	if err := s.Delete(ctx, objs[0].Name); err != nil {
		t.Fatal(err)
	}
}

func TestDirStoragePutListOpenDelete(t *testing.T) {
	dir := t.TempDir()
	s := backup.NewDirStorage(dir)
	exerciseStorage(t, s)
	if !strings.Contains(s.Describe(), dir) {
		t.Errorf("Describe = %q, want the folder path", s.Describe())
	}
}

func TestDirStorageIgnoresStrayFiles(t *testing.T) {
	dir := t.TempDir()
	s := backup.NewDirStorage(dir)
	ctx := context.Background()

	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	name := backup.ObjectName(at, "scheduled")
	if _, err := s.Put(ctx, name, bytes.NewReader(backupFile(t, at, "scheduled", "x"))); err != nil {
		t.Fatal(err)
	}
	for _, stray := range []string{name + ".partial", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, stray), []byte("stray"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.pxbak"), 0o700); err != nil {
		t.Fatal(err)
	}

	objs, err := s.List(ctx)
	if err != nil || len(objs) != 1 || objs[0].Name != name {
		t.Fatalf("List = %+v, %v", objs, err)
	}
}

type failingReader struct {
	data []byte
	read bool
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.read {
		return 0, errors.New("disk on fire")
	}
	f.read = true
	return copy(p, f.data), nil
}

func TestDirStorageFailedPutLeavesNoObject(t *testing.T) {
	dir := t.TempDir()
	s := backup.NewDirStorage(dir)
	name := backup.ObjectName(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC), "manual")

	_, err := s.Put(context.Background(), name, &failingReader{data: []byte("half a backup")})
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("Put = %v, want the reader error", err)
	}
	objs, err := s.List(context.Background())
	if err != nil || len(objs) != 0 {
		t.Fatalf("List = %+v, %v", objs, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("directory holds %v (err %v), want it empty", entries, err)
	}
}

func TestDirStoragePutHonoursCancelledContext(t *testing.T) {
	dir := t.TempDir()
	s := backup.NewDirStorage(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, "pulsix-x.pxbak", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put = %v, want context.Canceled", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("directory holds %v", entries)
	}
}

func TestDirStorageRejectsPathNames(t *testing.T) {
	s := backup.NewDirStorage(t.TempDir())
	ctx := context.Background()
	for _, name := range []string{"", ".", "..", "../escape.pxbak", "sub/x.pxbak", `sub\x.pxbak`, ".hidden"} {
		if _, err := s.Put(ctx, name, strings.NewReader("x")); err == nil {
			t.Errorf("Put(%q) accepted", name)
		}
		if _, err := s.Open(ctx, name); err == nil {
			t.Errorf("Open(%q) accepted", name)
		}
		if err := s.Delete(ctx, name); err == nil {
			t.Errorf("Delete(%q) accepted", name)
		}
	}
}

func TestDirStorageCreatesMissingFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	s := backup.NewDirStorage(dir)
	if objs, err := s.List(context.Background()); err != nil || len(objs) != 0 {
		t.Fatalf("List of a missing folder = %+v, %v, want empty", objs, err)
	}
	if _, err := s.Put(context.Background(), "pulsix-x.pxbak", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
}

func TestProbeAgainstDirStorage(t *testing.T) {
	dir := t.TempDir()
	if err := backup.Probe(context.Background(), backup.NewDirStorage(dir)); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("probe left %v behind", entries)
	}

	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	if err := backup.Probe(context.Background(), backup.NewDirStorage(ro)); err == nil {
		t.Fatal("Probe on a read-only folder succeeded")
	}
}

// listWithin runs List and fails the test when it does not return promptly,
// which is how a blocking open on a FIFO shows up.
func listWithin(t *testing.T, s backup.Storage, limit time.Duration) ([]backup.Object, error) {
	t.Helper()
	type result struct {
		objs []backup.Object
		err  error
	}
	done := make(chan result, 1)
	go func() {
		objs, err := s.List(context.Background())
		done <- result{objs, err}
	}()
	select {
	case r := <-done:
		return r.objs, r.err
	case <-time.After(limit):
		t.Fatal("List did not return: it blocked on a storage entry")
		return nil, nil
	}
}

func TestDirListSkipsNonRegularEntries(t *testing.T) {
	dir := t.TempDir()
	s := backup.NewDirStorage(dir)
	ctx := context.Background()

	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	name := backup.ObjectName(at, "scheduled")
	if _, err := s.Put(ctx, name, bytes.NewReader(backupFile(t, at, "scheduled", "x"))); err != nil {
		t.Fatal(err)
	}
	fifo := backup.ObjectName(at.Add(time.Hour), "manual")
	if err := syscall.Mkfifo(filepath.Join(dir, fifo), 0o600); err != nil {
		t.Fatal(err)
	}
	link := backup.ObjectName(at.Add(2*time.Hour), "manual")
	if err := os.Symlink(filepath.Join(dir, name), filepath.Join(dir, link)); err != nil {
		t.Fatal(err)
	}

	objs, err := listWithin(t, s, 5*time.Second)
	if err != nil || len(objs) != 1 || objs[0].Name != name {
		t.Fatalf("List = %+v, %v; want only the regular file", objs, err)
	}

	// Download and restore must not block on, or follow, them either.
	for _, special := range []string{fifo, link} {
		opened := make(chan error, 1)
		go func() {
			rc, err := s.Open(ctx, special)
			if err == nil {
				_ = rc.Close()
			}
			opened <- err
		}()
		select {
		case err := <-opened:
			if err == nil {
				t.Errorf("Open(%s) succeeded, want a refusal", special)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("Open(%s) blocked", special)
		}
	}
}

func TestDirListMarksUnreadableEntryDamaged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 0000 file")
	}
	dir := t.TempDir()
	s := backup.NewDirStorage(dir)
	ctx := context.Background()

	good := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	goodName := backup.ObjectName(good, "scheduled")
	if _, err := s.Put(ctx, goodName, bytes.NewReader(backupFile(t, good, "scheduled", "x"))); err != nil {
		t.Fatal(err)
	}
	badName := backup.ObjectName(good.Add(time.Hour), "manual")
	badPath := filepath.Join(dir, badName)
	if err := os.WriteFile(badPath, backupFile(t, good, "manual", "y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(badPath, 0); err != nil {
		t.Fatal(err)
	}

	objs, err := s.List(ctx)
	if err != nil || len(objs) != 2 {
		t.Fatalf("List = %+v, %v; want both entries and no error", objs, err)
	}
	for _, o := range objs {
		switch o.Name {
		case goodName:
			if o.HeaderErr != nil {
				t.Errorf("good backup: HeaderErr = %v", o.HeaderErr)
			}
		case badName:
			if o.HeaderErr == nil {
				t.Errorf("unreadable backup listed without HeaderErr")
			}
		default:
			t.Errorf("unexpected object %s", o.Name)
		}
	}
}

func TestDirCleanPartialsRemovesOnlyRegularPartialFiles(t *testing.T) {
	dir := t.TempDir()
	s := backup.NewDirStorage(dir)
	ctx := context.Background()

	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	kept := backup.ObjectName(at, "scheduled")
	if _, err := s.Put(ctx, kept, bytes.NewReader(backupFile(t, at, "scheduled", "x"))); err != nil {
		t.Fatal(err)
	}
	stale := backup.ObjectName(at.Add(time.Hour), "manual") + ".partial"
	stale2 := backup.ObjectName(at.Add(2*time.Hour), "manual") + ".partial"
	for _, name := range []string{stale, stale2} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("half"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// None of these may be touched: another file, a partial file some other
	// program left (the folder may be shared), a symlink and a directory that
	// merely carry the suffix, and a partial file nested in a folder.
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, "foo.partial")
	if err := os.WriteFile(foreign, []byte("not ours"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "precious")
	if err := os.WriteFile(target, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.pxbak.partial")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dir, "folder.pxbak.partial")
	if err := os.Mkdir(subdir, 0o750); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(subdir, "inner.pxbak.partial")
	if err := os.WriteFile(nested, []byte("inner"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleaner, ok := s.(backup.PartialCleaner)
	if !ok {
		t.Fatal("the folder storage does not implement PartialCleaner")
	}
	n, err := cleaner.CleanPartials(ctx)
	if err != nil || n != 2 {
		t.Fatalf("CleanPartials = %d, %v; want 2 files removed", n, err)
	}
	for _, name := range []string{stale, stale2} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists: %v", name, err)
		}
	}
	for _, p := range []string{filepath.Join(dir, kept), other, foreign, target, nested} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was touched: %v", p, err)
		}
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was touched: %v", err)
	}
	if fi, err := os.Lstat(subdir); err != nil || !fi.IsDir() {
		t.Errorf("the directory was touched: %v", err)
	}

	// Nothing left to remove, and a folder that does not exist yet is fine.
	if n, err := cleaner.CleanPartials(ctx); err != nil || n != 0 {
		t.Errorf("second CleanPartials = %d, %v; want 0, nil", n, err)
	}
	missing := backup.NewDirStorage(filepath.Join(dir, "missing")).(backup.PartialCleaner)
	if n, err := missing.CleanPartials(ctx); err != nil || n != 0 {
		t.Errorf("CleanPartials on a missing folder = %d, %v; want 0, nil", n, err)
	}
}
