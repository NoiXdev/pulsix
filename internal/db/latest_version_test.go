package db

import (
	"testing"
	"testing/fstest"
)

// TestLatestVersionIn reads the newest migration's version from a migrations
// directory: subdirectories and files that are no migration (a README) are
// skipped, and a directory without any migration, or none at all, is an
// error rather than version 0.
func TestLatestVersionIn(t *testing.T) {
	file := func() *fstest.MapFile { return &fstest.MapFile{Data: []byte("-- +goose Up\n")} }
	cases := []struct {
		name    string
		fsys    fstest.MapFS
		want    int64
		wantErr bool
	}{
		{"highest wins", fstest.MapFS{
			"migrations/00001_init.sql":  file(),
			"migrations/00083_later.sql": file(),
			"migrations/00007_mid.sql":   file(),
		}, 83, false},
		{"README and subdirectories are skipped", fstest.MapFS{
			"migrations/README.md":          file(),
			"migrations/00002_two.sql":      file(),
			"migrations/00099_nested/x.sql": file(),
		}, 2, false},
		{"no migration is an error", fstest.MapFS{"migrations/README.md": file()}, 0, true},
		{"no directory is an error", fstest.MapFS{"other/00001_x.sql": file()}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := latestVersionIn(tc.fsys)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("latestVersionIn = %d, %v; want %d (error %v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// TestLatestVersionReadsTheEmbeddedMigrations: the binary's own migrations
// have a newest version.
func TestLatestVersionReadsTheEmbeddedMigrations(t *testing.T) {
	v, err := LatestVersion()
	if err != nil || v < 83 {
		t.Fatalf("LatestVersion = %d, %v; want at least 83", v, err)
	}
}
