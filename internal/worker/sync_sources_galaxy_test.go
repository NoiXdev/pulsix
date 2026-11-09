// The sync's read of the roles installed from requirements (backlog 7.83,
// spec 2026-11-06-requirements-cache goal 7): the roles/ of the commit's
// requirements entry is a second read root with the clone's symlink rules,
// found in ansible-playbook's order, and a sync without one reads as
// before.
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
	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

// installingSync is a sync executor over clone whose requirements service
// installs roles (slash path to content below the entry's roles/) through
// a fake galaxy.
func installingSync(clone string, roles map[string]string) *SyncExecutor {
	g := &requirementstest.Galaxy{Roles: roles}
	return NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(g))
}

// entryOf is the published requirements entry of testCommit for clone.
func entryOf(t *testing.T, clone string) string {
	t.Helper()
	entries, err := os.ReadDir(requirements.CacheDir(clone))
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range entries {
		if strings.HasPrefix(de.Name(), testCommit+"-") {
			return filepath.Join(requirements.CacheDir(clone), de.Name())
		}
	}
	t.Fatalf("no entry for %s in %v", testCommit, entries)
	return ""
}

func TestSyncExecutorReadsRolesInstalledFromRequirements(t *testing.T) {
	clone := requirementsClone(t, map[string]string{
		"site.yml":         "- hosts: all\n  roles: [acme.web]\n",
		"requirements.yml": "- name: acme.web\n",
	})
	e := installingSync(clone, map[string]string{"acme.web/meta/argument_specs.yml": webSpec})
	r, _, stats := syncSources(t, e, "")
	if stats["requirements_installed"] != true {
		t.Fatalf("stats = %v", stats)
	}
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

// TestSyncExecutorReadsCollectionRolesInstalledFromRequirements: a role
// of a collection the commit's requirements installed is read (backlog
// 7.96).
func TestSyncExecutorReadsCollectionRolesInstalledFromRequirements(t *testing.T) {
	clone := requirementsClone(t, map[string]string{
		"site.yml":         "- hosts: all\n  roles: [acme.web.nginx]\n",
		"requirements.yml": "collections:\n  - name: acme.web\n",
	})
	g := &requirementstest.Galaxy{Collections: map[string]string{"ansible_collections/acme/web/roles/nginx/meta/argument_specs.yml": webSpec}}
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(g))
	r, _, _ := syncSources(t, e, "")
	if len(r.Playbooks) != 1 || len(r.Notes) != 0 {
		t.Fatalf("report = %+v", r)
	}
	pb := r.Playbooks[0]
	if len(pb.Fields) != 1 || pb.Fields[0].Name != "web_port" ||
		pb.Fields[0].Source.File != "(collections)/acme/web/roles/nginx/meta/argument_specs.yml" {
		t.Fatalf("fields = %+v (notes %v)", pb.Fields, pb.Notes)
	}
}

// TestSyncExecutorDoesNotFollowSymlinksOutOfTheInstalledRoles: the
// take-over already refuses an install whose links leave it, so the links
// that leave the roles are planted into the published entry after the
// first sync (an entry changed behind the worker's back); the second sync
// reuses the entry and its read must not follow them.
func TestSyncExecutorDoesNotFollowSymlinksOutOfTheInstalledRoles(t *testing.T) {
	leaked := "argument_specs:\n  main:\n    options:\n      leaked:\n        type: str\n"
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"spec.yml": leaked})
	clone := requirementsClone(t, map[string]string{
		"site.yml":         "- hosts: all\n  roles: [abs, rel, git, inner]\n",
		"requirements.yml": "- name: inner\n",
		"secret.yml":       leaked,
		".git/config":      leaked,
	})
	e := NewSyncExecutor(&fakeFetcher{dir: clone, commit: testCommit}, fakeRequirements(&requirementstest.Galaxy{
		Roles: map[string]string{"shared/spec.yml": webSpec},
		OnInstall: func(in requirements.Install) error {
			// Inside the installed roles: followed.
			dir := filepath.Join(in.Entry, "roles", "inner", "meta")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			return os.Symlink("../../shared/spec.yml", filepath.Join(dir, "argument_specs.yml"))
		},
	}))
	syncSources(t, e, "")

	// <cache>/<entry>/roles/<role>/meta: five levels below the clone's
	// parent.
	symlinks(t, filepath.Join(entryOf(t, clone), "roles"), map[string]string{
		"abs/meta/argument_specs.yml": filepath.Join(outside, "spec.yml"), // absolute, outside
		"rel/meta/argument_specs.yml": "../../../../../repo/secret.yml",   // climbs out into the clone
		"git/meta/argument_specs.yml": "../../../../../repo/.git/config",  // and into git's files
	})
	r, _, _ := syncSources(t, e, "")
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

// roleSpec declares one option named option.
func roleSpec(option string) string {
	return "argument_specs:\n  main:\n    options:\n      " + option + ":\n        type: str\n"
}

// TestSyncReadFindsInstalledRolesInAnsiblePlaybookOrder: for a playbook at
// <dir>, <dir>/roles first, then the installed roles, then <dir>; the
// repository root's roles/ is not searched (spec goal 7).
func TestSyncReadFindsInstalledRolesInAnsiblePlaybookOrder(t *testing.T) {
	clone := requirementsClone(t, map[string]string{
		"playbooks/site.yml": "- hosts: all\n  roles: [inroles, beside, besideonly, rootonly]\n",
		"requirements.yml":   "- name: inroles\n- name: beside\n",
		"playbooks/roles/inroles/meta/argument_specs.yml": roleSpec("inroles_repo"),
		"playbooks/beside/meta/argument_specs.yml":        roleSpec("beside_repo"),
		"playbooks/besideonly/meta/argument_specs.yml":    roleSpec("besideonly_repo"),
		"roles/rootonly/meta/argument_specs.yml":          roleSpec("rootonly_repo"),
	})
	e := installingSync(clone, map[string]string{
		"inroles/meta/argument_specs.yml": roleSpec("inroles_installed"),
		"beside/meta/argument_specs.yml":  roleSpec("beside_installed"),
	})
	r, _, _ := syncSources(t, e, "")
	if len(r.Playbooks) != 1 {
		t.Fatalf("report = %+v", r)
	}
	pb := r.Playbooks[0]
	var names []string
	for _, f := range pb.Fields {
		names = append(names, f.Name)
		if f.Name == "beside_installed" && f.Source.File != "(requirements)/beside/meta/argument_specs.yml" {
			t.Errorf("beside_installed source = %q", f.Source.File)
		}
	}
	if !reflect.DeepEqual(names, []string{"inroles_repo", "beside_installed", "besideonly_repo"}) {
		t.Fatalf("fields = %v", names)
	}
	var rootonly *varsource.RoleUse
	for i := range pb.Roles {
		if pb.Roles[i].Name == "rootonly" {
			rootonly = &pb.Roles[i]
		}
	}
	if rootonly == nil || rootonly.State != varsource.RoleNotFound {
		t.Fatalf("roles = %+v, want rootonly not found", pb.Roles)
	}
}

func TestSyncExecutorWithoutInstalledRoles(t *testing.T) {
	files := map[string]string{
		"site.yml":                          "- hosts: all\n  roles: [web, nope]\n",
		"roles/web/meta/argument_specs.yml": webSpec,
	}
	clone := requirementsClone(t, files)
	var handed []any
	e := &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit},
		parseRepo: func(ctx context.Context, fsys fs.FS, in varsource.Installed, commit string) varsource.Report {
			handed = append(handed, in.Roles, in.Collections)
			return varsource.ParseRepoInstalled(ctx, fsys, in, commit)
		}}
	r, _, _ := syncSources(t, e, "")
	if !reflect.DeepEqual(handed, []any{nil, nil}) {
		t.Fatalf("installed roles and collections handed to the parser = %v, want nil", handed)
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

// TestSyncReadOfAnEntryThatCannotBeOpened: an entry that does not open as
// a real directory (spec goal 1a) is not read; the log names the reason,
// one read_failed note says so, and the rest of the read goes on (spec
// goal 7, the 7.75 rule).
func TestSyncReadOfAnEntryThatCannotBeOpened(t *testing.T) {
	const (
		entryLog  = "open the installed requirements"
		entryNote = "the roles and collections installed from requirements.yml could not be opened; roles found only there are reported as not found"
		rolesLog  = "open the installed roles"
		rolesNote = "the roles installed from requirements.yml could not be opened; roles found only there are reported as not found"
	)
	for _, tc := range []struct {
		name, reason, logged, note string
		make                       func(t *testing.T, entry string)
	}{
		{"symlink to a directory of roles", "a symlink, which is not followed", entryLog, entryNote, func(t *testing.T, entry string) {
			target := filepath.Join(t.TempDir(), "elsewhere")
			writeTree(t, target, map[string]string{"roles/acme.web/meta/argument_specs.yml": webSpec})
			if err := os.Symlink(target, entry); err != nil {
				t.Fatal(err)
			}
		}},
		{"dangling symlink", "a symlink, which is not followed", entryLog, entryNote, func(t *testing.T, entry string) {
			if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), entry); err != nil {
				t.Fatal(err)
			}
		}},
		{"file", "not a directory", entryLog, entryNote, func(t *testing.T, entry string) {
			if err := os.WriteFile(entry, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing", "no such file or directory", entryLog, entryNote, func(*testing.T, string) {}},
		{"without roles", "no such file or directory", rolesLog, rolesNote, func(t *testing.T, entry string) {
			if err := os.Mkdir(entry, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clone := requirementsClone(t, map[string]string{"site.yml": "- hosts: all\n  roles: [acme.web]\n"})
			cache := requirements.CacheDir(clone)
			if err := os.Mkdir(cache, 0o700); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(cache, testCommit+"-0123456789abcdef")
			tc.make(t, dir)
			var logged bytes.Buffer
			e := &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}, Log: slog.New(slog.NewTextHandler(&logged, nil))}
			entry := requirements.Entry{Dir: dir, RolesDir: filepath.Join(dir, "roles"), CollectionsDir: filepath.Join(dir, "collections")}
			r := e.collectVarSources(context.Background(), clone, "", entry)
			if !strings.Contains(logged.String(), tc.logged) || !strings.Contains(logged.String(), tc.reason) {
				t.Fatalf("log = %s, want %q and %q", logged.String(), tc.logged, tc.reason)
			}
			if r.Failed || len(r.Notes) != 1 || r.Notes[0].Code != "read_failed" || r.Notes[0].Message != tc.note {
				t.Fatalf("report = %+v", r)
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

// TestSyncReadDoesNotFollowCollectionsOutOfTheEntry: installed collections
// that are a symlink leaving the entry are not read (backlog 7.96); the
// read says so once and still reads the installed roles.
func TestSyncReadDoesNotFollowCollectionsOutOfTheEntry(t *testing.T) {
	clone := requirementsClone(t, map[string]string{"site.yml": "- hosts: all\n  roles: [acme.web.nginx, acme.db]\n"})
	cache := requirements.CacheDir(clone)
	dir := filepath.Join(cache, testCommit+"-0123456789abcdef")
	writeTree(t, dir, map[string]string{"roles/acme.db/meta/argument_specs.yml": webSpec})
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"acme/web/roles/nginx/meta/argument_specs.yml": webSpec})
	if err := os.MkdirAll(filepath.Join(dir, "collections"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "collections", "ansible_collections")); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	e := &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}, Log: slog.New(slog.NewTextHandler(&logged, nil))}
	entry := requirements.Entry{Dir: dir, RolesDir: filepath.Join(dir, "roles"), CollectionsDir: filepath.Join(dir, "collections")}
	r := e.collectVarSources(context.Background(), clone, "", entry)
	if !strings.Contains(logged.String(), "open the installed collections") {
		t.Fatalf("log = %s", logged.String())
	}
	if len(r.Notes) != 1 || r.Notes[0].Message != "the collections installed from requirements.yml could not be opened; their roles are not read" {
		t.Fatalf("notes = %+v", r.Notes)
	}
	pb := r.Playbooks[0]
	want := []varsource.RoleUse{
		{Name: "acme.web.nginx", EntryPoint: "main", State: varsource.RoleCollection},
		{Name: "acme.db", EntryPoint: "main", State: varsource.RoleRead},
	}
	if !reflect.DeepEqual(pb.Roles, want) {
		t.Fatalf("roles = %+v", pb.Roles)
	}
}
