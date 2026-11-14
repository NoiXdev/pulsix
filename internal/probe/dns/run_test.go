package dns_test

import (
	"context"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/dns"
)

// run is the dns probe called the way the registry calls it.
func run(ctx context.Context, g *probe.Guard, params map[string]any, address string) []probe.Observation {
	return dns.Probe(ctx, g, params, address, nil)
}
