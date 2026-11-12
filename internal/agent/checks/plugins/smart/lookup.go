package smart

import (
	"errors"
	"slices"
)

var errNotFound = errors.New("smartctl not found; install smartmontools")

// smartctlCandidates lists the standard install locations, in the order
// they are tried after the agent's PATH: the distribution packages
// (/usr/sbin), a source build (/usr/local/sbin), Homebrew on Apple silicon
// and Intel Macs, and the Windows installer's directory.
func smartctlCandidates(goos string, getenv func(string) string) []string {
	if goos == "windows" {
		const rel = `\smartmontools\bin\smartctl.exe`
		out := []string{`C:\Program Files` + rel}
		if pf := getenv("ProgramFiles"); pf != "" && pf+rel != out[0] {
			out = slices.Insert(out, 0, pf+rel)
		}
		return out
	}
	return []string{
		"/usr/sbin/smartctl",
		"/usr/local/sbin/smartctl",
		"/opt/homebrew/bin/smartctl",
		"/opt/homebrew/sbin/smartctl",
		"/usr/local/bin/smartctl",
	}
}

// findSmartctl returns the smartctl to run: the one on PATH, else the first
// candidate that is an executable file. lookPath is exec.LookPath, which
// for an absolute candidate only checks that file.
func findSmartctl(lookPath func(string) (string, error), candidates []string) (string, error) {
	for _, name := range append([]string{"smartctl"}, candidates...) {
		if p, err := lookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errNotFound
}
