package secrets

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// FileName is the configuration file's name inside the agent's data
// directory.
const FileName = "agent.yaml"

// ErrAbsent is Load's error for a file that does not exist: the agent then
// simply has no secrets and no commands.
var ErrAbsent = errors.New("the configuration file does not exist")

// Check refuses a file that someone other than the allowed principals could
// read or change, or that is not a regular file. The agent passes its
// env-file rule (agent.CheckConfigFile); its error text is the file's state.
type Check func(path string) error

// maxAttempts bounds how often Load starts over when the file is replaced
// while it reads it.
const maxAttempts = 3

// errReplaced marks an attempt that saw the file replaced under it.
var errReplaced = errors.New("replaced while it was read")

// Load reads the secrets and commands in path. The file must pass check first, every
// time: an editor that saves by renaming a temporary file over it can
// leave a moment where the file is readable by everyone, and such a file
// is refused rather than read. Load holds the file open while it checks
// it and makes sure the path still names that same file afterwards, so the
// content it parses is the content that passed. A missing file is
// ErrAbsent. No error ever quotes the file's content.
func Load(path string, check Check) (Set, error) {
	for range maxAttempts {
		set, err := loadOnce(path, check)
		if !errors.Is(err, errReplaced) {
			return set, err
		}
	}
	return Set{}, fmt.Errorf("%s was replaced while it was read; it is read again at the next check", path)
}

func loadOnce(path string, check Check) (Set, error) {
	before, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Set{}, ErrAbsent
	}
	if err != nil || !before.Mode().IsRegular() {
		// The check names what is wrong (a link, a directory, an
		// unreadable path) in the agent's own words.
		if cerr := check(path); cerr != nil {
			return Set{}, cerr
		}
		if err != nil {
			return Set{}, fmt.Errorf("read %s: %w", path, err)
		}
		return Set{}, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := openFile(path)
	if err != nil {
		return Set{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return Set{}, fmt.Errorf("read %s: %w", path, err)
	}
	if !os.SameFile(before, opened) {
		return Set{}, errReplaced
	}
	if err := check(path); err != nil {
		return Set{}, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) {
		return Set{}, errReplaced
	}
	if opened.Size() > maxFileBytes {
		return Set{}, fmt.Errorf("%s is larger than 1 MiB", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return Set{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxFileBytes {
		return Set{}, fmt.Errorf("%s is larger than 1 MiB", path)
	}
	set, err := parse(data)
	if err != nil {
		return Set{}, fmt.Errorf("%s: %w", path, err)
	}
	return set, nil
}
