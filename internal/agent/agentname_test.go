package agent

import (
	"regexp"
	"strings"
	"testing"
)

func TestDefaultDataDir(t *testing.T) {
	tests := []struct{ goos, programData, want string }{
		{"linux", "", "/etc/pulsix"},
		{"darwin", "", "/Library/Application Support/Pulsix"},
		{"windows", `C:\ProgramData`, `C:\ProgramData\Pulsix`},
		{"windows", "", `C:\ProgramData\Pulsix`},
		{"freebsd", "", "/etc/pulsix"},
	}
	for _, tt := range tests {
		if got := DefaultDataDir(tt.goos, tt.programData); got != tt.want {
			t.Errorf("DefaultDataDir(%q, %q) = %q, want %q", tt.goos, tt.programData, got, tt.want)
		}
	}
}

func TestNormalizeAgentName(t *testing.T) {
	hostsPattern := regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,252}[a-z0-9])?$`)
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"WIN-SRV01", "win-srv01", true},
		{"Johns-MacBook-Pro.local", "johns-macbook-pro", true},
		{"host.example.com.", "host.example.com", true},
		{"MÜLLER-PC", "m-ller-pc", true},
		{"my_box", "my-box", true},
		{"---.local", "", false},
		{"web-01", "web-01", true},
		{"", "", false},
		{strings.Repeat("a", 254), "", false},
		{strings.Repeat("a", 253), strings.Repeat("a", 253), true},
	}
	for _, tt := range tests {
		got, ok := normalizeAgentName(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("normalizeAgentName(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.ok)
		}
		if ok && !hostsPattern.MatchString(got) {
			t.Errorf("normalizeAgentName(%q) = %q does not match the hosts pattern", tt.in, got)
		}
	}
}
