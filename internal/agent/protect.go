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
)

// protector inspects and restricts who can access a path.
type protector interface {
	// Loose reports who besides the allowed principals can access path; "" means none.
	Loose(path string) (string, error)
	// Protect makes path accessible to the allowed principals only.
	Protect(path string) error
}

// CheckEnvFile refuses an env file another user could read or change: it
// must be a regular file (not a directory, device or symlink) and p must
// report nobody beyond the allowed principals.
func CheckEnvFile(p protector, path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("env file %s: %w", path, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("env file %s is a symlink; point --env-file at the file itself", path)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("env file %s is not a regular file", path)
	}
	found, err := p.Loose(path)
	if err != nil {
		return fmt.Errorf("env file %s: check access: %w", path, err)
	}
	if found != "" {
		return fmt.Errorf("env file %s holds the enrollment token but is accessible to %s; restrict it", path, found)
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
// principals can reach it. A loose directory the agent may own — the
// default one, or one this call just created — is tightened and logged; a
// loose directory the operator chose and created is left alone, because a
// rewritten access list cannot be undone and the directory may be shared.
// Every failure is ErrSetupFailed: no manager retry fixes it.
func ensureDataDir(p protector, dir string, isDefault bool, log *slog.Logger) error {
	_, statErr := os.Stat(dir)
	created := errors.Is(statErr, fs.ErrNotExist)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: create data dir %s: %v", ErrSetupFailed, dir, err)
	}
	found, err := p.Loose(dir)
	if err != nil {
		return fmt.Errorf("%w: check data dir %s access: %v", ErrSetupFailed, dir, err)
	}
	if found == "" {
		return nil
	}
	if !isDefault && !created {
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
