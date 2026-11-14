//go:build windows

package script

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// osFS is the walk's file system on Windows. Every entry is opened as
// itself (FILE_FLAG_OPEN_REPARSE_POINT): a symlink or junction is never
// followed by an open, only by the walk, which reads its target.
type osFS struct{}

// split cleans path as Win32 does before any link is followed (a ".."
// takes the name before it back by its text) and splits it into its
// drive's root and names. Only a path on a drive is walked; one on a
// mapped network drive is refused before anything on it is opened.
func (osFS) split(path string) (string, []string, error) {
	clean := filepath.Clean(path)
	vol := filepath.VolumeName(clean)
	if len(vol) != 2 || vol[1] != ':' || len(clean) < 3 || !os.IsPathSeparator(clean[2]) {
		return "", nil, errors.New("not an absolute path on a drive")
	}
	root := vol + `\`
	r, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", nil, err
	}
	if windows.GetDriveType(r) == windows.DRIVE_REMOTE {
		return "", nil, refuse(textNetworkPath)
	}
	var names []string
	for _, name := range strings.Split(clean[3:], `\`) {
		if name != "" {
			names = append(names, name)
		}
	}
	return root, names, nil
}

func (osFS) join(root string, names []string) string { return root + strings.Join(names, `\`) }

// fileAttributeTagInfo is FILE_ATTRIBUTE_TAG_INFO, which x/sys/windows
// does not define.
type fileAttributeTagInfo struct {
	attributes uint32
	reparseTag uint32
}

func (osFS) lookup(p string) (entryKind, error) {
	h, err := openEntry(p, windows.FILE_READ_ATTRIBUTES)
	switch {
	case errors.Is(err, errNotFile) || (err != nil && namesNoFile(err)):
		return 0, errNotFile
	case err != nil:
		return 0, fmt.Errorf("open %s: %w", p, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	typ, err := windows.GetFileType(h)
	if err != nil {
		return 0, fmt.Errorf("read the type of %s: %w", p, err)
	}
	if typ != windows.FILE_TYPE_DISK {
		return entryOther, nil // a device behind a reserved name (CON, NUL)
	}
	var info fileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(h, windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, fmt.Errorf("read the attributes of %s: %w", p, err)
	}
	return entryKindOf(info.attributes, info.reparseTag)
}

// Reparse tags x/sys/windows does not define: a Unix socket's, and the bit
// that marks a tag whose target is another name.
const (
	ioReparseTagAFUnix      = 0x80000023
	reparseTagNameSurrogate = 0x20000000
)

// entryKindOf is what an entry with attrs and reparse tag is. A symlink
// and a junction (mount point) are links the walk follows; another tag
// that names another entry is one it cannot follow, so the path cannot be
// checked; any other tag (deduplicated or cloud files) leaves the entry
// what its attributes say.
func entryKindOf(attrs, tag uint32) (entryKind, error) {
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		switch {
		case tag == windows.IO_REPARSE_TAG_SYMLINK || tag == windows.IO_REPARSE_TAG_MOUNT_POINT:
			return entryLink, nil
		case tag == ioReparseTagAFUnix:
			return entryOther, nil
		case tag&reparseTagNameSurrogate != 0:
			return 0, fmt.Errorf("a reparse point of tag %#x, which the check does not follow", tag)
		}
	}
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return entryDir, nil
	}
	return entryFile, nil
}

// follow reads the link or junction at p, which lies in dir, and returns
// the cleaned path it leads to: a drive path as it is, one rooted without
// a drive on dir's drive, a relative one from dir. A target on the network
// is refused; one that names a volume or device (a junction a volume is
// mounted at) is not followed, so the path cannot be checked.
func (osFS) follow(p, dir string) (string, []string, error) {
	target, err := os.Readlink(p)
	if err != nil {
		return "", nil, err
	}
	switch windowsPathKindOf(target) {
	case pathLocal:
	case pathNetwork:
		return "", nil, refuse(textNetworkPath)
	default:
		return "", nil, errors.New("a link leads to a volume or device path, which the check does not follow")
	}
	var full string
	switch vol := filepath.VolumeName(target); {
	case vol != "" && !filepath.IsAbs(target):
		return "", nil, errors.New("a link leads to a path relative to a drive")
	case vol != "":
		full = target
	case target != "" && os.IsPathSeparator(target[0]):
		full = filepath.VolumeName(dir) + target
	default:
		full = filepath.Join(dir, target)
	}
	return osFS{}.split(full)
}

func (osFS) judgeDir(dir string, use dirUse, which string) error {
	return judgeEntry(dir, which+" ", dirMask(use))
}

// judgeLink judges the link or junction object itself with every write
// right: whoever may write it may rewrite where it leads.
func (osFS) judgeLink(p string) error { return judgeEntry(p, "a link on its way ", writeMask) }

func (osFS) judgeFile(p string) error { return judgeEntry(p, "", writeMask) }

// judgeEntry reads the security descriptor of the entry at p, not of what
// it leads to, and judges it against mask (judgeSecurity).
func judgeEntry(p, subject string, mask windows.ACCESS_MASK) error {
	trusted, err := trustedSIDs()
	if err != nil {
		return err
	}
	h, err := openEntry(p, windows.READ_CONTROL)
	if err != nil {
		return fmt.Errorf("open %s: %w", p, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read the security descriptor of %s: %w", p, err)
	}
	return judgeSecurity(sd, subject, mask, trusted)
}

// openEntry opens the entry at p itself, a link or junction included, a
// directory too, with access; a name no file can have is errNotFile.
func openEntry(p string, access uint32) (windows.Handle, error) {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return 0, errNotFile
	}
	return windows.CreateFile(u, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}
