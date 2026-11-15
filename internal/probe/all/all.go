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
	"github.com/noixdev/pulsix/internal/probe/snmp"
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
		"snmp": snmp.Probe,
	}
}

// Run probes address with plugin through Registry; see probe.Registry.Run.
func Run(ctx context.Context, g *probe.Guard, plugin string, params map[string]any, address string, secret map[string]string) []probe.Observation {
	return Registry().Run(ctx, g, plugin, params, address, secret)
}

// failAlls are the plugins whose metrics depend on their params (list
// items, a secret the runner may not have): each reports, for a probe that
// could not be attempted, exactly the metrics and items its params would
// have produced.
func failAlls() map[string]func(params map[string]any, msg string) []probe.Observation {
	return map[string]func(map[string]any, string) []probe.Observation{
		"http": http.FailAll,
		"snmp": snmp.FailAll,
	}
}

// FailAll is plugin's own result for a probe of params that could not be
// attempted, msg on every metric; ok is false for a plugin without one,
// whose metrics are its descriptor's, item "" each.
func FailAll(plugin string, params map[string]any, msg string) (obs []probe.Observation, ok bool) {
	f, ok := failAlls()[plugin]
	if !ok {
		return nil, false
	}
	return f(params, msg), true
}
