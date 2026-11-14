package script

import "testing"

// TestWindowsPathKind: paths that start with two separators are decided
// by their syntax alone, never by a stat, which for a network path would
// reach out to the host it names.
func TestWindowsPathKind(t *testing.T) {
	for p, want := range map[string]windowsPathKind{
		`C:\scripts\x.ps1`:        pathLocal,
		`\scripts\x.ps1`:          pathLocal,
		`/T:30`:                   pathLocal,
		`x.ps1`:                   pathLocal,
		``:                        pathLocal,
		`\\.\PhysicalDrive0`:      pathDevice,
		`\\?\C:\scripts\x.ps1`:    pathDeviceFile,
		`\\.\C:\scripts\x.ps1`:    pathDeviceFile,
		`\\?\UNC\host\share\x`:    pathDeviceFile,
		`\\.\unc\host\share\x`:    pathDeviceFile,
		`//?/UNC/host/share/x`:    pathDeviceFile,
		`\\?\Volume{0a}\x.ps1`:    pathDeviceFile,
		`\\?\GLOBALROOT\Device\x`: pathDeviceFile,
		`\\.\C:`:                  pathDevice,
		`\\?\HarddiskVolume1\x`:   pathDeviceFile,
		`\\.\PhysicalDrive0\x`:    pathDeviceFile,
		`//./COM1/x`:              pathDeviceFile,
		`\\.\PhysicalDrive0\`:     pathDevice,
		`\\.\mailslot\x`:          pathDevice,
		`\\?\PIPE\x\y`:            pathDevice,
		`\\.\pipe\x`:              pathDevice,
		`\\.\UNCx`:                pathDevice,
		`//./pipe/x`:              pathDevice,
		`//?/C:/x`:                pathDeviceFile,
		`\/.\x`:                   pathDevice,
		`\\.`:                     pathDevice,
		`//Nologo`:                pathNoShare,
		`//B`:                     pathNoShare,
		`\\host`:                  pathNoShare,
		`\\host\`:                 pathNoShare,
		`\\host\share`:            pathNetwork,
		`\\host\share\x.ps1`:      pathNetwork,
		`//host/share/x.ps1`:      pathNetwork,
		`\\\share\x`:              pathNetwork,
		`\\..\x`:                  pathNetwork,
		`\\?host\share`:           pathNetwork,
		`\\host.example\c$\x.ps1`: pathNetwork,
	} {
		if got := windowsPathKindOf(p); got != want {
			t.Errorf("windowsPathKindOf(%q) = %d, want %d", p, got, want)
		}
	}
}
