// The `pulsix-manager restore` command: list the backups of a folder or a
// bucket, or put one back into the database.
package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/db"
)

// StdinName is the backup name that reads the backup from standard input.
const StdinName = "-"

// RestoreOptions are `restore`'s inputs.
type RestoreOptions struct {
	// From is a folder or s3://bucket/prefix; empty means backup.DefaultDir.
	From string
	// List lists the backups in From instead of restoring one.
	List bool
	// Name is the backup to restore, or StdinName.
	Name string
	// Replace allows replacing a database that is not empty; Yes confirms
	// it without asking.
	Replace, Yes bool
	// Stdin carries the backup for StdinName, and otherwise the answer to
	// the confirmation, which is only asked when StdinIsTerminal.
	Stdin           io.Reader
	StdinIsTerminal bool
	// Getenv reads PULSIX_BACKUP_S3_* for an s3:// From; nil is os.Getenv.
	Getenv func(string) string
}

// Validate refuses argument lists restore cannot run with, before anything
// is opened.
func (o RestoreOptions) Validate() error {
	switch {
	case o.List && (o.Name != "" || o.Replace || o.Yes):
		return UsageError("--list takes no backup name, --replace or --yes")
	case !o.List && o.Name == "":
		return UsageError("name the backup to restore (see restore --list), or - for standard input")
	case o.Yes && !o.Replace:
		return UsageError("--yes only confirms --replace")
	case o.Name == StdinName && o.From != "":
		return UsageError("--from and - (standard input) exclude each other")
	case o.Name == StdinName && o.StdinIsTerminal:
		return UsageError("- reads the backup from standard input, which is a terminal here: pipe the file in (docker compose run --rm -T cli restore - < FILE.pxbak)")
	case o.Name == StdinName && o.Replace && !o.Yes:
		return UsageError("standard input carries the backup and cannot answer the confirmation: --replace needs --yes")
	}
	return nil
}

// restoreNextStep closes a successful restore. The manager accepts only
// certificate serials the database knows, so an agent or worker that
// renewed its certificate after the backup was taken is locked out until
// it is enrolled again (final review I1; backlog 7.47).
const restoreNextStep = "Start the stack. Workers and agents reconnect on their own, unless they renewed their certificate\n" +
	"after this backup was taken (certificates renew about every 60 days): those show as offline on the Agents\n" +
	"and Workers pages, their logs say to enroll anew, and they must be enrolled again. The bundled compose\n" +
	"worker re-enrolls itself.\n"

// RunRestore implements `restore`. It refuses while a manager is connected
// to the database and, on a database that is not empty, without --replace
// and its confirmation. Standard input is copied to a temporary file
// first, because a restore reads the backup twice: once to verify all of
// it before anything is dropped, once to load it.
func RunRestore(ctx context.Context, cfg config.Config, opts RestoreOptions, stdout, stderr io.Writer) error {
	if err := opts.Validate(); err != nil {
		return err
	}
	from := opts.From
	if from == "" {
		from = backup.DefaultDir
	}
	if opts.List {
		st, err := storageAt(from, opts.Getenv)
		if err != nil {
			return err
		}
		return listBackups(ctx, st, stdout)
	}

	d, err := openForCommand(ctx, cfg)
	if err != nil {
		return err
	}
	defer d.Close()
	if n, err := ManagersConnected(ctx, d.Pool()); err != nil {
		return err
	} else if n > 0 {
		return errors.New("a manager is connected to this database: stop the manager first (docker compose down stops the stack and keeps its volumes), then run restore again")
	}
	empty, err := backup.IsEmpty(ctx, d.Pool())
	if err != nil {
		return err
	}
	replace, err := confirmReplace(opts, empty, stderr)
	if err != nil {
		return err
	}

	label, open, cleanup, err := openSource(ctx, opts, from)
	if err != nil {
		return err
	}
	defer cleanup()
	latest, err := db.LatestVersion()
	if err != nil {
		return err
	}
	m, err := backup.Restore(ctx, d, cfg.MasterKey, open, backup.RestoreOptions{Replace: replace, BinarySchemaVersion: latest})
	if err != nil {
		return explainRestore(err, label, from, m)
	}
	return printRestored(stdout, label, m)
}

// errNotEmpty is the refusal for a database with users or organizations.
var errNotEmpty = errors.New("the database is not empty (it has users or organizations); run restore with --replace to replace it")

// confirmReplace decides the Replace the restore runs with. A database that
// is not empty is only replaced with --replace and either --yes or the word
// restore typed at a terminal.
func confirmReplace(opts RestoreOptions, empty bool, stderr io.Writer) (bool, error) {
	switch {
	case empty || (opts.Replace && opts.Yes):
		return opts.Replace, nil
	case !opts.Replace:
		return false, errNotEmpty
	case !opts.StdinIsTerminal || opts.Stdin == nil:
		return false, errors.New("the database is not empty and no terminal is there to confirm replacing it: add --yes")
	}
	_, _ = fmt.Fprint(stderr, "This replaces everything in the database (every organization, user, host and setting) with the backup.\n"+
		"Type restore to continue: ")
	line, err := bufio.NewReader(opts.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read the confirmation: %w", err)
	}
	if line != "restore\n" && line != "restore" && line != "restore\r\n" {
		return false, errors.New("not confirmed; the database was not changed")
	}
	return true, nil
}

// openSource returns how to read the backup, a label for it, and what to
// clean up afterwards. Standard input is spooled to a temporary file.
func openSource(ctx context.Context, opts RestoreOptions, from string) (label string, open func() (io.ReadCloser, error), cleanup func(), err error) {
	if opts.Name != StdinName {
		st, err := storageAt(from, opts.Getenv)
		if err != nil {
			return "", nil, nil, err
		}
		open = func() (io.ReadCloser, error) { return st.Open(ctx, opts.Name) }
		return opts.Name, open, func() {}, nil
	}
	path, err := spool(opts.Stdin)
	if err != nil {
		return "", nil, nil, err
	}
	open = func() (io.ReadCloser, error) { return os.Open(path) } //nolint:gosec // our own temporary file
	return "the backup from standard input", open, func() { _ = os.Remove(path) }, nil
}

// spool copies r into a new file in os.TempDir and returns its path.
func spool(r io.Reader) (path string, err error) {
	if r == nil {
		return "", errors.New("no standard input to read the backup from")
	}
	f, err := os.CreateTemp("", "pulsix-restore-*.pxbak")
	if err != nil {
		return "", fmt.Errorf("spool standard input: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("spool standard input: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("spool standard input: %w", err)
	}
	return f.Name(), nil
}

// explainedError is an error with the message an operator needs, which
// still unwraps to its cause.
type explainedError struct {
	msg   string
	cause error
}

func (e explainedError) Error() string { return e.msg }
func (e explainedError) Unwrap() error { return e.cause }

// unchanged ends every message of a refusal before the wipe.
const unchanged = "; the database was not changed"

// explainRestore turns a restore failure into what the operator reads.
func explainRestore(err error, label, from string, m backup.Manifest) error {
	var msg string
	switch {
	case errors.Is(err, backup.ErrNotMigrated):
		msg = fmt.Sprintf("the backup's data is restored, but migrating it from schema version %d to this Pulsix failed: %s. "+
			"The manager retries the migrations when it starts; to start over, run the restore again with --replace",
			m.Header.SchemaVersion, detail(err, backup.ErrRestoreIncomplete, backup.ErrNotMigrated))
	case errors.Is(err, backup.ErrRestoreIncomplete):
		msg = fmt.Sprintf("the restore failed after the database was cleared: %s. "+
			"The database is now empty, at most at the backup's schema version %d; run the same restore command again",
			detail(err, backup.ErrRestoreIncomplete), m.Header.SchemaVersion)
	case errors.Is(err, backup.ErrWrongKey):
		msg = "the master key does not match this backup (or the file's encryption header is damaged)" + unchanged
	case errors.Is(err, backup.ErrCorrupt):
		msg = fmt.Sprintf("%s is damaged or incomplete (%v)%s", label, err, unchanged)
	case errors.Is(err, backup.ErrNotABackup):
		msg = label + " is not a Pulsix backup file" + unchanged
	case errors.Is(err, backup.ErrObjectNotFound):
		msg = fmt.Sprintf("there is no backup named %q in %s; see restore --list --from %s", label, from, from)
	case errors.Is(err, backup.ErrNotEmpty):
		return errNotEmpty
	case errors.Is(err, backup.ErrBackupTooNew):
		msg = err.Error() + unchanged
	default:
		return err
	}
	return explainedError{msg: msg, cause: err}
}

// detail is err's text without Restore's "restore: " and the sentinels'
// own texts in front, which the explanation already says in other words.
func detail(err error, sentinels ...error) string {
	text := strings.TrimPrefix(err.Error(), "restore: ")
	for _, s := range sentinels {
		text = strings.TrimPrefix(text, s.Error()+": ")
	}
	return text
}

// printRestored reports a finished restore: what the backup was, how many
// rows each table got, and what to do next.
func printRestored(stdout io.Writer, label string, m backup.Manifest) error {
	h := m.Header
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Restored %s: a %s backup taken %s UTC by Pulsix %s (schema version %d).\n",
		label, h.Kind, h.CreatedAt.UTC().Format("2006-01-02 15:04:05"), h.PulsixVersion, h.SchemaVersion)
	if !h.IncludesMetrics {
		_, _ = fmt.Fprintln(tw, "It holds no metric history.")
	}
	_, _ = fmt.Fprintln(tw, "Rows per table:")
	for _, t := range m.Tables {
		_, _ = fmt.Fprintf(tw, "  %s\t%d\n", t.Name, t.Rows)
	}
	_, _ = fmt.Fprint(tw, restoreNextStep)
	return tw.Flush()
}

// listBackups prints the backups in st, newest first.
func listBackups(ctx context.Context, st backup.Storage, stdout io.Writer) error {
	objs, err := st.List(ctx)
	if err != nil {
		return fmt.Errorf("list %s: %w", st.Describe(), err)
	}
	if len(objs) == 0 {
		_, err := fmt.Fprintf(stdout, "No backups in %s.\n", st.Describe())
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tCREATED (UTC)\tKIND\tVERSION\tSIZE\tMETRICS")
	for _, o := range objs {
		created, kind, version, metrics := "-", "-", "damaged", "-"
		if o.HeaderErr == nil {
			created, kind, version = o.Header.CreatedAt.UTC().Format("2006-01-02 15:04:05"), o.Header.Kind, o.Header.PulsixVersion
			metrics = "no"
			if o.Header.IncludesMetrics {
				metrics = "yes"
			}
		} else if at, k, ok := backup.ParseObjectName(o.Name); ok {
			created, kind = at.Format("2006-01-02 15:04:05"), k
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", o.Name, created, kind, version, formatSize(o.Size), metrics)
	}
	return tw.Flush()
}
