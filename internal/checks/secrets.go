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

// KnownSecrets is what a check's secret is looked up in: the host's
// agent's report for a plugin the agent runs, and the names in the
// organization's secret store (internal/orgsecrets, backlog 3.13d) for a
// plugin the manager probes. The store is read from the database, so its
// names are always known.
type KnownSecrets struct {
	Agent AgentSecrets
	Org   []string
}

// MissingSecret returns the secret a check names that is not where its
// plugin looks for it, or "" when there is nothing to warn about: the
// check names no secret (its plugin takes none, or the parameter is
// unset), the secret is there, the check does not run (enabled is false:
// a disabled definition), or — for an agent plugin — the agent's report is
// unknown (unknown is not missing).
//
// An agent plugin's secret lives in the host's agent.yaml: a missing one
// is what the agent fails the check with on every round — "secret
// `<name>` is not defined in agent.yaml". A server plugin's lives in the
// organization's secret store, whatever the host's agent reported: the
// probe fails with "secret "<name>" is not defined". Either way the host
// page can say it before the first result arrives. The manager never
// refuses such a definition: definitions bind to groups and organisations,
// each host's file is its own, and a store secret may be added after the
// check.
//
// It takes the check's plugin and params rather than a Definition because
// a check defined in the host's agent.yaml (backlog 3.5a) is warned about
// the same way, and it has no definition — it always runs.
func MissingSecret(plugin string, params map[string]any, enabled bool, known KnownSecrets) string {
	return missingSecret(checkplugins.Get, plugin, params, enabled, known)
}

// missingSecret is MissingSecret over a descriptor lookup, so its tests
// can use plugins of their own.
func missingSecret(lookup func(string) (checkplugins.Descriptor, bool), plugin string, params map[string]any, enabled bool, known KnownSecrets) string {
	if !enabled {
		return ""
	}
	desc, ok := lookup(plugin)
	if !ok {
		return ""
	}
	name, ok := desc.SecretNameOf(params)
	if !ok {
		return ""
	}
	switch desc.Runs {
	case checkplugins.Agent:
		if !known.Agent.Known || slices.Contains(known.Agent.Names, name) {
			return ""
		}
	case checkplugins.Server:
		if slices.Contains(known.Org, name) {
			return ""
		}
	default:
		return ""
	}
	return name
}

// NeedOrgSecretNames reports whether MissingSecret needs the organization's
// secret names for any of defs: an enabled definition of a server plugin
// that names a secret. A host page without one reads no secret store.
func NeedOrgSecretNames(defs []Definition) bool {
	return needOrgSecretNames(checkplugins.Get, defs)
}

func needOrgSecretNames(lookup func(string) (checkplugins.Descriptor, bool), defs []Definition) bool {
	for _, d := range defs {
		if !d.Enabled {
			continue
		}
		if desc, ok := lookup(d.Plugin); ok && desc.Runs == checkplugins.Server {
			if _, named := desc.SecretNameOf(d.Params); named {
				return true
			}
		}
	}
	return false
}
