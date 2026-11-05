// The sync_repo executor's read of the playbook variable sources (backlog
// 7.75): the encoded report under the stats key, its summary on the ok
// event, the subpath, the time limit, the filesystem boundary of the
// clone, and that nothing in the read fails the sync.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
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
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", subpath, ""), RunEnv{WorkDir: t.TempDir()}, c.sink)
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
	if len(c.events) != 1 {
		t.Fatalf("events = %+v", c.events)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(c.events[0].GetPayload()), &payload); err != nil {
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
		if r.Commit != "" || !reflect.DeepEqual(noteCodes(r.Notes), []string{"read_failed"}) || len(r.Playbooks) != 1 {
			t.Fatalf("report = %+v", r)
		}
	})
	t.Run("the parser panics", func(t *testing.T) {
		clone := t.TempDir()
		writeTree(t, clone, map[string]string{"site.yml": "- hosts: all\n"})
		var logged bytes.Buffer
		e := &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}, Log: slog.New(slog.NewTextHandler(&logged, nil)),
			parseRepo: func(context.Context, fs.FS, string) varsource.Report { panic("boom") }}
		r, summary, _ := syncSources(t, e, "")
		if !strings.Contains(logged.String(), "panic=boom") {
			t.Fatalf("the panic is not logged: %s", logged.String())
		}
		if r.Commit != testCommit || len(r.Playbooks) != 0 || !reflect.DeepEqual(noteCodes(r.Notes), []string{"read_failed"}) {
			t.Fatalf("report = %+v", r)
		}
		if summary["playbooks"] != float64(0) || summary["warnings"] != float64(1) {
			t.Fatalf("summary = %v", summary)
		}
	})
	t.Run("the report does not fit", func(t *testing.T) {
		huge := varsource.Note{Level: varsource.LevelWarning, Code: "x", Message: strings.Repeat("x", varsource.MaxReportBytes)}
		e := &SyncExecutor{Fetcher: &fakeFetcher{dir: t.TempDir(), commit: testCommit},
			parseRepo: func(_ context.Context, _ fs.FS, commit string) varsource.Report {
				return varsource.Report{Commit: commit, Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{huge}}
			}}
		r, summary, _ := syncSources(t, e, "")
		if r.Commit != testCommit || len(r.Playbooks) != 0 || !reflect.DeepEqual(noteCodes(r.Notes), []string{"read_failed"}) {
			t.Fatalf("report = %+v", r)
		}
		if summary["warnings"] != float64(1) {
			t.Fatalf("summary = %v", summary)
		}
	})
	t.Run("the subpath does not exist", func(t *testing.T) {
		r, _, _ := syncSources(t, &SyncExecutor{Fetcher: &fakeFetcher{dir: t.TempDir(), commit: testCommit}}, "nope")
		if len(r.Playbooks) != 0 || !reflect.DeepEqual(noteCodes(r.Notes), []string{"read_failed"}) {
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

// TestSyncExecutorDoesNotFollowSymlinksOutOfTheClone is Review Focus 5: a
// file of the clone that links outside the read root is never read. A link
// that stays inside the root is followed.
func TestSyncExecutorDoesNotFollowSymlinksOutOfTheClone(t *testing.T) {
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{
		"spec.yml":   "argument_specs:\n  main:\n    options:\n      stolen:\n        type: str\n",
		"theirs.yml": "- hosts: all\n  vars:\n    leaked: 1 # pulsix: string\n",
	})
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		"site.yml":     "- hosts: all\n  roles:\n    - x\n    - y\n",
		"shared/y.yml": webSpec,
	})
	links := map[string]string{
		"roles/x/meta/argument_specs.yml": filepath.Join(outside, "spec.yml"),
		"roles/y/meta/argument_specs.yml": "../../../shared/y.yml",
		"theirs.yml":                      filepath.Join(outside, "theirs.yml"),
	}
	for name, target := range links {
		p := filepath.Join(clone, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	r, _, _ := syncSources(t, &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}}, "")
	if got := reportPaths(r); !reflect.DeepEqual(got, []string{"site.yml"}) {
		t.Fatalf("playbooks = %q, want site.yml only (theirs.yml links outside)", got)
	}
	pb := r.Playbooks[0]
	want := []varsource.RoleUse{
		{Name: "x", EntryPoint: "main", State: varsource.RoleUnreadable},
		{Name: "y", EntryPoint: "main", State: varsource.RoleRead},
	}
	if !reflect.DeepEqual(pb.Roles, want) {
		t.Fatalf("roles = %+v, want %+v", pb.Roles, want)
	}
	if len(pb.Fields) != 1 || pb.Fields[0].Name != "web_port" {
		t.Fatalf("fields = %+v, want web_port only", pb.Fields)
	}
	for _, s := range []string{"stolen", "leaked"} {
		if raw, _ := json.Marshal(r); strings.Contains(string(raw), s) {
			t.Fatalf("the report carries %q from outside the clone: %s", s, raw)
		}
	}
}
