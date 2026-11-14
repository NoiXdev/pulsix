//go:build windows

package script

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// osFS is the walk's file system on Windows. Every entry is opened as
// itself (FILE_FLAG_OPEN_REPARSE_POINT): a symlink or junction is never
// followed by an open, only by the walk, which reads its reparse data.
// Opens go through the \\?\ form, so neither the MAX_PATH limit nor
// Win32's renaming (a trailing dot dropped, CON made a device) changes
// the name looked up.
type osFS struct{}

// split cleans path as Win32 does before any link is followed (a ".."
// takes the name before it back by its text) and splits it into its
// drive's root and names. Only a path on a drive is walked; one on a
// mapped network drive is refused before anything on it is opened, and so
// is a name ending in a dot or a space, which Win32 would open by another
// name than the one checked.
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
		if name == "" {
			continue
		}
		if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
			return "", nil, refuse(textTrailingDot)
		}
		names = append(names, name)
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

// lookup says what the entry at p is. A name with a colon is an alternate
// data stream of the entry its part before the colon names (C:\T:30 is
// stream 30 of C:\T): missing when that entry is, else a file, judged by
// that entry (judgeFile), since a stream has no access list of its own.
func (osFS) lookup(p string) (entryKind, error) {
	if host, ok := streamHost(p); ok {
		if strings.HasSuffix(host, `\`) {
			return 0, errors.New("a stream of a directory the path names")
		}
		kind, err := lookupEntry(host)
		switch {
		case err != nil:
			return 0, err
		case kind == entryLink:
			return 0, errors.New("a stream of a link")
		}
		return entryFile, nil
	}
	return lookupEntry(p)
}

// streamHost is the path of the entry a stream name at p belongs to.
func streamHost(p string) (string, bool) {
	dir, name := p[:strings.LastIndexByte(p, '\\')+1], p[strings.LastIndexByte(p, '\\')+1:]
	i := strings.IndexByte(name, ':')
	if i < 0 {
		return "", false
	}
	return dir + name[:i], true
}

func lookupEntry(p string) (entryKind, error) {
	h, err := openEntry(p, windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return 0, winLookupError(p, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	typ, err := windows.GetFileType(h)
	if err != nil {
		return 0, fmt.Errorf("read the type of %s: %w", p, err)
	}
	if typ != windows.FILE_TYPE_DISK {
		return entryOther, nil
	}
	var info fileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(h, windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, fmt.Errorf("read the attributes of %s: %w", p, err)
	}
	return entryKindOf(info.attributes, info.reparseTag)
}

// maxComponent is the longest name NTFS takes, in UTF-16 units.
const maxComponent = 255

// winLookupError is what a failed open of p means: a missing name names
// no file, and so does a last name too long for any file system; anything
// else could not be checked.
func winLookupError(p string, err error) error {
	switch {
	case errors.Is(err, errNotFile):
		return errNotFile
	case errors.Is(err, windows.ERROR_FILENAME_EXCED_RANGE) &&
		len(utf16.Encode([]rune(p[strings.LastIndexByte(p, '\\')+1:]))) > maxComponent:
		return errNotFile
	case namesNoFile(err):
		return errNotFile
	}
	return fmt.Errorf("open %s: %w", p, err)
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

// follow reads the reparse data of the link or junction at p, which lies
// in dir, and returns the cleaned path it leads to (windowsLinkPath).
func (osFS) follow(p, dir string) (string, []string, error) {
	h, err := openEntry(p, windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return "", nil, fmt.Errorf("open %s: %w", p, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	buf := make([]byte, windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE)
	var n uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_GET_REPARSE_POINT, nil, 0,
		&buf[0], uint32(len(buf)), &n, nil); err != nil {
		return "", nil, fmt.Errorf("read the link %s: %w", p, err)
	}
	target, relative, err := parseReparse(buf[:n])
	if err != nil {
		return "", nil, err
	}
	full, err := windowsLinkPath(dir, target, relative)
	if err != nil {
		return "", nil, err
	}
	return osFS{}.split(full)
}

// below is an error: Win32's rules for a path that goes on below a file
// or a device (streams, devices behind reserved names) are not ones the
// walk follows.
func (osFS) below(string) error {
	return errors.New("the path goes on below a file or a device")
}

func (osFS) judgeDir(dir string, use dirUse, which string) error {
	return judgeEntry(dir, which+" ", dirMask(use))
}

// judgeLink judges the link or junction object itself with every write
// right: whoever may write it may rewrite where it leads.
func (osFS) judgeLink(p string) error { return judgeEntry(p, "a link on its way ", writeMask) }

// judgeFile judges the file at p, or the entry a stream at p belongs to.
func (osFS) judgeFile(p string) error {
	if host, ok := streamHost(p); ok {
		p = host
	}
	return judgeEntry(p, "", writeMask)
}

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

// openEntry opens the entry at p (a drive path) itself, a link or
// junction included, a directory too, with access, through the \\?\ form;
// a name no file can have is errNotFile.
func openEntry(p string, access uint32) (windows.Handle, error) {
	u, err := windows.UTF16PtrFromString(`\\?\` + p)
	if err != nil {
		return 0, errNotFile
	}
	return windows.CreateFile(u, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}
