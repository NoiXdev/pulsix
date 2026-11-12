package network

import (
	"strings"
	"testing"

	"github.com/shirou/gopsutil/v4/net"
)

func TestExcludedInterfacesPerSystem(t *testing.T) {
	up := func(name string) net.InterfaceStat { return iface(name, "up", "broadcast") }
	tests := []struct {
		goos, name string
		want       bool
	}{
		{"darwin", "utun3", false}, {"darwin", "awdl0", false}, {"darwin", "bridge100", false}, {"darwin", "en0", true},
		{"windows", "isatap.{ABC-123}", false}, {"windows", "Local Area Connection* 2", false}, {"windows", "Ethernet 2", true},
		{"linux", "ap0", true}, {"linux", "docker0", false},
	}
	for _, tt := range tests {
		got := discoveredInterface(up(tt.name), excludedInterfacePrefixesFor(tt.goos))
		if got != tt.want {
			t.Errorf("%s %q discovered = %v, want %v", tt.goos, tt.name, got, tt.want)
		}
	}
}

func TestNewInstanceKeepsExcludedPrefixes(t *testing.T) {
	p := New().(*networkPlugin)
	p.ExcludedPrefixes = []string{"x"}
	if got := p.NewInstance().(*networkPlugin).ExcludedPrefixes; !strings.EqualFold(strings.Join(got, ","), "x") {
		t.Errorf("instance prefixes = %v", got)
	}
}
