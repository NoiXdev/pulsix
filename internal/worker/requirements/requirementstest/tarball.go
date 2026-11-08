package requirementstest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"slices"
	"strings"
	"testing"
)

// TarGz is a gzipped tarball of files (slash path to content), with an
// entry for every parent directory, in path order. The result is a string
// because fixture repositories carry file contents as strings.
func TarGz(t testing.TB, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := slices.Sorted(func(yield func(string) bool) {
		for name := range files {
			if !yield(name) {
				return
			}
		}
	})
	dirs := map[string]bool{}
	for _, name := range names {
		var parents []string
		for d := path.Dir(name); d != "." && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
			parents = append(parents, d)
		}
		slices.Reverse(parents)
		for _, d := range parents {
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d + "/", Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
		}
		content := files[name]
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// RoleTarball is a role tarball as galaxy installs it: files (paths
// relative to the role) under the top directory name, always with a
// meta/main.yml (galaxy refuses a role tarball without one); files may
// replace that default.
func RoleTarball(t testing.TB, name string, files map[string]string) string {
	t.Helper()
	members := map[string]string{name + "/meta/main.yml": "galaxy_info: {author: pulsix}\ndependencies: []\n"}
	for rel, content := range files {
		members[name+"/"+rel] = content
	}
	return TarGz(t, members)
}

// EvilCollectionTarball is a collection tarball (type: file) whose
// MANIFEST.json name climbs from galaxy's install target to the absolute
// path target: the minimal shape ansible-core 2.21.5 installed at target.
// Two tests use it and must keep agreeing on that shape: internal/worker/
// runner's TestTraversalFixturesEscapeWithoutIsolationInRealAnsible, the
// control that proves the fixture attacks when galaxy is not isolated, and
// internal/e2e/uidboundary's TestRequirementsCannotWriteOutsideTheirEntry,
// the in-image boundary that proves it cannot.
func EvilCollectionTarball(t testing.TB, target string) string {
	t.Helper()
	marshal := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	filesJSON := marshal(map[string]any{
		"files":  []any{map[string]any{"name": ".", "ftype": "dir", "chksum_type": nil, "chksum_sha256": nil, "format": 1}},
		"format": 1,
	})
	sum := sha256.Sum256(filesJSON)
	manifest := marshal(map[string]any{
		"collection_info": map[string]any{
			"namespace":    "pulsix_test",
			"name":         strings.Repeat("../", 32) + strings.TrimPrefix(target, "/"),
			"version":      "1.0.0",
			"dependencies": map[string]any{},
		},
		"file_manifest_file": map[string]any{
			"name": "FILES.json", "ftype": "file", "chksum_type": "sha256",
			"chksum_sha256": hex.EncodeToString(sum[:]), "format": 1,
		},
		"format": 1,
	})
	return TarGz(t, map[string]string{"MANIFEST.json": string(manifest), "FILES.json": string(filesJSON)})
}
