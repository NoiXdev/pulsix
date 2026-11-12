package smart

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestSmartctlCandidates(t *testing.T) {
	unix := []string{"/usr/sbin/smartctl", "/usr/local/sbin/smartctl", "/opt/homebrew/sbin/smartctl", "/opt/homebrew/bin/smartctl", "/usr/local/bin/smartctl"}
	for _, tc := range []struct {
		goos string
		env  map[string]string
		want []string
	}{
		{"linux", nil, unix},
		{"darwin", nil, unix},
		{"windows", nil, []string{`C:\Program Files\smartmontools\bin\smartctl.exe`}},
		{"windows", map[string]string{"ProgramFiles": `C:\Program Files`}, []string{`C:\Program Files\smartmontools\bin\smartctl.exe`}},
		{"windows", map[string]string{"ProgramFiles": `D:\Apps\`}, []string{`D:\Apps\smartmontools\bin\smartctl.exe`}},
	} {
		l := locator{goos: tc.goos, getenv: func(k string) string { return tc.env[k] }}
		if got := l.candidates(); !slices.Equal(got, tc.want) {
			t.Errorf("%s %v: %q, want %q", tc.goos, tc.env, got, tc.want)
		}
	}
}

// fakeLocator finds exactly the names in found; "smartctl" (the PATH
// lookup) resolves to fromPath.
func fakeLocator(goos, fromPath string, found ...string) locator {
	return locator{
		goos:   goos,
		getenv: func(string) string { return "" },
		lookPath: func(name string) (string, error) {
			if !slices.Contains(found, name) {
				return "", errors.New("not found")
			}
			if name == "smartctl" {
				return fromPath, nil
			}
			return name, nil
		},
		isAbs: func(p string) bool {
			return strings.HasPrefix(p, "/") || (len(p) > 2 && p[1] == ':' && p[2] == '\\')
		},
	}
}

// TestFindSmartctl: the standard locations first, in order, PATH only as
// the fallback off Windows; on Windows only the installer's location
// counts. Nothing usable is the install hint.
func TestFindSmartctl(t *testing.T) {
	const winStd = `C:\Program Files\smartmontools\bin\smartctl.exe`
	for _, tc := range []struct {
		desc     string
		goos     string
		fromPath string
		found    []string
		want     string // "" means errNotFound
	}{
		{"standard before PATH", "linux", "/home/u/bin/smartctl", []string{"smartctl", "/usr/local/sbin/smartctl"}, "/usr/local/sbin/smartctl"},
		{"standard order", "linux", "", []string{"/usr/local/bin/smartctl", "/usr/sbin/smartctl"}, "/usr/sbin/smartctl"},
		{"homebrew sbin before bin", "darwin", "", []string{"/opt/homebrew/bin/smartctl", "/opt/homebrew/sbin/smartctl"}, "/opt/homebrew/sbin/smartctl"},
		{"PATH fallback", "linux", "/srv/tools/smartctl", []string{"smartctl"}, "/srv/tools/smartctl"},
		{"relative PATH result", "linux", "bin/smartctl", []string{"smartctl"}, ""},
		{"nothing", "linux", "", nil, ""},
		{"windows standard", "windows", `C:\tools\smartctl.exe`, []string{"smartctl", winStd}, winStd},
		{"windows PATH exe is not used", "windows", `C:\tools\SMARTCTL.EXE`, []string{"smartctl"}, ""},
		{"windows PATH bat", "windows", `C:\tools\smartctl.bat`, []string{"smartctl"}, ""},
		{"windows PATH cmd", "windows", `C:\tools\smartctl.cmd`, []string{"smartctl"}, ""},
		{"windows PATH com", "windows", `C:\tools\smartctl.com`, []string{"smartctl"}, ""},
		{"windows relative", "windows", `tools\smartctl.exe`, []string{"smartctl"}, ""},
	} {
		got, err := fakeLocator(tc.goos, tc.fromPath, tc.found...).find()
		if tc.want == "" {
			if !errors.Is(err, errNotFound) {
				t.Errorf("%s: %q, %v; want errNotFound", tc.desc, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", tc.desc, got, err, tc.want)
		}
	}
	// On Windows the PATH is never even asked: the agent runs smartctl as
	// SYSTEM, and a folder on the machine-wide PATH may be writable by
	// users who are not administrators.
	l := fakeLocator("windows", `C:\tools\smartctl.exe`, "smartctl")
	asked := l.lookPath
	l.lookPath = func(name string) (string, error) {
		if name == "smartctl" {
			t.Errorf("windows: PATH lookup for %q", name)
		}
		return asked(name)
	}
	if _, err := l.find(); !errors.Is(err, errNotFound) {
		t.Errorf("windows without the standard location: %v, want errNotFound", err)
	}
	if errNotFound.Error() != "smartctl not found; install smartmontools" {
		t.Fatalf("errNotFound = %q", errNotFound)
	}
}
