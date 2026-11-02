package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/config"
)

// The synopses the backup commands print with --help and on a usage error;
// usageText repeats them.
const (
	backupSynopsis    = "pulsix-manager backup [--to <dir>|s3://bucket/prefix] [--without-metrics]"
	backupKeySynopsis = "pulsix-manager backup key"
	restoreListSyn    = "pulsix-manager restore --list [--from <dir>|s3://bucket/prefix]"
	restoreSynopsis   = "pulsix-manager restore [--from <dir>|s3://bucket/prefix] [--replace] [--yes] <name>|-"
	s3Note            = "An s3:// location takes its credentials from PULSIX_BACKUP_S3_ACCESS_KEY and\n" +
		"PULSIX_BACKUP_S3_SECRET_KEY (and PULSIX_BACKUP_S3_ENDPOINT, _REGION, _PATH_STYLE).\n"
)

// Exit codes of the backup commands: a failure or refusal is 1, an
// argument list they cannot run with is 2.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// newCommandFlags is a flag set whose --help and errors print synopsis.
func newCommandFlags(name string, stderr io.Writer, synopsis ...string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		for _, s := range synopsis {
			_, _ = fmt.Fprintf(stderr, "usage: %s\n", s)
		}
		fs.PrintDefaults()
		_, _ = fmt.Fprint(stderr, s3Note)
	}
	return fs
}

// parseExit maps a flag parse error to an exit code: --help is 0, anything
// else a usage error. The flag package has printed the usage already.
func parseExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	return exitUsage
}

// usageFailure reports a usage error with the command's usage.
func usageFailure(fs *flag.FlagSet, stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "%s: %v\n", fs.Name(), err)
	fs.Usage()
	return exitUsage
}

// commandConfig loads the configuration the commands share with the
// manager, stamped with this binary's version.
func commandConfig(name string, stderr io.Writer) (config.Config, bool) {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: configuration error: %v\n", name, err)
		return config.Config{}, false
	}
	cfg.Version = version
	return cfg, true
}

// commandResult maps a command's error to its exit code.
func commandResult(fs *flag.FlagSet, stderr io.Writer, err error) int {
	var usage app.UsageError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &usage):
		return usageFailure(fs, stderr, err)
	default:
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", fs.Name(), err)
		return exitFailure
	}
}

// signalContext ends on SIGTERM or SIGINT, so an interrupted command stops
// at its next database round trip.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
}

// runBackup parses `backup` (and routes `backup key`) and takes a backup.
func runBackup(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "key" {
		return runBackupKey(args[1:], stdout, stderr)
	}
	fs := newCommandFlags("backup", stderr, backupSynopsis, backupKeySynopsis)
	to := fs.String("to", "", "a folder or s3://bucket/prefix to write the backup to (default: the destination set in the web interface)")
	withoutMetrics := fs.Bool("without-metrics", false, "leave the metric history out of the backup")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() > 0 {
		return usageFailure(fs, stderr, fmt.Errorf("unexpected arguments %v", fs.Args()))
	}
	cfg, ok := commandConfig("backup", stderr)
	if !ok {
		return exitFailure
	}
	ctx, stop := signalContext()
	defer stop()
	err := app.RunBackup(ctx, cfg, app.BackupOptions{To: *to, WithoutMetrics: *withoutMetrics}, stdout)
	return commandResult(fs, stderr, err)
}

// runBackupKey prints the backup passphrase.
func runBackupKey(args []string, stdout, stderr io.Writer) int {
	fs := newCommandFlags("backup key", stderr, backupKeySynopsis)
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() > 0 {
		return usageFailure(fs, stderr, fmt.Errorf("unexpected arguments %v", fs.Args()))
	}
	cfg, ok := commandConfig("backup key", stderr)
	if !ok {
		return exitFailure
	}
	return commandResult(fs, stderr, app.RunBackupKey(cfg, stdout))
}

// runRestore parses `restore` and lists or restores backups. Flags may
// also follow the backup's name.
func runRestore(args []string, stdout, stderr io.Writer) int {
	fs := newCommandFlags("restore", stderr, restoreListSyn, restoreSynopsis)
	list := fs.Bool("list", false, "list the backups instead of restoring one")
	from := fs.String("from", "", "the folder or s3://bucket/prefix that holds the backup (default: /var/lib/pulsix/backups)")
	replace := fs.Bool("replace", false, "replace a database that already has users or organizations")
	yes := fs.Bool("yes", false, "with --replace: do not ask for confirmation")
	names, err := parseInterspersed(fs, args)
	if err != nil {
		return parseExit(err)
	}
	if len(names) > 1 {
		return usageFailure(fs, stderr, fmt.Errorf("one backup at a time, not %v", names))
	}
	opts := app.RestoreOptions{List: *list, From: *from, Replace: *replace, Yes: *yes, Stdin: os.Stdin, StdinIsTerminal: stdinIsTerminal()}
	if len(names) == 1 {
		opts.Name = names[0]
	}
	if err := opts.Validate(); err != nil {
		return usageFailure(fs, stderr, err)
	}
	cfg, ok := commandConfig("restore", stderr)
	if !ok {
		return exitFailure
	}
	ctx, stop := signalContext()
	defer stop()
	return commandResult(fs, stderr, app.RunRestore(ctx, cfg, opts, stdout, stderr))
}

// parseInterspersed parses flags before and after positional arguments and
// returns the positional ones.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// stdinIsTerminal reports whether standard input is a terminal someone can
// answer a confirmation at.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
