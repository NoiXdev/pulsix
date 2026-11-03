// The `pulsix-manager backup` and `backup key` commands, and what they share
// with `restore`: the database connection under the command's own
// application_name, the storage a --to or --from names, and sizes for
// people.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
)

// UsageError is an argument list a command cannot run with. The command
// line reports it with the usage text and exit code 2.
type UsageError string

func (e UsageError) Error() string { return string(e) }

// BackupOptions are `backup`'s inputs.
type BackupOptions struct {
	// To is a folder or s3://bucket/prefix; empty means the destination
	// the settings name.
	To             string
	WithoutMetrics bool
	// Getenv reads PULSIX_BACKUP_S3_* for an s3:// To; nil is os.Getenv.
	Getenv func(string) string
}

// keepKeyNote is printed after every backup: the master key is not in it.
const keepKeyNote = "Keep PULSIX_MASTER_KEY safe and apart from your backups: no backup can be restored without it.\n"

// RunBackup implements `backup`: one manual backup, with no actor, taken
// through backup.Service.Backup like the manager's, so it holds the same
// lock and is recorded the same way. It dumps through the schema owner's
// connection. It prints the object's name and size.
func RunBackup(ctx context.Context, cfg config.Config, opts BackupOptions, stdout io.Writer) error {
	d, err := openForCommand(ctx, cfg)
	if err != nil {
		return err
	}
	defer d.Close()
	store, err := commandStore(ctx, d)
	if err != nil {
		return err
	}
	svc := backup.NewService(store, crypto.NewBox(cfg.MasterKey), backup.HeaderKey(cfg.MasterKey), backupDump(d, cfg.MasterKey),
		backup.StorageFor(backup.DefaultDir), noAdminMailer{}, commandVersion(cfg), time.Now,
		// Errors reach the operator as the command's own error.
		slog.New(slog.DiscardHandler))

	var st backup.Storage
	if opts.To == "" {
		st, err = svc.Destination(ctx)
	} else {
		st, err = storageAt(opts.To, opts.Getenv)
	}
	if err != nil {
		return err
	}
	e, err := svc.Backup(ctx, backup.KindManual, nil, st, !opts.WithoutMetrics)
	if errors.Is(err, backup.ErrBusy) {
		return errors.New("another backup is running; try again once it has finished")
	}
	if err != nil {
		return fmt.Errorf("back up to %s: %w", st.Describe(), err)
	}
	_, err = fmt.Fprintf(stdout, "Backed up to %s: %s (%s)\n"+keepKeyNote, st.Describe(), e.Name, formatSize(e.Size))
	return err
}

// RunBackupKey implements `backup key`: the passphrase every backup of this
// instance is encrypted with, for decrypting one without Pulsix.
func RunBackupKey(cfg config.Config, stdout io.Writer) error {
	_, err := fmt.Fprintf(stdout, "%s\n%s\n", backup.DeriveKey(cfg.MasterKey),
		"This is the passphrase of every backup of this instance (keep it as secret as the master key); "+
			"the stock age tool decrypts a backup with it: tail -n +3 FILE.pxbak | age -d | gunzip")
	return err
}

// openForCommand opens the database as the schema owner, under the
// commands' own application_name so they never count as a running manager.
func openForCommand(ctx context.Context, cfg config.Config) (*db.DB, error) {
	url, err := db.WithApplicationName(cfg.DatabaseURL, cliApplicationName)
	if err != nil {
		return nil, err
	}
	d, err := db.Open(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return d, nil
}

// commandVersion is the Pulsix version written into a backup.
func commandVersion(cfg config.Config) string {
	if cfg.Version == "" {
		return "dev"
	}
	return cfg.Version
}

// storageAt opens the storage loc names: s3://bucket/prefix with
// credentials from getenv, or a folder.
func storageAt(loc string, getenv func(string) string) (backup.Storage, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.HasPrefix(loc, "s3://") {
		c, err := backup.S3ConfigFromEnv(getenv, loc)
		if err != nil {
			return nil, err
		}
		return backup.NewS3Storage(c)
	}
	return backup.NewDirStorage(loc), nil
}

// commandStore is the backup store over d. A database whose schema
// predates the attempt history — backed up with a newer image before the
// upgrade — gets a store that records nothing.
func commandStore(ctx context.Context, d *db.DB) (backup.Store, error) {
	var recorded bool
	if err := d.Pool().QueryRow(ctx, `SELECT to_regclass('public.backup_runs') IS NOT NULL`).Scan(&recorded); err != nil {
		return nil, fmt.Errorf("look for the backup history: %w", err)
	}
	store := backup.NewSQLStore(d)
	if !recorded {
		return unrecordedStore{store}, nil
	}
	return store, nil
}

// unrecordedStore keeps the lock and the settings of the store it wraps and
// records no attempts.
type unrecordedStore struct{ backup.Store }

func (unrecordedStore) AnyRunning(context.Context) (bool, error) { return false, nil }

func (unrecordedStore) AbandonRunning(context.Context, time.Time, string) (int64, error) {
	return 0, nil
}

func (unrecordedStore) SlotClaimed(context.Context, time.Time) (bool, error) { return false, nil }

func (unrecordedStore) InsertAttempt(context.Context, backup.NewAttempt) (uuid.UUID, bool, error) {
	return uuid.Nil, true, nil
}

func (unrecordedStore) FinishAttempt(context.Context, uuid.UUID, backup.Finish) error { return nil }

func (unrecordedStore) RecentAttempts(context.Context, int) ([]backup.Attempt, error) {
	return nil, nil
}

// noAdminMailer: a command reports its own failure; only scheduled backups
// mail the administrators.
type noAdminMailer struct{}

func (noAdminMailer) SendToInstanceAdmins(context.Context, string, string) error { return nil }

// formatSize renders n bytes with a binary unit: 512 B, 1.5 KiB, 3.2 GiB.
func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
