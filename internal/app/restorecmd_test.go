// The restore command's argument rules and messages, without a database.
package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/backup"
)

func TestRestoreOptionsValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts RestoreOptions
		want string // "" = valid
	}{
		{"list", RestoreOptions{List: true}, ""},
		{"list from a bucket", RestoreOptions{List: true, From: "s3://b/p"}, ""},
		{"by name", RestoreOptions{Name: "x.pxbak"}, ""},
		{"replace by name, asking", RestoreOptions{Name: "x.pxbak", Replace: true}, ""},
		{"replace by name, confirmed", RestoreOptions{Name: "x.pxbak", Replace: true, Yes: true}, ""},
		{"stdin", RestoreOptions{Name: StdinName}, ""},
		{"stdin replacing, confirmed", RestoreOptions{Name: StdinName, Replace: true, Yes: true}, ""},
		{"nothing to restore", RestoreOptions{}, "name the backup"},
		{"list with a name", RestoreOptions{List: true, Name: "x.pxbak"}, "--list"},
		{"list with replace", RestoreOptions{List: true, Replace: true}, "--list"},
		{"yes alone", RestoreOptions{Name: "x.pxbak", Yes: true}, "--yes only confirms --replace"},
		{"stdin with from", RestoreOptions{Name: StdinName, From: "/x"}, "--from"},
		{"stdin replacing, unconfirmed", RestoreOptions{Name: StdinName, Replace: true}, "--replace needs --yes"},
		{"stdin at a terminal", RestoreOptions{Name: StdinName, StdinIsTerminal: true}, "pipe the file in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.opts.Validate()
			var usage UsageError
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("Validate = %v, want valid", err)
			case tc.want != "" && (!errors.As(err, &usage) || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("Validate = %v, want a usage error containing %q", err, tc.want)
			}
		})
	}
}

// TestExplainRestore: every failure says whether the database changed, and
// a failure after the wipe says how to recover.
func TestExplainRestore(t *testing.T) {
	m := backup.Manifest{Header: backup.Header{SchemaVersion: 70}}
	cause := errors.New("connection reset")
	for _, tc := range []struct {
		name     string
		err      error
		want     []string
		sentinel error
	}{
		{"wrong key", fmt.Errorf("restore: %w", backup.ErrWrongKey),
			[]string{"the master key does not match this backup (or the file's encryption header is damaged)", "not changed"}, backup.ErrWrongKey},
		{"corrupt", fmt.Errorf("restore: %w: cut short", backup.ErrCorrupt),
			[]string{"x.pxbak is damaged or incomplete", "not changed"}, backup.ErrCorrupt},
		{"not a backup", fmt.Errorf("restore: %w", backup.ErrNotABackup),
			[]string{"x.pxbak is not a Pulsix backup file", "not changed"}, backup.ErrNotABackup},
		{"missing", fmt.Errorf("restore: open backup: %w", backup.ErrObjectNotFound),
			[]string{`no backup named "x.pxbak" in /b`, "restore --list --from /b"}, backup.ErrObjectNotFound},
		{"not empty", backup.ErrNotEmpty, []string{"--replace"}, nil},
		{"too new", fmt.Errorf("%w: run restore with Pulsix 9.9.9 or newer", backup.ErrBackupTooNew),
			[]string{"9.9.9", "not changed"}, backup.ErrBackupTooNew},
		{"incomplete", fmt.Errorf("restore: %w: %w", backup.ErrRestoreIncomplete, cause),
			[]string{"connection reset", "now empty", "schema version 70", "run the same restore command again"}, backup.ErrRestoreIncomplete},
		{"not migrated", fmt.Errorf("restore: %w: %w: %w", backup.ErrRestoreIncomplete, backup.ErrNotMigrated, cause),
			[]string{"data is restored", "schema version 70", "connection reset", "--replace"}, backup.ErrNotMigrated},
		{"anything else", cause, []string{"connection reset"}, cause},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := explainRestore(tc.err, "x.pxbak", "/b", m)
			for _, want := range tc.want {
				if !strings.Contains(got.Error(), want) {
					t.Errorf("message %q lacks %q", got, want)
				}
			}
			if tc.sentinel != nil && !errors.Is(got, tc.sentinel) {
				t.Errorf("message %q no longer unwraps to %v", got, tc.sentinel)
			}
		})
	}
	if got := explainRestore(fmt.Errorf("restore: %w: %w: %w", backup.ErrRestoreIncomplete, backup.ErrNotMigrated, cause), "x", "/b", m); strings.Contains(got.Error(), "now empty") {
		t.Errorf("a restore whose rows are in says the database is empty: %q", got)
	}
	// The cause is shown once, without the sentinel's own text.
	got := explainRestore(fmt.Errorf("restore: %w: %w", backup.ErrRestoreIncomplete, cause), "x", "/b", m).Error()
	if !strings.HasPrefix(got, "the restore failed after the database was cleared: connection reset. ") {
		t.Errorf("incomplete message %q", got)
	}
}

func TestFormatSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 5 << 20: "5.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := formatSize(n); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestStorageAt(t *testing.T) {
	if _, err := storageAt("s3://bucket/prefix", func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "PULSIX_BACKUP_S3_ACCESS_KEY") {
		t.Errorf("s3 without credentials: %v, want the missing variable named", err)
	}
	env := map[string]string{"PULSIX_BACKUP_S3_ACCESS_KEY": "a", "PULSIX_BACKUP_S3_SECRET_KEY": "s", "PULSIX_BACKUP_S3_ENDPOINT": "http://127.0.0.1:1"}
	st, err := storageAt("s3://bucket/prefix", func(k string) string { return env[k] })
	if err != nil || !strings.Contains(st.Describe(), "bucket") {
		t.Errorf("s3 storage = %v, %v", st, err)
	}
	st, err = storageAt("/srv/backups", nil)
	if err != nil || st.Describe() != "/srv/backups" {
		t.Errorf("folder storage = %v, %v", st, err)
	}
}

// listedStorage is a Storage whose List returns fixed objects.
type listedStorage struct {
	backup.Storage
	objs []backup.Object
}

func (s listedStorage) List(context.Context) ([]backup.Object, error) { return s.objs, nil }
func (s listedStorage) Describe() string                              { return "fixed" }

// TestRestoreListEscapesControlBytes: object names and header strings come
// from the storage, which anyone with write access to it controls. Terminal
// escapes in them are shown escaped, never sent to the operator's terminal.
func TestRestoreListEscapesControlBytes(t *testing.T) {
	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	st := listedStorage{objs: []backup.Object{
		{Name: "pulsix-\x1b[2J.pxbak", Size: 10, HeaderErr: backup.ErrNotABackup},
		{Name: "forged\x1b]0;x\a.pxbak", Size: 20, Header: backup.Header{CreatedAt: at, Kind: "\x1b[8mmanual", PulsixVersion: "1.0\x1b[0m\t"}},
	}}
	var out bytes.Buffer
	if err := listBackups(context.Background(), st, &out); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\a") {
		t.Fatalf("the listing carries control bytes: %q", out.String())
	}
	for _, want := range []string{`pulsix-\x1b[2J.pxbak`, `forged\x1b]0;x\a.pxbak`, `\x1b[8mmanual`, `1.0\x1b[0m\t`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("listing lacks the escaped %s:\n%s", want, out.String())
		}
	}
}

func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"pulsix-20261102T030000Z-manual.pxbak": "pulsix-20261102T030000Z-manual.pxbak",
		"1.2.3 (dev)":                          "1.2.3 (dev)",
		"a\x1bb":                               `"a\x1bb"`,
		"bad\xffutf8":                          `"bad\xffutf8"`,
		"rtl\u202eoverride":                    `"rtl\u202eoverride"`,
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %s, want %s", in, got, want)
		}
	}
}
