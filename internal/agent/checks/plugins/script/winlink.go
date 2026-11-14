package script

import (
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf16"
)

// The reparse data the Windows walk reads (FSCTL_GET_REPARSE_POINT), kept
// as plain byte and string rules so they are tested on every platform.
const (
	reparseTagSymlink    = 0xA000000C // IO_REPARSE_TAG_SYMLINK
	reparseTagMountPoint = 0xA0000003 // IO_REPARSE_TAG_MOUNT_POINT, a junction
	symlinkFlagRelative  = 0x1        // SYMLINK_FLAG_RELATIVE
)

var errBadReparse = errors.New("a link's reparse data cannot be read")

// parseReparse reads a REPARSE_DATA_BUFFER: the substitute name of a
// symlink or junction (the name Windows follows, not the print name it
// shows) and whether a symlink's target is relative. Any other tag, or a
// name outside the buffer, is an error.
func parseReparse(b []byte) (target string, relative bool, err error) {
	if len(b) < 8 {
		return "", false, errBadReparse
	}
	var names []byte
	var off, n uint16
	switch tag := binary.LittleEndian.Uint32(b); tag {
	case reparseTagSymlink:
		if len(b) < 20 {
			return "", false, errBadReparse
		}
		relative = binary.LittleEndian.Uint32(b[16:])&symlinkFlagRelative != 0
		names = b[20:]
	case reparseTagMountPoint:
		if len(b) < 16 {
			return "", false, errBadReparse
		}
		names = b[16:]
	default:
		return "", false, errBadReparse
	}
	off, n = binary.LittleEndian.Uint16(b[8:]), binary.LittleEndian.Uint16(b[10:])
	if int(off)+int(n) > len(names) || n%2 != 0 {
		return "", false, errBadReparse
	}
	u := make([]uint16, n/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(names[int(off)+2*i:])
	}
	return string(utf16.Decode(u)), relative, nil
}

// windowsLinkPath is where a link in dir (a drive path) whose substitute
// name is target leads, not yet cleaned. A relative symlink goes on from
// dir, or from dir's drive's root when it starts with a separator; an
// absolute symlink or a junction must name a drive path in the NT form
// \??\X:\…. Anything else (a volume, the network, a drive-relative path,
// a stream) is not followed: the path cannot be checked.
func windowsLinkPath(dir, target string, relative bool) (string, error) {
	if relative {
		switch {
		case strings.Contains(target, ":"), strings.HasPrefix(target, `\\`):
			return "", errors.New("a link leads to a path the check does not follow")
		case strings.HasPrefix(target, `\`):
			return dir[:2] + target, nil
		}
		return dir + `\` + target, nil
	}
	rest, ok := strings.CutPrefix(target, `\??\`)
	if !ok || len(rest) < 3 || rest[1] != ':' || rest[2] != '\\' ||
		(rest[0]|0x20 < 'a' || rest[0]|0x20 > 'z') {
		return "", errors.New("a link leads to a path the check does not follow")
	}
	return rest, nil
}
