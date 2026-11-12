package disk

import (
	"strings"

	"github.com/shirou/gopsutil/v4/disk"
)

// darwinPseudoFilesystems are skipped on macOS on top of the Linux ones.
var darwinPseudoFilesystems = map[string]bool{
	"devfs":  true,
	"autofs": true,
	"nullfs": true,
}

// darwinDataVolume is the one APFS system volume that holds user data; the
// other volumes under /System/Volumes share one container with "/".
const darwinDataVolume = "/System/Volumes/Data"

// discoveredMount reports whether a partition belongs to the default mount
// set on goos. driveKind classifies a Windows drive; it is unused elsewhere.
func discoveredMount(goos string, p disk.PartitionStat, driveKind func(string) string) bool {
	if p.Mountpoint == "" {
		return false
	}
	switch goos {
	case "windows":
		return driveKind != nil && driveKind(p.Mountpoint) == "fixed"
	case "darwin":
		if pseudoFilesystems[p.Fstype] || darwinPseudoFilesystems[p.Fstype] {
			return false
		}
		mp := p.Mountpoint
		switch {
		case mp == "/" || mp == darwinDataVolume:
			return true
		case strings.HasPrefix(mp, "/System/Volumes/"):
			return false
		default:
			return strings.HasPrefix(mp, "/Volumes/")
		}
	default:
		return !pseudoFilesystems[p.Fstype]
	}
}

// reportsInodes reports whether goos has an inode count to report.
func reportsInodes(goos string) bool { return goos != "windows" }
