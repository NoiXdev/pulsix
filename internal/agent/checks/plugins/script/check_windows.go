//go:build windows

package script

import (
	"errors"
	"fmt"
	"io/fs"
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

// ancestorMask are the rights that let someone swap what a directory a
// name is looked up in holds: delete or rename it or its entries (DELETE,
// FILE_DELETE_CHILD), or give themselves more rights on it (WRITE_DAC,
// WRITE_OWNER, GENERIC_ALL). Adding files or subdirectories
// (FILE_ADD_FILE, FILE_ADD_SUBDIRECTORY), which every user may do in C:\,
// replaces nothing on the way: the name looked up is taken.
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

// placeMask are the rights on the directory a switch-like argument's last
// name is missing in that let someone put a file at that name: writeMask
// without the right to add a subdirectory (FILE_ADD_SUBDIRECTORY, which is
// FILE_APPEND_DATA), which every user has on C:\ and which creates a
// directory, no file. A switch like /c reads as a missing file in C:\, so
// C:\ must pass here.
const placeMask = writeMask &^ windows.FILE_APPEND_DATA

// textNetworkPath refuses a network path, by its syntax or on a mapped
// network drive.
const textNetworkPath = "network paths are not supported"

// textTrailingDot refuses a name that ends in a dot or a space, which
// Windows drops when it opens the path: another name than the one
// checked.
const textTrailingDot = "names ending in a dot or a space are not supported"

// textDeviceForm refuses a file path written in device form (\\?\C:\…,
// \\.\UNC\…): the plain form says the same, and the checks read paths
// in it.
const textDeviceForm = "paths in device form are not supported"

// judgeSyntax decides a path that starts with two separators by its
// syntax alone, without a stat (windowsPathKindOf): a network path and a
// file path in device form are refused, a device or namespace path too
// (as a program; an argument of that form never reaches the checker,
// fileArgument), and \\x without a share names no file.
func judgeSyntax(path string) error {
	switch windowsPathKindOf(path) {
	case pathNetwork:
		return refuse(textNetworkPath)
	case pathDeviceFile:
		return refuse(textDeviceForm)
	case pathDevice:
		return refuse("device and namespace paths are not supported")
	case pathNoShare:
		return errNotFile
	}
	return nil
}

// dirMask is what a directory used as use must deny others: a directory
// a name is looked up in only the rights to swap what it holds
// (ancestorMask); one where a switch-like argument's last name is missing
// every write right but adding a subdirectory (placeMask); every other
// use every write right (writeMask): the directory holding the file or
// the link the path ends in, one the path names, one where a name written
// with its drive is missing, and one where a directory on the way is
// missing (adding a subdirectory there makes the way).
func dirMask(use dirUse) windows.ACCESS_MASK {
	switch use {
	case useLookup:
		return ancestorMask
	case useMissingRooted:
		return placeMask
	}
	return writeMask
}

// judgeSecurity judges one security descriptor against mask. subject (""
// for the file, "its directory ", "a directory on its way " or "a link on
// its way ") starts a refusal, which names SIDs, never a path. An entry
// that is inherit-only does not apply to the object itself; an allow entry
// of a type it cannot read is refused.
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
		errors.Is(err, windows.ERROR_INVALID_DRIVE) ||
		errors.Is(err, windows.ERROR_BAD_NETPATH) ||
		errors.Is(err, windows.ERROR_BAD_NET_NAME) ||
		errors.Is(err, windows.ERROR_DIRECTORY)
}
