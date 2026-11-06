// The sync's requirements step (spec 2026-11-06-requirements-cache, goals
// 5 and 6): the sync installs a commit's requirements through the same
// per-commit cache as runs, streams the step as stdout lines ahead of its
// ok event and fails the job when the install fails.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

// requirementsClone is a clone at <tmp>/repo with files, so its cache
// directory <tmp>/repo.requirements stays inside the test's temp dir.
func requirementsClone(t *testing.T, files map[string]string) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "repo")
	writeTree(t, clone, files)
	return clone
}

// fakeRequirements is the requirements service over a fake galaxy.
func fakeRequirements(g *requirementstest.Galaxy) *requirements.Service {
	return requirements.NewService(g, &requirementstest.Clock{Step: time.Second}, 0)
}

// runRequirementsSync runs one sync of clone with subpath through e and
// returns its outcome and events.
func runRequirementsSync(t *testing.T, e *SyncExecutor, clone, subpath string) (Status, map[string]any, []*workerpb.RunEvent, error) {
	t.Helper()
	c := &evCollector{}
	status, stats, err := e.Run(context.Background(),
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", subpath, ""),
		RunEnv{WorkDir: t.TempDir(), RepoCacheDir: filepath.Dir(clone)}, c.sink)
	return status, stats, c.events, err
}

// lineText is the text of a stdout event's {"text": …} payload.
func lineText(t *testing.T, ev *workerpb.RunEvent) string {
	t.Helper()
	if ev.GetState() != "stdout" {
		t.Fatalf("event %+v is not a stdout line", ev)
	}
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(ev.GetPayload()), &p); err != nil {
		t.Fatalf("line payload %q: %v", ev.GetPayload(), err)
	}
	return p.Text
}

func TestSyncExecutorInstallsRequirementsForItsCommit(t *testing.T) {
	clone := requirementsClone(t, map[string]string{"ansible/requirements.yml": "- name: acme.web\n"})
	g := &requirementstest.Galaxy{Roles: map[string]string{"acme.web/meta/main.yml": "{}\n"}, Lines: []string{"- acme.web was installed successfully"}}
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(g))
	status, stats, events, err := runRequirementsSync(t, e, clone, "ansible")
	if err != nil || status != StatusSucceeded {
		t.Fatalf("Run = %q, %v", status, err)
	}
	if stats["requirements_installed"] != true {
		t.Fatalf("stats = %v", stats)
	}
	if len(events) < 3 {
		t.Fatalf("events = %+v", events)
	}
	if got, want := lineText(t, events[0]), "Requirements: installing for commit "+testCommit[:7]+" from requirements.yml"; got != want {
		t.Fatalf("first line = %q, want %q", got, want)
	}
	for _, ev := range events[1 : len(events)-1] {
		_ = lineText(t, ev)
	}
	if last := events[len(events)-1]; last.GetState() != "ok" {
		t.Fatalf("last event = %+v, want the ok event", last)
	}
	if n := len(g.Calls()); n != 1 {
		t.Fatalf("galaxy calls = %d, want 1", n)
	}
	entries, err := os.ReadDir(requirements.CacheDir(clone))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, de := range entries {
		if strings.HasPrefix(de.Name(), testCommit+"-") && de.IsDir() {
			found = true
		}
	}
	if !found {
		t.Fatalf("no entry for %s under %s: %v", testCommit, requirements.CacheDir(clone), entries)
	}
}

func TestSyncExecutorReusesTheInstallOfItsCommit(t *testing.T) {
	clone := requirementsClone(t, map[string]string{"requirements.yml": "- name: acme.web\n"})
	g := &requirementstest.Galaxy{Roles: map[string]string{"acme.web/meta/main.yml": "{}\n"}}
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(g))
	if status, _, _, err := runRequirementsSync(t, e, clone, ""); err != nil || status != StatusSucceeded {
		t.Fatalf("first Run = %q, %v", status, err)
	}
	status, stats, events, err := runRequirementsSync(t, e, clone, "")
	if err != nil || status != StatusSucceeded {
		t.Fatalf("second Run = %q, %v", status, err)
	}
	if stats["requirements_installed"] != true {
		t.Fatalf("stats = %v", stats)
	}
	if len(events) != 2 || events[1].GetState() != "ok" {
		t.Fatalf("events = %+v", events)
	}
	if got, want := lineText(t, events[0]), "Requirements: using the install for commit "+testCommit[:7]+" on this worker"; got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
	if n := len(g.Calls()); n != 1 {
		t.Fatalf("galaxy calls = %d, want only the first sync's", n)
	}
}

func TestSyncExecutorRequirementsFailureFailsTheJob(t *testing.T) {
	clone := requirementsClone(t, map[string]string{"requirements.yml": "- name: acme.web\n"})
	g := &requirementstest.Galaxy{Err: &requirements.ExitError{Code: 3, Stderr: "cannot resolve"}}
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(g))
	status, _, events, err := runRequirementsSync(t, e, clone, "")
	want := "install galaxy requirements: ansible-galaxy role install -r requirements.yml: exit 3: cannot resolve"
	if status != StatusError || err == nil || err.Error() != want {
		t.Fatalf("Run = %q, %v; want error %q", status, err, want)
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
	if got := lineText(t, events[len(events)-1]); got != "Requirements: "+want {
		t.Fatalf("last line = %q", got)
	}
	for _, ev := range events {
		if ev.GetState() == "ok" {
			t.Fatalf("a failed sync sent the ok event: %+v", events)
		}
	}
}

func TestSyncExecutorCommentOnlyRequirementsInstallNothing(t *testing.T) {
	clone := requirementsClone(t, map[string]string{"requirements.yml": "# none\n"})
	g := &requirementstest.Galaxy{}
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(g))
	status, stats, events, err := runRequirementsSync(t, e, clone, "")
	if err != nil || status != StatusSucceeded {
		t.Fatalf("Run = %q, %v", status, err)
	}
	if stats["requirements_installed"] != false {
		t.Fatalf("stats = %v", stats)
	}
	if len(events) != 2 || events[1].GetState() != "ok" {
		t.Fatalf("events = %+v", events)
	}
	if got := lineText(t, events[0]); got != "Requirements: requirements.yml lists nothing to install" {
		t.Fatalf("line = %q", got)
	}
	if n := len(g.Calls()); n != 0 {
		t.Fatalf("galaxy calls = %d, want none", n)
	}
}

func TestSyncExecutorRefusesARequirementsFileLinkedOutOfTheClone(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "requirements.yml")
	if err := os.WriteFile(outside, []byte("- name: acme.web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clone := requirementsClone(t, map[string]string{"site.yml": "- hosts: all\n"})
	if err := os.Symlink(outside, filepath.Join(clone, "requirements.yml")); err != nil {
		t.Fatal(err)
	}
	g := &requirementstest.Galaxy{}
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(g))
	status, _, _, err := runRequirementsSync(t, e, clone, "")
	if status != StatusError || err == nil ||
		!strings.Contains(err.Error(), "requirements.yml") || !strings.Contains(err.Error(), "outside the repository's files") {
		t.Fatalf("Run = %q, %v", status, err)
	}
	if n := len(g.Calls()); n != 0 {
		t.Fatalf("galaxy calls = %d, want none", n)
	}
}

func TestSyncExecutorRefusesALocalSourceOutsideTheClone(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "r.tar.gz"), []byte("not a tarball"), 0o644); err != nil {
		t.Fatal(err)
	}
	clone := requirementsClone(t, map[string]string{"requirements.yml": "- src: ./link/r.tar.gz\n  name: r\n"})
	if err := os.Symlink(outside, filepath.Join(clone, "link")); err != nil {
		t.Fatal(err)
	}
	g := &requirementstest.Galaxy{}
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(g))
	status, _, _, err := runRequirementsSync(t, e, clone, "")
	if status != StatusError || err == nil ||
		!strings.Contains(err.Error(), `requirements.yml: the source "./link/r.tar.gz" is outside the repository`) {
		t.Fatalf("Run = %q, %v", status, err)
	}
	if n := len(g.Calls()); n != 0 {
		t.Fatalf("galaxy calls = %d, want none", n)
	}
}

// TestSyncExecutorWithoutARequirementsInstallerFailsClosed is the plan's
// Ruling 12: counted files and no installer wired are a wiring bug.
func TestSyncExecutorWithoutARequirementsInstallerFailsClosed(t *testing.T) {
	clone := requirementsClone(t, map[string]string{"requirements.yml": "- name: acme.web\n"})
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, nil)
	status, _, events, err := runRequirementsSync(t, e, clone, "")
	want := "install galaxy requirements: no requirements installer wired"
	if status != StatusError || err == nil || err.Error() != want {
		t.Fatalf("Run = %q, %v", status, err)
	}
	if len(events) != 1 || lineText(t, events[0]) != "Requirements: "+want {
		t.Fatalf("events = %+v", events)
	}
}

// TestSyncExecutorCancelledRequirementsSendNoFailureLine: a cancelled
// install ends the sync without the failure line (the plan's Ruling 6).
func TestSyncExecutorCancelledRequirementsSendNoFailureLine(t *testing.T) {
	clone := requirementsClone(t, map[string]string{"requirements.yml": "- name: acme.web\n"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &requirementstest.Galaxy{OnInstall: func(requirements.Install) error { cancel(); return nil }, Hold: make(chan struct{})}
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(g))
	c := &evCollector{}
	status, _, err := e.Run(ctx,
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", "", ""),
		RunEnv{WorkDir: t.TempDir(), RepoCacheDir: filepath.Dir(clone)}, c.sink)
	if status != StatusError || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %q, %v", status, err)
	}
	for _, ev := range c.events {
		if ev.GetState() == "ok" || strings.HasPrefix(lineText(t, ev), "Requirements: install galaxy requirements") {
			t.Fatalf("events = %+v", c.events)
		}
	}
}

// TestSyncExecutorStopsWhenALineCannotBeSent: the job can no longer be
// observed, so the install stops and the sync fails with the send error.
func TestSyncExecutorStopsWhenALineCannotBeSent(t *testing.T) {
	clone := requirementsClone(t, map[string]string{"requirements.yml": "- name: acme.web\n"})
	g := &requirementstest.Galaxy{Roles: map[string]string{"acme.web/meta/main.yml": "{}\n"}}
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(g))
	var sent []*workerpb.RunEvent
	status, _, err := e.Run(context.Background(),
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", "", ""),
		RunEnv{WorkDir: t.TempDir(), RepoCacheDir: filepath.Dir(clone)}, func(ev *workerpb.RunEvent) error {
			sent = append(sent, ev)
			return errors.New("stream closed")
		})
	if status != StatusError || err == nil || err.Error() != "send requirements log: stream closed" {
		t.Fatalf("Run = %q, %v", status, err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want the one line that failed", sent)
	}
}

// TestSyncRequirementsLinesCarryNoURLCredentials: galaxy's lines reach the
// sync's log with URL userinfo removed (the plan's Ruling 6).
func TestSyncRequirementsLinesCarryNoURLCredentials(t *testing.T) {
	clone := requirementsClone(t, map[string]string{"requirements.yml": "- name: acme.web\n"})
	g := &requirementstest.Galaxy{Roles: map[string]string{"acme.web/meta/main.yml": "{}\n"},
		Lines: []string{"- extracting from https://bob:s3cret@git.example.org/r.git"}}
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(g))
	_, _, events, err := runRequirementsSync(t, e, clone, "")
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, ev := range events[:len(events)-1] {
		texts = append(texts, lineText(t, ev))
	}
	if len(texts) < 2 || strings.Contains(texts[1], "s3cret") || strings.Contains(texts[1], "bob") ||
		!strings.HasSuffix(texts[1], "@git.example.org/r.git") {
		t.Fatalf("lines = %q", texts)
	}
}

// TestSyncExecutorFailsOnASubpathIntoGitsFiles: a counted location the
// clone's confined root refuses fails the step (the plan's Ruling 3), so a
// repository subpath into git's own files fails the sync as it fails every
// run of the commit, before the read.
func TestSyncExecutorFailsOnASubpathIntoGitsFiles(t *testing.T) {
	for _, sub := range []string{".git", "gitlink"} {
		t.Run(sub, func(t *testing.T) {
			clone := requirementsClone(t, map[string]string{".git/x.yml": "- hosts: all\n"})
			symlinks(t, clone, map[string]string{"gitlink": ".git"})
			e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(&requirementstest.Galaxy{}))
			status, _, events, err := runRequirementsSync(t, e, clone, sub)
			if status != StatusError || err == nil ||
				!strings.HasPrefix(err.Error(), "install galaxy requirements: find requirements: "+sub+"/requirements.yml") ||
				!strings.Contains(err.Error(), "outside the repository's files") {
				t.Fatalf("Run = %q, %v", status, err)
			}
			if len(events) != 1 || lineText(t, events[0]) != "Requirements: "+err.Error() {
				t.Fatalf("events = %+v", events)
			}
		})
	}
}

// TestSyncExecutorRequirementsNeedTheCommit: the entry is keyed by the
// clone's commit, so a commit that cannot be read fails the step.
func TestSyncExecutorRequirementsNeedTheCommit(t *testing.T) {
	clone := requirementsClone(t, map[string]string{"requirements.yml": "- name: acme.web\n"})
	g := &requirementstest.Galaxy{}
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commitErr: errors.New("git rev-parse HEAD: exit 128")}, fakeRequirements(g))
	status, _, _, err := runRequirementsSync(t, e, clone, "")
	if status != StatusError || err == nil || err.Error() != "install galaxy requirements: git rev-parse HEAD: exit 128" {
		t.Fatalf("Run = %q, %v", status, err)
	}
	if n := len(g.Calls()); n != 0 {
		t.Fatalf("galaxy calls = %d, want none", n)
	}
}
