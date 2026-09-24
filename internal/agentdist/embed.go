// Package agentdist embeds the agent distribution the manager serves:
// the cross-compiled agent binaries (`make agent-binaries`, gitignored),
// the install script template and the systemd unit — canonical sources
// live in deploy/ and are guarded against drift by a test here.
//
// A plain dev build (`go build`/`go test` without make agent-binaries)
// embeds only the committed placeholder: the binary routes answer 503
// with "run make agent-binaries" (see internal/api's install routes) and
// everything else keeps working.
package agentdist

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed all:dist
//go:embed all:files
var embedded embed.FS

// distRoot is the embed path the binaries live under; Files strips it.
const distRoot = "dist"

// placeholderName is the only file a plain dev build has in the dist:
// without a real binary, Files returns nil (the routes' 503 fallback).
const placeholderName = ".placeholder"

// BinaryPrefix is what every agent binary and checksum file in the dist
// is named: pulsix-agent-<os>-<arch>[.sha256]. The manager's binary route
// builds the name it looks up from it (internal/api/agent_install.go) and
// scripts/agent-binaries.sh writes it — one constant, three users.
const BinaryPrefix = "pulsix-agent-"

// ChecksumSuffix is what the checksum file beside each binary is named;
// the install script fetches <binary>.sha256 and verifies against it.
const ChecksumSuffix = ".sha256"

// InstallScript returns the raw install script TEMPLATE (the manager's
// route executes it with the base URL and the unit).
func InstallScript() []byte { return mustRead("files/install.sh") }

// Unit returns the systemd unit text embedded into the install script.
func Unit() []byte { return mustRead("files/pulsix-agent.service") }

// mustRead reads one embedded file. The embed guarantees both files
// exist, so a failure here is a build wiring bug — a panic at startup,
// not an empty script served to a host that pipes it into a shell.
func mustRead(name string) []byte {
	b, err := embedded.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return b
}

// Files returns the embedded distribution directory rooted at the
// serving root (so Files().Open("pulsix-agent-linux-amd64") works), or
// nil when the dist carries no binaries — a plain dev build — which the
// API routes turn into the documented 503 fallback.
func Files() fs.FS { return distFS(embedded) }

// distFS is Files' logic over any filesystem, so both build states — a
// dist with binaries and the placeholder-only one — can be tested from
// either build. A missing dist directory is a build wiring bug (the
// embed declares it), hence the panic.
func distFS(fsys fs.FS) fs.FS {
	entries, err := fs.ReadDir(fsys, distRoot)
	if err != nil {
		panic(err)
	}
	hasBinary := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// A checksum without its binary is not something to serve: the
		// install script fetches the binary first and would fail on it.
		if strings.HasPrefix(e.Name(), BinaryPrefix) && !strings.HasSuffix(e.Name(), ChecksumSuffix) {
			hasBinary = true
		}
	}
	if !hasBinary {
		return nil
	}
	sub, err := fs.Sub(fsys, distRoot)
	if err != nil {
		panic(err)
	}
	return sub
}
