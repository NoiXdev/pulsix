package tcp_test

import (
	"context"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/tcp"
)

// run is the tcp probe called the way the registry calls it.
func run(ctx context.Context, g *probe.Guard, params map[string]any, address string) []probe.Observation {
	return tcp.Probe(ctx, g, params, address, nil)
}
