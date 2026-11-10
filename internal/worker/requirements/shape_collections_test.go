package requirements

import (
	"reflect"
	"slices"
	"testing"
	"testing/fstest"
)

// Galaxy installs a collection to <path>/<namespace>/<name>, split from the
// requirement's name, and removes that directory first. A name galaxy keeps
// as written must therefore be namespace.name; a name galaxy takes from the
// artifact is not the file's to check.
func TestParseFileAllowsOnlyNamespaceNameCollectionNames(t *testing.T) {
	refused := []struct {
		name, data, collName string
	}{
		// The reviewer's input and its siblings: with type: and source: galaxy
		// keeps the name as written and installs under it.
		{"type file with a source", "collections:\n  - name: \"x./abs/ESCAPED\"\n    type: file\n    source: ./ns-coll-1.0.0.tar.gz\n", "x./abs/ESCAPED"},
		{"type url with a source", "collections:\n  - name: \"x./abs/ESCAPED\"\n    type: url\n    source: https://example.org/c.tar.gz\n", "x./abs/ESCAPED"},
		{"type git with a source", "collections:\n  - name: \"x./abs/ESCAPED\"\n    type: git\n    source: git+https://example.org/c.git\n", "x./abs/ESCAPED"},
		{"type dir with a source", "collections:\n  - {name: ../../x, type: dir, source: ./c}\n", "../../x"},
		{"type file with an empty source", "collections:\n  - {name: \"x./abs/ESCAPED\", type: file, source: \"\"}\n", "x./abs/ESCAPED"},
		// type: galaxy keeps the name with or without a source.
		{"type galaxy", "collections:\n  - {name: \"x./abs/ESCAPED\", type: galaxy}\n", "x./abs/ESCAPED"},
		{"type galaxy with a source", "collections:\n  - {name: ../x.y, type: galaxy, source: https://galaxy.example}\n", "../x.y"},
		{"three parts", "collections:\n  - {name: a.b.c, type: galaxy}\n", "a.b.c"},
		{"a trailing space", "collections:\n  - {name: \"ns.coll \", type: galaxy}\n", "ns.coll "},
		// Without type: galaxy keeps any name its is_valid_collection_name
		// accepts, and that takes any Python identifier, not only ASCII.
		{"a plain string with a non-ASCII name", "collections:\n  - \"ns.cöll\"\n", "ns.cöll"},
		{"a mapping without type", "collections:\n  - {name: \"ns.cöll\", version: 1.0.0}\n", "ns.cöll"},
		{"a null type is no type", "collections:\n  - {name: \"ns.cöll\", type: ~, source: x}\n", "ns.cöll"},
		{"a plain string starting with a digit", "collections:\n  - 1ns.coll\n", "1ns.coll"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			want := `requirements.yml: the collection name "` + tc.collName + `" is not namespace.name`
			got, err := parseFile("requirements.yml", []byte(tc.data))
			if err == nil || err.Error() != want {
				t.Fatalf("parseFile = %+v, %v; want %q", got, err, want)
			}
		})
	}

	accepted := []struct {
		name, data string
		sources    []string
	}{
		{
			name: "the usual forms",
			data: "collections:\n" +
				"  - community.general\n" +
				"  - {name: community.docker, version: \">=3.0\"}\n" +
				"  - {name: https://example.org/ns-coll-1.0.0.tar.gz, type: url}\n" +
				"  - {name: ./local.tar.gz, type: file}\n" +
				"  - {name: git+https://github.com/x/y.git, type: git, version: main}\n" +
				"  - {name: ns.coll, source: https://galaxy.example, type: galaxy}\n",
			sources: []string{
				"community.general", "community.docker", "https://example.org/ns-coll-1.0.0.tar.gz",
				"./local.tar.gz", "git+https://github.com/x/y.git", "ns.coll", "https://galaxy.example",
			},
		},
		// A type other than galaxy without a source: galaxy moves the name into
		// the source and reads the name from the artifact.
		{name: "type dir without a source", data: "collections:\n  - {name: ../c, type: dir}\n", sources: []string{"../c"}},
		// No name, or a null one, with type and source: from the artifact too.
		{name: "no name", data: "collections:\n  - {type: file, source: ./local.tar.gz}\n", sources: []string{"./local.tar.gz"}},
		{name: "a null name", data: "collections:\n  - {name: ~, type: file, source: ./local.tar.gz}\n", sources: []string{"./local.tar.gz"}},
		// Without type, a string that can never be a collection name is a path
		// or URL to galaxy, named from the artifact.
		{name: "a plain string path", data: "collections:\n  - \"x./abs/ESCAPED\"\n", sources: []string{"x./abs/ESCAPED"}},
		{name: "a mapping path without type", data: "collections:\n  - {name: \"x./abs/ESCAPED\"}\n", sources: []string{"x./abs/ESCAPED"}},
		{name: "a plain string URL", data: "collections:\n  - https://example.org/ns-coll-1.0.0.tar.gz\n", sources: []string{"https://example.org/ns-coll-1.0.0.tar.gz"}},
		{name: "a name and a source without type", data: "collections:\n  - {name: ns.col, source: https://hub.example.org}\n", sources: []string{"ns.col", "https://hub.example.org"}},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFile("requirements.yml", []byte(tc.data))
			if err != nil || !got.collections || !reflect.DeepEqual(got.sources, tc.sources) {
				t.Fatalf("parseFile = %+v, %v; want collections with sources %q", got, err, tc.sources)
			}
		})
	}
}

// mightBeCollectionName must match every string Python's
// is_valid_collection_name accepts and miss only where it provably refuses.
func TestMightBeCollectionName(t *testing.T) {
	might := []string{"ns.coll", "ns.c\u00f6ll", "ns.class", "1ns.coll", "_.x", ".coll"}
	never := []string{"x./abs/ESCAPED", "ns.coll ", "a.b.c", "nscoll", "https://h/x.tar.gz", "./local.tar.gz", "git+x.y", "ns-x.coll"}
	for _, s := range append(might, never...) {
		want := slices.Contains(might, s)
		if got := mightBeCollectionName.MatchString(s); got != want {
			t.Errorf("mightBeCollectionName(%q) = %v, want %v", s, got, want)
		}
	}
}

// Pins existing behaviour, expected to pass at once (ruling P1): an untyped
// ns.class is accepted by parseFile, and the string is walked as a directory
// by checkSources, so galaxy reading it as a local path (a Python keyword is
// no collection name to galaxy) leaves nothing unchecked.
func TestAnUntypedKeywordNameIsCheckedAsAPath(t *testing.T) {
	p, err := parseFile("requirements.yml", []byte("collections: [ns.class]\n"))
	if err != nil {
		t.Fatalf("parseFile = %v, want nil", err)
	}
	clone := refusingFS{fstest.MapFS{
		"ns.class/galaxy.yml": {Data: []byte("namespace: ns\n")},
		"ns.class/out":        link("../../outside"),
	}}
	want := `requirements.yml: the source "ns.class" holds the link "ns.class/out", which leads outside the repository`
	if err := checkSources(t.Context(), clone, testCloneDir, "", p); err == nil || err.Error() != want {
		t.Fatalf("checkSources = %v, want %q", err, want)
	}
}
