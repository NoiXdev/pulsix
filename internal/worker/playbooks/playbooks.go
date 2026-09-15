// Package playbooks embeds the two system playbooks the worker's bootstrap
// and rotate-key executors run (spec §5, backlog 2.7c part 2): the account
// bootstrap and the key rotation. The YAML travels inside the worker
// binary, so a worker always runs the playbook shape its manager version
// paired with it — the claim carries no playbook for these kinds.
package playbooks

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
)

//go:embed *.yml
var files embed.FS

// Names of the embedded playbooks. The claim's kind decides which one
// runs; a job kind the worker has no playbook for is an executor error.
const (
	NameBootstrap = "pulsix.bootstrap.yml"
	NameRotateKey = "pulsix.rotate_key.yml"
)

// Get returns one embedded playbook's bytes. An unknown name is an error —
// the callers pass their own constants, so a failure is a programming
// error, never operator input.
func Get(name string) ([]byte, error) {
	raw, err := files.ReadFile(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no embedded playbook named %q", name)
		}
		return nil, fmt.Errorf("read embedded playbook %s: %w", name, err)
	}
	return raw, nil
}

// Files exposes the embedded file set for consumers that need the raw
// read; the Get helper with its own name constants is the normal entry.
var Files = files
