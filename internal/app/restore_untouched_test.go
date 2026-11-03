// Review Focus 1 at the command line: a damaged or foreign backup given to
// `restore --replace --yes -` never touches the live database.
package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/db"
)

func schemaVersionAt(t *testing.T, url string) int64 {
	t.Helper()
	d, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	v, err := d.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRunRestoreOfABadFileLeavesTheDatabaseUntouched(t *testing.T) {
	cfg := testConfig(t)
	dir, name := takeBackup(t, cfg)
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		key  bool // restore with another master key
		want string
	}{
		{name: "truncated", data: data[:len(data)/2], want: "damaged or incomplete"},
		{name: "wrong key", data: data, key: true, want: "the master key does not match this backup"},
		{name: "not a backup", data: []byte("hello, this is not a backup\n"), want: "is not a Pulsix backup file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			target := nonEmptyTarget(t, cfg)
			if tc.key {
				target.MasterKey = [32]byte{'a', 'n', 'o', 't', 'h', 'e', 'r'}
			}
			version := schemaVersionAt(t, target.DatabaseURL)

			var stdout bytes.Buffer
			err := RunRestore(context.Background(), target,
				RestoreOptions{Name: StdinName, Replace: true, Yes: true, Stdin: bytes.NewReader(tc.data)}, &stdout, &bytes.Buffer{})
			if err == nil {
				t.Fatal("the restore succeeded")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "the database was not changed") {
				t.Errorf("error %q, want %q and that the database was not changed", err, tc.want)
			}
			if got := organizationSlugs(t, target.DatabaseURL); len(got) != 1 || got[0] != "old" {
				t.Errorf("organizations after the refused restore %v, want [old]", got)
			}
			if v := schemaVersionAt(t, target.DatabaseURL); v != version {
				t.Errorf("schema version %d after the refused restore, want %d", v, version)
			}
			if stdout.Len() != 0 {
				t.Errorf("a refused restore printed %q", stdout.String())
			}
			if left, _ := os.ReadDir(tmp); len(left) != 0 {
				t.Errorf("the spooled copy of stdin was left behind: %v", left)
			}
		})
	}
}
