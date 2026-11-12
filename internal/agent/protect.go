// Access protection for the two places that hold the agent's secrets: the
// env file (the enrollment token) and the data directory (the private
// key). The platform rules live behind protector — protect_unix.go and
// protect_windows.go — so the decisions here stay testable with a fake.
package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// protector inspects and restricts who can access a path.
type protector interface {
	// Loose reports who besides the allowed principals can access path; "" means none.
	Loose(path string) (string, error)
	// ForeignOwner reports path's owner when it is not an allowed
	// principal (or that path is a link, whose owner it does not
	// inspect); "" means the owner is allowed.
	ForeignOwner(path string) (string, error)
	// Protect makes path accessible to the allowed principals only.
	Protect(path string) error
	// checksEntries reports whether every entry directly inside the data
	// directory must pass LooseEntry too (Windows: files could have been
	// planted before the directory was protected). Unix never checks them.
	checksEntries() bool
	// LooseEntry is Loose for an entry inside the protected data
	// directory: inheriting that directory's list is fine.
	LooseEntry(path string) (string, error)
}

// privateFile names a file the agent's file rule protects, for the
// rule's messages: what it is, what it holds and the flag that names it.
type privateFile struct{ kind, holds, flag string }

var (
	envFile    = privateFile{kind: "env file", holds: "the enrollment token", flag: "--env-file"}
	configFile = privateFile{kind: "configuration file", holds: "secrets", flag: "--config"}
)

// CheckEnvFile refuses an env file another user could read or change: it
// must be a regular file (not a directory, device or symlink) and p must
// report nobody beyond the allowed principals.
func CheckEnvFile(p protector, path string) error { return checkPrivateFile(p, path, envFile) }

// CheckConfigFile applies CheckEnvFile's rule, with this platform's
// protector, to the configuration file that holds the agent's secrets
// (agent.yaml). Its error text is reported to the manager as the file's
// state, so it names the path and the finding only.
func CheckConfigFile(path string) error { return checkConfigFile(newProtector(), path) }

func checkConfigFile(p protector, path string) error { return checkPrivateFile(p, path, configFile) }

// checkPrivateFile is the rule behind CheckEnvFile and CheckConfigFile.
func checkPrivateFile(p protector, path string, f privateFile) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%s %s: %w", f.kind, path, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s %s is a symlink; point %s at the file itself", f.kind, path, f.flag)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s %s is not a regular file", f.kind, path)
	}
	found, err := p.Loose(path)
	if err != nil {
		return fmt.Errorf("%s %s: check access: %w", f.kind, path, err)
	}
	if found != "" {
		return fmt.Errorf("%s %s holds %s but is accessible to %s; restrict it", f.kind, path, f.holds, found)
	}
	return nil
}

// LoadEnvFile checks the env file's access (CheckEnvFile) and parses it
// (ParseEnvFile).
func LoadEnvFile(path string) (map[string]string, error) {
	if err := CheckEnvFile(newProtector(), path); err != nil {
		return nil, err
	}
	f, err := os.Open(path) // #nosec G304 -- the operator names the env file
	if err != nil {
		return nil, fmt.Errorf("open env file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	vals, err := ParseEnvFile(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return vals, nil
}

// ensureDataDir creates the data directory and makes sure only the allowed
// principals can reach it. Run calls it before it reads anything there.
//
//   - A directory owned by anyone else is refused, whether it already
//     existed or appeared under a race: its owner can reopen it with
//     WRITE_DAC at any time, and whatever it planted inside would be kept.
//   - A loose directory the agent may own — the default one, or one this
//     call just created — is tightened and logged.
//   - A loose directory the operator chose and created is left alone,
//     because a rewritten access list cannot be undone and the directory
//     may be shared.
//   - Where the protector checks entries (Windows), every entry directly
//     inside must pass as well: until the directory was protected anyone
//     could add files to it, and a planted key or CA would be trusted.
//
// Every failure is ErrSetupFailed: no manager retry fixes it.
func ensureDataDir(p protector, dir string, isDefault bool, log *slog.Logger) error {
	_, statErr := os.Stat(dir)
	created := errors.Is(statErr, fs.ErrNotExist)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: create data dir %s: %v", ErrSetupFailed, dir, err)
	}
	owner, err := p.ForeignOwner(dir)
	if err != nil {
		return fmt.Errorf("%w: check data dir %s owner: %v", ErrSetupFailed, dir, err)
	}
	if owner != "" {
		return fmt.Errorf("%w: data directory %s is owned by %s; remove it (or have it owned by Administrators) and start the agent again", ErrSetupFailed, dir, owner)
	}
	if err := restrictDataDir(p, dir, isDefault || created, log); err != nil {
		return err
	}
	if p.checksEntries() {
		return checkDataDirEntries(p, dir)
	}
	return nil
}

// restrictDataDir tightens a loose directory when mayProtect, and refuses
// it otherwise.
func restrictDataDir(p protector, dir string, mayProtect bool, log *slog.Logger) error {
	found, err := p.Loose(dir)
	if err != nil {
		return fmt.Errorf("%w: check data dir %s access: %v", ErrSetupFailed, dir, err)
	}
	if found == "" {
		return nil
	}
	if !mayProtect {
		return fmt.Errorf("%w: data directory %s is accessible to %s; restrict it or unset PULSIX_DATA_DIR", ErrSetupFailed, dir, found)
	}
	if err := p.Protect(dir); err != nil {
		return fmt.Errorf("%w: restrict data dir %s (accessible to %s): %v", ErrSetupFailed, dir, found, err)
	}
	still, err := p.Loose(dir)
	if err != nil {
		return fmt.Errorf("%w: check data dir %s access: %v", ErrSetupFailed, dir, err)
	}
	if still != "" {
		return fmt.Errorf("%w: data directory %s is still accessible to %s after restricting it", ErrSetupFailed, dir, still)
	}
	log.Info("tightened the data directory's access", "dir", dir, "found", found)
	return nil
}

// checkDataDirEntries refuses the first entry directly inside dir (a file
// or a subdirectory; not recursive) with a foreign owner or a loose list.
func checkDataDirEntries(p protector, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("%w: list data dir %s: %v", ErrSetupFailed, dir, err)
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		found, err := p.ForeignOwner(path)
		if err == nil && found == "" {
			found, err = p.LooseEntry(path)
		}
		if err != nil {
			return fmt.Errorf("%w: check %s in the data directory: %v", ErrSetupFailed, path, err)
		}
		if found != "" {
			return fmt.Errorf("%w: data directory %s holds %s, which is not the agent's own (%s); remove it and start the agent again", ErrSetupFailed, dir, path, found)
		}
	}
	return nil
}
