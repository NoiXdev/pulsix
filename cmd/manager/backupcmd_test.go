package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// TestDispatchKnowsBackupAndRestore: the backup and restore commands are
// routed, answer --help with their usage (exit 0), and refuse a malformed
// argument list as a usage error (exit 2) — never by starting a server.
func TestDispatchKnowsBackupAndRestore(t *testing.T) {
	for _, args := range [][]string{
		{"backup", "--help"},
		{"backup", "key", "--help"},
		{"restore", "--help"},
	} {
		var stderr bytes.Buffer
		if code := dispatch(args, io.Discard, &stderr); code != 0 {
			t.Errorf("dispatch %v: exit code = %d, want 0", args, code)
		}
		if !strings.Contains(stderr.String(), "usage: pulsix-manager "+args[0]) {
			t.Errorf("dispatch %v: stderr = %q, want its usage", args, stderr.String())
		}
	}

	for _, args := range [][]string{
		{"backup", "foo"},
		{"backup", "--to"},
		{"backup", "--bogus"},
		{"backup", "key", "extra"},
		{"restore"},
		{"restore", "--list", "pulsix-20261102T030000Z-manual.pxbak"},
		{"restore", "a.pxbak", "b.pxbak"},
		{"restore", "--list", "--replace"},
		{"restore", "--yes", "a.pxbak"},
		// Standard input carries the backup, so it cannot answer the
		// confirmation: --replace needs --yes.
		{"restore", "--replace", "-"},
		{"restore", "--from", "/x", "-"},
	} {
		var stderr bytes.Buffer
		if code := dispatch(args, io.Discard, &stderr); code != 2 {
			t.Errorf("dispatch %v: exit code = %d, want 2 (usage error); stderr %q", args, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "usage: pulsix-manager") {
			t.Errorf("dispatch %v: stderr = %q, want the usage", args, stderr.String())
		}
	}

	// Well-formed, but without configuration: a failure (exit 1), not a
	// usage error and not a server.
	t.Setenv("PULSIX_DATABASE_URL", "")
	for _, args := range [][]string{
		{"backup"},
		{"backup", "key"},
		{"restore", "--list"},
		{"restore", "pulsix-20261102T030000Z-manual.pxbak"},
	} {
		var stderr bytes.Buffer
		if code := dispatch(args, io.Discard, &stderr); code != 1 {
			t.Errorf("dispatch %v without configuration: exit code = %d, want 1; stderr %q", args, code, stderr.String())
		}
	}
}

func TestUsageTextNamesTheBackupCommands(t *testing.T) {
	for _, want := range []string{
		"pulsix-manager backup [--to <dir>|s3://bucket/prefix] [--without-metrics]",
		"pulsix-manager backup key",
		"pulsix-manager restore --list [--from <dir>|s3://bucket/prefix]",
		"pulsix-manager restore [--from <dir>|s3://bucket/prefix] [--replace] [--yes] <name>|-",
		"--replace",
	} {
		if !strings.Contains(usageText, want) {
			t.Errorf("usage text lacks %q:\n%s", want, usageText)
		}
	}
}

// TestBackupKeyCommand runs a whole command through dispatch: configuration
// from the environment, output on stdout, exit 0.
func TestBackupKeyCommand(t *testing.T) {
	t.Setenv("PULSIX_DATABASE_URL", "postgres://u:p@db.invalid/pulsix")
	t.Setenv("PULSIX_PUBLIC_URL", "https://pulsix.example.com")
	t.Setenv("PULSIX_MASTER_KEY", strings.Repeat("ab", 32))
	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"backup", "key"}, &stdout, &stderr); code != 0 {
		t.Fatalf("backup key: exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "age -d") {
		t.Errorf("backup key printed %q", stdout.String())
	}

	// A failing command: exit 1 with the command's name on stderr.
	stderr.Reset()
	if code := dispatch([]string{"backup", "--to", t.TempDir()}, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "backup: ") {
		t.Errorf("backup against an unreachable database: exit %d, stderr %q; want 1 and the error", code, stderr.String())
	}
}

func TestParseInterspersed(t *testing.T) {
	fs := newCommandFlags("restore", io.Discard)
	replace := fs.Bool("replace", false, "")
	from := fs.String("from", "", "")
	names, err := parseInterspersed(fs, []string{"--from", "/b", "a.pxbak", "--replace"})
	if err != nil || len(names) != 1 || names[0] != "a.pxbak" || !*replace || *from != "/b" {
		t.Fatalf("parse = %v, %v (replace %v, from %q)", names, err, *replace, *from)
	}
	fs = newCommandFlags("restore", io.Discard)
	names, err = parseInterspersed(fs, []string{"-"})
	if err != nil || len(names) != 1 || names[0] != "-" {
		t.Fatalf("parse of - = %v, %v; want the standard input name", names, err)
	}
}
