package checks

import (
	"slices"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// AgentSecrets is what a host's agent last reported about its local
// secrets file (agent.yaml, backlog 3.5): the names of the secrets it
// loaded. Known is false when there is no report — an agent older than
// the file, no heartbeat since it enrolled, or no agent at all.
type AgentSecrets struct {
	Known bool
	Names []string
}

// MissingSecret returns the secret a check names that the host's agent
// did not report, or "" when there is nothing to warn about: the check
// names no secret (its plugin takes none, or the parameter is unset), the
// agent reported it, the agent's report is unknown (unknown is not
// missing), the check does not run (enabled is false: a disabled
// definition), or the plugin runs on the server (secrets live with
// agents). A missing secret is what the agent will fail the check with on
// every round — "secret `<name>` is not defined in agent.yaml" — so the
// host page can say it before the first result arrives. The manager never
// refuses such a definition: definitions bind to groups and organisations,
// and each host's file is its own.
//
// It takes the check's plugin and params rather than a Definition because
// a check defined in the host's agent.yaml (backlog 3.5a) is warned about
// the same way, and it has no definition — it always runs.
func MissingSecret(plugin string, params map[string]any, enabled bool, agent AgentSecrets) string {
	return missingSecret(checkplugins.Get, plugin, params, enabled, agent)
}

// missingSecret is MissingSecret over a descriptor lookup, so its tests
// can use a plugin that takes a secret before any real one does.
func missingSecret(lookup func(string) (checkplugins.Descriptor, bool), plugin string, params map[string]any, enabled bool, agent AgentSecrets) string {
	if !agent.Known || !enabled {
		return ""
	}
	desc, ok := lookup(plugin)
	if !ok || desc.Runs != checkplugins.Agent {
		return ""
	}
	name, ok := desc.SecretNameOf(params)
	if !ok || slices.Contains(agent.Names, name) {
		return ""
	}
	return name
}
