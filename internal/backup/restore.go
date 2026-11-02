package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

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
)

// RestoreOptions controls Restore.
type RestoreOptions struct {
	// Replace allows restoring over a database that is not empty.
	Replace bool
	// BinarySchemaVersion is the newest migration this binary carries.
	BinarySchemaVersion int64

	// afterMigrate runs between MigrateTo and the load; tests only.
	afterMigrate func(*pgxpool.Pool) error
}

// Restore replaces d's public schema with the backup open yields.
//
// open is called twice: the first pass reads the whole file through Verify,
// so a truncated, tampered or wrong-key file is refused before anything is
// dropped; the second pass loads it. Every decision uses the authenticated
// manifest header, never the plaintext one.
//
// After the checks (Verify, not empty unless Replace, not newer than the
// binary) Restore drops public's objects, migrates up to the backup's schema
// version, loads every table in one transaction and then runs the remaining
// migrations, exactly as a manager start would. d must connect as the
// schema owner; no superuser right is needed.
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

	if err := wipe(ctx, d.Pool()); err != nil {
		return Manifest{}, fmt.Errorf("restore: %w", err)
	}
	if err := d.MigrateTo(ctx, m.Header.SchemaVersion); err != nil {
		return Manifest{}, fmt.Errorf("restore: %w", err)
	}
	if opts.afterMigrate != nil {
		if err := opts.afterMigrate(d.Pool()); err != nil {
			return Manifest{}, fmt.Errorf("restore: after migrate: %w", err)
		}
	}
	if err := load(ctx, d.Pool(), masterKey, open, m); err != nil {
		return Manifest{}, fmt.Errorf("restore: %w", err)
	}
	if err := d.Migrate(ctx); err != nil {
		return Manifest{}, fmt.Errorf("restore: %w", err)
	}
	return m, nil
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

	if err := copyTables(ctx, tx, fr, tables, want); err != nil {
		return err
	}

	for _, fk := range fks {
		if _, err := tx.Exec(ctx, "ALTER TABLE "+qualified(fk.table)+" ADD CONSTRAINT "+pgx.Identifier{fk.name}.Sanitize()+" "+fk.definition); err != nil {
			return fmt.Errorf("load: re-add foreign key %s on %s: %w", fk.name, fk.table, err)
		}
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
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("load: commit: %w", err)
	}
	return nil
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
	if _, err := tx.Exec(ctx, `UPDATE public.backup_runs SET state = 'abandoned' WHERE state = 'running'`); err != nil {
		return fmt.Errorf("load: abandon running backup attempts: %w", err)
	}
	return nil
}
