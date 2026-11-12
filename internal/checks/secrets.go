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

// MissingSecret returns the secret a check of d names that the host's
// agent did not report, or "" when there is nothing to warn about: the
// check names no secret (its plugin takes none, or the parameter is
// unset), the agent reported it, the agent's report is unknown (unknown is
// not missing), the check is disabled (it does not run), or the plugin
// runs on the server (secrets live with agents). A missing secret is what
// the agent will fail the check with on every round — "secret `<name>` is
// not defined in agent.yaml" — so the host page can say it before the
// first result arrives. The manager never refuses such a definition:
// definitions bind to groups and organisations, and each host's file is
// its own.
func MissingSecret(d Definition, agent AgentSecrets) string {
	return missingSecret(checkplugins.Get, d, agent)
}

// missingSecret is MissingSecret over a descriptor lookup, so its tests
// can use a plugin that takes a secret before any real one does.
func missingSecret(lookup func(string) (checkplugins.Descriptor, bool), d Definition, agent AgentSecrets) string {
	if !agent.Known || !d.Enabled {
		return ""
	}
	desc, ok := lookup(d.Plugin)
	if !ok || desc.Runs != checkplugins.Agent {
		return ""
	}
	name, ok := desc.SecretNameOf(d.Params)
	if !ok || slices.Contains(agent.Names, name) {
		return ""
	}
	return name
}
