package varsource

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// parseWithRoles reads repo with the installed roles and returns its only
// playbook.
func parseWithRoles(t *testing.T, repo fstest.MapFS, roles fs.FS) Playbook {
	t.Helper()
	r := ParseRepoWithRoles(context.Background(), repo, roles, "c")
	if len(r.Playbooks) != 1 {
		t.Fatalf("playbooks = %q (notes %v)", playbookPaths(r), r.Notes)
	}
	return r.Playbooks[0]
}

func mapFS(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, data := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return fsys
}

func TestInstalledRoleIsRead(t *testing.T) {
	repo := mapFS(map[string]string{"site.yml": "- hosts: all\n  roles: [acme.web]\n"})
	roles := mapFS(map[string]string{"acme.web/meta/argument_specs.yml": "argument_specs:\n  main:\n    options:\n      web_port:\n        type: int\n"})
	pb := parseWithRoles(t, repo, roles)
	if pb.Failed || !reflect.DeepEqual(fieldNames(pb.Fields), []string{"web_port"}) {
		t.Fatalf("failed = %v, fields %q (notes %v)", pb.Failed, fieldNames(pb.Fields), pb.Notes)
	}
	if src := pb.Fields[0].Source; src.File != "(requirements)/acme.web/meta/argument_specs.yml" || src.Role != "acme.web" {
		t.Fatalf("source = %+v", src)
	}
	wantRoles(t, pb.Roles, RoleUse{Name: "acme.web", EntryPoint: "main", State: RoleRead})
	// Files stays the playbook files and sidecars (controller ruling).
	wantFiles(t, pb.Files, FileUse{Path: "site.yml", State: FileRead})
	wantNotes(t, pb.Notes)
}

func TestRepoRoleWinsOverInstalledRole(t *testing.T) {
	for _, dir := range []string{".", "sub"} {
		t.Run(dir, func(t *testing.T) {
			play := joinDir(dir, "site.yml")
			repo := mapFS(map[string]string{
				play: "- hosts: all\n  roles: [common]\n",
				joinDir(dir, "roles/common/meta/argument_specs.yml"): oneOption("a"),
			})
			roles := mapFS(map[string]string{"common/meta/argument_specs.yml": oneOption("b")})
			pb := parseWithRoles(t, repo, roles)
			if got := fieldNames(pb.Fields); !reflect.DeepEqual(got, []string{"a"}) {
				t.Fatalf("fields = %q, want the repo role's", got)
			}
		})
	}
}

func joinDir(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

// TestInstalledRolesAreSearchedForPaths: a name with a slash is looked up in
// the installed roles too, as Ansible does (backlog 7.91; it was a not-found
// before).
func TestInstalledRolesAreSearchedForPaths(t *testing.T) {
	repo := mapFS(map[string]string{"site.yml": "- hosts: all\n  roles: [x/y]\n"})
	roles := mapFS(map[string]string{"x/y/meta/argument_specs.yml": oneOption("y_var")})
	pb := parseWithRoles(t, repo, roles)
	wantRoles(t, pb.Roles, RoleUse{Name: "x/y", EntryPoint: "main", State: RoleRead})
	wantNotes(t, pb.Notes)
	if src := pb.Fields[0].Source; src.File != "(requirements)/x/y/meta/argument_specs.yml" || src.Role != "x/y" {
		t.Fatalf("source = %+v", src)
	}
}

func TestRoleNotFoundNamesInstalledRoles(t *testing.T) {
	repo := mapFS(map[string]string{"site.yml": "- hosts: all\n  roles: [nope]\n"})
	with := parseWithRoles(t, repo, mapFS(map[string]string{"other/meta/main.yml": "{}\n"}))
	without := parseWithRoles(t, repo, nil)
	for _, pb := range []Playbook{with, without} {
		wantRoles(t, pb.Roles, RoleUse{Name: "nope", EntryPoint: "main", State: RoleNotFound})
		wantNotes(t, pb.Notes, "warning role_not_found site.yml:2")
	}
	if msg := with.Notes[0].Message; !strings.Contains(msg, "at roles/nope or nope or among the roles installed from requirements.yml") {
		t.Fatalf("with installed roles: %q", msg)
	}
	if msg := without.Notes[0].Message; strings.Contains(msg, "requirements.yml") || !strings.Contains(msg, "at roles/nope") {
		t.Fatalf("without installed roles: %q", msg)
	}
}

func TestParseRepoWithoutInstalledRoles(t *testing.T) {
	fsys := os.DirFS("testdata/basic")
	got := ParseRepoWithRoles(context.Background(), fsys, nil, "abc")
	want := ParseRepo(context.Background(), fsys, "abc")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseRepoWithRoles(nil) = %+v\nParseRepo = %+v", got, want)
	}
	if len(want.Playbooks) == 0 || len(want.Playbooks[0].Roles) == 0 {
		t.Fatalf("the fixture uses no roles: %+v", want)
	}
}

// TestInstalledRolesAreKeptApartFromRepoPaths: a repo directory named
// "(requirements)" is a different role from the installed one, with its own
// spec read; the installed role comes before a repo directory of its name
// beside the playbook (and "./acme.web" is that same installed role since
// backlog 7.91, so it is not listed here).
func TestInstalledRolesAreKeptApartFromRepoPaths(t *testing.T) {
	repo := mapFS(map[string]string{
		"site.yml": "- hosts: all\n  roles: [\"(requirements)/acme.web\", acme.web]\n",
		"(requirements)/acme.web/meta/argument_specs.yml": oneOption("shadow_var"),
		"acme.web/meta/argument_specs.yml":                oneOption("local_var"),
	})
	roles := mapFS(map[string]string{"acme.web/meta/argument_specs.yml": oneOption("installed_var")})
	pb := parseWithRoles(t, repo, roles)
	if got := fieldNames(pb.Fields); !reflect.DeepEqual(got, []string{"shadow_var", "installed_var"}) {
		t.Fatalf("fields = %q (notes %v)", got, pb.Notes)
	}
	wantRoles(t, pb.Roles,
		RoleUse{Name: "(requirements)/acme.web", EntryPoint: "main", State: RoleRead},
		RoleUse{Name: "acme.web", EntryPoint: "main", State: RoleRead},
	)
}

func TestInstalledRoleNotesUseTheShownPath(t *testing.T) {
	repo := mapFS(map[string]string{"site.yml": "- hosts: all\n  roles: [bad, big]\n"})
	roles := mapFS(map[string]string{
		"bad/meta/argument_specs.yml": "argument_specs: [\n",
		"big/meta/argument_specs.yml": oneOption("x") + "#" + strings.Repeat("x", MaxFileBytes),
	})
	pb := parseWithRoles(t, repo, roles)
	wantRoles(t, pb.Roles,
		RoleUse{Name: "bad", EntryPoint: "main", State: RoleUnreadable},
		RoleUse{Name: "big", EntryPoint: "main", State: RoleUnreadable},
	)
	wantNotes(t, pb.Notes,
		"warning invalid_yaml (requirements)/bad/meta/argument_specs.yml:1",
		"warning file_too_large (requirements)/big/meta/argument_specs.yml:0",
	)
}

func TestInstalledCollectionStyleNames(t *testing.T) {
	repo := mapFS(map[string]string{"site.yml": "- hosts: all\n  roles: [ns.col.here, ns.col.gone]\n"})
	roles := mapFS(map[string]string{"ns.col.here/meta/argument_specs.yml": oneOption("here_var")})
	pb := parseWithRoles(t, repo, roles)
	wantRoles(t, pb.Roles,
		RoleUse{Name: "ns.col.here", EntryPoint: "main", State: RoleRead},
		RoleUse{Name: "ns.col.gone", EntryPoint: "main", State: RoleCollection},
	)
}

// TestInstalledRolesCountAgainstTheWalkBudget: bytes read from the
// installed roles count like the repo's.
func TestInstalledRolesCountAgainstTheWalkBudget(t *testing.T) {
	var play strings.Builder
	play.WriteString("- hosts: all\n  roles:\n")
	roles := fstest.MapFS{}
	pad := oneOption("v") + "#" + strings.Repeat("x", MaxFileBytes-100) + "\n"
	for i := range maxWalkBytes/MaxFileBytes + 1 {
		fmt.Fprintf(&play, "    - r%d\n", i)
		roles[fmt.Sprintf("r%d/meta/argument_specs.yml", i)] = &fstest.MapFile{Data: []byte(pad)}
	}
	pb := parseWithRoles(t, mapFS(map[string]string{"site.yml": play.String()}), roles)
	if !pb.Failed || codes(pb.Notes)[codePlaybookTooComplex] != 1 {
		t.Fatalf("failed = %v, notes %v", pb.Failed, codes(pb.Notes))
	}
}
