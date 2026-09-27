//go:build linux

package checks

import "testing"

// TestNewSystemdListerIsTheDBusAdapterOnLinux confirms the registry-facing
// constructor wires the real adapter on Linux. It stops at construction —
// exercising ListUnits against a live systemd is the v0.3a e2e gate's job
// (agent container on Linux, Task 11), not a unit test's.
func TestNewSystemdListerIsTheDBusAdapterOnLinux(t *testing.T) {
	lister := newSystemdLister()
	if _, ok := lister.(dbusLister); !ok {
		t.Fatalf("newSystemdLister() = %T, want dbusLister", lister)
	}
}

var _ unitLister = dbusLister{}
