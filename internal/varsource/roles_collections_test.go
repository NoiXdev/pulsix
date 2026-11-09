package varsource

import (
	"context"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// Roles inside collections (backlog 7.96), against ansible-core 2.21.5's
// RoleDefinition._load_role_path and _get_collection_role_path, each case
// checked with ansible-playbook --syntax-check.

// parseInstalled reads repo with the installed roles and collections and
// returns its playbooks by path.
func parseInstalled(t *testing.T, repo fstest.MapFS, roles, collections fs.FS) map[string]Playbook {
	t.Helper()
	r := ParseRepoInstalled(context.Background(), repo, Installed{Roles: roles, Collections: collections}, "c")
	out := map[string]Playbook{}
	for _, pb := range r.Playbooks {
		out[pb.Path] = pb
	}
	return out
}

// onePlaybook is the playbook at p, failing the test without it.
func onePlaybook(t *testing.T, pbs map[string]Playbook, p string) Playbook {
	t.Helper()
	pb, ok := pbs[p]
	if !ok {
		t.Fatalf("no playbook %s among %d", p, len(pbs))
	}
	return pb
}

const collSpecs = "/meta/argument_specs.yml"

func TestInstalledCollectionRoleIsRead(t *testing.T) {
	repo := mapFS(map[string]string{"site.yml": "- hosts: all\n  roles: [acme.web.nginx]\n"})
	colls := mapFS(map[string]string{"acme/web/roles/nginx" + collSpecs: oneOption("nginx_port")})
	pb := onePlaybook(t, parseInstalled(t, repo, nil, colls), "site.yml")
	if got := fieldNames(pb.Fields); !reflect.DeepEqual(got, []string{"nginx_port"}) {
		t.Fatalf("fields = %q (notes %v)", got, pb.Notes)
	}
	if src := pb.Fields[0].Source; src.File != InstalledCollectionsPrefix+"acme/web/roles/nginx"+collSpecs || src.Role != "acme.web.nginx" {
		t.Fatalf("source = %+v", src)
	}
	wantRoles(t, pb.Roles, RoleUse{Name: "acme.web.nginx", EntryPoint: "main", State: RoleRead})
	wantNotes(t, pb.Notes)
}

// TestCollectionBesideThePlaybookComesFirst: the collections directory
// next to the playbook is searched before the installed collections, and
// a collection found there hides the installed one of the same name
// entirely, even when the role is only in the installed one.
func TestCollectionBesideThePlaybookComesFirst(t *testing.T) {
	for _, dir := range []string{".", "sub"} {
		t.Run(dir, func(t *testing.T) {
			play := joinDir(dir, "site.yml")
			adjacent := joinDir(dir, "collections/ansible_collections/acme/web/roles/")
			repo := mapFS(map[string]string{
				play:                           "- hosts: all\n  roles: [acme.web.nginx, acme.web.only_installed]\n",
				adjacent + "nginx" + collSpecs: oneOption("beside"),
				adjacent + "other" + collSpecs: oneOption("other"),
			})
			colls := mapFS(map[string]string{
				"acme/web/roles/nginx" + collSpecs:          oneOption("installed"),
				"acme/web/roles/only_installed" + collSpecs: oneOption("hidden"),
			})
			pb := onePlaybook(t, parseInstalled(t, repo, nil, colls), play)
			if got := fieldNames(pb.Fields); !reflect.DeepEqual(got, []string{"beside"}) {
				t.Fatalf("fields = %q, want the role beside the playbook's (notes %v)", got, pb.Notes)
			}
			if src := pb.Fields[0].Source.File; src != adjacent+"nginx"+collSpecs {
				t.Errorf("source = %q", src)
			}
			wantRoles(t, pb.Roles,
				RoleUse{Name: "acme.web.nginx", EntryPoint: "main", State: RoleRead},
				RoleUse{Name: "acme.web.only_installed", EntryPoint: "main", State: RoleNotFound},
			)
			wantNotes(t, pb.Notes, "warning role_not_found "+play+":2")
			if msg := pb.Notes[0].Message; !strings.Contains(msg, "collection acme.web") {
				t.Errorf("message = %q, want the collection named", msg)
			}
		})
	}
}

// TestCollectionsBesideAnImportedPlaybookDoNotCount: only the playbook a
// run starts with has its collections directory searched; ansible-playbook
// does not add an imported playbook's.
func TestCollectionsBesideAnImportedPlaybookDoNotCount(t *testing.T) {
	repo := mapFS(map[string]string{
		"site.yml":      "- import_playbook: sub/inner.yml\n",
		"sub/inner.yml": "- hosts: all\n  roles: [g.h.r]\n",
		"sub/collections/ansible_collections/g/h/roles/r" + collSpecs: oneOption("r_var"),
	})
	pbs := parseInstalled(t, repo, nil, nil)
	top := onePlaybook(t, pbs, "site.yml")
	if len(top.Fields) != 0 {
		t.Errorf("site.yml fields = %q, want none", fieldNames(top.Fields))
	}
	wantRoles(t, top.Roles, RoleUse{Name: "g.h.r", EntryPoint: "main", State: RoleCollection})
	inner := onePlaybook(t, pbs, "sub/inner.yml")
	if got := fieldNames(inner.Fields); !reflect.DeepEqual(got, []string{"r_var"}) {
		t.Errorf("sub/inner.yml fields = %q, want r_var (notes %v)", got, inner.Notes)
	}
}

// TestCollectionsKeyword: the play's collections list resolves short role
// names, for roles: and for role tasks; a block's or a task's own list
// replaces the play's (an empty one too), nested blocks inherit, and a
// roles: entry's collections key is not one.
func TestCollectionsKeyword(t *testing.T) {
	repo := mapFS(map[string]string{"site.yml": `- hosts: all
  collections: [acme.web]
  roles:
    - nginx
    - {role: other, collections: [acme.db]}
  pre_tasks:
    - import_role: {name: nginx, tasks_from: install}
  tasks:
    - block:
        - block:
            - include_role: {name: other}
      collections: acme.db
    - import_role: {name: nginx}
      collections: []
- hosts: all
  roles: [nginx]
`})
	colls := mapFS(map[string]string{
		"acme/web/roles/nginx" + collSpecs: "argument_specs:\n  main:\n    options:\n      nginx_port: {type: int}\n  install:\n    options:\n      nginx_pkg: {type: str}\n",
		"acme/db/roles/other" + collSpecs:  oneOption("other_var"),
	})
	pb := onePlaybook(t, parseInstalled(t, repo, nil, colls), "site.yml")
	// pre_tasks are read before roles:, as Ansible runs them.
	if got, want := fieldNames(pb.Fields), []string{"nginx_pkg", "nginx_port", "other_var"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %q, want %q (notes %v)", got, want, pb.Notes)
	}
	wantRoles(t, pb.Roles,
		RoleUse{Name: "nginx", EntryPoint: "install", State: RoleRead},
		RoleUse{Name: "nginx", EntryPoint: "main", State: RoleRead},
		RoleUse{Name: "other", EntryPoint: "main", State: RoleNotFound},
		RoleUse{Name: "other", EntryPoint: "main", State: RoleRead},
		RoleUse{Name: "nginx", EntryPoint: "main", State: RoleNotFound},
	)
	wantNotes(t, pb.Notes,
		"warning role_not_found site.yml:5",
		"warning role_not_found site.yml:13",
	)
}

// TestCollectionRoleNames: inside a collection a dotted short name is a
// path (x.y is roles/x/y), slashes and hyphens are kept, a fully qualified
// name's middle parts are subdirectories, a file is no role, an entry of
// the list that is no collection name is skipped, and a dotted name whose
// collection part is no collection name is a short name.
func TestCollectionRoleNames(t *testing.T) {
	colls := mapFS(map[string]string{
		"c/d/roles/x/y" + collSpecs:      oneOption("xy"),
		"c/d/roles/my-role" + collSpecs:  oneOption("hyphen"),
		"c/d/roles/sub/deep" + collSpecs: oneOption("deep"),
		"c/d/roles/afile":                "x",
		"c/d/roles/1a/b/c" + collSpecs:   oneOption("digits"),
	})
	cases := []struct {
		name  string
		state RoleState
		field string
	}{
		{"x.y", RoleRead, "xy"},
		{"x/y", RoleRead, "xy"},
		{"my-role", RoleRead, "hyphen"},
		{"c.d.sub.deep", RoleRead, "deep"},
		{"c.d.x.y", RoleRead, "xy"},
		{"1a.b.c", RoleRead, "digits"},
		{"afile", RoleNotFound, ""},
		{"c.d.afile", RoleNotFound, ""},
		{"x..y", RoleNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := mapFS(map[string]string{"site.yml": "- hosts: all\n  collections: [class.x, 'c.d']\n  roles: ['" + tc.name + "']\n"})
			pb := onePlaybook(t, parseInstalled(t, repo, nil, colls), "site.yml")
			wantRoles(t, pb.Roles, RoleUse{Name: tc.name, EntryPoint: "main", State: tc.state})
			var want []string
			if tc.field != "" {
				want = []string{tc.field}
			}
			if got := fieldNames(pb.Fields); len(got) != len(want) || (len(want) == 1 && got[0] != want[0]) {
				t.Errorf("fields = %q, want %q (notes %v)", got, want, pb.Notes)
			}
		})
	}
}

// TestQualifiedNameFallsBackToTheRoleSearch: a fully qualified name that
// no collection holds is looked up as a plain role name next.
func TestQualifiedNameFallsBackToTheRoleSearch(t *testing.T) {
	repo := mapFS(map[string]string{
		"site.yml":                "- hosts: all\n  roles: [x.y.z]\n",
		"roles/x.y.z" + collSpecs: oneOption("plain"),
	})
	pb := onePlaybook(t, parseInstalled(t, repo, nil, mapFS(map[string]string{"q/r/roles/s" + collSpecs: oneOption("s")})), "site.yml")
	if got := fieldNames(pb.Fields); !reflect.DeepEqual(got, []string{"plain"}) {
		t.Fatalf("fields = %q (notes %v)", got, pb.Notes)
	}
}

// TestUnknownCollectionStaysAHint: a qualified name whose collection is
// neither installed nor beside the playbook may still be on the worker's
// own collections path, so it is a hint naming the collection.
func TestUnknownCollectionStaysAHint(t *testing.T) {
	repo := mapFS(map[string]string{"site.yml": "- hosts: all\n  roles: [acme.web.nginx]\n"})
	pb := onePlaybook(t, parseInstalled(t, repo, nil, nil), "site.yml")
	wantRoles(t, pb.Roles, RoleUse{Name: "acme.web.nginx", EntryPoint: "main", State: RoleCollection})
	wantNotes(t, pb.Notes, "hint role_collection site.yml:2")
	if msg := pb.Notes[0].Message; !strings.Contains(msg, "acme.web") || !strings.Contains(msg, "requirements.yml") {
		t.Errorf("message = %q", msg)
	}
}

// TestInvalidCollectionsKeyword: a collections value that is no list of
// names fails the run; the read says so and looks the play's roles up
// without it.
func TestInvalidCollectionsKeyword(t *testing.T) {
	repo := mapFS(map[string]string{
		"site.yml":                 "- hosts: all\n  collections: {a: b}\n  roles: [common]\n",
		"roles/common" + collSpecs: oneOption("common_var"),
	})
	pb := onePlaybook(t, parseInstalled(t, repo, nil, nil), "site.yml")
	if got := fieldNames(pb.Fields); !reflect.DeepEqual(got, []string{"common_var"}) {
		t.Fatalf("fields = %q (notes %v)", got, pb.Notes)
	}
	wantNotes(t, pb.Notes, "warning collections_invalid site.yml:2")
}

// TestCollectionComesBeforeTheRoleSearch: a role a collection holds wins
// over a role of the same name in the role search, for a short name
// through the collections list and for a fully qualified name.
func TestCollectionComesBeforeTheRoleSearch(t *testing.T) {
	repo := mapFS(map[string]string{
		"site.yml":                         "- hosts: all\n  collections: [acme.web]\n  roles: [nginx, acme.web.proxy]\n",
		"roles/nginx" + collSpecs:          oneOption("repo_nginx"),
		"roles/acme.web.proxy" + collSpecs: oneOption("repo_proxy"),
	})
	colls := mapFS(map[string]string{
		"acme/web/roles/nginx" + collSpecs: oneOption("coll_nginx"),
		"acme/web/roles/proxy" + collSpecs: oneOption("coll_proxy"),
	})
	pb := onePlaybook(t, parseInstalled(t, repo, nil, colls), "site.yml")
	if got, want := fieldNames(pb.Fields), []string{"coll_nginx", "coll_proxy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %q, want %q (notes %v)", got, want, pb.Notes)
	}
}

// TestCollectionsListOrder: a collection that exists but lacks the role
// passes the name on to the next one in the list; a collection beside the
// playbook hides the installed one of the same name for short names too;
// ansible.builtin is never looked for on disk.
func TestCollectionsListOrder(t *testing.T) {
	repo := mapFS(map[string]string{
		"site.yml": "- hosts: all\n  collections: [ansible.builtin, acme.web, acme.db, acme.web]\n  roles: [db, web, inbuiltin]\n",
		"collections/ansible_collections/acme/web/roles/other" + collSpecs:            oneOption("other"),
		"collections/ansible_collections/ansible/builtin/roles/inbuiltin" + collSpecs: oneOption("builtin"),
	})
	colls := mapFS(map[string]string{
		"acme/web/roles/web" + collSpecs: oneOption("hidden"),
		"acme/db/roles/db" + collSpecs:   oneOption("db"),
	})
	pb := onePlaybook(t, parseInstalled(t, repo, nil, colls), "site.yml")
	if got := fieldNames(pb.Fields); !reflect.DeepEqual(got, []string{"db"}) {
		t.Fatalf("fields = %q, want db only (notes %v)", got, pb.Notes)
	}
	wantRoles(t, pb.Roles,
		RoleUse{Name: "db", EntryPoint: "main", State: RoleRead},
		RoleUse{Name: "web", EntryPoint: "main", State: RoleNotFound},
		RoleUse{Name: "inbuiltin", EntryPoint: "main", State: RoleNotFound},
	)
}

// TestNullCollections: a null collections value on a block empties the
// list (Block._load_collections), on a task it keeps the inherited one
// (Task.preprocess_data); both checked with ansible-playbook.
func TestNullCollections(t *testing.T) {
	repo := mapFS(map[string]string{"site.yml": `- hosts: all
  collections: [acme.web]
  tasks:
    - import_role: {name: viatask}
      collections: ~
    - block:
        - import_role: {name: viablock}
      collections: ~
`})
	colls := mapFS(map[string]string{
		"acme/web/roles/viatask" + collSpecs:  oneOption("task"),
		"acme/web/roles/viablock" + collSpecs: oneOption("block"),
	})
	pb := onePlaybook(t, parseInstalled(t, repo, nil, colls), "site.yml")
	wantRoles(t, pb.Roles,
		RoleUse{Name: "viatask", EntryPoint: "main", State: RoleRead},
		RoleUse{Name: "viablock", EntryPoint: "main", State: RoleNotFound},
	)
}

// TestShortNameFromAnUnknownCollectionIsAHint: a short name found nowhere
// whose list names a collection in neither place may come from the
// worker's own collections, so it is a hint, as for a qualified name.
func TestShortNameFromAnUnknownCollectionIsAHint(t *testing.T) {
	repo := mapFS(map[string]string{"site.yml": "- hosts: all\n  collections: [community.general, ansible.legacy]\n  roles: [nginx]\n"})
	pb := onePlaybook(t, parseInstalled(t, repo, nil, nil), "site.yml")
	wantRoles(t, pb.Roles, RoleUse{Name: "nginx", EntryPoint: "main", State: RoleCollection})
	wantNotes(t, pb.Notes, "hint role_collection site.yml:3")
	if msg := pb.Notes[0].Message; !strings.Contains(msg, "collection community.general") || strings.Contains(msg, "ansible.legacy") {
		t.Errorf("message = %q", msg)
	}
}

// TestDottedNamesThatAreNoCollectionRole: a dotted name whose first two
// parts are no collection name is no collection role; found nowhere it is
// not found, as the run reports it.
func TestDottedNamesThatAreNoCollectionRole(t *testing.T) {
	for _, name := range []string{"my-ns.col.role", "ns.class.role", "1ns.col.role"} {
		t.Run(name, func(t *testing.T) {
			repo := mapFS(map[string]string{"site.yml": "- hosts: all\n  roles: ['" + name + "']\n"})
			pb := onePlaybook(t, parseInstalled(t, repo, nil, nil), "site.yml")
			wantRoles(t, pb.Roles, RoleUse{Name: name, EntryPoint: "main", State: RoleNotFound})
		})
	}
}

// TestLongCollectionsListEndsInTheWalkLimits: every collection tried
// costs a step, so a list of many collections used by many roles gives
// the walk up instead of running on.
func TestLongCollectionsListEndsInTheWalkLimits(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: all\n  collections:\n")
	for i := range 20000 {
		fmt.Fprintf(&b, "    - a.b%d\n", i)
	}
	b.WriteString("  roles:\n")
	for i := range 300 {
		fmt.Fprintf(&b, "    - r%d\n", i)
	}
	repo := mapFS(map[string]string{"site.yml": b.String()})
	pb := onePlaybook(t, parseInstalled(t, repo, nil, nil), "site.yml")
	found := false
	for _, n := range pb.Notes {
		found = found || n.Code == "playbook_too_complex"
	}
	if !found {
		t.Fatalf("notes = %v, want the walk given up", pb.Notes)
	}
}
