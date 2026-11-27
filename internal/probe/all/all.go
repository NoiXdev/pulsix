// Package all wires the manager's probes into one registry: the agent
// vantage's (internal/probe/vantage) plus snmp, which runs on the manager
// only. It is the only package that imports every probe, so the core
// (internal/probe) and the probes themselves stay free of import cycles
// and of init-time registration.
package all

import (
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/http"
	"github.com/noixdev/pulsix/internal/probe/snmp"
	"github.com/noixdev/pulsix/internal/probe/vantage"
)

// Registry returns the probe of every plugin that runs on the manager.
// Its http probe reads JSON in slots of jsonGate, which bounds the JSON
// stages across every registry sharing it: the manager builds one gate
// and one registry (nil is a gate of the registry's own).
func Registry(jsonGate *http.JSONGate) probe.Registry {
	reg := vantage.Registry(jsonGate)
	reg["snmp"] = snmp.Probe
	return reg
}

// FailAll is plugin's own result for a probe of params that could not be
// attempted, msg on every metric; ok is false for a plugin without one,
// whose metrics are its descriptor's, item "" each.
func FailAll(plugin string, params map[string]any, msg string) (obs []probe.Observation, ok bool) {
	if plugin == "snmp" {
		return snmp.FailAll(params, msg), true
	}
	return vantage.FailAll(plugin, params, msg)
}
