//go:build !linux

package raid

import (
	"context"
	"errors"
)

var errUnsupported = errors.New("not supported on this platform")

func newSource() mdstatReader { return stubSource{} }

type stubSource struct{}

func (stubSource) Read(context.Context) ([]byte, error) { return nil, errUnsupported }
