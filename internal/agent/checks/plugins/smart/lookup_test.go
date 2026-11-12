package smart

import (
	"errors"
	"slices"
	"testing"
)

func TestSmartctlCandidates(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, tc := range []struct {
		goos string
		env  map[string]string
		want []string
	}{
		{"linux", nil, []string{"/usr/sbin/smartctl", "/usr/local/sbin/smartctl", "/opt/homebrew/bin/smartctl", "/opt/homebrew/sbin/smartctl", "/usr/local/bin/smartctl"}},
		{"darwin", nil, []string{"/usr/sbin/smartctl", "/usr/local/sbin/smartctl", "/opt/homebrew/bin/smartctl", "/opt/homebrew/sbin/smartctl", "/usr/local/bin/smartctl"}},
		{"windows", nil, []string{`C:\Program Files\smartmontools\bin\smartctl.exe`}},
		{"windows", map[string]string{"ProgramFiles": `C:\Program Files`}, []string{`C:\Program Files\smartmontools\bin\smartctl.exe`}},
		{"windows", map[string]string{"ProgramFiles": `D:\Apps`}, []string{`D:\Apps\smartmontools\bin\smartctl.exe`, `C:\Program Files\smartmontools\bin\smartctl.exe`}},
	} {
		if got := smartctlCandidates(tc.goos, env(tc.env)); !slices.Equal(got, tc.want) {
			t.Errorf("%s %v: %q, want %q", tc.goos, tc.env, got, tc.want)
		}
	}
}

// TestFindSmartctl: PATH first, then the install locations in order; none
// is the install hint.
func TestFindSmartctl(t *testing.T) {
	look := func(found ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			if slices.Contains(found, name) {
				if name == "smartctl" {
					return "/bin/from-path/smartctl", nil
				}
				return name, nil
			}
			return "", errors.New("not found")
		}
	}
	cands := []string{"/a/smartctl", "/b/smartctl"}
	for _, tc := range []struct {
		found []string
		want  string
	}{
		{[]string{"smartctl", "/a/smartctl"}, "/bin/from-path/smartctl"},
		{[]string{"/b/smartctl"}, "/b/smartctl"},
		{[]string{"/a/smartctl", "/b/smartctl"}, "/a/smartctl"},
	} {
		got, err := findSmartctl(look(tc.found...), cands)
		if err != nil || got != tc.want {
			t.Errorf("found %q: %q, %v; want %q", tc.found, got, err, tc.want)
		}
	}
	if _, err := findSmartctl(look(), cands); !errors.Is(err, errNotFound) {
		t.Fatalf("nothing found: %v, want errNotFound", err)
	}
	if errNotFound.Error() != "smartctl not found; install smartmontools" {
		t.Fatalf("errNotFound = %q", errNotFound)
	}
}
