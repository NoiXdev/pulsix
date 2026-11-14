package script

import "strings"

// windowsPathKind is what a path that starts with two separators is to
// Windows, decided by its syntax alone: a stat of a network path reaches
// out to the host it names (and signs in there as the agent), so such
// paths are never stat'ed. The rules are plain string rules, testable on
// every platform; only the Windows checker uses them.
type windowsPathKind int

const (
	// pathLocal is every path that does not start with two separators.
	pathLocal windowsPathKind = iota
	// pathDevice is a device or namespace path (\\.\…, \\?\…): no file
	// a check judges, so an argument of this form is left alone.
	pathDevice
	// pathNoShare is \\x (or //x) without a share: no path at all, a
	// switch like cscript's //Nologo.
	pathNoShare
	// pathNetwork is a network (UNC) path, \\host\share\…: refused.
	pathNetwork
)

// windowsPathKindOf classifies p by its syntax, either separator counting.
func windowsPathKindOf(p string) windowsPathKind {
	if len(p) < 2 || !isWindowsSeparator(p[0]) || !isWindowsSeparator(p[1]) {
		return pathLocal
	}
	rest := p[2:]
	if rest == "." || rest == "?" || (len(rest) > 1 && (rest[0] == '.' || rest[0] == '?') && isWindowsSeparator(rest[1])) {
		return pathDevice
	}
	i := strings.IndexAny(rest, `\/`)
	if i < 0 || i == len(rest)-1 {
		return pathNoShare
	}
	return pathNetwork
}

func isWindowsSeparator(c byte) bool { return c == '\\' || c == '/' }
