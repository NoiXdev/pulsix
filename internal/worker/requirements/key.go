package requirements

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// fileData is a counted requirements file and the exact bytes galaxy is handed.
type fileData struct {
	path string // clone-relative slash path
	data []byte
}

// key names the cache entry of commit's install (spec goal 1):
// <commit>-<hash16>, where hash16 is the first 16 hex digits of a SHA-256 over
// the requirements root and, per counted file in goal 3 order, its path and
// its exact bytes. Each part is framed (quoted path, byte length), so no two
// different inputs hash the same bytes. Every worker computes the same key
// for the same inputs; changing the algorithm orphans every entry.
func key(commit, root string, files []fileData) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "root %q\n", root)
	for _, f := range files {
		_, _ = fmt.Fprintf(h, "file %q %d\n", f.path, len(f.data))
		h.Write(f.data)
	}
	return commit + "-" + hex.EncodeToString(h.Sum(nil))[:16]
}
