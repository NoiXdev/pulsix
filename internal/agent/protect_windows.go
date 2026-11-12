//go:build windows

package agent

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileAllAccess is FILE_ALL_ACCESS, which x/sys/windows does not define.
// The protected list grants it rather than GENERIC_ALL so the ACE on the
// directory itself carries specific rights, exactly as icacls' "F" does.
const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1FF

// accessRights are the ACE mask bits treated as access to the agent's
// secrets: reading the data (FILE_READ_DATA, for a directory listing it),
// writing it (FILE_WRITE_DATA, FILE_APPEND_DATA, for a directory adding
// files or subdirectories), the generic rights that include either, and
// WRITE_DAC and WRITE_OWNER, whose holder can grant itself the rest.
// Traversal/execute, attribute and extended-attribute access,
// READ_CONTROL, SYNCHRONIZE and DELETE alone reveal or change no secret.
const accessRights = windows.GENERIC_READ | windows.GENERIC_WRITE | windows.GENERIC_ALL |
	windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA |
	windows.WRITE_DAC | windows.WRITE_OWNER

// windowsProtector allows SYSTEM (S-1-5-18) and the Administrators group
// (S-1-5-32-544) and nobody else.
type windowsProtector struct{}

func newProtector() protector { return windowsProtector{} }

func allowedSIDs() (system, admins *windows.SID, err error) {
	system, err = windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, nil, fmt.Errorf("look up the SYSTEM SID: %w", err)
	}
	admins, err = windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, nil, fmt.Errorf("look up the Administrators SID: %w", err)
	}
	return system, admins, nil
}

// Loose lists the trustees of allow entries beyond SYSTEM and
// Administrators that grant any of accessRights. A directory must also
// have its list protected (inheritance off); a file need not, because the
// entries it inherits from the protected directory name only those two.
func (windowsProtector) Loose(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", path, err)
	}
	system, admins, err := allowedSIDs()
	if err != nil {
		return "", err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("read the access list of %s: %w", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		return "", fmt.Errorf("read the access list of %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && dacl == nil) {
		return "everyone (it has no access list)", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the access list of %s: %w", path, err)
	}
	var found []string
	seen := make(map[string]bool)
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			found = append(found, s)
		}
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return "", fmt.Errorf("read entry %d of the access list of %s: %w", i, path, err)
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		default:
			add(fmt.Sprintf("an access entry of type %d", ace.Header.AceType))
			continue
		}
		if ace.Mask&accessRights == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.Equals(system) || sid.Equals(admins) {
			continue
		}
		add(trusteeName(sid))
	}
	if fi.IsDir() && control&windows.SE_DACL_PROTECTED == 0 {
		add("whoever its parent's access list admits (it inherits that list)")
	}
	return strings.Join(found, ", "), nil
}

// trusteeName is DOMAIN\account when the SID resolves, else the SID string.
func trusteeName(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	switch {
	case err != nil || account == "":
		return sid.String()
	case domain == "":
		return account
	default:
		return domain + `\` + account
	}
}

// Protect replaces path's access list with a protected one (inheritance
// off) granting full control to SYSTEM and Administrators only, inherited
// by everything created beneath it.
func (windowsProtector) Protect(path string) error {
	system, admins, err := allowedSIDs()
	if err != nil {
		return err
	}
	entries := []windows.EXPLICIT_ACCESS{fullControl(system), fullControl(admins)}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build the access list for %s: %w", path, err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
	if err != nil {
		return fmt.Errorf("set the access list of %s: %w", path, err)
	}
	return nil
}

func fullControl(sid *windows.SID) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: fileAllAccess,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}
