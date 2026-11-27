// Package vantage wires the probes an agent vantage may run: every
// manager probe but snmp. An snmp check always names a secret, and a
// check with a secret is probed only from the manager, so snmp never
// reaches an agent; keeping it out of this package keeps its library out
// of the agent binary (backlog 3.78). internal/probe/all is this registry
// plus snmp, the manager's.
package vantage

import (
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/dns"
	"github.com/noixdev/pulsix/internal/probe/http"
	"github.com/noixdev/pulsix/internal/probe/icmp"
	"github.com/noixdev/pulsix/internal/probe/tcp"
	"github.com/noixdev/pulsix/internal/probe/tls"
)

// Registry returns the probe of every plugin an agent vantage runs. Its
// http probe reads JSON in slots of jsonGate (nil is a gate of the
// registry's own); see http.JSONGate.
func Registry(jsonGate *http.JSONGate) probe.Registry {
	return probe.Registry{
		"tcp":  tcp.Probe,
		"icmp": icmp.Probe,
		"dns":  dns.Probe,
		"http": http.NewProbe(jsonGate),
		"tls":  tls.Probe,
	}
}

// failAlls are the plugins of Registry whose metrics depend on their
// params (list items such as assertions): each reports, for a probe that
// could not be attempted, exactly the metrics and items its params would
// have produced.
func failAlls() map[string]func(params map[string]any, msg string) []probe.Observation {
	return map[string]func(map[string]any, string) []probe.Observation{
		"http": http.FailAll,
		"tcp":  tcp.FailAll,
		"dns":  dns.FailAll,
		"tls":  tls.FailAll,
	}
}

// FailAll is plugin's own result for a probe of params that could not be
// attempted, msg on every metric; ok is false for a plugin without one.
func FailAll(plugin string, params map[string]any, msg string) (obs []probe.Observation, ok bool) {
	f, ok := failAlls()[plugin]
	if !ok {
		return nil, false
	}
	return f(params, msg), true
}
