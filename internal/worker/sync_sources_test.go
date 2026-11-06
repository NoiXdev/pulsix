// The sync_repo executor's read of the playbook variable sources (backlog
// 7.75): the encoded report under the stats key, its summary on the ok
// event, the subpath, the time limit, the filesystem boundary of the
// clone, and that nothing in the read fails the sync.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/varsource"
)

const testCommit = "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432"

// writeTree writes files (slash paths relative to root) under root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const webSpec = "argument_specs:\n  main:\n    options:\n      web_port:\n        type: int\n"

// syncSources runs a sync of clone (with subpath) through e and returns the
// decoded report, the event payload's var_sources summary and the stats.
func syncSources(t *testing.T, e *SyncExecutor, subpath string) (varsource.Report, map[string]any, map[string]any) {
	t.Helper()
	if e.Log == nil {
		e.Log = slog.New(slog.DiscardHandler)
	}
	c := &evCollector{}
	status, stats, err := e.Run(context.Background(),
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", subpath, ""), RunEnv{WorkDir: t.TempDir(), RepoCacheDir: t.TempDir()}, c.sink)
	if err != nil || status != StatusSucceeded {
		t.Fatalf("Run = %q, %v", status, err)
	}
	raw, ok := stats[VarSourcesStatsKey].(json.RawMessage)
	if !ok {
		t.Fatalf("stats[%s] = %T, want json.RawMessage", VarSourcesStatsKey, stats[VarSourcesStatsKey])
	}
	var r varsource.Report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode the report: %v", err)
	}
	// The requirements step's lines, if any, come first; the ok event is
	// the last one.
	if len(c.events) == 0 || c.events[len(c.events)-1].GetState() != "ok" {
		t.Fatalf("events = %+v", c.events)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(c.events[len(c.events)-1].GetPayload()), &payload); err != nil {
		t.Fatal(err)
	}
	summary, _ := payload[VarSourcesStatsKey].(map[string]any)
	return r, summary, stats
}

func reportPaths(r varsource.Report) []string {
	paths := []string{}
	for _, pb := range r.Playbooks {
		paths = append(paths, pb.Path)
	}
	return paths
}

func noteCodes(notes []varsource.Note) []string {
	codes := []string{}
	for _, n := range notes {
		codes = append(codes, n.Code)
	}
	return codes
}

func TestSyncExecutorReportsVarSources(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		"site.yml":                          "- hosts: all\n  roles:\n    - web\n",
		"roles/web/meta/argument_specs.yml": webSpec,
	})
	f := &fakeFetcher{dir: clone, commit: testCommit}
	r, summary, stats := syncSources(t, &SyncExecutor{Fetcher: f}, "")
	if !reflect.DeepEqual(f.commitDirs, []string{clone}) {
		t.Fatalf("HeadCommit dirs = %q, want the clone", f.commitDirs)
	}
	if r.Commit != testCommit || r.Incomplete || len(r.Playbooks) != 1 || r.Playbooks[0].Path != "site.yml" {
		t.Fatalf("report = %+v", r)
	}
	if fields := r.Playbooks[0].Fields; len(fields) != 1 || fields[0].Name != "web_port" {
		t.Fatalf("fields = %+v", fields)
	}
	want := map[string]any{"commit": testCommit, "playbooks": float64(1), "warnings": float64(0)}
	if !reflect.DeepEqual(summary, want) {
		t.Fatalf("event summary = %v, want %v (no playbooks array)", summary, want)
	}
	// The rest of the result is what it was.
	if stats["repo_id"] != "01900000-0000-7000-8000-000000000123" || stats["requirements_installed"] != false {
		t.Fatalf("stats = %v", stats)
	}
}

func TestSyncExecutorReadsSourcesUnderTheSubpath(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		"ansible/site.yml":                          "- hosts: all\n  roles:\n    - web\n",
		"ansible/roles/web/meta/argument_specs.yml": webSpec,
		"outside.yml":                               "- hosts: all\n",
	})
	r, _, _ := syncSources(t, &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}}, "ansible")
	if got := reportPaths(r); !reflect.DeepEqual(got, []string{"site.yml"}) {
		t.Fatalf("playbooks = %q, want site.yml relative to the subpath", got)
	}
	if len(r.Playbooks[0].Fields) != 1 {
		t.Fatalf("fields = %+v", r.Playbooks[0].Fields)
	}
}

func TestSyncExecutorSourcesNeverFailTheSync(t *testing.T) {
	t.Run("invalid YAML and a missing role", func(t *testing.T) {
		clone := t.TempDir()
		writeTree(t, clone, map[string]string{
			"bad.yml":  "- hosts: all\n  vars: [\n",
			"site.yml": "- hosts: all\n  roles:\n    - nope\n",
		})
		r, summary, _ := syncSources(t, &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}}, "")
		if got := noteCodes(r.Notes); !reflect.DeepEqual(got, []string{"invalid_yaml"}) {
			t.Fatalf("report notes = %q", got)
		}
		if len(r.Playbooks) != 1 || !reflect.DeepEqual(noteCodes(r.Playbooks[0].Notes), []string{"role_not_found"}) {
			t.Fatalf("playbooks = %+v", r.Playbooks)
		}
		if summary["warnings"] != float64(2) {
			t.Fatalf("summary = %v, want two warnings", summary)
		}
	})
	t.Run("the commit cannot be read", func(t *testing.T) {
		clone := t.TempDir()
		writeTree(t, clone, map[string]string{"site.yml": "- hosts: all\n"})
		f := &fakeFetcher{dir: clone, commitErr: os.ErrPermission}
		r, _, _ := syncSources(t, &SyncExecutor{Fetcher: f}, "")
		if r.Commit != "" || r.Failed || !reflect.DeepEqual(noteCodes(r.Notes), []string{"read_failed"}) || len(r.Playbooks) != 1 {
			t.Fatalf("report = %+v", r)
		}
	})
	t.Run("the parser panics", func(t *testing.T) {
		clone := t.TempDir()
		writeTree(t, clone, map[string]string{"site.yml": "- hosts: all\n"})
		var logged bytes.Buffer
		e := &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}, Log: slog.New(slog.NewTextHandler(&logged, nil)),
			parseRepo: func(context.Context, fs.FS, fs.FS, string) varsource.Report { panic("boom") }}
		r, summary, _ := syncSources(t, e, "")
		if !strings.Contains(logged.String(), "panic=boom") {
			t.Fatalf("the panic is not logged: %s", logged.String())
		}
		if r.Commit != testCommit || !r.Failed || len(r.Playbooks) != 0 || !reflect.DeepEqual(noteCodes(r.Notes), []string{"read_failed"}) {
			t.Fatalf("report = %+v", r)
		}
		if summary["playbooks"] != float64(0) || summary["warnings"] != float64(1) {
			t.Fatalf("summary = %v", summary)
		}
	})
	t.Run("the report does not fit", func(t *testing.T) {
		huge := varsource.Note{Level: varsource.LevelWarning, Code: "x", Message: strings.Repeat("x", varsource.MaxReportBytes)}
		e := &SyncExecutor{Fetcher: &fakeFetcher{dir: t.TempDir(), commit: testCommit},
			parseRepo: func(_ context.Context, _, _ fs.FS, commit string) varsource.Report {
				return varsource.Report{Commit: commit, Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{huge}}
			}}
		r, summary, _ := syncSources(t, e, "")
		if r.Commit != testCommit || !r.Failed || len(r.Playbooks) != 0 || !reflect.DeepEqual(noteCodes(r.Notes), []string{"read_failed"}) {
			t.Fatalf("report = %+v", r)
		}
		if summary["warnings"] != float64(1) {
			t.Fatalf("summary = %v", summary)
		}
	})
	// A subpath into git's own files (".git", or a link to it) never
	// reaches the read: the requirements step refuses it first, as a run's
	// does (TestSyncExecutorFailsOnASubpathIntoGitsFiles).
	t.Run("the subpath cannot be opened", func(t *testing.T) {
		clone := t.TempDir()
		writeTree(t, clone, map[string]string{"site.yml": "- hosts: all\n"})
		r, _, _ := syncSources(t, &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}}, "nope")
		if !r.Failed || len(r.Playbooks) != 0 || !reflect.DeepEqual(noteCodes(r.Notes), []string{"read_failed"}) {
			t.Fatalf("report = %+v", r)
		}
	})
	t.Run("a whole read is not failed", func(t *testing.T) {
		r, _, _ := syncSources(t, &SyncExecutor{Fetcher: &fakeFetcher{dir: t.TempDir(), commit: testCommit}}, "")
		if r.Failed || r.Incomplete || len(r.Notes) != 0 {
			t.Fatalf("report = %+v", r)
		}
	})
}

// TestSyncExecutorStopsReadingSourcesAtTheTimeLimit drives the repo-wide
// time limit with a small value: the commit read waits for it, so every
// playbook is left out, and the sync still succeeds.
func TestSyncExecutorStopsReadingSourcesAtTheTimeLimit(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{"a.yml": "- hosts: all\n", "b.yml": "- hosts: all\n"})
	e := &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit, commitWaits: true}, sourcesTimeLimit: time.Millisecond}
	r, _, _ := syncSources(t, e, "")
	if len(r.Playbooks) != 0 || !r.Incomplete || !reflect.DeepEqual(r.LeftOut, []string{"a.yml", "b.yml"}) {
		t.Fatalf("report = %+v", r)
	}
	if got := noteCodes(r.Notes); !reflect.DeepEqual(got, []string{"time_limit"}) {
		t.Fatalf("notes = %q", got)
	}
	if varSourcesTimeLimit != 60*time.Second {
		t.Fatalf("the production limit is %v", varSourcesTimeLimit)
	}
}

// TestSyncExecutorCancelledDuringTheRead: a job cancelled while the
// sources are read ends cancelled, not succeeded with a partial report.
func TestSyncExecutorCancelledDuringTheRead(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{"a.yml": "- hosts: all\n"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeFetcher{dir: clone, commit: testCommit, onCommit: cancel}
	c := &evCollector{}
	status, stats, err := (&SyncExecutor{Fetcher: f, Log: slog.New(slog.DiscardHandler)}).Run(ctx,
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", "", ""), RunEnv{WorkDir: t.TempDir()}, c.sink)
	if status != StatusError || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %q, %v; want the cancellation", status, err)
	}
	if stats != nil || len(c.events) != 0 {
		t.Fatalf("stats = %v, events = %d", stats, len(c.events))
	}
}

// TestSyncExecutorDoesNotFollowSymlinksOutOfTheClone is Review Focus 5: a
// file of the clone that links outside the read root is never read. A link
// that stays inside the root is followed.
func TestSyncExecutorDoesNotFollowSymlinksOutOfTheClone(t *testing.T) {
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{
		"spec.yml":   "argument_specs:\n  main:\n    options:\n      stolen:\n        type: str\n",
		"theirs.yml": "- hosts: all\n  vars:\n    leaked: 1 # pulsix: string\n",
	})
	writeTree(t, outside, map[string]string{"roles/w/meta/argument_specs.yml": "argument_specs:\n  main:\n    options:\n      foreign:\n        type: str\n"})
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		".git/config":          "argument_specs:\n  main:\n    options:\n      gitconfig:\n        type: str\n", // a spec, so only the refusal keeps it out
		"ansible/site.yml":     "- hosts: all\n  roles:\n    - x\n    - y\n    - g\n    - w\n    - s\n",
		"ansible/shared/y.yml": webSpec,
		"shared/s.yml":         "argument_specs:\n  main:\n    options:\n      sibling:\n        type: str\n",
	})
	symlinks(t, clone, map[string]string{
		"ansible/roles/x/meta/argument_specs.yml": filepath.Join(outside, "spec.yml"),   // absolute, outside
		"ansible/roles/y/meta/argument_specs.yml": "../../../shared/y.yml",              // inside the subpath: followed
		"ansible/roles/g/meta/argument_specs.yml": "../../../../.git/config",            // git's own files
		"ansible/roles/w":                         filepath.Join(outside, "roles", "w"), // a role directory outside
		"ansible/roles/s/meta/argument_specs.yml": "../../../../shared/s.yml",           // the clone, outside the subpath
		"ansible/theirs.yml":                      filepath.Join(outside, "theirs.yml"),
	})
	r, _, _ := syncSources(t, &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}}, "ansible")
	if got := reportPaths(r); !reflect.DeepEqual(got, []string{"site.yml"}) {
		t.Fatalf("playbooks = %q, want site.yml only (theirs.yml links outside)", got)
	}
	pb := r.Playbooks[0]
	want := []varsource.RoleUse{
		{Name: "x", EntryPoint: "main", State: varsource.RoleUnreadable},
		{Name: "y", EntryPoint: "main", State: varsource.RoleRead},
		{Name: "g", EntryPoint: "main", State: varsource.RoleUnreadable},
		{Name: "w", EntryPoint: "main", State: varsource.RoleNotFound},
		{Name: "s", EntryPoint: "main", State: varsource.RoleUnreadable},
	}
	if !reflect.DeepEqual(pb.Roles, want) {
		t.Fatalf("roles = %+v, want %+v", pb.Roles, want)
	}
	if len(pb.Fields) != 1 || pb.Fields[0].Name != "web_port" {
		t.Fatalf("fields = %+v, want web_port only", pb.Fields)
	}
	for _, s := range []string{"stolen", "leaked", "foreign", "sibling", "gitconfig"} {
		if raw, _ := json.Marshal(r); strings.Contains(string(raw), s) {
			t.Fatalf("the report carries %q from outside the clone: %s", s, raw)
		}
	}

	// Without a subpath the read root holds git's own directory: a link
	// into it is refused like a link out of the clone.
	writeTree(t, clone, map[string]string{"root.yml": "- hosts: all\n  roles:\n    - g\n"})
	symlinks(t, clone, map[string]string{"roles/g/meta/argument_specs.yml": "../../../.git/config"})
	r, _, _ = syncSources(t, &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}}, "")
	var root *varsource.Playbook
	for i := range r.Playbooks {
		if r.Playbooks[i].Path == "root.yml" {
			root = &r.Playbooks[i]
		}
	}
	if root == nil || !reflect.DeepEqual(root.Roles, []varsource.RoleUse{{Name: "g", EntryPoint: "main", State: varsource.RoleUnreadable}}) || len(root.Fields) != 0 {
		t.Fatalf("root.yml = %+v", root)
	}
}
