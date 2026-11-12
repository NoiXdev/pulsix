//go:build linux

package raid

import (
	"context"
	"fmt"
	"io"
	"os"
)

const (
	mdstatPath   = "/proc/mdstat"
	maxMdstatLen = 1 << 20
)

func newSource() mdstatReader { return fileSource{path: mdstatPath} }

// fileSource reads a mdstat file, capped at 1 MiB.
type fileSource struct{ path string }

func (s fileSource) Read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxMdstatLen))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	return data, nil
}
