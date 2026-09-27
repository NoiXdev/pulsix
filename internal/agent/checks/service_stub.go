//go:build !linux

package checks

import (
	"context"
	"errors"
)

// ErrServiceUnsupported is what a non-Linux build reports for every unit
// lookup: systemd over D-Bus is Linux-only (service_linux.go); a
// misconfigured host on any other platform gets a plain, explicit reason
// rather than a silent no-op or a build failure.
var ErrServiceUnsupported = errors.New("service checks are not supported on this platform")

type stubLister struct{}

func newSystemdLister() unitLister { return stubLister{} }

func (stubLister) ListUnits(context.Context, []string) ([]unitState, error) {
	return nil, ErrServiceUnsupported
}
