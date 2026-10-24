// Atomic file writes for the agent's identity: content lands in a temp
// file in the destination directory, is flushed and renamed over the
// target, so a reader (or a crash) never sees a torn identity file.
package agent

import (
	"os"

	"github.com/noixdev/pulsix/internal/atomicfile"
)

// writeFileAtomic is atomicfile.Write with the identity directory
// created first, owner-only. The directory mode is the agent's own
// business, which is why it is here and not in the shared helper
// (backlog 2.64): these files are a private key and a certificate.
func writeFileAtomic(dir, name string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(dir, name, content, mode)
}
