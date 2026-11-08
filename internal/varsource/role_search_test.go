package varsource

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestRolesAreSearchedWhereAnsiblePlaybookLooks pins the role search order
// of a playbook in a subdirectory to ansible-playbook's (backlog 7.90):
// <playbook dir>/roles, then the installed roles (roles_path), then the
// playbook's own directory; never roles/ at the repository root.
// TestRoleSearchFromASubdirectoryInRealAnsible (internal/worker/runner)
// checks the same order against a real run.
func TestRolesAreSearchedWhereAnsiblePlaybookLooks(t *testing.T) {
	repo := mapFS(map[string]string{
		"playbooks/site.yml":                              "- hosts: all\n  roles: [inroles, beside, shadowed, rootonly]\n",
		"playbooks/roles/inroles/meta/argument_specs.yml": oneOption("inroles_var"),
		"playbooks/beside/meta/argument_specs.yml":        oneOption("beside_var"),
		"playbooks/shadowed/meta/argument_specs.yml":      oneOption("shadowed_var"),
		"roles/rootonly/meta/argument_specs.yml":          oneOption("rootonly_var"),
	})
	roles := mapFS(map[string]string{"shadowed/meta/argument_specs.yml": oneOption("installed_shadowed")})
	pb := parseWithRoles(t, repo, roles)
	if got, want := fieldNames(pb.Fields), []string{"inroles_var", "beside_var", "installed_shadowed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %q, want %q (notes %v)", got, want, pb.Notes)
	}
	main := func(name string, state RoleState) RoleUse {
		return RoleUse{Name: name, EntryPoint: "main", State: state}
	}
	wantRoles(t, pb.Roles,
		main("inroles", RoleRead),
		main("beside", RoleRead),
		main("shadowed", RoleRead),
		main("rootonly", RoleNotFound),
	)
	wantNotes(t, pb.Notes, "warning role_not_found playbooks/site.yml:2")
	const want = `role "rootonly" not found at playbooks/roles/rootonly or playbooks/rootonly or among the roles installed from requirements.yml; its variables are not read`
	if got := pb.Notes[0].Message; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

// TestRolesBesideARootPlaybook: for a playbook at the root the playbook's
// directory is the root, so roles/<name> is found first and <name> at the
// root comes after the installed roles, as Ansible does it.
func TestRolesBesideARootPlaybook(t *testing.T) {
	repo := mapFS(map[string]string{
		"site.yml":                              "- hosts: all\n  roles: [inroles, beside, shadowed, nowhere]\n",
		"roles/inroles/meta/argument_specs.yml": oneOption("inroles_var"),
		"beside/meta/argument_specs.yml":        oneOption("beside_var"),
		"shadowed/meta/argument_specs.yml":      oneOption("shadowed_var"),
	})
	roles := mapFS(map[string]string{"shadowed/meta/argument_specs.yml": oneOption("installed_shadowed")})
	pb := parseWithRoles(t, repo, roles)
	if got, want := fieldNames(pb.Fields), []string{"inroles_var", "beside_var", "installed_shadowed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %q, want %q (notes %v)", got, want, pb.Notes)
	}
	if src := pb.Fields[1].Source; src.File != "beside/meta/argument_specs.yml" || src.Role != "beside" {
		t.Fatalf("beside_var source = %+v", src)
	}
	wantNotes(t, pb.Notes, "warning role_not_found site.yml:2")
	const want = `role "nowhere" not found at roles/nowhere or nowhere or among the roles installed from requirements.yml; its variables are not read`
	if got := pb.Notes[0].Message; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

// TestRoleCandidatesFollowAnsiblesOrder pins the candidates of a role name
// and their order (ansible-core 2.21 RoleDefinition._load_role_path,
// backlog 7.91): <dir>/roles/<name>, the installed roles' <name>, then
// <dir>/<name>. A candidate that leaves its tree is left out.
func TestRoleCandidatesFollowAnsiblesOrder(t *testing.T) {
	cases := []struct {
		name, dir     string
		withInstalled bool
		want          []string // "(i)" marks an installed candidate
	}{
		{"x", "pb", true, []string{"pb/roles/x", "(i)x", "pb/x"}},
		{"a/b", "pb", true, []string{"pb/roles/a/b", "(i)a/b", "pb/a/b"}},
		{"a/b", "", true, []string{"roles/a/b", "(i)a/b", "a/b"}},
		{"../x", "pb", true, []string{"pb/x", "x"}},
		{"../../x", "pb", true, []string{"x"}},
		{"../../../x", "pb", true, nil},
		{"./roles/x", "pb", false, []string{"pb/roles/roles/x", "pb/roles/x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name+" in "+tc.dir, func(t *testing.T) {
			var got []string
			for _, c := range roleCandidates(tc.name, tc.dir, tc.withInstalled) {
				if c.installed {
					c.path = "(i)" + c.path
				}
				got = append(got, c.path)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("candidates = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSlashRoleNamesAreSearchedLikeAPlainName: a name with a slash is
// looked up in <dir>/roles, the installed roles and <dir>, in that order,
// as Ansible does it (TestRoleSearchFromASubdirectoryInRealAnsible's "slash
// names").
func TestSlashRoleNamesAreSearchedLikeAPlainName(t *testing.T) {
	repo := mapFS(map[string]string{
		"playbooks/site.yml": "- hosts: all\n  roles: [a/inroles, b/installed, c/beside, a/shadowed, d/nowhere]\n",
		"playbooks/roles/a/inroles/meta/argument_specs.yml": oneOption("inroles_var"),
		"playbooks/c/beside/meta/argument_specs.yml":        oneOption("beside_var"),
		"playbooks/a/shadowed/meta/argument_specs.yml":      oneOption("beside_shadowed"),
	})
	roles := mapFS(map[string]string{
		"b/installed/meta/argument_specs.yml": oneOption("installed_var"),
		"a/shadowed/meta/argument_specs.yml":  oneOption("installed_shadowed"),
	})
	pb := parseWithRoles(t, repo, roles)
	if got, want := fieldNames(pb.Fields), []string{"inroles_var", "installed_var", "beside_var", "installed_shadowed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %q, want %q (notes %v)", got, want, pb.Notes)
	}
	main := func(name string, state RoleState) RoleUse {
		return RoleUse{Name: name, EntryPoint: "main", State: state}
	}
	wantRoles(t, pb.Roles,
		main("a/inroles", RoleRead),
		main("b/installed", RoleRead),
		main("c/beside", RoleRead),
		main("a/shadowed", RoleRead),
		main("d/nowhere", RoleNotFound),
	)
	wantNotes(t, pb.Notes, "warning role_not_found playbooks/site.yml:2")
	const want = `role "d/nowhere" not found at playbooks/roles/d/nowhere or playbooks/d/nowhere or among the roles installed from requirements.yml; its variables are not read`
	if got := pb.Notes[0].Message; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

// TestAFileAtARoleCandidateEndsTheSearch: Ansible takes a regular file at a
// candidate for the role, a role without content, and searches no further
// (TestRoleSearchFromASubdirectoryInRealAnsible's "a file at a candidate").
// A slash name whose first component is a file in roles/ is a different
// matter: the candidate <dir>/roles/p/q does not exist (on disk the lookup
// gets ENOTDIR, not ErrNotExist), so the search goes on.
func TestAFileAtARoleCandidateEndsTheSearch(t *testing.T) {
	repo := map[string]string{
		"playbooks/site.yml":                         "- hosts: all\n  roles: [fileonly, p/q, instfile]\n",
		"playbooks/roles/fileonly":                   "x",
		"playbooks/fileonly/meta/argument_specs.yml": oneOption("beside_var"),
		"playbooks/roles/p":                          "x",
		"playbooks/p/q/meta/argument_specs.yml":      oneOption("pq_var"),
		"playbooks/instfile/meta/argument_specs.yml": oneOption("beside_inst"),
	}
	installed := map[string]string{"instfile": "x"}
	check := func(t *testing.T, repoFS, rolesFS fs.FS) {
		t.Helper()
		r := ParseRepoWithRoles(context.Background(), repoFS, rolesFS, "c")
		if len(r.Playbooks) != 1 {
			t.Fatalf("playbooks = %q (notes %v)", playbookPaths(r), r.Notes)
		}
		pb := r.Playbooks[0]
		if got, want := fieldNames(pb.Fields), []string{"pq_var"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("fields = %q, want %q (notes %v)", got, want, pb.Notes)
		}
		wantRoles(t, pb.Roles,
			RoleUse{Name: "fileonly", EntryPoint: "main", State: RoleNoSpecs},
			RoleUse{Name: "p/q", EntryPoint: "main", State: RoleRead},
			RoleUse{Name: "instfile", EntryPoint: "main", State: RoleNoSpecs},
		)
		wantNotes(t, pb.Notes, "hint role_without_specs playbooks/site.yml:2", "hint role_without_specs playbooks/site.yml:2")
		for i, want := range []string{
			`role "fileonly" is the file playbooks/roles/fileonly, which Ansible takes for a role without content; it adds no fields`,
			`role "instfile" is the file (requirements)/instfile, which Ansible takes for a role without content; it adds no fields`,
		} {
			if got := pb.Notes[i].Message; got != want {
				t.Errorf("note %d message = %q, want %q", i, got, want)
			}
		}
	}
	t.Run("mapFS", func(t *testing.T) {
		check(t, mapFS(repo), mapFS(installed))
	})
	t.Run("os.DirFS", func(t *testing.T) {
		repoDir, rolesDir := t.TempDir(), t.TempDir()
		writeTree(t, repoDir, repo)
		writeTree(t, rolesDir, installed)
		check(t, os.DirFS(repoDir), os.DirFS(rolesDir))
	})
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
