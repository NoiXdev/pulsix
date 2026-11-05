// The sync's read of the roles installed from requirements.yml (backlog
// 7.83): the galaxy directory next to the clone is a second read root with
// the clone's symlink rules, and a sync without one reads as before.
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

	"github.com/noixdev/pulsix/internal/varsource"
)

// galaxyClone is a clone directory whose galaxy directory (its sibling)
// does not exist yet.
func galaxyClone(t *testing.T, files map[string]string) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "repo")
	writeTree(t, clone, files)
	return clone
}

func TestSyncExecutorReadsRolesInstalledFromRequirements(t *testing.T) {
	clone := galaxyClone(t, map[string]string{"site.yml": "- hosts: all\n  roles: [acme.web]\n"})
	writeTree(t, GalaxyDir(clone), map[string]string{"acme.web/meta/argument_specs.yml": webSpec})
	r, _, _ := syncSources(t, &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}}, "")
	if len(r.Playbooks) != 1 || len(r.Notes) != 0 {
		t.Fatalf("report = %+v", r)
	}
	pb := r.Playbooks[0]
	if len(pb.Fields) != 1 || pb.Fields[0].Name != "web_port" || pb.Fields[0].Source.File != "(requirements)/acme.web/meta/argument_specs.yml" {
		t.Fatalf("fields = %+v", pb.Fields)
	}
	if !reflect.DeepEqual(pb.Roles, []varsource.RoleUse{{Name: "acme.web", EntryPoint: "main", State: varsource.RoleRead}}) {
		t.Fatalf("roles = %+v", pb.Roles)
	}
}

func TestSyncExecutorDoesNotFollowSymlinksOutOfTheInstalledRoles(t *testing.T) {
	leaked := "argument_specs:\n  main:\n    options:\n      leaked:\n        type: str\n"
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"spec.yml": leaked})
	clone := galaxyClone(t, map[string]string{
		"site.yml":    "- hosts: all\n  roles: [abs, rel, git, inner]\n",
		"secret.yml":  leaked,
		".git/config": leaked,
	})
	galaxy := GalaxyDir(clone)
	writeTree(t, galaxy, map[string]string{"shared/spec.yml": webSpec})
	symlinks(t, galaxy, map[string]string{
		"abs/meta/argument_specs.yml":   filepath.Join(outside, "spec.yml"), // absolute, outside
		"rel/meta/argument_specs.yml":   "../../../repo/secret.yml",         // climbs out into the clone
		"git/meta/argument_specs.yml":   "../../../repo/.git/config",        // and into git's files
		"inner/meta/argument_specs.yml": "../../shared/spec.yml",            // inside the galaxy dir: followed
	})
	r, _, _ := syncSources(t, &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}}, "")
	pb := r.Playbooks[0]
	want := []varsource.RoleUse{
		{Name: "abs", EntryPoint: "main", State: varsource.RoleUnreadable},
		{Name: "rel", EntryPoint: "main", State: varsource.RoleUnreadable},
		{Name: "git", EntryPoint: "main", State: varsource.RoleUnreadable},
		{Name: "inner", EntryPoint: "main", State: varsource.RoleRead},
	}
	if !reflect.DeepEqual(pb.Roles, want) {
		t.Fatalf("roles = %+v, want %+v", pb.Roles, want)
	}
	if len(pb.Fields) != 1 || pb.Fields[0].Name != "web_port" {
		t.Fatalf("fields = %+v, want web_port only", pb.Fields)
	}
	if raw, _ := json.Marshal(r); strings.Contains(string(raw), "leaked") {
		t.Fatalf("the report carries a field from outside the installed roles: %s", raw)
	}
}

func TestSyncExecutorWithoutInstalledRoles(t *testing.T) {
	files := map[string]string{
		"site.yml":                          "- hosts: all\n  roles: [web, nope]\n",
		"roles/web/meta/argument_specs.yml": webSpec,
	}
	clone := galaxyClone(t, files)
	var roles []any
	e := &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit},
		parseRepo: func(ctx context.Context, fsys, installed fs.FS, commit string) varsource.Report {
			roles = append(roles, installed)
			return varsource.ParseRepoWithRoles(ctx, fsys, installed, commit)
		}}
	r, _, _ := syncSources(t, e, "")
	if !reflect.DeepEqual(roles, []any{nil}) {
		t.Fatalf("installed roles handed to the parser = %v, want nil", roles)
	}
	// The report as it was before the installed roles, through the same
	// encoding.
	raw, err := json.Marshal(varsource.ParseRepo(context.Background(), os.DirFS(clone), testCommit))
	if err != nil {
		t.Fatal(err)
	}
	var want varsource.Report
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if r.Notes == nil { // the sync's prepend must not turn [] into null
		t.Fatal("report notes are nil, want an empty list")
	}
	if got := mustJSON(t, r); !bytes.Equal(got, mustJSON(t, want)) {
		t.Fatalf("report = %s\nwant %s", got, mustJSON(t, want))
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestSyncExecutorInstalledRolesThatCannotBeOpened: a galaxy path that is
// no directory of its own (a symlink, a file) is not read; one read_failed
// note says so, and the sync and the rest of the read go on.
func TestSyncExecutorInstalledRolesThatCannotBeOpened(t *testing.T) {
	for _, kind := range []string{"symlink", "file"} {
		t.Run(kind, func(t *testing.T) {
			clone := galaxyClone(t, map[string]string{"site.yml": "- hosts: all\n  roles: [acme.web]\n"})
			switch kind {
			case "symlink":
				target := filepath.Join(t.TempDir(), "elsewhere")
				writeTree(t, target, map[string]string{"acme.web/meta/argument_specs.yml": webSpec})
				if err := os.Symlink(target, GalaxyDir(clone)); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(GalaxyDir(clone), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var logged bytes.Buffer
			e := &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}, Log: slog.New(slog.NewTextHandler(&logged, nil))}
			r, _, _ := syncSources(t, e, "")
			if r.Failed || len(r.Notes) != 1 || r.Notes[0].Code != "read_failed" ||
				r.Notes[0].Message != "the roles installed from requirements.yml could not be opened; roles found only there are reported as not found" {
				t.Fatalf("report = %+v", r)
			}
			if !strings.Contains(logged.String(), "installed roles") {
				t.Fatalf("not logged: %s", logged.String())
			}
			pb := r.Playbooks[0]
			if len(pb.Fields) != 0 || !reflect.DeepEqual(pb.Roles, []varsource.RoleUse{{Name: "acme.web", EntryPoint: "main", State: varsource.RoleNotFound}}) {
				t.Fatalf("playbook = %+v", pb)
			}
		})
	}
}

func TestPrependNotes(t *testing.T) {
	a := varsource.ReadFailed("a")
	b := varsource.ReadFailed("b")
	for _, tc := range []struct {
		name        string
		front, rest []varsource.Note
		want        []varsource.Note
	}{
		{"both nil", nil, nil, []varsource.Note{}},
		{"front only", []varsource.Note{a}, nil, []varsource.Note{a}},
		{"rest only", nil, []varsource.Note{b}, []varsource.Note{b}},
		{"both in order", []varsource.Note{a}, []varsource.Note{b}, []varsource.Note{a, b}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := prependNotes(tc.front, tc.rest)
			if got == nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("prependNotes = %#v, want %#v", got, tc.want)
			}
		})
	}
}
