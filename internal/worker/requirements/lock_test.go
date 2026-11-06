package requirements

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// openTestRoot opens dir as a root that is closed when the test ends.
func openTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestLockExcludesASecondHolder(t *testing.T) {
	dir := t.TempDir()
	first := openTestRoot(t, dir)
	second := openTestRoot(t, dir)

	unlock, err := lock(context.Background(), first)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := lock(ctx, second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock while held: err = %v, want context.DeadlineExceeded", err)
	}
	if waited := time.Since(start); waited < 90*time.Millisecond {
		t.Fatalf("second lock gave up after %v, before its context ended", waited)
	}

	unlock()

	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	third, err := lock(ctx, second)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	third()

	fi, err := os.Lstat(filepath.Join(dir, ".lock"))
	if err != nil {
		t.Fatalf("stat .lock: %v", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".lock mode = %v, want a regular file with 0600", fi.Mode())
	}
}

func TestLockWaitingIsCancellable(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lock(context.Background(), openTestRoot(t, dir))
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	waiter := openTestRoot(t, dir)
	go func() {
		_, err := lock(ctx, waiter)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled wait for the lock did not return")
	}
}

func TestLockWithAnEndedContextDoesNotLock(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lock(ctx, openTestRoot(t, dir)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// Nobody holds it, so a live context gets it at once.
	unlock, err := lock(context.Background(), openTestRoot(t, dir))
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	unlock()
}

func TestLockGivesOneHolderAtATime(t *testing.T) {
	dir := t.TempDir()
	const holders = 8
	var inside, most atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, holders)
	for range holders {
		root := openTestRoot(t, dir)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			unlock, err := lock(ctx, root)
			if err != nil {
				errs <- err
				return
			}
			n := inside.Add(1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			inside.Add(-1)
			unlock()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("lock: %v", err)
	}
	if got := most.Load(); got != 1 {
		t.Fatalf("at most %d holders at once, want 1", got)
	}
}

func TestLockRestoresTheLockFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	unlock, err := lock(context.Background(), openTestRoot(t, dir))
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	unlock()
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".lock mode = %v (%v), want 0600", fi.Mode(), err)
	}
}

func TestLockRefusesASymlinkedLockFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "elsewhere"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", filepath.Join(dir, ".lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := lock(context.Background(), openTestRoot(t, dir)); !errors.Is(err, errLockNotFile) {
		t.Fatalf("lock through a symlinked .lock: err = %v, want errLockNotFile", err)
	}
	fi, err := os.Lstat(filepath.Join(dir, ".lock"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".lock is no longer the symlink: %v, %v", fi, err)
	}
}

// lockHelperEnv makes the test binary a second process that holds the lock of
// the directory it names until its stdin closes (TestLockExcludesAnotherProcess).
const lockHelperEnv = "PULSIX_TEST_REQUIREMENTS_LOCK_DIR"

func TestLockHelperProcess(t *testing.T) {
	dir := os.Getenv(lockHelperEnv)
	if dir == "" {
		t.Skip("only runs as the helper of TestLockExcludesAnotherProcess")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lock(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stdout.WriteString("locked\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	unlock()
}

func TestLockExcludesAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("helper did not take the lock: %q, %v", line, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := lock(ctx, openTestRoot(t, dir)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock while another process holds it: err = %v, want context.DeadlineExceeded", err)
	}

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unlock, err := lock(ctx, openTestRoot(t, dir))
	if err != nil {
		t.Fatalf("lock after the other process let go: %v", err)
	}
	unlock()
}
