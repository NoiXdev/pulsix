package varsource

import (
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
