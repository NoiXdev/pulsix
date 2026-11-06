package requirements

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"gopkg.in/yaml.v3"
)

func TestParseFile(t *testing.T) {
	const name = "roles/requirements.yml"
	const notReq = name + " is not a requirements file: "
	const include = name + ": include: is not supported; list the roles in this file"
	const plainYAML = notReq + "anchors, aliases, merge keys and tags are not supported"
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
		{name: "an anchor on a value", data: "x: &r []\n", err: plainYAML, exact: true},
		{name: "roles as an alias", data: "collections: &c\n  - name: a.b\nroles: *c\n", err: plainYAML, exact: true},
		{name: "an alias as a role name", data: "- src: &s ./r.tar.gz\n  name: *s\n", err: plainYAML, exact: true},
		{
			name: "an alias key that galaxy merges, list form", data: "- &m <<: {src: ./r1.tar.gz, name: ok1}\n- *m : {name: ../../x}\n  src: ./r.tar.gz\n",
			err: plainYAML, exact: true,
		},
		{
			name: "an alias key that galaxy merges, collections", data: "collections:\n  - &m <<: {name: ns.ok}\n  - *m : {name: ../c, type: dir}\n",
			err: plainYAML, exact: true,
		},
		{name: "a tagged merge key, list form", data: "- !!merge foo: {name: ../../x}\n  src: ./r.tar.gz\n", err: plainYAML, exact: true},
		{name: "a tagged merge key, collections", data: "collections:\n  - !!merge foo: {name: ../c, source: ../d}\n", err: plainYAML, exact: true},
		{name: "a tagged merge key hiding include", data: "roles:\n  - !!merge foo: {include: other.yml}\n", err: plainYAML, exact: true},
		{name: "a plain merge key", data: "- <<: {name: ../../x}\n  src: ./r.tar.gz\n", err: plainYAML, exact: true},
		{name: "a quoted merge key", data: "- '<<': {name: a}\n  src: ./r.tar.gz\n", err: plainYAML, exact: true},
		{name: "a !!str tag on a name", data: "- src: ./r.tar.gz\n  name: !!str web\n", err: plainYAML, exact: true},
		{name: "an unknown tag", data: "- !unknown acme.web\n", err: plainYAML, exact: true},
		{name: "a non-specific tag", data: "roles:\n  - src: ./r.tar.gz\n    name: ! web\n", err: plainYAML, exact: true},
		{name: "a non-specific tag after wide characters", data: "- {name: \"\u00fc\u00fc\", src: ! ./r.tar.gz}\n", err: plainYAML, exact: true},
		{name: "a tag on the top level", data: "--- !!seq\n- a.b\n", err: plainYAML, exact: true},
		{name: "a tag on a key", data: "!!str roles: []\n", err: plainYAML, exact: true},
		{name: "a second document", data: "roles: []\n---\nroles:\n  - name: ../../x\n", err: notReq + "only one YAML document is allowed", exact: true},
		{name: "an empty second document", data: "- a.b\n---\n", err: notReq + "only one YAML document is allowed", exact: true},
		{name: "invalid YAML in a second document", data: "- a.b\n---\n[\n", err: notReq + "invalid YAML: "},
		{name: "a document start marker", data: "---\n- a.b\n...\n", roles: true},
		{name: "a byte order mark", data: "\ufeff- {name: a, src: ./r.tar.gz}\n", roles: true},
		{name: "a non-specific tag after a byte order mark", data: "\ufeff- ! a.b\n", err: plainYAML, exact: true},
		{name: "a non-specific tag on a key", data: "- src: ./r.tar.gz\n  ! name: web\n", err: plainYAML, exact: true},
		{name: "exclamation and ampersand inside strings", data: "- src: 'a!b&c'\n  name: \"w!&\"\n- x!y&z\n", roles: true},
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

// utf16Bytes encodes s as UTF-16, big or little endian, with or without a BOM.
func utf16Bytes(s string, bigEndian, bom bool) []byte {
	units := utf16.Encode([]rune(s))
	if bom {
		units = append([]uint16{0xFEFF}, units...)
	}
	out := make([]byte, 0, 2*len(units))
	for _, u := range units {
		if bigEndian {
			out = append(out, byte(u>>8), byte(u))
		} else {
			out = append(out, byte(u), byte(u>>8))
		}
	}
	return out
}

// The tag check reads the source text at each node's position, so the lines
// must be the lines yaml.v3 counts: only UTF-8 with \n or \r\n breaks.
func TestParseFileRefusesTextYAMLCountsDifferently(t *testing.T) {
	const name = "requirements.yml"
	const refused = name + " is not a requirements file: only UTF-8 text with ordinary line breaks is supported"
	const tagged = "- src: ./r.tar.gz\n  name: ! \"~\"\n"
	cases := []struct {
		name string
		data []byte
	}{
		{"CR breaks", []byte(strings.ReplaceAll(tagged, "\n", "\r"))},
		{"a lone CR", []byte("- a.b\r- c.d\n")},
		{"NEL breaks", []byte(strings.ReplaceAll(tagged, "\n", "\u0085"))},
		{"LS breaks", []byte(strings.ReplaceAll(tagged, "\n", "\u2028"))},
		{"PS breaks", []byte(strings.ReplaceAll(tagged, "\n", "\u2029"))},
		{"NEL inside a string", []byte("- name: \"a\u0085b\"\n")},
		{"UTF-16LE with a BOM", utf16Bytes(tagged, false, true)},
		{"UTF-16BE with a BOM", utf16Bytes(tagged, true, true)},
		{"UTF-16LE without a BOM", utf16Bytes(tagged, false, false)},
		{"UTF-16BE without a BOM", utf16Bytes(tagged, true, false)},
		{"invalid UTF-8", []byte("- name: \xff\xfe\n")},
		{"a NUL", []byte("- a.b\x00\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFile(name, tc.data)
			if err == nil || err.Error() != refused {
				t.Fatalf("parseFile = %+v, %v; want %q", got, err, refused)
			}
		})
	}
}

func TestParseFileAcceptsCRLF(t *testing.T) {
	got, err := parseFile("requirements.yml", []byte("roles:\r\n  - src: ./r.tar.gz\r\n    name: web\r\ncollections:\r\n  - ns.col\r\n"))
	if err != nil || !got.roles || !got.collections || !reflect.DeepEqual(got.sources, []string{"./r.tar.gz", "ns.col"}) {
		t.Fatalf("parseFile = %+v, %v", got, err)
	}
	if _, err := parseFile("requirements.yml", []byte("- src: ./r.tar.gz\r\n  name: ! web\r\n")); err == nil ||
		!strings.HasSuffix(err.Error(), "anchors, aliases, merge keys and tags are not supported") {
		t.Fatalf("a tag in a CRLF file: err = %v", err)
	}
}

// The TaggedStyle guard on its own: a tagged node whose position the source
// text cannot confirm is still refused.
func TestIsPlainRefusesATaggedStyleNodeWithoutAPosition(t *testing.T) {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "web", Style: yaml.TaggedStyle}
	if isPlain(n, nil) {
		t.Fatal("isPlain accepted a TaggedStyle node")
	}
	n.Style = 0
	if !isPlain(n, nil) {
		t.Fatal("isPlain refused an untagged node")
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
