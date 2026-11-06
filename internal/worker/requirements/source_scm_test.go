package requirements

import (
	"fmt"
	"reflect"
	"testing"
)

// TestCheckSourcesKeepsAGitSubdirectoryInsideTheCheckout: galaxy installs
// a collection of type git from os.path.join(<checkout>, <fragment>)
// (collection/concrete_artifact_manager.py:397-421,475-477), the fragment
// taken by urldefrag after the ",version" is split off, with only "/"
// stripped from its ends. Each expectation below was confirmed with
// galaxy's own parse_scm.
func TestCheckSourcesKeepsAGitSubdirectoryInsideTheCheckout(t *testing.T) {
	const out = `requirements.yml: the source %q names the subdirectory %q, which leads outside the checkout`
	refused := []struct{ name, file, src, frag string }{
		{"the reviewer's input, string form", "collections:\n  - git+https://h/x.git#../../../../cache/otherorg/repo\n",
			"git+https://h/x.git#../../../../cache/otherorg/repo", "../../../../cache/otherorg/repo"},
		{"the reviewer's input, mapping form", "collections:\n  - name: https://h/x.git#../../../../cache/otherorg/repo\n    type: git\n",
			"https://h/x.git#../../../../cache/otherorg/repo", "../../../../cache/otherorg/repo"},
		{"in the source field", "collections:\n  - name: ns.col\n    type: git\n    source: git+https://h/x.git#a/../../b\n",
			"git+https://h/x.git#a/../../b", "a/../../b"},
		{"absolute", "collections:\n  - git+https://h/x.git#/abs\n", "git+https://h/x.git#/abs", "/abs"},
		{"an empty segment", "collections:\n  - git+https://h/x.git#a//b\n", "git+https://h/x.git#a//b", "a//b"},
		{"a backslash", "collections:\n  - 'git+https://h/x.git#a\\b'\n", `git+https://h/x.git#a\b`, `a\b`},
		{"a tab galaxy removes", "collections:\n  - \"git+https://h/x.git#.\\t./x\"\n", "git+https://h/x.git#.\t./x", "../x"},
		{"before the version", "collections:\n  - git+https://h/x.git#../x,v1\n", "git+https://h/x.git#../x,v1", "../x"},
		{"after a query", "collections:\n  - git+https://h/x.git?q#../x\n", "git+https://h/x.git?q#../x", "../x"},
		{"an ssh source", "collections:\n  - git@github.com:x/y.git#..\n", "git@github.com:x/y.git#..", ".."},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parseFile("requirements.yml", []byte(tc.file))
			if err != nil {
				t.Fatalf("parseFile = %v", err)
			}
			err = checkSources(t.Context(), briefClone(), testCloneDir, "", p)
			want := fmt.Sprintf(out, tc.src, tc.frag)
			if err == nil || err.Error() != want {
				t.Fatalf("checkSources = %v, want %q", err, want)
			}
		})
	}
	accepted := []string{
		"collections:\n  - git+https://h/x.git#sub/dir\n",
		"collections:\n  - git+https://h/x.git#sub/dir/\n",
		"collections:\n  - git+https://h/x.git#./sub\n",
		"collections:\n  - git+https://h/x.git#%2e%2e/x\n", // galaxy does not decode: a directory named %2e%2e
		"collections:\n  - git+https://h/x.git,v1#../x\n",  // the "#" is in the version: no subdirectory
		"collections:\n  - git+https://h/x.git#\n",
		"collections:\n  - git@github.com:x/y.git#sub\n",
		// galaxy uses no fragment of a role source (utils/galaxy.py:75 hands
		// the whole source to git clone after "--").
		"roles:\n  - src: git+https://h/x.git#../..\n    name: x\n",
	}
	for _, file := range accepted {
		p, err := parseFile("requirements.yml", []byte(file))
		if err != nil {
			t.Fatalf("parseFile(%q) = %v", file, err)
		}
		if err := checkSources(t.Context(), briefClone(), testCloneDir, "", p); err != nil {
			t.Errorf("checkSources(%q) = %v, want nil", file, err)
		}
	}
}

func TestParseFileCollectsGitSources(t *testing.T) {
	file := "roles:\n  - src: git+https://h/r.git\n    name: r\n" +
		"collections:\n  - git+https://h/a.git#sub\n  - name: https://h/b.git\n    type: git\n  - name: ns.b\n    type: git\n    source: git+https://h/c.git\n  - name: ns.col\n    source: 42\n"
	p, err := parseFile("requirements.yml", []byte(file))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"git+https://h/a.git#sub", "https://h/b.git", "ns.b", "git+https://h/c.git", "ns.col"}
	if !reflect.DeepEqual(p.gitSources, want) {
		t.Errorf("gitSources = %q, want %q", p.gitSources, want)
	}
}

// TestCheckSourcesChecksANonStringCollectionSource: a source YAML reads
// as a number is no string source, but galaxy passes its text on.
func TestCheckSourcesChecksANonStringCollectionSource(t *testing.T) {
	for _, src := range []string{"-0o17", "-17", "-1.5"} {
		p, err := parseFile("requirements.yml", []byte("collections:\n  - name: ns.col\n    type: git\n    source: "+src+"\n"))
		if err != nil {
			t.Fatal(err)
		}
		err = checkSources(t.Context(), briefClone(), testCloneDir, "", p)
		if want := `requirements.yml: the source "` + src + `" starts with "-"`; err == nil || err.Error() != want {
			t.Errorf("checkSources(%s) = %v, want %q", src, err, want)
		}
	}
}
