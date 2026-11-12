//go:build windows

package checks

import "golang.org/x/sys/windows"

// driveKindOf classifies a drive by its letter ("C:") through the Windows
// API; anything it cannot classify is "other".
func driveKindOf(mount string) string {
	root, err := windows.UTF16PtrFromString(mount + `\`)
	if err != nil {
		return "other"
	}
	switch windows.GetDriveType(root) {
	case windows.DRIVE_FIXED:
		return "fixed"
	case windows.DRIVE_REMOVABLE:
		return "removable"
	case windows.DRIVE_REMOTE:
		return "remote"
	case windows.DRIVE_CDROM:
		return "cdrom"
	default:
		return "other"
	}
}
