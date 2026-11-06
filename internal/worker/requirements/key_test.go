package requirements

import (
	"regexp"
	"strings"
	"testing"
)

func TestKey(t *testing.T) {
	sha256Commit := strings.Repeat("ab", 32)
	a := fileData{path: "requirements.yml", data: []byte("- src: ./r.tar.gz\n")}
	b := fileData{path: "roles/requirements.yml", data: []byte("- acme.web\n")}
	base := key(testCommit, "", []fileData{a, b})

	if again := key(testCommit, "", []fileData{a, b}); again != base {
		t.Fatalf("the same inputs gave %q and %q", base, again)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}-[0-9a-f]{16}$`).MatchString(base) {
		t.Fatalf("key %q does not match a SHA-1 commit key", base)
	}
	if k := key(sha256Commit, "", []fileData{a, b}); !regexp.MustCompile(`^[0-9a-f]{64}-[0-9a-f]{16}$`).MatchString(k) {
		t.Fatalf("key %q does not match a SHA-256 commit key", k)
	}

	hash := func(k string) string { return k[strings.LastIndex(k, "-")+1:] }
	changedByte := fileData{path: a.path, data: []byte("- src: ./r.tar.gx\n")}
	otherPath := fileData{path: "collections/requirements.yml", data: b.data}
	cases := []struct {
		name string
		key  string
	}{
		{"another root", key(testCommit, "ansible", []fileData{a, b})},
		{"one changed byte", key(testCommit, "", []fileData{changedByte, b})},
		{"another path with the same bytes", key(testCommit, "", []fileData{a, otherPath})},
		{"swapped order", key(testCommit, "", []fileData{b, a})},
		{"one file fewer", key(testCommit, "", []fileData{a})},
	}
	for _, tc := range cases {
		if hash(tc.key) == hash(base) {
			t.Errorf("%s: the hash part stayed %q", tc.name, hash(base))
		}
	}
	other := key(strings.Repeat("1", 40), "", []fileData{a, b})
	if other == base || !strings.HasPrefix(other, strings.Repeat("1", 40)+"-") {
		t.Errorf("another commit: key %q (base %q)", other, base)
	}
	// A path and its bytes are framed: moving bytes between them is a change.
	split1 := key(testCommit, "", []fileData{{path: "a", data: []byte("bc")}})
	split2 := key(testCommit, "", []fileData{{path: "ab", data: []byte("c")}})
	if split1 == split2 {
		t.Error("moving bytes between the path and the data kept the key")
	}
}

// The algorithm is fixed: every worker computes the same key, so a change to
// it must be deliberate. The value is the shasum -a 256 of the bytes the
// plan's algorithm (Task 2) hashes, computed outside Go.
func TestKeyIsStable(t *testing.T) {
	got := key(testCommit, "ansible", []fileData{{path: "ansible/requirements.yml", data: []byte("[]\n")}})
	const want = testCommit + "-" + "17f967cf0e61caf3"
	if got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
}
