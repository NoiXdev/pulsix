package requirements

import "testing"

// Galaxy runs hg for a role whose scm is hg, and the worker image has none, so
// every Mercurial source is refused before galaxy runs. The scm is
// read as galaxy's role_yaml_parse reads it: from the "scm+" prefix of a
// role's src and, only without a "+" in src, from scm:; a collection's
// "hg+" prefix is its URL scheme.
func TestParseFileRefusesMercurialSources(t *testing.T) {
	refused := []struct{ name, data, src string }{
		{"role string", "- hg+https://h/r\n", "hg+https://h/r"},
		{"role string with a version and a name", "- \"hg+./vendor/r,1.0,r\"\n", "hg+./vendor/r"},
		{"role mapping with an hg+ src", "- {src: \"hg+https://h/r\", name: r}\n", "hg+https://h/r"},
		{"role mapping with scm hg", "- {src: \"https://h/r\", scm: hg, name: r}\n", "https://h/r"},
		{"role mapping with scm hg and no src", "- {name: r, scm: hg}\n", "r"},
		{"collection string", "collections: [\"hg+./vendor/c\"]\n", "hg+./vendor/c"},
		{"collection name", "collections: [{name: \"hg+https://h/c\", type: git}]\n", "hg+https://h/c"},
		{"collection source", "collections: [{name: ns.c, type: git, source: \"hg+https://h/c\"}]\n", "hg+https://h/c"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			want := `requirements.yml: the source "` + tc.src + `" is a Mercurial source; Mercurial sources are not supported`
			got, err := parseFile("requirements.yml", []byte(tc.data))
			if err == nil || err.Error() != want {
				t.Fatalf("parseFile = %+v, %v; want %q", got, err, want)
			}
		})
	}

	accepted := []struct{ name, data string }{
		{"a git+ role string", "- git+https://h/r\n"},
		// galaxy takes the scm from the prefix when src has one, so this is git.
		{"a git+ src with scm hg", "- {src: \"git+https://h/r\", scm: hg, name: r}\n"},
		{"scm git", "- {src: https://h/r, scm: git, name: r}\n"},
		{"a name that ends in hg", "- acme.hg\n"},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseFile("requirements.yml", []byte(tc.data)); err != nil {
				t.Fatalf("parseFile = %v, want nil", err)
			}
		})
	}
}
