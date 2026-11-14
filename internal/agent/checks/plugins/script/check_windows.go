//go:build windows

package script

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
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

// judgeFile judges resolved, a regular file, and its directory: each owned
// by a trusted principal, with no allow entry granting anyone else a
// write right.
func judgeFile(resolved string, _ fs.FileInfo) error {
	trusted, err := trustedSIDs()
	if err != nil {
		return err
	}
	if err := judgePath(resolved, "", trusted); err != nil {
		return err
	}
	return judgePath(filepath.Dir(resolved), "its directory ", trusted)
}

func judgePath(path, subject string, trusted []*windows.SID) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read the security descriptor of %s: %w", path, err)
	}
	return judgeSecurity(sd, subject, trusted)
}

// judgeSecurity judges one security descriptor. subject ("" for the file,
// "its directory " for its directory) starts a refusal, which names SIDs,
// never a path. An entry that is inherit-only does not apply to the
// object itself; an allow entry of a type it cannot read is refused.
func judgeSecurity(sd *windows.SECURITY_DESCRIPTOR, subject string, trusted []*windows.SID) error {
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
		if ace.Mask&writeMask == 0 {
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
// directory on the way, does not exist, or the name cannot be a file's.
func namesNoFile(err error) bool {
	return errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, windows.ERROR_PATH_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_INVALID_NAME) ||
		errors.Is(err, windows.ERROR_INVALID_DRIVE) ||
		errors.Is(err, windows.ERROR_BAD_NETPATH) ||
		errors.Is(err, windows.ERROR_BAD_NET_NAME) ||
		errors.Is(err, windows.ERROR_DIRECTORY)
}
