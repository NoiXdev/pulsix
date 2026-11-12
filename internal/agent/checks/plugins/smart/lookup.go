package smart

import (
	"errors"
	"strings"
)

var errNotFound = errors.New("smartctl not found; install smartmontools")

// locator finds the smartctl binary. The agent runs it as root or SYSTEM,
// so the standard install locations — absolute paths only root can write —
// are tried before the agent's PATH, which is only the fallback.
type locator struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) (string, error) // exec.LookPath
	isAbs    func(string) bool            // filepath.IsAbs
}

// candidates lists the standard install locations in the order they are
// tried: the distribution packages (/usr/sbin), a source build
// (/usr/local/sbin), Homebrew on Apple silicon and Intel Macs, and on
// Windows the installer's directory under %ProgramFiles%.
func (l locator) candidates() []string {
	if l.goos == "windows" {
		pf := l.getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		return []string{strings.TrimRight(pf, `\`) + `\smartmontools\bin\smartctl.exe`}
	}
	return []string{
		"/usr/sbin/smartctl",
		"/usr/local/sbin/smartctl",
		"/opt/homebrew/sbin/smartctl",
		"/opt/homebrew/bin/smartctl",
		"/usr/local/bin/smartctl",
	}
}

// find returns the smartctl to run: the first standard location that holds
// an executable, else the one on PATH. lookPath checks an absolute
// candidate in place. Whatever is found must be an absolute path and, on
// Windows, an .exe — never a .bat, .cmd or .com that PATHEXT would also
// accept, which would run through the command interpreter.
func (l locator) find() (string, error) {
	for _, name := range append(l.candidates(), "smartctl") {
		if p, err := l.lookPath(name); err == nil && l.acceptable(p) {
			return p, nil
		}
	}
	return "", errNotFound
}

func (l locator) acceptable(path string) bool {
	if !l.isAbs(path) {
		return false
	}
	return l.goos != "windows" || strings.HasSuffix(strings.ToLower(path), ".exe")
}
