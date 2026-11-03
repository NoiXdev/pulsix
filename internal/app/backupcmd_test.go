// The backup and restore commands against real databases: the shared test
// database as the source, FreshDatabase as a restore target.
package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// seedOrganization creates an organization in the database at url.
func seedOrganization(t *testing.T, url, slug string) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := gen.New(d.Pool()).CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug}); err != nil {
		t.Fatal(err)
	}
}

// organizationSlugs lists the organizations in the database at url.
func organizationSlugs(t *testing.T, url string) []string {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var exists bool
	if err := d.Pool().QueryRow(ctx, `SELECT to_regclass('public.organizations') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		return nil
	}
	rows, err := d.Pool().Query(ctx, `SELECT slug FROM organizations ORDER BY slug`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var slugs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		slugs = append(slugs, s)
	}
	return slugs
}

// takeBackup backs the shared test database (with organization "acme") up
// into a temporary folder and returns the folder and the file's name.
func takeBackup(t *testing.T, cfg config.Config) (dir, name string) {
	t.Helper()
	seedOrganization(t, cfg.DatabaseURL, "acme")
	dir = t.TempDir()
	var out bytes.Buffer
	if err := RunBackup(context.Background(), cfg, BackupOptions{To: dir}, &out); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.pxbak"))
	if err != nil || len(files) != 1 {
		t.Fatalf("backup folder holds %v (%v), want one .pxbak", files, err)
	}
	name = filepath.Base(files[0])
	if !strings.Contains(out.String(), name) {
		t.Errorf("backup output %q does not name %s", out.String(), name)
	}
	return dir, name
}

// restoreTarget is cfg pointed at a fresh, empty database.
func restoreTarget(t *testing.T, cfg config.Config) config.Config {
	t.Helper()
	cfg.DatabaseURL = testdb.FreshDatabase(t)
	return cfg
}

// nonEmptyTarget is cfg pointed at a fresh, migrated database with the
// organization "old".
func nonEmptyTarget(t *testing.T, cfg config.Config) config.Config {
	t.Helper()
	cfg = restoreTarget(t, cfg)
	d, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.Close()
	seedOrganization(t, cfg.DatabaseURL, "old")
	return cfg
}

func TestRunBackupToDirThenRestoreList(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	dir, name := takeBackup(t, cfg)

	// The attempt is recorded: manual, nobody's, succeeded.
	d, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var kind, state, object string
	var noActor bool
	if err := d.Pool().QueryRow(ctx, `SELECT kind, state, object_name, actor_id IS NULL FROM backup_runs`).Scan(&kind, &state, &object, &noActor); err != nil {
		t.Fatalf("read the recorded attempt: %v", err)
	}
	if kind != backup.KindManual || state != backup.StateSucceeded || object != name || !noActor {
		t.Errorf("attempt = %s %s %s actorless=%v; want manual succeeded %s without actor", kind, state, object, noActor, name)
	}

	var out bytes.Buffer
	if err := RunRestore(ctx, cfg, RestoreOptions{List: true, From: dir}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("restore --list: %v", err)
	}
	for _, want := range []string{"NAME", "CREATED (UTC)", "METRICS", name, "manual", "dev", "yes"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("listing lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRunBackupWithoutMetrics(t *testing.T) {
	cfg := testConfig(t)
	dir := t.TempDir()
	if err := RunBackup(context.Background(), cfg, BackupOptions{To: dir, WithoutMetrics: true}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunRestore(context.Background(), cfg, RestoreOptions{List: true, From: dir}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), " no") {
		t.Errorf("a backup without metrics is listed as having them:\n%s", out.String())
	}
}

// TestRunBackupToTheConfiguredFolder: without --to the backup goes where
// the settings say; for the folder destination that is the default folder,
// which this test cannot write, so the error names it.
func TestRunBackupToTheConfiguredFolder(t *testing.T) {
	if _, statErr := os.Stat(backup.DefaultDir); statErr == nil {
		t.Skip(backup.DefaultDir + " exists on this machine")
	}
	cfg := testConfig(t)
	err := RunBackup(context.Background(), cfg, BackupOptions{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), backup.DefaultDir) {
		t.Fatalf("backup to the configured folder: %v, want an error naming %s", err, backup.DefaultDir)
	}
}

// TestRunBackupOfAnOlderSchema: a database that predates the attempt
// history (before upgrading, with the new image) can still be backed up.
func TestRunBackupOfAnOlderSchema(t *testing.T) {
	ctx := context.Background()
	cfg := restoreTarget(t, testConfig(t))
	d, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MigrateTo(ctx, 60); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := RunBackup(ctx, cfg, BackupOptions{To: dir}, &bytes.Buffer{}); err != nil {
		t.Fatalf("backup of a schema-60 database: %v", err)
	}
	var out bytes.Buffer
	if err := RunRestore(ctx, cfg, RestoreOptions{List: true, From: dir}, &out, &bytes.Buffer{}); err != nil || !strings.Contains(out.String(), "manual") {
		t.Fatalf("listing %q, %v", out.String(), err)
	}
}

func TestRunBackupKeyPrintsDerivedKey(t *testing.T) {
	cfg := testConfig(t)
	var out bytes.Buffer
	if err := RunBackupKey(cfg, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), backup.DeriveKey(cfg.MasterKey)) || !strings.Contains(out.String(), "age -d") {
		t.Errorf("backup key output:\n%s", out.String())
	}
}

func TestRunRestoreByNameIntoFreshDatabase(t *testing.T) {
	cfg := testConfig(t)
	dir, name := takeBackup(t, cfg)
	target := restoreTarget(t, cfg)

	var out bytes.Buffer
	if err := RunRestore(context.Background(), target, RestoreOptions{From: dir, Name: name}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := organizationSlugs(t, target.DatabaseURL); len(got) != 1 || got[0] != "acme" {
		t.Errorf("organizations after restore %v, want [acme]", got)
	}
	for _, want := range []string{name, "organizations", "Start the stack. Workers and agents reconnect on their own, unless they renewed their", "enrolled again", "(ended by the restore)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("restore output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRunRestoreFromStdinIntoFreshDatabase(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	cfg := testConfig(t)
	dir, name := takeBackup(t, cfg)
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	target := restoreTarget(t, cfg)

	var out bytes.Buffer
	if err := RunRestore(context.Background(), target, RestoreOptions{Name: "-", Stdin: bytes.NewReader(data)}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("restore from stdin: %v", err)
	}
	if got := organizationSlugs(t, target.DatabaseURL); len(got) != 1 || got[0] != "acme" {
		t.Errorf("organizations after restore %v, want [acme]", got)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("the spooled copy of stdin was left behind: %v", left)
	}
}

func TestRunRestoreFromStdinRequiresYesWithReplace(t *testing.T) {
	cfg := testConfig(t)
	dir, name := takeBackup(t, cfg)
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	target := nonEmptyTarget(t, cfg)

	err = RunRestore(context.Background(), target, RestoreOptions{Name: "-", Replace: true, Stdin: bytes.NewReader(data)}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("stdin with --replace and without --yes: %v, want a refusal naming --yes", err)
	}
	if got := organizationSlugs(t, target.DatabaseURL); len(got) != 1 || got[0] != "old" {
		t.Errorf("the target changed: organizations %v, want [old]", got)
	}

	if err := RunRestore(context.Background(), target, RestoreOptions{Name: "-", Replace: true, Yes: true, Stdin: bytes.NewReader(data)}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("stdin with --replace --yes: %v", err)
	}
	if got := organizationSlugs(t, target.DatabaseURL); len(got) != 1 || got[0] != "acme" {
		t.Errorf("organizations after replace %v, want [acme]", got)
	}
}

func TestRunRestoreRefusesANonEmptyDatabase(t *testing.T) {
	cfg := testConfig(t)
	dir, name := takeBackup(t, cfg)
	target := nonEmptyTarget(t, cfg)

	err := RunRestore(context.Background(), target, RestoreOptions{From: dir, Name: name}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("restore into a non-empty database: %v, want a refusal naming --replace", err)
	}
	// --replace without --yes, and nobody at a terminal to ask.
	err = RunRestore(context.Background(), target, RestoreOptions{From: dir, Name: name, Replace: true, Stdin: strings.NewReader("restore\n")}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("--replace without a terminal: %v, want a refusal naming --yes", err)
	}
	if got := organizationSlugs(t, target.DatabaseURL); len(got) != 1 || got[0] != "old" {
		t.Errorf("the target changed: organizations %v, want [old]", got)
	}
}

func TestRunRestoreReplaceAsksOnATerminal(t *testing.T) {
	cfg := testConfig(t)
	dir, name := takeBackup(t, cfg)
	target := nonEmptyTarget(t, cfg)

	var prompt bytes.Buffer
	err := RunRestore(context.Background(), target,
		RestoreOptions{From: dir, Name: name, Replace: true, Stdin: strings.NewReader("yes\n"), StdinIsTerminal: true}, &bytes.Buffer{}, &prompt)
	if err == nil || !strings.Contains(prompt.String(), "Type restore") {
		t.Fatalf("a wrong answer: %v (prompt %q), want the restore cancelled after asking", err, prompt.String())
	}
	if got := organizationSlugs(t, target.DatabaseURL); len(got) != 1 || got[0] != "old" {
		t.Fatalf("the target changed after a wrong answer: %v", got)
	}

	if err := RunRestore(context.Background(), target,
		RestoreOptions{From: dir, Name: name, Replace: true, Stdin: strings.NewReader("restore\n"), StdinIsTerminal: true}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("typing restore: %v", err)
	}
	if got := organizationSlugs(t, target.DatabaseURL); len(got) != 1 || got[0] != "acme" {
		t.Errorf("organizations after replace %v, want [acme]", got)
	}
}

func TestRunRestoreRefusesWhileManagerConnected(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	dir, name := takeBackup(t, cfg)
	target := restoreTarget(t, cfg)

	managerURL, err := db.WithApplicationName(target.DatabaseURL, "pulsix-manager")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := db.Open(ctx, managerURL)
	if err != nil {
		t.Fatal(err)
	}
	err = RunRestore(ctx, target, RestoreOptions{From: dir, Name: name}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "stop the manager first") {
		t.Fatalf("restore with a manager connected: %v, want stop the manager first", err)
	}
	if got := organizationSlugs(t, target.DatabaseURL); len(got) != 0 {
		t.Errorf("the target changed: %v", got)
	}

	manager.Close()
	if err := RunRestore(ctx, target, RestoreOptions{From: dir, Name: name}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("restore once the manager is gone: %v", err)
	}
}

func TestRunRestoreUnknownName(t *testing.T) {
	cfg := testConfig(t)
	dir := t.TempDir()
	err := RunRestore(context.Background(), restoreTarget(t, cfg), RestoreOptions{From: dir, Name: "pulsix-20260101T000000Z-manual.pxbak"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "restore --list") {
		t.Fatalf("restore of a missing backup: %v, want a pointer to restore --list", err)
	}
}

func TestManagersConnectedCountsOnlyManagersOfThisDatabase(t *testing.T) {
	ctx := context.Background()
	url := testdb.FreshDatabase(t)
	cli, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if n, err := ManagersConnected(ctx, cli.Pool()); err != nil || n != 0 {
		t.Fatalf("ManagersConnected with none = %d, %v", n, err)
	}
	// A manager on another database does not count.
	other, err := db.WithApplicationName(testdb.FreshDatabase(t), "pulsix-manager")
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := db.Open(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	defer elsewhere.Close()
	if n, err := ManagersConnected(ctx, cli.Pool()); err != nil || n != 0 {
		t.Fatalf("ManagersConnected with a manager elsewhere = %d, %v", n, err)
	}
	here, err := db.WithApplicationName(url, "pulsix-manager")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := db.Open(ctx, here)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if n, err := ManagersConnected(ctx, cli.Pool()); err != nil || n < 1 {
		t.Fatalf("ManagersConnected with a manager = %d, %v", n, err)
	}
}

// TestRestoreRechecksForManagersBeforeTheWipe: the check RunRestore hands
// backup.Restore as BeforeWipe refuses once a manager has connected, even
// though none was there when the command started.
func TestRestoreRechecksForManagersBeforeTheWipe(t *testing.T) {
	ctx := context.Background()
	url := testdb.FreshDatabase(t)
	cli, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	check := noManagerConnected(cli.Pool())
	if err := check(ctx); err != nil {
		t.Fatalf("check without a manager: %v", err)
	}
	managerURL, err := db.WithApplicationName(url, "pulsix-manager")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := db.Open(ctx, managerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Pool().Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := check(ctx); !errors.Is(err, errManagerConnected) {
		t.Fatalf("check with a manager connected: %v, want errManagerConnected", err)
	}
	got := explainRestore(fmt.Errorf("restore: %w", errManagerConnected), "x", "/b", backup.Manifest{})
	if !strings.Contains(got.Error(), "stop the manager first") || !strings.Contains(got.Error(), "not changed") {
		t.Errorf("explained %q, want stop the manager first and that nothing changed", got)
	}
}
