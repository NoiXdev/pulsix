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
	// pathDevice is a device or namespace path (\\.\…, \\?\…) that
	// names no drive's or share's file (a pipe, a disk, \\.\C: the
	// volume): no file a check judges, so an argument of this form is
	// left alone.
	pathDevice
	// pathDeviceFile is a file path in device form: \\?\ or \\.\ ahead
	// of a drive (C:\…), UNC\…, a volume (Volume{…}\…) or GLOBALROOT\…:
	// refused, never stat'ed.
	pathDeviceFile
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
	if rest == "." || rest == "?" {
		return pathDevice
	}
	if len(rest) > 1 && (rest[0] == '.' || rest[0] == '?') && isWindowsSeparator(rest[1]) {
		if inDeviceFileForm(rest[2:]) {
			return pathDeviceFile
		}
		return pathDevice
	}
	i := strings.IndexAny(rest, `\/`)
	if i < 0 || i == len(rest)-1 {
		return pathNoShare
	}
	return pathNetwork
}

// inDeviceFileForm reports whether what follows a \\?\ or \\.\ prefix
// names a file: a drive with a root, UNC, a volume or GLOBALROOT.
func inDeviceFileForm(after string) bool {
	if len(after) > 2 && after[1] == ':' && isWindowsSeparator(after[2]) &&
		(after[0]|0x20 >= 'a' && after[0]|0x20 <= 'z') {
		return true
	}
	first := after
	if i := strings.IndexAny(after, `\/`); i >= 0 {
		first = after[:i]
	}
	return strings.EqualFold(first, "UNC") || strings.EqualFold(first, "GLOBALROOT") ||
		(len(first) >= 7 && strings.EqualFold(first[:7], "Volume{"))
}

func isWindowsSeparator(c byte) bool { return c == '\\' || c == '/' }
