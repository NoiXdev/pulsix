// The repo cache sweep (backlog 7.95): what it removes, what it leaves,
// and when the loop runs it.
package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// mkdirs creates each directory below root with a file inside, so a
// removal has contents to remove.
func mkdirs(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// exists reports whether rel below root is there, without following a
// symlink.
func exists(t *testing.T, root, rel string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(root, rel))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// TestRemoveStaleRepoCaches removes the clone, the requirements cache and
// the legacy galaxy directory of a repository the manager no longer lists,
// the directories of other organizations and its own interrupted removals.
// Everything it did not create stays: names that are not a canonical uuid,
// symlinks (and what they point at) and regular files.
func TestRemoveStaleRepoCaches(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	org := uuid.Must(uuid.NewV7())
	other := uuid.Must(uuid.NewV7())
	known := uuid.Must(uuid.NewV7())
	gone := uuid.Must(uuid.NewV7())
	goneReqs := uuid.Must(uuid.NewV7())
	linked := uuid.Must(uuid.NewV7())
	file := uuid.Must(uuid.NewV7())
	upper := uuid.Must(uuid.NewV7())
	o := org.String()

	mkdirs(t, root,
		o+"/"+known.String(), o+"/"+known.String()+".requirements/c-1",
		o+"/"+gone.String(), o+"/"+gone.String()+".requirements/c-1", o+"/"+gone.String()+".galaxy",
		o+"/"+goneReqs.String()+".requirements",
		o+"/"+strings.ToUpper(upper.String()),
		o+"/not-a-repo", o+"/"+gone.String()+".other",
		o+"/.removing-1234", ".removing-5678",
		other.String()+"/"+gone.String(),
		"not-an-org",
	)
	mkdirs(t, outside, "target")
	if err := os.Symlink(filepath.Join(outside, "target"), filepath.Join(root, o, linked.String())); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, uuid.Must(uuid.NewV7()).String())); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, o, file.String()), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedReqs := uuid.Must(uuid.NewV7()).String() + ".requirements"
	if err := os.Symlink(filepath.Join(outside, "target"), filepath.Join(root, o, linkedReqs)); err != nil {
		t.Fatal(err)
	}

	l := NewLoop(cfgFor(t.TempDir(), zoneID), &fakeWorkerClient{}, &scriptExecutor{})
	removed, err := l.removeStaleRepoCaches(t.Context(), root, org, map[uuid.UUID]bool{known: true})
	if err != nil {
		t.Fatalf("removeStaleRepoCaches: %v", err)
	}

	wantRemoved := []string{
		o + "/" + gone.String(), o + "/" + gone.String() + ".galaxy", o + "/" + gone.String() + ".requirements",
		o + "/" + goneReqs.String() + ".requirements",
		o + "/.removing-1234", ".removing-5678",
		other.String(),
	}
	slices.Sort(wantRemoved)
	if !slices.Equal(removed, wantRemoved) {
		t.Errorf("removed = %v\nwant      %v", removed, wantRemoved)
	}
	for _, rel := range wantRemoved {
		if exists(t, root, rel) {
			t.Errorf("%s is still there", rel)
		}
	}
	for _, rel := range []string{
		o + "/" + known.String() + "/f", o + "/" + known.String() + ".requirements/c-1/f",
		o + "/" + strings.ToUpper(upper.String()), o + "/not-a-repo", o + "/" + gone.String() + ".other",
		o + "/" + linked.String(), o + "/" + linkedReqs, o + "/" + file.String(), "not-an-org",
	} {
		if !exists(t, root, rel) {
			t.Errorf("%s was removed; the sweep must leave it", rel)
		}
	}
	if !exists(t, outside, "target/f") {
		t.Error("the sweep removed what a symlink points at")
	}
	// Nothing is left behind under a temporary name.
	entries, err := os.ReadDir(filepath.Join(root, o))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), removingPrefix) {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

// TestRemoveStaleRepoCachesWithoutOrgDir: a worker that never synced a
// repository has no directory for its organization; that is no error.
func TestRemoveStaleRepoCachesWithoutOrgDir(t *testing.T) {
	root := t.TempDir()
	l := NewLoop(cfgFor(t.TempDir(), zoneID), &fakeWorkerClient{}, &scriptExecutor{})
	removed, err := l.removeStaleRepoCaches(t.Context(), root, uuid.Must(uuid.NewV7()), nil)
	if err != nil || len(removed) != 0 {
		t.Fatalf("removeStaleRepoCaches = %v, %v; want nothing removed and no error", removed, err)
	}
}

// sweepFixture is a data dir whose repo cache holds one clone of a listed
// repository and one of a repository the manager no longer has.
type sweepFixture struct {
	cfg         Config
	cache       string
	org         uuid.UUID
	keep, stale string // paths relative to cache
}

func newSweepFixture(t *testing.T) sweepFixture {
	t.Helper()
	cfg := cfgFor(t.TempDir(), zoneID)
	cache := filepath.Join(cfg.DataDir, repoCacheDirName)
	org := uuid.Must(uuid.NewV7())
	f := sweepFixture{
		cfg: cfg, cache: cache, org: org,
		keep:  org.String() + "/" + uuid.Must(uuid.NewV7()).String(),
		stale: org.String() + "/" + uuid.Must(uuid.NewV7()).String(),
	}
	mkdirs(t, cache, f.keep, f.stale)
	return f
}

// listing is the manager's answer naming only the fixture's kept clone.
func (f sweepFixture) listing() *workerpb.ListRepositoriesResponse {
	return &workerpb.ListRepositoriesResponse{OrgId: f.org.String(), RepoIds: []string{filepath.Base(f.keep)}}
}

// TestLoopSweepsTheRepoCacheBeforeItsFirstClaim: at start the loop asks
// the manager for the organization's repositories and removes the caches
// of every other one before it claims.
func TestLoopSweepsTheRepoCacheBeforeItsFirstClaim(t *testing.T) {
	f := newSweepFixture(t)
	client := &fakeWorkerClient{repos: f.listing()}
	l := NewLoop(f.cfg, client, &scriptExecutor{})

	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(client.claimsNow()) > 0 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if exists(t, f.cache, f.stale) {
		t.Error("the clone of a repository the manager no longer lists is still there")
	}
	if !exists(t, f.cache, f.keep+"/f") {
		t.Error("the clone of a listed repository was removed")
	}
}

// TestLoopSweepsTheRepoCacheEverySixHours: the loop asks again only once
// repoCacheSweepInterval has passed since its last attempt.
func TestLoopSweepsTheRepoCacheEverySixHours(t *testing.T) {
	f := newSweepFixture(t)
	client := &fakeWorkerClient{repos: f.listing()}
	start := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	var clock atomicTime
	clock.Store(start)
	l := NewLoop(f.cfg, client, &scriptExecutor{}, WithNow(clock.Load))

	cancel, done := runLoop(t, l)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
	}()
	waitFor(t, 2*time.Second, func() bool { return len(client.claimsNow()) > 3 })
	if n := client.listsNow(); n != 1 {
		t.Fatalf("lists before the interval passed = %d, want 1", n)
	}
	clock.Store(start.Add(repoCacheSweepInterval - time.Second))
	claims := len(client.claimsNow())
	waitFor(t, 2*time.Second, func() bool { return len(client.claimsNow()) > claims+3 })
	if n := client.listsNow(); n != 1 {
		t.Fatalf("lists a second before the interval = %d, want 1", n)
	}
	clock.Store(start.Add(repoCacheSweepInterval))
	waitFor(t, 2*time.Second, func() bool { return client.listsNow() == 2 })
}

// TestLoopRetriesAFailedListSoon: a list that fails (the manager not up
// yet when the worker starts) is tried again after repoListRetry, not
// after the full sweep interval.
func TestLoopRetriesAFailedListSoon(t *testing.T) {
	f := newSweepFixture(t)
	client := &fakeWorkerClient{reposErr: status.Error(codes.Unavailable, "down")}
	start := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	var clock atomicTime
	clock.Store(start)
	l := NewLoop(f.cfg, client, &scriptExecutor{}, WithNow(clock.Load))

	cancel, done := runLoop(t, l)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
	}()
	waitFor(t, 2*time.Second, func() bool { return len(client.claimsNow()) > 3 })
	if n := client.listsNow(); n != 1 {
		t.Fatalf("lists before the retry = %d, want 1", n)
	}
	clock.Store(start.Add(repoListRetry))
	waitFor(t, 2*time.Second, func() bool { return client.listsNow() == 2 })
}

// TestRemoveCacheDirRetriesAfterThePlayUID: what the worker cannot remove
// alone is cleared as the play uid, and the worker removes the rest after
// it even when the play uid's own pass reports a failure (it cannot unlink
// the worker's entries).
func TestRemoveCacheDirRetriesAfterThePlayUID(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes a read-only directory's contents without help")
	}
	root := t.TempDir()
	org := uuid.Must(uuid.NewV7())
	stale := org.String() + "/" + uuid.Must(uuid.NewV7()).String() + ".requirements"
	mkdirs(t, root, stale+"/.tmp-1/roles/r")
	locked := filepath.Join(root, stale, ".tmp-1", "roles")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	cfg := cfgFor(t.TempDir(), zoneID)
	cfg.PlayUID = 65533
	l := NewLoop(cfg, &fakeWorkerClient{}, &scriptExecutor{})
	var asked []string
	l.removeAsPlayUID = func(_ context.Context, _ Config, dirs ...string) error {
		asked = append(asked, dirs...)
		// The play uid opens up what it owns, then fails on the worker's
		// entries, as the real rm does.
		_ = filepath.WalkDir(dirs[0], func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
		return errors.New("rm: cannot remove '.lock': Permission denied")
	}

	removed, err := l.removeStaleRepoCaches(t.Context(), root, org, nil)
	if err != nil {
		t.Fatalf("removeStaleRepoCaches: %v", err)
	}
	if !slices.Equal(removed, []string{stale}) {
		t.Errorf("removed = %v, want [%s]", removed, stale)
	}
	if exists(t, root, stale) {
		t.Error("the requirements cache is still there")
	}
	if len(asked) != 1 || !strings.HasPrefix(filepath.Base(asked[0]), removingPrefix) {
		t.Errorf("the play uid was asked about %v, want the renamed directory", asked)
	}
}

// TestSweepRepoCacheRemovesNothingWithoutAListing: whatever keeps the
// worker from a valid listing (an older manager, a failed call, an answer
// it cannot read) leaves the cache as it is.
func TestSweepRepoCacheRemovesNothingWithoutAListing(t *testing.T) {
	cases := []struct {
		name    string
		client  func(f sweepFixture) *fakeWorkerClient
		wantLog string
	}{
		{"older manager", func(sweepFixture) *fakeWorkerClient { return &fakeWorkerClient{} },
			"the manager does not list repositories yet"},
		{"failed call", func(sweepFixture) *fakeWorkerClient {
			return &fakeWorkerClient{reposErr: status.Error(codes.Unavailable, "down")}
		}, "could not list the organization's repositories"},
		{"unreadable organization", func(f sweepFixture) *fakeWorkerClient {
			r := f.listing()
			r.OrgId = "nope"
			return &fakeWorkerClient{repos: r}
		}, "could not read the manager's repository list"},
		{"unreadable repository", func(f sweepFixture) *fakeWorkerClient {
			r := f.listing()
			r.RepoIds = append(r.RepoIds, "nope")
			return &fakeWorkerClient{repos: r}
		}, "could not read the manager's repository list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSweepFixture(t)
			var logs bytes.Buffer
			l := NewLoop(f.cfg, tc.client(f), &scriptExecutor{},
				WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
			l.sweepRepoCache(context.Background())
			if !exists(t, f.cache, f.stale+"/f") || !exists(t, f.cache, f.keep+"/f") {
				t.Error("the sweep removed a cache without a valid listing")
			}
			if !strings.Contains(logs.String(), tc.wantLog) {
				t.Errorf("log = %q, want it to contain %q", logs.String(), tc.wantLog)
			}
		})
	}
}

// TestSweepRepoCacheEmptyListingRemovesEveryClone: an organization without
// repositories keeps none of their caches.
func TestSweepRepoCacheEmptyListingRemovesEveryClone(t *testing.T) {
	f := newSweepFixture(t)
	l := NewLoop(f.cfg, &fakeWorkerClient{repos: &workerpb.ListRepositoriesResponse{OrgId: f.org.String()}}, &scriptExecutor{})
	l.sweepRepoCache(context.Background())
	if exists(t, f.cache, f.stale) || exists(t, f.cache, f.keep) {
		t.Error("a clone survived an empty listing")
	}
	if !exists(t, f.cache, f.org.String()) {
		t.Error("the organization's own directory was removed")
	}
}

// atomicTime is a clock the test moves while the loop reads it.
type atomicTime struct{ v atomic.Pointer[time.Time] }

func (a *atomicTime) Store(t time.Time) { a.v.Store(&t) }
func (a *atomicTime) Load() time.Time   { return *a.v.Load() }
