package checks

import (
	"fmt"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// The plugins a definition may name. internal/checkplugins describes each
// one — where it runs, the parameters it takes, the item list, the metrics
// — and everything this package knows about a plugin derives from there
// (backlog 3.54): the set ValidateDefinitionShape accepts, the order its
// error lists them in, validateParams, IsSynthetic and AsksForItem.
//
// These constants stay because code and tests across the module name
// plugins through them; they are spellings of checkplugins.Names(), not a
// second list, and TestPluginConstantsAreTheDescriptors keeps them so.
const (
	PluginCPU         = "cpu"
	PluginMemory      = "memory"
	PluginDisk        = "disk"
	PluginService     = "service"
	PluginNetwork     = "network"
	PluginUptime      = "uptime"
	PluginDocker      = "docker"
	PluginTemperature = "temperature"
	PluginSmart       = "smart"
	PluginRaid        = "raid"

	PluginICMP = "icmp"
	PluginTCP  = "tcp"
	PluginHTTP = "http"
	PluginTLS  = "tls"
	PluginDNS  = "dns"
)

// IsSynthetic reports whether plugin is one of the synthetic plugins
// (backlog 3.13) the manager probes itself — its descriptor runs on the
// server. Such a check never goes to an agent: the agent does not know
// these plugins and would report "unknown plugin" (spec
// 2026-11-11-synthetic-checks-design.md, "Where they run").
func IsSynthetic(plugin string) bool {
	d, ok := checkplugins.Get(plugin)
	return ok && d.Runs == checkplugins.Server
}

// validateParams checks params against plugin's descriptor
// (checkplugins.Validate: no unknown key, every required one present, every
// value valid for its spec). The refusal wraps ErrInvalidInput, so callers
// read "invalid input: disk: …" as they always did, and still wraps
// checkplugins.ErrInvalidParams.
func validateParams(plugin string, params map[string]any) error {
	if err := checkplugins.Validate(plugin, params); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return nil
}
