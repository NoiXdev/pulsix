// Package plugins wires the agent's check plugins together. Each plugin
// lives in its own package below this one (cpu, memory, disk, network,
// service, uptime, docker, temperature, smart, raid, postgres) and imports internal/agent/checks for the
// Plugin interface and the shared observation helpers; the registry lives
// here rather than in checks because checks importing the plugins would
// close an import cycle.
//
// Adding a plugin, everything one change must touch:
//
//   - Agent side (an agent plugin only): create plugins/<name>/ exporting
//     only New(), add it to Registry() and to the want list of
//     TestRegistryWiresEveryPlugin (registry_test.go).
//   - Descriptor: add ONE file in internal/checkplugins (params, metrics,
//     item param, formats) and register it with one entry in that package's
//     constructor list (registry() in registry.go); Names, Get and All read
//     only that list. The manager's validation, items, rules catalogue and
//     API enums derive from it. TestAgentPluginsMatchDescriptors keeps the
//     agent's Registry and the descriptors with Runs = Agent in step, and
//     TestListParamsAreItemParams wants every list parameter to be the
//     ItemParam.
//   - Literal golden tests, each updated by hand: TestNamesInTheSPAsOrder
//     and TestRuns (internal/checkplugins/registry_test.go); a new
//     checks.PluginX constant (internal/checks/plugins.go) and
//     TestPluginConstantsAreTheDescriptors; the plugin list in
//     TestUnsupportedPluginMessage (internal/checks/plugins_test.go); the
//     golden metric table of TestCatalogueMatchesShippedPlugins
//     (internal/rules/catalog_test.go).
//   - A server-run plugin also needs its case in internal/probe's Run (the
//     probe pin test TestRunKnowsExactlyTheServerPlugins checks it).
//   - SPA: regenerate the descriptor fixture (go test ./internal/checkplugins
//     -run TestSPAFixtureIsCurrent -update-spa-fixture) and ALWAYS
//     regenerate src/api/schema.d.ts with web/scripts/api-gen.sh (the
//     plugin enums are in it; the script may need web/node_modules/.bin on
//     PATH for openapi-typescript). Add the en and de plugin names to both
//     picker maps, checks.dialog.plugins and instance.shared.checks.plugins,
//     and the texts of each parameter under checks.params
//     (web/src/features/checks/paramTexts.test.ts enforces them).
//   - Docs: for an agent plugin, a paragraph under "Version skew between
//     the manager, workers and agents" in docs/upgrades.md (an older agent
//     skips the check, warns on every configuration push and shows no
//     readings until updated); the end-user docs in the noix-docs repo.
package plugins

import (
	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/cpu"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/disk"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/docker"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/memory"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/network"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/postgres"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/raid"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/service"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/smart"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/temperature"
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
		"cpu":         cpu.New(),
		"memory":      memory.New(),
		"disk":        disk.New(),
		"service":     service.New(),
		"network":     network.New(),
		"uptime":      uptime.New(),
		"docker":      docker.New(),
		"temperature": temperature.New(),
		"smart":       smart.New(),
		"raid":        raid.New(),
		"postgres":    postgres.New(),
	}
}
