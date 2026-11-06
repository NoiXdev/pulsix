package requirements

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseFile(t *testing.T) {
	const name = "roles/requirements.yml"
	const notReq = name + " is not a requirements file: "
	const include = name + ": include: is not supported; list the roles in this file"
	cases := []struct {
		name               string
		data               string
		roles, collections bool
		err                string // a prefix of the error; "" for none
		exact              bool   // err is the whole error
	}{
		{name: "empty", data: ""},
		{name: "only a comment", data: "# only a comment\n"},
		{name: "null", data: "null"},
		{name: "empty list", data: "[]"},
		{name: "empty mapping", data: "{}"},
		{name: "both lists empty", data: "roles: []\ncollections: []\n"},
		{name: "both keys null", data: "roles:\ncollections:\n"},
		{name: "list form", data: "- src: ./r.tar.gz\n", roles: true},
		{name: "plain role strings", data: "- geerlingguy.apache\n- acme.web,1.0\n", roles: true},
		{name: "roles only", data: "roles:\n  - name: a.b\n", roles: true},
		{name: "collections only", data: "collections:\n  - community.general\n", collections: true},
		{name: "both", data: "roles:\n  - name: a.b\ncollections:\n  - community.general\n", roles: true, collections: true},
		{name: "an alias of a list", data: "x: &r []\n", err: notReq + "only roles and collections are allowed at the top level, found x"},
		{name: "roles as an alias", data: "collections: &c\n  - name: a.b\nroles: *c\n", roles: true, collections: true},
		{
			name: "scalar", data: "42",
			err: notReq + "the top level must be a list of roles or a mapping of roles and collections",
		},
		{
			name: "a string", data: "roles\n",
			err: notReq + "the top level must be a list of roles or a mapping of roles and collections",
		},
		{name: "roles not a list", data: "roles: x\n", err: notReq + "roles must be a list", exact: true},
		{name: "collections not a list", data: "collections: {a: 1}\n", err: notReq + "collections must be a list", exact: true},
		{name: "another key", data: "roles: []\nfoo: 1\n", err: notReq + "only roles and collections are allowed at the top level, found foo", exact: true},
		{name: "invalid YAML", data: "roles: [\n", err: notReq + "invalid YAML: "},
		{name: "a duplicate key", data: "roles: []\nroles: [a]\n", err: notReq + "invalid YAML: "},
		{name: "a duplicate key in an entry", data: "- name: a\n  name: ../../x\n", err: notReq + "invalid YAML: "},
		{name: "include in the list form", data: "- include: other.yml\n", err: include, exact: true},
		{name: "include in roles", data: "roles:\n  - include: other.yml\n", err: include, exact: true},
		{name: "include after a role", data: "- a.b\n- src: x\n  include: other.yml\n", err: include, exact: true},
		{
			name: "role name with a path", data: "- src: ./r.tar.gz\n  name: ../../escaped\n",
			err: notReq + `the role name "../../escaped" is not a plain directory name`, exact: true,
		},
		{name: "role name with a slash", data: "- src: ./r.tar.gz\n  name: a/b\n", err: notReq + `the role name "a/b" is not a plain directory name`, exact: true},
		{name: "role name with a backslash", data: "- src: ./r.tar.gz\n  name: 'a\\b'\n", err: notReq + `the role name "a\\b" is not a plain directory name`, exact: true},
		{name: "role name ..", data: "- src: ./r.tar.gz\n  name: ..\n", err: notReq + `the role name ".." is not a plain directory name`, exact: true},
		{name: "role name .", data: "roles:\n  - src: ./r.tar.gz\n    name: .\n", err: notReq + `the role name "." is not a plain directory name`, exact: true},
		{name: "role name empty", data: "- src: ./r.tar.gz\n  name: ''\n", err: notReq + `the role name "" is not a plain directory name`, exact: true},
		{name: "role name null", data: "- src: ./r.tar.gz\n  name:\n", err: notReq + `the role name "" is not a plain directory name`, exact: true},
		{name: "old style role key", data: "- role: ../../escaped\n", err: notReq + `the role name "../../escaped" is not a plain directory name`, exact: true},
		{name: "role key beats name", data: "- role: ../x\n  name: fine\n  src: ./r.tar.gz\n", err: notReq + `the role name "../x" is not a plain directory name`, exact: true},
		{name: "role name in a merge", data: "- <<: {name: ../../x}\n  src: ./r.tar.gz\n", err: notReq + `the role name "../../x" is not a plain directory name`, exact: true},
		{name: "third part of a role string", data: "- ./r.tar.gz,1.0,../x\n", err: notReq + `the role name "../x" is not a plain directory name`, exact: true},
		{name: "a plain third part", data: "- ./r.tar.gz,1.0,web\n", roles: true},
		{name: "a line galaxy refuses itself", data: "- a,b,c,d\n", roles: true},
		{name: "a role name that is a number", data: "- src: ./r.tar.gz\n  name: 42\n", roles: true},
		{
			name: "a name taken from a source above the roles directory", data: "- src: ../../r.tar.gz\n",
			err: notReq + `the role name "../../r.tar.gz", taken from its source, leads outside the roles directory`, exact: true,
		},
		{
			name: "a name taken from a role string above the roles directory", data: "- ../../r.tar.gz,1.0\n",
			err: notReq + `the role name "../../r.tar.gz", taken from its source, leads outside the roles directory`, exact: true,
		},
		{
			name: "a name taken from an absolute source", data: "- /srv/clone/r.tar.gz\n",
			err: notReq + `the role name "/srv/clone/r.tar.gz", taken from its source, leads outside the roles directory`, exact: true,
		},
		{
			name: "a name taken from the source after the scm prefix", data: "- src: git+../../r\n",
			err: notReq + `the role name "../../r", taken from its source, leads outside the roles directory`, exact: true,
		},
		{name: "a name taken from a URL is its last part", data: "- src: https://example.org/x/../r.tar.gz\n", roles: true},
		{name: "a name taken from a URL that is ..", data: "- git+https://example.org/x/..\n", err: notReq + `the role name "..", taken from its source, leads outside the roles directory`, exact: true},
		{name: "an explicit name makes the source's path irrelevant", data: "- src: ../../r.tar.gz\n  name: r\n", roles: true},
		{name: "roles entry is a list", data: "- [a]\n", err: notReq + "roles entry 1 is neither a name nor a mapping", exact: true},
		{name: "roles entry is null", data: "roles:\n  - a.b\n  -\n", err: notReq + "roles entry 2 is neither a name nor a mapping", exact: true},
		{name: "roles entry is a number", data: "- 42\n", err: notReq + "roles entry 1 is neither a name nor a mapping", exact: true},
		{name: "collections entry is a list", data: "collections:\n  - [a]\n", err: notReq + "collections entry 1 is neither a name nor a mapping", exact: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFile(name, []byte(tc.data))
			if tc.err != "" {
				if err == nil {
					t.Fatalf("parseFile = %+v, want error %q", got, tc.err)
				}
				if tc.exact && err.Error() != tc.err || !tc.exact && !strings.HasPrefix(err.Error(), tc.err) {
					t.Fatalf("err = %q, want %q (exact %v)", err, tc.err, tc.exact)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFile: %v", err)
			}
			if got.name != name || got.roles != tc.roles || got.collections != tc.collections {
				t.Fatalf("parseFile = %+v, want name %q roles %v collections %v", got, name, tc.roles, tc.collections)
			}
		})
	}
}

func TestParseFileCollectsSources(t *testing.T) {
	cases := []struct {
		name string
		data string
		want []string
	}{
		{
			name: "the brief's file",
			data: "roles:\n  - src: ./r.tar.gz\n  - name: acme.web\n  - geerlingguy.apache,1.2.0\n" +
				"collections:\n  - name: ../c\n    type: dir\n  - name: ns.col\n    source: https://hub.example.org\n",
			want: []string{"./r.tar.gz", "acme.web", "geerlingguy.apache", "../c", "ns.col", "https://hub.example.org"},
		},
		{
			name: "file order of the keys",
			data: "collections:\n  - ns.col\nroles:\n  - acme.web\n",
			want: []string{"ns.col", "acme.web"},
		},
		{
			name: "src over name, the role key without src",
			data: "- src: ./vendor/r.tar.gz\n  name: r\n- role: acme.old\n- git+https://example.org/r.git,v1,r2\n",
			want: []string{"./vendor/r.tar.gz", "acme.old", "git+https://example.org/r.git"},
		},
		{
			name: "fields from a merge",
			data: "- <<: {src: ./vendor/r.tar.gz}\n  name: r\n",
			want: []string{"./vendor/r.tar.gz"},
		},
		{name: "nothing", data: "roles: []\n", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFile("requirements.yml", []byte(tc.data))
			if err != nil {
				t.Fatalf("parseFile: %v", err)
			}
			if !reflect.DeepEqual(got.sources, tc.want) {
				t.Fatalf("sources = %q, want %q", got.sources, tc.want)
			}
		})
	}
}
