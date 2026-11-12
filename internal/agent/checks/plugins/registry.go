// Package plugins wires the agent's check plugins together. Each plugin
// lives in its own package below this one (cpu, memory, disk, network,
// service, uptime, docker) and imports internal/agent/checks for the
// Plugin interface and the shared observation helpers; the registry lives
// here rather than in checks because checks importing the plugins would
// close an import cycle.
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
