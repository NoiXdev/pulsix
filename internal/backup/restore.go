package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/db"
)

var (
	// ErrNotEmpty means the target database has a user or an organization and
	// the restore was not asked to replace it.
	ErrNotEmpty = errors.New("backup: the database is not empty")
	// ErrBackupTooNew means the backup's schema is newer than this binary's
	// migrations; the wrapping message names the Pulsix version to use.
	ErrBackupTooNew = errors.New("backup: the backup is newer than this Pulsix")
	// ErrRestoreIncomplete wraps every failure after the wipe. Unless the
	// error is also ErrNotMigrated, the load did not commit: the database
	// holds no Pulsix data, its schema is at most the backup's version, and
	// the same restore can simply run again.
	ErrRestoreIncomplete = errors.New("backup: the restore stopped after the database was cleared")
	// ErrNotMigrated means the backup's rows were loaded and committed, but
	// the migrations past the backup's schema version failed. It always
	// comes wrapped together with ErrRestoreIncomplete.
	ErrNotMigrated = errors.New("backup: the restored data was not migrated to this version")
	// ErrWrittenDuringRestore means the load found that something else
	// (a manager, most likely) wrote to the database or migrated it during
	// the restore. The load did not commit, but the database may hold what
	// the other writer put there, so it is not known to be empty. It always
	// comes wrapped together with ErrRestoreIncomplete.
	ErrWrittenDuringRestore = errors.New("backup: something else wrote to the database during the restore")
)

// RestoreOptions controls Restore.
type RestoreOptions struct {
	// Replace allows restoring over a database that is not empty.
	Replace bool
	// BinarySchemaVersion is the newest migration this binary carries.
	BinarySchemaVersion int64
	// BeforeWipe, when set, runs after every check and right before the
	// wipe; its error stops the restore with the database unchanged. The
	// restore command rechecks there that no manager has connected since
	// it started.
	BeforeWipe func(ctx context.Context) error

	// afterMigrate runs between MigrateTo and the load; tests only.
	afterMigrate func(*pgxpool.Pool) error
	// afterWipe runs between the wipe and MigrateTo; tests only.
	afterWipe func(*pgxpool.Pool) error
}

// Restore replaces d's public schema with the backup open yields.
//
// open is called twice: the first pass reads the whole file through Verify,
// so a truncated, tampered or wrong-key file is refused before anything is
// dropped; the second pass loads it. Every decision uses the authenticated
// manifest header, never the plaintext one.
//
// After the checks (Verify, not empty unless Replace, not newer than the
// binary, BeforeWipe) Restore drops public's objects, migrates up to the
// backup's schema version, loads every table in one transaction and then
// runs the remaining migrations, exactly as a manager start would. The
// load ends every session the backup holds. d must connect as the schema
// owner; no superuser right is needed.
//
// A failure before the wipe leaves the database as it was. A failure after
// it wraps ErrRestoreIncomplete (and ErrNotMigrated once the rows are in)
// and returns the verified manifest with the error.
func Restore(ctx context.Context, d *db.DB, masterKey [32]byte, open func() (io.ReadCloser, error), opts RestoreOptions) (Manifest, error) {
	m, err := verifyFrom(open, masterKey)
	if err != nil {
		return Manifest{}, fmt.Errorf("restore: %w", err)
	}
	empty, err := IsEmpty(ctx, d.Pool())
	if err != nil {
		return Manifest{}, fmt.Errorf("restore: %w", err)
	}
	if !empty && !opts.Replace {
		return Manifest{}, ErrNotEmpty
	}
	if m.Header.SchemaVersion > opts.BinarySchemaVersion {
		return Manifest{}, fmt.Errorf("%w: this backup is from Pulsix %s (schema %d, this binary knows %d); run restore with Pulsix %s or newer",
			ErrBackupTooNew, m.Header.PulsixVersion, m.Header.SchemaVersion, opts.BinarySchemaVersion, m.Header.PulsixVersion)
	}
	if opts.BeforeWipe != nil {
		if err := opts.BeforeWipe(ctx); err != nil {
			return Manifest{}, fmt.Errorf("restore: %w", err)
		}
	}

	if err := wipe(ctx, d.Pool()); err != nil {
		// One transaction: the database is as it was.
		return Manifest{}, fmt.Errorf("restore: %w", err)
	}
	if err := migrateAndLoad(ctx, d, masterKey, open, m, opts); err != nil {
		return m, fmt.Errorf("restore: %w: %w", ErrRestoreIncomplete, err)
	}
	if err := d.Migrate(ctx); err != nil {
		return m, fmt.Errorf("restore: %w: %w: %w", ErrRestoreIncomplete, ErrNotMigrated, err)
	}
	return m, nil
}

// migrateAndLoad migrates the wiped schema to the backup's version and loads
// the backup into it.
//
// The version is read back after MigrateTo: MigrateTo never migrates down,
// so a manager that migrated the wiped schema further in the meantime would
// otherwise have the backup's rows skip every migration in between.
func migrateAndLoad(ctx context.Context, d *db.DB, masterKey [32]byte, open func() (io.ReadCloser, error), m Manifest, opts RestoreOptions) error {
	want := m.Header.SchemaVersion
	if opts.afterWipe != nil {
		if err := opts.afterWipe(d.Pool()); err != nil {
			return fmt.Errorf("after wipe: %w", err)
		}
	}
	if err := d.MigrateTo(ctx, want); err != nil {
		return err
	}
	got, err := d.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("the schema is at version %d instead of the backup's %d: something else migrated the database during the restore (is a manager running?)", got, want)
	}
	if opts.afterMigrate != nil {
		if err := opts.afterMigrate(d.Pool()); err != nil {
			return fmt.Errorf("after migrate: %w", err)
		}
	}
	return load(ctx, d.Pool(), masterKey, open, m)
}

// verifyFrom runs Verify over one pass of open.
func verifyFrom(open func() (io.ReadCloser, error), masterKey [32]byte) (Manifest, error) {
	rc, err := open()
	if err != nil {
		return Manifest{}, fmt.Errorf("open backup: %w", err)
	}
	defer rc.Close() //nolint:errcheck // read only
	return Verify(rc, masterKey)
}

// IsEmpty reports whether pool's database counts as empty: the schema is not
// there, or users and organizations have no rows. A manager writes the
// instance CA on its first start, so other tables may have rows.
func IsEmpty(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	for _, table := range []string{"users", "organizations"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists); err != nil {
			return false, fmt.Errorf("is empty: %w", err)
		}
		if !exists {
			continue
		}
		var found bool
		if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+qualified(table)+")").Scan(&found); err != nil {
			return false, fmt.Errorf("is empty: %w", err)
		}
		if found {
			return false, nil
		}
	}
	return true, nil
}

// load copies the backup's tables into the freshly migrated schema in one
// transaction.
//
// The schema owner is subject to the policies of a table that forces
// row-level security, and COPY FROM refuses such a table outright, so FORCE
// is lifted for the load and put back before commit. Foreign keys are
// dropped for the load and re-added from their catalogue definitions
// afterwards, which validates every loaded row; that frees the load from any
// table order, self-references included, without session_replication_role
// (superuser only). The second read of the file is authenticated as well:
// the transaction commits only after the stream was read to its end and its
// manifest matched the verified one.
//
// The load refuses to start when the freshly migrated schema already has a
// user, an organization or a membership, which only something writing
// during the restore can have put there, and it deletes every session
// after the copy: a restore signs everybody out. It then records the restore
// itself in the restore marker (see RestoreMarker), replacing any marker the
// backup carried.
func load(ctx context.Context, pool *pgxpool.Pool, masterKey [32]byte, open func() (io.ReadCloser, error), want Manifest) error {
	rc, err := open()
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer rc.Close() //nolint:errcheck // read only
	if _, err := ReadHeader(rc); err != nil {
		return err
	}
	dec, err := Decrypt(rc, masterKey)
	if err != nil {
		return err
	}
	fr := NewFrameReader(dec)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("load: begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed

	forced, err := listForcedTables(ctx, tx)
	if err != nil {
		return err
	}
	fks, err := listForeignKeys(ctx, tx)
	if err != nil {
		return err
	}
	tables, err := listTables(ctx, tx)
	if err != nil {
		return err
	}
	for _, name := range forced {
		if _, err := tx.Exec(ctx, "ALTER TABLE "+qualified(name)+" NO FORCE ROW LEVEL SECURITY"); err != nil {
			return fmt.Errorf("load: lift forced row-level security on %s: %w", name, err)
		}
	}
	for _, fk := range fks {
		if _, err := tx.Exec(ctx, "ALTER TABLE "+qualified(fk.table)+" DROP CONSTRAINT "+pgx.Identifier{fk.name}.Sanitize()); err != nil {
			return fmt.Errorf("load: drop foreign key %s on %s: %w", fk.name, fk.table, err)
		}
	}

	if err := refuseNonEmptyCore(ctx, tx, tables); err != nil {
		return err
	}
	if err := copyTables(ctx, tx, fr, tables, want); err != nil {
		return err
	}

	for _, fk := range fks {
		if _, err := tx.Exec(ctx, "ALTER TABLE "+qualified(fk.table)+" ADD CONSTRAINT "+pgx.Identifier{fk.name}.Sanitize()+" "+fk.definition); err != nil {
			return fmt.Errorf("load: re-add foreign key %s on %s: %w", fk.name, fk.table, err)
		}
	}
	// After the foreign keys are back, so a table that ever references
	// sessions cascades or refuses as it would anywhere else; before FORCE
	// is, so no policy hides a row from the delete.
	if err := endSessions(ctx, tx, tables); err != nil {
		return err
	}
	if err := writeRestoreMarker(ctx, tx, want.Header.CreatedAt); err != nil {
		return err
	}
	for _, name := range forced {
		if _, err := tx.Exec(ctx, "ALTER TABLE "+qualified(name)+" FORCE ROW LEVEL SECURITY"); err != nil {
			return fmt.Errorf("load: force row-level security on %s: %w", name, err)
		}
	}
	for _, s := range want.Sequences {
		if _, err := tx.Exec(ctx, `SELECT setval($1::regclass, $2, $3)`, qualified(s.Name), s.LastValue, s.IsCalled); err != nil {
			return fmt.Errorf("load: set sequence %s: %w", s.Name, err)
		}
	}
	if err := abandonRunningAttempts(ctx, tx); err != nil {
		return err
	}
	// The same check as after MigrateTo, for a migration that committed
	// while the load ran.
	if v, err := db.ReadSchemaVersion(ctx, tx); err != nil {
		return fmt.Errorf("load: %w", err)
	} else if v != want.Header.SchemaVersion {
		return fmt.Errorf("%w: the schema moved to version %d during the load", ErrWrittenDuringRestore, v)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("load: commit: %w", err)
	}
	return nil
}

// coreTables are the tables a freshly migrated schema leaves empty and
// IsEmpty looks at, plus the memberships that join them.
var coreTables = []string{"users", "organizations", "org_memberships"}

// refuseNonEmptyCore fails when one of coreTables has a row before the
// load. The tables are locked against writers until the load commits, so
// nothing can slip in between the check and the copy.
func refuseNonEmptyCore(ctx context.Context, tx pgx.Tx, tables []tableInfo) error {
	var present, quoted []string
	for _, name := range coreTables {
		if hasTable(tables, name) {
			present, quoted = append(present, name), append(quoted, qualified(name))
		}
	}
	if len(present) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE "+strings.Join(quoted, ", ")+" IN SHARE MODE"); err != nil {
		return fmt.Errorf("load: lock %s: %w", strings.Join(present, ", "), err)
	}
	for _, name := range present {
		var found bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+qualified(name)+")").Scan(&found); err != nil {
			return fmt.Errorf("load: look into %s: %w", name, err)
		}
		if found {
			return fmt.Errorf("%w: %s had rows before the load started", ErrWrittenDuringRestore, name)
		}
	}
	return nil
}

// endSessions deletes every browser session the backup brought along.
func endSessions(ctx context.Context, tx pgx.Tx, tables []tableInfo) error {
	if !hasTable(tables, "sessions") {
		return nil
	}
	if _, err := tx.Exec(ctx, "DELETE FROM "+qualified("sessions")); err != nil {
		return fmt.Errorf("load: end the restored sessions: %w", err)
	}
	return nil
}

func hasTable(tables []tableInfo, name string) bool {
	return slices.ContainsFunc(tables, func(t tableInfo) bool { return t.name == name })
}

// copyTables streams every table of fr into its target table and checks each
// row count against the verified manifest. It reads fr to io.EOF, so the
// payload is authenticated before the caller commits.
func copyTables(ctx context.Context, tx pgx.Tx, fr *FrameReader, tables []tableInfo, want Manifest) error {
	columns := make(map[string][]string, len(tables))
	for _, t := range tables {
		columns[t.name] = t.columns
	}
	var loaded []TableCount
	for {
		t, err := fr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		have, ok := columns[t.Name]
		if !ok {
			return fmt.Errorf("load: the backup has table %s, which schema version %d does not", t.Name, want.Header.SchemaVersion)
		}
		for _, c := range t.Columns {
			if !slices.Contains(have, c) {
				return fmt.Errorf("load: the backup has column %s.%s, which schema version %d does not store", t.Name, c, want.Header.SchemaVersion)
			}
		}
		tag, err := tx.Conn().PgConn().CopyFrom(ctx, t.Data,
			"COPY "+qualified(t.Name)+" ("+columnList(t.Columns)+") FROM STDIN")
		if err != nil {
			return fmt.Errorf("load: copy %s: %w", t.Name, err)
		}
		loaded = append(loaded, TableCount{Name: t.Name, Rows: tag.RowsAffected()})
	}
	got := fr.Manifest()
	if !sameHeader(got.Header, want.Header) || !slices.Equal(got.Tables, want.Tables) || !slices.Equal(got.Sequences, want.Sequences) {
		return fmt.Errorf("%w: the backup changed between verifying and loading it", ErrCorrupt)
	}
	if !slices.Equal(loaded, want.Tables) {
		return fmt.Errorf("load: loaded %v rows, the manifest lists %v", loaded, want.Tables)
	}
	return nil
}

// reasonRestored is the error of an attempt a restore marks abandoned.
const reasonRestored = "this backup was still running when the restored backup was taken"

// abandonRunningAttempts marks backup attempts that were running when the
// backup was taken as abandoned: the backup that took the snapshot is one of
// them, and it is not running in the restored instance. Schema versions
// before backup_runs existed have nothing to mark.
func abandonRunningAttempts(ctx context.Context, tx pgx.Tx) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.backup_runs') IS NOT NULL`).Scan(&exists); err != nil {
		return fmt.Errorf("load: look for backup_runs: %w", err)
	}
	if !exists {
		return nil
	}
	// finished_at is set because backup_runs requires it of every attempt
	// that is no longer running.
	if _, err := tx.Exec(ctx, `UPDATE public.backup_runs SET state = 'abandoned', finished_at = now(), error = $1 WHERE state = 'running'`,
		reasonRestored); err != nil {
		return fmt.Errorf("load: abandon running backup attempts: %w", err)
	}
	return nil
}
