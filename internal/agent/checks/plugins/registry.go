// Package plugins wires the agent's check plugins together. Each plugin
// lives in its own package below this one (cpu, memory, disk, network,
// service, uptime, docker) and imports internal/agent/checks for the
// Plugin interface and the shared observation helpers; the registry lives
// here rather than in checks because checks importing the plugins would
// close an import cycle.
//
// Adding a plugin: create plugins/<name>/ exporting only New(); add it to
// Registry() and to the want list in registry_test.go. On the manager side,
// add it to internal/checks (supportedPlugins and the params validation in
// definitions.go) and to the plugin enums in internal/api (check_handlers.go,
// host_check_handlers.go, shared_handlers.go); add its metrics to
// internal/rules/catalog.go (see its OBLIGATION note). In the SPA, add it to
// NUMERIC_PLUGIN in web/src/features/hosts/hostChecks.ts (and its label
// metrics to DISCRETE_METRICS there) and to the plugin lists of
// web/src/features/checks/CheckDialog.tsx and
// web/src/features/instance/shared/SharedCheckFormPage.tsx. Finally update
// the end-user docs in the noix-docs repo.
package plugins

import (
	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/cpu"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/disk"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/docker"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/memory"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/network"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/service"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/uptime"
)

// Registry returns one instance of every check plugin the agent ships,
// wired to the real providers (gopsutil, and systemd over D-Bus on Linux —
// launchd on macOS, the service control manager on Windows, and
// service/stub.go's placeholder everywhere else). It is a func, not a
// package var, so every caller gets its own plugin values. The stateless
// plugins are shared by every tick and every check key using them; a
// checks.StatefulPlugin (network) is a prototype the scheduler clones per
// key.
func Registry() map[string]checks.Plugin {
	return map[string]checks.Plugin{
		"cpu":     cpu.New(),
		"memory":  memory.New(),
		"disk":    disk.New(),
		"service": service.New(),
		"network": network.New(),
		"uptime":  uptime.New(),
		"docker":  docker.New(),
	}
}
