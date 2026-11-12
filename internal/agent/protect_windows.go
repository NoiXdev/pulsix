//go:build windows

package agent

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
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

// Loose reports what lets anyone beyond SYSTEM and Administrators reach
// path:
//   - path itself being a link (symlink, junction or other reparse point),
//     whose target is not inspected;
//   - an owner other than those two, because the owner implicitly holds
//     READ_CONTROL and WRITE_DAC whatever the access list says;
//   - allow entries for anyone else that grant any of accessRights;
//   - for a directory, an unprotected list (it inherits its parent's).
//
// A file need not have a protected list, because the entries it inherits
// from the protected directory name only the two allowed principals.
func (w windowsProtector) Loose(path string) (string, error) { return w.loose(path, true) }

// loose is Loose; protectedDir says whether a directory must have its
// list protected (the data directory) or may inherit (an entry in it).
func (windowsProtector) loose(path string, protectedDir bool) (string, error) {
	fi, link, err := lstatLink(path)
	if err != nil {
		return "", err
	}
	if link {
		return "whoever its target admits (it is a link: a symlink or junction)", nil
	}
	system, admins, err := allowedSIDs()
	if err != nil {
		return "", err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("read the security descriptor of %s: %w", path, err)
	}
	var found findings
	owner, err := ownerFinding(path, sd, system, admins)
	if err != nil {
		return "", err
	}
	if owner != "" {
		found.add(owner)
	}
	control, _, err := sd.Control()
	if err != nil {
		return "", fmt.Errorf("read the access list of %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && dacl == nil) {
		found.add("everyone (it has no access list)")
		return found.String(), nil
	}
	if err != nil {
		return "", fmt.Errorf("read the access list of %s: %w", path, err)
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
			found.add(fmt.Sprintf("an access entry of type %d", ace.Header.AceType))
			continue
		}
		if ace.Mask&accessRights == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.Equals(system) || sid.Equals(admins) {
			continue
		}
		found.add(trusteeName(sid))
	}
	if protectedDir && fi.IsDir() && control&windows.SE_DACL_PROTECTED == 0 {
		found.add("whoever its parent's access list admits (it inherits that list)")
	}
	return found.String(), nil
}

// LooseEntry is Loose without the protected-list rule for a directory:
// an entry inside the protected data directory may inherit its list.
func (w windowsProtector) LooseEntry(path string) (string, error) { return w.loose(path, false) }

// ForeignOwner reports path's owner unless it is SYSTEM or Administrators.
// A link is reported as such: GetNamedSecurityInfo would read its target.
func (windowsProtector) ForeignOwner(path string) (string, error) {
	_, link, err := lstatLink(path)
	if err != nil {
		return "", err
	}
	if link {
		return "whoever made the link (it is a symlink or junction)", nil
	}
	system, admins, err := allowedSIDs()
	if err != nil {
		return "", err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("read the owner of %s: %w", path, err)
	}
	return ownerFinding(path, sd, system, admins)
}

// ownerFinding is "owner DOMAIN\user" when sd's owner is neither allowed
// principal (a missing owner included), else "".
func ownerFinding(path string, sd *windows.SECURITY_DESCRIPTOR, system, admins *windows.SID) (string, error) {
	owner, _, err := sd.Owner()
	if err != nil {
		return "", fmt.Errorf("read the owner of %s: %w", path, err)
	}
	if owner != nil && (owner.Equals(system) || owner.Equals(admins)) {
		return "", nil
	}
	return "owner " + trusteeName(owner), nil
}

// checksEntries is true: until the data directory was protected it
// inherited a list that lets every user add files to it.
func (windowsProtector) checksEntries() bool { return true }

// findings collects distinct findings in the order they were found.
type findings []string

func (f *findings) add(s string) {
	for _, have := range *f {
		if have == s {
			return
		}
	}
	*f = append(*f, s)
}

func (f findings) String() string { return strings.Join(f, ", ") }

// lstatLink stats path without following it and says whether path itself
// is a reparse point (a symlink, a junction or anything else Windows would
// redirect through).
func lstatLink(path string) (os.FileInfo, bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, false, fmt.Errorf("inspect %s: %w", path, err)
	}
	if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fi, true, nil
	}
	if attr, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok &&
		attr.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fi, true, nil
	}
	return fi, false, nil
}

// trusteeName is DOMAIN\account when the SID resolves, else the SID string.
func trusteeName(sid *windows.SID) string {
	if sid == nil {
		return "nobody (none is set)"
	}
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

// Protect makes Administrators the owner of path and replaces its access
// list with a protected one (inheritance off) granting full control to
// SYSTEM and Administrators only, inherited by everything created beneath
// it. It refuses a link: the access list it would rewrite is the
// target's, and the target is wherever the link's author pointed it.
// ensureDataDir never calls it on a directory with a foreign owner: that
// owner could hold a WRITE_DAC handle open across the change and keep
// whatever it planted inside, so such a directory is refused instead.
func (windowsProtector) Protect(path string) error {
	_, link, err := lstatLink(path)
	if err != nil {
		return err
	}
	if link {
		return fmt.Errorf("refusing to change the access of %s: it is a link (symlink or junction)", path)
	}
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
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		admins, nil, acl, nil)
	if err != nil {
		return fmt.Errorf("set the owner and access list of %s: %w", path, err)
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
