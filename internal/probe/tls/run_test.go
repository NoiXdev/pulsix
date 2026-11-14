package tls_test

import (
	"context"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/tls"
)

// run is the tls probe called the way the registry calls it.
func run(ctx context.Context, g *probe.Guard, params map[string]any, address string) []probe.Observation {
	return tls.Probe(ctx, g, params, address, nil)
}
