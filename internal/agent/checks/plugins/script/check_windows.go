//go:build windows

package script

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// trustedInstallerSID is the TrustedInstaller service's SID, the owner of
// Windows' own files.
const trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

// fileDeleteChild is FILE_DELETE_CHILD, which x/sys/windows does not
// define.
const fileDeleteChild = 0x40

// writeMask are the rights to change a file or what a directory holds:
// the rights the installer's Assert-RemovableBinDir refuses on Program
// Files\Pulsix, plus writing extended attributes and attributes.
const writeMask = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA |
	fileDeleteChild | windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC |
	windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL

// ancestorMask are the rights that let someone swap what a directory
// above the file's own holds: delete or rename it or its entries
// (DELETE, FILE_DELETE_CHILD), or give themselves more rights on it
// (WRITE_DAC, WRITE_OWNER, GENERIC_ALL). Adding files or subdirectories
// (FILE_ADD_FILE, FILE_ADD_SUBDIRECTORY), which every user may do in C:\,
// replaces nothing on the way.
const ancestorMask = windows.DELETE | fileDeleteChild | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_ALL

// The deny entry types besides ACCESS_DENIED_ACE_TYPE, which
// x/sys/windows does not define. A deny entry grants nothing.
const (
	accessDeniedObjectAceType         = 6
	accessDeniedCallbackAceType       = 10
	accessDeniedCallbackObjectAceType = 12
)

// trustedSIDs are the principals that may own and change a file a command
// runs: SYSTEM, Administrators and TrustedInstaller.
func trustedSIDs() ([]*windows.SID, error) {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("look up the SYSTEM SID: %w", err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, fmt.Errorf("look up the Administrators SID: %w", err)
	}
	installer, err := windows.StringToSid(trustedInstallerSID)
	if err != nil {
		return nil, fmt.Errorf("look up the TrustedInstaller SID: %w", err)
	}
	return []*windows.SID{system, admins, installer}, nil
}

// resolveFile returns the final path of the regular file path names,
// with every symlink and junction on the way resolved, as Windows itself
// reports it for the opened file; a path that names none is errNotFile.
func resolveFile(path string) (string, error) {
	fi, err := os.Stat(path)
	switch {
	case err != nil && namesNoFile(err):
		return "", errNotFile
	case err != nil:
		return "", err
	case !fi.Mode().IsRegular():
		return "", errNotFile
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", errNotFile
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		if namesNoFile(err) {
			return "", errNotFile
		}
		return "", fmt.Errorf("open: %w", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	final, err := finalPath(h)
	if err != nil {
		return "", err
	}
	if fi, err := os.Lstat(final); err != nil || !fi.Mode().IsRegular() {
		if err != nil && !namesNoFile(err) {
			return "", err
		}
		return "", errNotFile
	}
	return final, nil
}

// GetFinalPathNameByHandle's flags (both zero: the default), which
// x/sys/windows does not define.
const (
	fileNameNormalized = 0x0
	volumeNameDOS      = 0x0
)

// finalPath is the DOS path of the opened file, without the \\?\ prefix.
func finalPath(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), fileNameNormalized|volumeNameDOS)
		if err != nil {
			return "", fmt.Errorf("read the final path: %w", err)
		}
		if n < uint32(len(buf)) {
			return stripPrefix(windows.UTF16ToString(buf[:n])), nil
		}
		buf = make([]uint16, n+1)
	}
}

// stripPrefix turns \\?\C:\x into C:\x and \\?\UNC\host\share\x into
// \\host\share\x.
func stripPrefix(p string) string {
	switch {
	case strings.HasPrefix(p, `\\?\UNC\`):
		return `\\` + p[len(`\\?\UNC\`):]
	case strings.HasPrefix(p, `\\?\`):
		return p[len(`\\?\`):]
	}
	return p
}

// judgeFile judges resolved, a regular file's final path: the file and
// its directory each owned by a trusted principal, with no allow entry
// granting anyone else a write right (writeMask); every directory above
// owned by a trusted principal, with no allow entry granting anyone else
// a right to swap what it holds (ancestorMask).
func judgeFile(resolved string) error {
	trusted, err := trustedSIDs()
	if err != nil {
		return err
	}
	if err := judgePath(resolved, "", writeMask, trusted); err != nil {
		return err
	}
	dir := filepath.Dir(resolved)
	if err := judgePath(dir, "its directory ", writeMask, trusted); err != nil {
		return err
	}
	for parent := filepath.Dir(dir); parent != dir; dir, parent = parent, filepath.Dir(parent) {
		if err := judgePath(parent, "a directory above it ", ancestorMask, trusted); err != nil {
			return err
		}
	}
	return nil
}

func judgePath(path, subject string, mask windows.ACCESS_MASK, trusted []*windows.SID) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read the security descriptor of %s: %w", path, err)
	}
	return judgeSecurity(sd, subject, mask, trusted)
}

// judgeSecurity judges one security descriptor against mask. subject (""
// for the file, "its directory " or "a directory above it ") starts a
// refusal, which names SIDs,
// never a path. An entry that is inherit-only does not apply to the
// object itself; an allow entry of a type it cannot read is refused.
func judgeSecurity(sd *windows.SECURITY_DESCRIPTOR, subject string, mask windows.ACCESS_MASK, trusted []*windows.SID) error {
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("read the owner: %w", err)
	}
	if !isTrusted(owner, trusted) {
		return refuse(subject + "is owned by " + sidName(owner) + ", not by SYSTEM, Administrators or TrustedInstaller")
	}
	dacl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && dacl == nil) {
		return refuse(subject + "has no access list, so everyone may change it")
	}
	if err != nil {
		return fmt.Errorf("read the access list: %w", err)
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("read entry %d of the access list: %w", i, err)
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE, accessDeniedObjectAceType, accessDeniedCallbackAceType, accessDeniedCallbackObjectAceType:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		default:
			return refuse(fmt.Sprintf("%shas an access entry of type %d, which is not judged", subject, ace.Header.AceType))
		}
		if ace.Mask&mask == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if isTrusted(sid, trusted) {
			continue
		}
		return refuse(subject + "lets " + sidName(sid) + " change it")
	}
	return nil
}

func isTrusted(sid *windows.SID, trusted []*windows.SID) bool {
	if sid == nil {
		return false
	}
	for _, t := range trusted {
		if sid.Equals(t) {
			return true
		}
	}
	return false
}

// sidName is the SID's string form: looking an account name up may ask a
// domain controller, which a check round must not wait on.
func sidName(sid *windows.SID) string {
	if sid == nil {
		return "nobody"
	}
	return sid.String()
}

// namesNoFile reports whether err says the path names nothing: it, or a
// directory on the way, does not exist, or the name cannot be a file's
// (a switch like /T:30 read as a rooted path, fileArgument).
func namesNoFile(err error) bool {
	return errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, windows.ERROR_PATH_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_INVALID_NAME) ||
		errors.Is(err, windows.ERROR_BAD_PATHNAME) ||
		errors.Is(err, windows.ERROR_FILENAME_EXCED_RANGE) ||
		errors.Is(err, windows.ERROR_INVALID_DRIVE) ||
		errors.Is(err, windows.ERROR_BAD_NETPATH) ||
		errors.Is(err, windows.ERROR_BAD_NET_NAME) ||
		errors.Is(err, windows.ERROR_DIRECTORY)
}
