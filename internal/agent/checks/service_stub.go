//go:build !linux && !windows && !darwin

package checks

import (
	"context"
	"errors"
)

// ErrServiceUnsupported is what a build for a platform without a supported
// service manager reports for every lookup: a misconfigured host gets a
// plain, explicit reason rather than a silent no-op or a build failure.
var ErrServiceUnsupported = errors.New("service checks are not supported on this platform")

type stubLister struct{}

func newUnitLister() unitLister { return stubLister{} }

func (stubLister) States(context.Context, []string) (map[string]unitResult, error) {
	return nil, ErrServiceUnsupported
}
