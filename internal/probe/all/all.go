// Package all wires the manager's probes into one registry. It is the only
// package that imports every probe, so the core (internal/probe) and the
// probes themselves stay free of import cycles and of init-time
// registration.
package all

import (
	"context"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/dns"
	"github.com/noixdev/pulsix/internal/probe/http"
	"github.com/noixdev/pulsix/internal/probe/icmp"
	"github.com/noixdev/pulsix/internal/probe/tcp"
	"github.com/noixdev/pulsix/internal/probe/tls"
)

// Registry returns the probe of every plugin that runs on the manager.
func Registry() probe.Registry {
	return probe.Registry{
		"tcp":  tcp.Probe,
		"icmp": icmp.Probe,
		"dns":  dns.Probe,
		"http": http.Probe,
		"tls":  tls.Probe,
	}
}

// Run probes address with plugin through Registry; see probe.Registry.Run.
func Run(ctx context.Context, g *probe.Guard, plugin string, params map[string]any, address string, secret map[string]string) []probe.Observation {
	return Registry().Run(ctx, g, plugin, params, address, secret)
}
