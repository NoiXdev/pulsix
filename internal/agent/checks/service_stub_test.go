//go:build !linux

package checks

import (
	"context"
	"errors"
	"testing"
)

// TestStubListerReportsUnsupported pins the non-Linux contract literally:
// no fixture, no real system involved — just the sentinel error every
// build of this package outside Linux must return, so a misconfigured
// host learns why rather than seeing a silent gap.
func TestStubListerReportsUnsupported(t *testing.T) {
	_, err := (stubLister{}).ListUnits(context.Background(), []string{"sshd"})
	if !errors.Is(err, ErrServiceUnsupported) {
		t.Fatalf("err = %v, want ErrServiceUnsupported", err)
	}
}

// TestNewSystemdListerIsTheStubOffLinux confirms the registry-facing
// constructor actually wires the stub on this platform, not a nil or a
// half-built adapter.
func TestNewSystemdListerIsTheStubOffLinux(t *testing.T) {
	lister := newSystemdLister()
	if _, ok := lister.(stubLister); !ok {
		t.Fatalf("newSystemdLister() = %T, want stubLister", lister)
	}
}

var _ unitLister = stubLister{}
