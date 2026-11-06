package requirements

import (
	"reflect"
	"testing"
	"unicode"
)

const oddText = "requirements.yml is not a requirements file: only UTF-8 text with ordinary line breaks and no invisible characters is supported"

// The decoded value of every scalar, keys included, must pass the rule the raw
// text passes: a double-quoted escape must not bring back a character
// checkText refuses. Each input here has plain raw text.
func TestParseFileChecksDecodedValues(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		// The reviewer's inputs: galaxy strips \x1c-\x1f with str.strip(), so
		// it read name ../../evil, .. and src /etc.
		{"\\x1c before a role string's source", `- "\x1c../../evil,v1"` + "\n"},
		{"\\x1f after a role string's name", `- "./r.tar.gz,v1,..\x1f"` + "\n"},
		{"\\u001e after a role string's name", `- "./r.tar.gz,v1,..\u001e"` + "\n"},
		{"\\x1d before an absolute source", `- "\x1d/etc,v1,x"` + "\n"},
		// The whole class, anywhere in the document.
		{"a NUL escape", `- {src: ./r.tar.gz, name: r, version: "\0"}` + "\n"},
		{"an ESC escape in a value nobody reads", `- {src: ./r.tar.gz, name: r, version: "\e"}` + "\n"},
		{"a DEL escape", `- {src: ./r.tar.gz, name: r, version: "\x7f"}` + "\n"},
		{"a NEL escape", `- {src: ./r.tar.gz, name: r, version: "\N"}` + "\n"},
		{"a C1 escape", `- {src: ./r.tar.gz, name: r, version: "\x9b"}` + "\n"},
		{"a zero-width space escape", `- {src: ./r.tar.gz, name: r, version: "\u200b"}` + "\n"},
		{"a BOM escape", `- {src: ./r.tar.gz, name: r, version: "\ufeff"}` + "\n"},
		{"a line separator escape", `- {src: ./r.tar.gz, name: r, version: "\L"}` + "\n"},
		{"a paragraph separator escape", `- {src: ./r.tar.gz, name: r, version: "\P"}` + "\n"},
		{"a vertical tab escape", `- {src: ./r.tar.gz, name: r, version: "\v"}` + "\n"},
		{"an escape in a key", `- {src: ./r.tar.gz, name: r, "x\x01": y}` + "\n"},
		{"an escape in a collection", `collections: [{name: ns.col, version: "\x1c1.0"}]` + "\n"},
		{"an escape in an unknown top-level key", `"x\x1f": 1` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFile("requirements.yml", []byte(tc.data))
			if err == nil || err.Error() != oddText {
				t.Fatalf("parseFile = %+v, %v; want %q", got, err, oddText)
			}
		})
	}
}

func TestParseFileAcceptsTabAndLineBreakEscapes(t *testing.T) {
	data := `- {src: ./r.tar.gz, name: r, version: "a\tb\nc\r"}` + "\n"
	if got, err := parseFile("requirements.yml", []byte(data)); err != nil || !got.roles {
		t.Fatalf("parseFile = %+v, %v", got, err)
	}
}

// pySpace is Python's str.isspace(): unicode.IsSpace plus the four
// information separators U+001C-U+001F.
func TestPySpaceIsPythonsWhitespace(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		want := unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f
		if got := pySpace(r); got != want {
			t.Fatalf("pySpace(%U) = %v, want %v", r, got, want)
		}
	}
	if got := pyStrip("\x1c\x1d \u00a0\u3000a b\u2003\x1e\x1f\t"); got != "a b" {
		t.Fatalf("pyStrip = %q", got)
	}
}

// Role strings read as galaxy's role_yaml_parse reads them: stripped only
// when they hold a comma.
func TestParseFileReadsRoleStringsLikeGalaxy(t *testing.T) {
	const notReq = "requirements.yml is not a requirements file: "
	cases := []struct {
		name    string
		data    string
		sources []string
		err     string
	}{
		{name: "no comma keeps the spaces", data: "- \"web \"\n",
			err: notReq + `the role name "web ", taken from its source, is not a plain directory name; give the role a name:`},
		{name: "no comma keeps leading spaces", data: "- \" ../../evil\"\n",
			err: notReq + `the role name " ../../evil", taken from its source, is not a plain directory name; give the role a name:`},
		{name: "one comma strips", data: "- \" acme.web,v1 \"\n", sources: []string{"acme.web"}},
		{name: "two commas strip", data: "- \"\u00a0./vendor/r.tar.gz,v1,r\u3000\"\n", sources: []string{"./vendor/r.tar.gz"}},
		{name: "only the ends are stripped", data: "- \"acme.web,v1,\u2003x\"\n",
			err: notReq + `the role name "\u2003x" is not a plain directory name`},
		{name: "an empty third part", data: "- acme.web,v1,\n", err: notReq + `the role name "" is not a plain directory name`},
		{name: "the name is taken before the scm prefix is cut", data: "- git+web,v1\n", sources: []string{"git+web"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFile("requirements.yml", []byte(tc.data))
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("parseFile = %+v, %v; want %q", got, err, tc.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got.sources, tc.sources) {
				t.Fatalf("parseFile = %+v, %v; want sources %q", got, err, tc.sources)
			}
		})
	}
}

// Every name galaxy installs a role under (name:, role:, a role string's third
// part, or taken from the source) must be a plain directory name.
func TestParseFileAllowsOnlyPlainRoleNames(t *testing.T) {
	const notReq = "requirements.yml is not a requirements file: "
	explicit := func(n string) string { return notReq + `the role name "` + n + `" is not a plain directory name` }
	taken := func(n string) string {
		return notReq + `the role name "` + n + `", taken from its source, is not a plain directory name; give the role a name:`
	}
	cases := []struct {
		name string
		data string
		err  string // "" when accepted
	}{
		{name: "a galaxy name", data: "- geerlingguy.nginx\n"},
		{name: "a galaxy name with a version", data: "- geerlingguy.nginx,3.1.0\n"},
		{name: "an underscore name", data: "- {src: ./r.tar.gz, name: my_role}\n"},
		{name: "a dash name", data: "- {src: ./r.tar.gz, name: role-name}\n"},
		{name: "an at name", data: "- {src: ./r.tar.gz, name: x@1}\n"},
		{name: "a plus name", data: "- {src: ./r.tar.gz, name: Role+2}\n"},
		{name: "a leading digit and underscore", data: "- {src: ./r.tar.gz, name: 9r}\n- {src: ./r.tar.gz, name: _r}\n"},
		{name: "the role key", data: "- role: acme.old\n"},
		{name: "a tarball beside the file is a plain name", data: "- src: r.tar.gz\n"},
		{name: "a git URL", data: "- src: https://github.com/acme/ansible-role-web.git\n"},
		{name: "a git URL with scm", data: "- git+https://github.com/acme/web.git,v1\n"},
		{name: "an ssh URL", data: "- src: git@github.com:acme/web.git\n"},
		{name: "a tarball URL", data: "- src: https://example.org/files/web.tar.gz\n"},
		{name: "a name with a leading dot", data: "- {src: ./r.tar.gz, name: .hidden}\n", err: explicit(".hidden")},
		{name: "three dots", data: "- {src: ./r.tar.gz, name: ...}\n", err: explicit("...")},
		{name: "a leading dash", data: "- {src: ./r.tar.gz, name: -r}\n", err: explicit("-r")},
		{name: "a space", data: "- {src: ./r.tar.gz, name: a b}\n", err: explicit("a b")},
		{name: "a tab", data: "- {src: ./r.tar.gz, name: \"a\\tb\"}\n", err: explicit(`a\tb`)},
		{name: "a colon", data: "- {src: ./r.tar.gz, name: 'a:b'}\n", err: explicit("a:b")},
		{name: "a tilde", data: "- {src: ./r.tar.gz, name: '~'}\n", err: explicit("~")},
		{name: "a wide character", data: "- {src: ./r.tar.gz, name: r\u00fc}\n", err: explicit("r\u00fc")},
		{name: "a non-breaking space", data: "- {src: ./r.tar.gz, name: \"r\u00a0\"}\n", err: explicit(`r\u00a0`)},
		{name: "the role key with a dot", data: "- role: .r\n", err: explicit(".r")},
		{name: "a third part with a dot", data: "- ./r.tar.gz,v1,.r\n", err: explicit(".r")},
		// PyYAML reads .5e3 as the string ".5e3", yaml.v3 as the number 500.
		{name: "a name the loaders type differently", data: "- {src: ./r.tar.gz, name: .5e3}\n", err: explicit(".5e3")},
		{name: "a list as the name", data: "- {src: ./r.tar.gz, name: [a]}\n", err: explicit("")},
		{name: "a local source as the name", data: "- src: ./r.tar.gz\n", err: taken("./r.tar.gz")},
		{name: "a local role string as the name", data: "- ./r.tar.gz,v1\n", err: taken("./r.tar.gz")},
		{name: "a hidden name from a URL", data: "- src: https://example.org/.r.tar.gz\n", err: taken(".r")},
		{name: "a null source", data: "- src:\n", err: taken("")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFile("requirements.yml", []byte(tc.data))
			if tc.err == "" {
				if err != nil || !got.roles {
					t.Fatalf("parseFile = %+v, %v; want roles", got, err)
				}
				return
			}
			if err == nil || err.Error() != tc.err {
				t.Fatalf("parseFile = %+v, %v; want %q", got, err, tc.err)
			}
		})
	}
}
