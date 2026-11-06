package requirements_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

type ensureResult struct {
	entry requirements.Entry
	lines []string
	err   error
}

// holder runs an Ensure of commitA in the background that holds the
// cache's lock inside galaxy until release is called; it returns once
// galaxy runs.
func holder(t *testing.T, c clone) (release func() ensureResult) {
	t.Helper()
	g := newGalaxy()
	g.Hold = make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	g.OnInstall = func(requirements.Install) error {
		once.Do(func() { close(started) })
		return nil
	}
	done := make(chan ensureResult, 1)
	go func() {
		rec := &recorder{}
		entry, err := newService(g).Ensure(t.Context(), c.request(commitA, c.files(""), rec))
		done <- ensureResult{entry, rec.get(), err}
	}()
	select {
	case <-started:
	case r := <-done:
		t.Fatalf("the holding Ensure returned before galaxy ran: %+v", r)
	case <-time.After(30 * time.Second):
		close(g.Hold)
		t.Fatal("galaxy never ran")
	}
	var closeOnce sync.Once
	return func() ensureResult {
		closeOnce.Do(func() { close(g.Hold) })
		return <-done
	}
}

// waitingService is a service that calls onWait once it finds the lock
// held.
func waitingService(g requirements.Galaxy, timeout time.Duration, onWait func()) *requirements.Service {
	svc := requirements.NewService(g, &requirementstest.Clock{}, timeout)
	var once sync.Once
	requirements.SetLockWaiting(svc, func() { once.Do(onWait) })
	return svc
}

func TestEnsureInstallsOnceForTwoConcurrentCalls(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesAndCollections})
	release := holder(t, c)
	waiting := make(chan struct{})
	g := newGalaxy()
	svc := waitingService(g, 0, func() { close(waiting) })
	second := make(chan ensureResult, 1)
	go func() {
		rec := &recorder{}
		entry, err := svc.Ensure(t.Context(), c.request(commitA, c.files(""), rec))
		second <- ensureResult{entry, rec.get(), err}
	}()
	select {
	case <-waiting:
	case r := <-second:
		release()
		t.Fatalf("the second Ensure returned without waiting for the lock: %+v", r)
	case <-time.After(30 * time.Second):
		release()
		t.Fatal("the second Ensure never waited for the lock")
	}
	a := release()
	b := <-second
	if a.err != nil || b.err != nil {
		t.Fatalf("Ensure: %v, %v", a.err, b.err)
	}
	if a.entry != b.entry || a.entry.IsZero() {
		t.Errorf("entries %+v and %+v, want one", a.entry, b.entry)
	}
	if n := len(g.Calls()); n != 0 {
		t.Errorf("the waiting Ensure ran galaxy %d times, want none", n)
	}
	if want := []string{usingLine(commitA)}; !slices.Equal(b.lines, want) {
		t.Errorf("the waiting Ensure's lines = %q, want %q", b.lines, want)
	}
}

func TestEnsureTimesOutWaitingForTheLock(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	release := holder(t, c)
	g := newGalaxy()
	_, _, err := c.ensure(requirements.NewService(g, &requirementstest.Clock{}, 50*time.Millisecond), commitA)
	assertErr(t, err, "lock requirements cache: timed out after 50ms")
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Errorf("err %v: want context.DeadlineExceeded, not Canceled", err)
	}
	if len(g.Calls()) > 0 {
		t.Errorf("galaxy ran: %+v", g.Calls())
	}
	if r := release(); r.err != nil {
		t.Errorf("the holding Ensure: %v", r.err)
	}
}

func TestEnsureCancelledWhileWaitingForTheLock(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	release := holder(t, c)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	g := newGalaxy()
	svc := waitingService(g, 30*time.Second, cancel)
	_, err := svc.Ensure(ctx, c.request(commitA, c.files(""), nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertErr(t, err, "lock requirements cache: context canceled")
	if len(g.Calls()) > 0 {
		t.Errorf("galaxy ran: %+v", g.Calls())
	}
	if r := release(); r.err != nil {
		t.Errorf("the holding Ensure: %v", r.err)
	}
}

// slowFS is a clone whose Lstat takes delay: the source check's lookups.
type slowFS struct {
	fs.FS
	delay time.Duration
}

func (s slowFS) Stat(name string) (fs.FileInfo, error) { return fs.Stat(s.FS, name) }

func (s slowFS) Lstat(name string) (fs.FileInfo, error) {
	time.Sleep(s.delay)
	return fs.Lstat(s.FS, name)
}

func (s slowFS) ReadLink(name string) (string, error) { return fs.ReadLink(s.FS, name) }

func TestEnsureTimesOutCheckingTheSources(t *testing.T) {
	c := newClone(t, map[string]string{
		"requirements.yml":  "- src: vendor/r.tar.gz\n  name: r\n",
		"vendor/r.tar.gz":   "not a tarball",
		"vendor/other/keep": "x",
	})
	req := c.request(commitA, c.files(""), nil)
	req.Clone = slowFS{FS: os.DirFS(c.dir), delay: 100 * time.Millisecond}
	g := newGalaxy()
	_, err := requirements.NewService(g, &requirementstest.Clock{}, 50*time.Millisecond).Ensure(t.Context(), req)
	assertErr(t, err, `requirements.yml: the source "vendor/r.tar.gz": timed out after 50ms`)
	if len(g.Calls()) > 0 {
		t.Errorf("galaxy ran: %+v", g.Calls())
	}
	c.assertNoCache()
}
