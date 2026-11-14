package checks

import (
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// fakeSecretLookup knows one test-only agent plugin and one test-only
// server plugin that take a secret, so the cases do not depend on which
// real plugins do.
func fakeSecretLookup(name string) (checkplugins.Descriptor, bool) {
	params := []checkplugins.ParamSpec{{Name: "host", Type: checkplugins.TypeString}, checkplugins.SecretParam()}
	switch name {
	case "fake-db":
		return checkplugins.Descriptor{Name: name, Runs: checkplugins.Agent, Params: params}, true
	case "fake-probe":
		return checkplugins.Descriptor{Name: name, Runs: checkplugins.Server, Params: params}, true
	}
	return checkplugins.Get(name)
}

func TestMissingSecret(t *testing.T) {
	reported := AgentSecrets{Known: true, Names: []string{"api", "mysql-prod"}}
	known := func(agent AgentSecrets, org ...string) KnownSecrets {
		return KnownSecrets{Agent: agent, Org: org}
	}
	def := func(plugin string, enabled bool, params map[string]any) Definition {
		return Definition{Key: plugin, Plugin: plugin, Enabled: enabled, Params: params}
	}
	cases := []struct {
		name  string
		d     Definition
		known KnownSecrets
		want  string
	}{
		{"reported", def("fake-db", true, map[string]any{"secret": "mysql-prod"}), known(reported), ""},
		{"not reported", def("fake-db", true, map[string]any{"secret": "redis"}), known(reported), "redis"},
		{"agent reported none", def("fake-db", true, map[string]any{"secret": "redis"}), known(AgentSecrets{Known: true, Names: []string{}}), "redis"},
		{"unknown is not missing", def("fake-db", true, map[string]any{"secret": "redis"}), known(AgentSecrets{}), ""},
		{"names no secret", def("fake-db", true, map[string]any{"host": "db"}), known(reported), ""},
		{"disabled check does not run", def("fake-db", false, map[string]any{"secret": "redis"}), known(reported), ""},
		// An agent plugin reads agent.yaml, never the organization's store.
		{"agent plugin ignores the org store", def("fake-db", true, map[string]any{"secret": "redis"}), known(reported, "redis"), "redis"},
		{"agent plugin found on the agent only", def("fake-db", true, map[string]any{"secret": "api"}), known(reported), ""},
		// A server plugin reads the organization's secret store (backlog
		// 3.13d), whatever the host's agent reported or whether it has one.
		{"server plugin in the org store", def("fake-probe", true, map[string]any{"secret": "sw"}), known(reported, "sw", "ups"), ""},
		{"server plugin missing from the org store", def("fake-probe", true, map[string]any{"secret": "sw"}), known(reported, "ups"), "sw"},
		{"server plugin, empty org store", def("fake-probe", true, map[string]any{"secret": "sw"}), known(reported), "sw"},
		{"server plugin ignores the agent's report", def("fake-probe", true, map[string]any{"secret": "api"}), known(reported), "api"},
		{"server plugin without an agent report", def("fake-probe", true, map[string]any{"secret": "sw"}), known(AgentSecrets{}), "sw"},
		{"server plugin found without an agent report", def("fake-probe", true, map[string]any{"secret": "sw"}), known(AgentSecrets{}, "sw"), ""},
		{"server plugin disabled", def("fake-probe", false, map[string]any{"secret": "sw"}), known(reported), ""},
		{"server plugin names no secret", def("fake-probe", true, map[string]any{"host": "x"}), known(reported), ""},
		{"server plugin compares exactly", def("fake-probe", true, map[string]any{"secret": "SW"}), known(reported, "sw"), "SW"},
		{"plugin without the parameter", def("cpu", true, map[string]any{"secret": "redis"}), known(reported), ""},
		{"server plugin without the parameter", def("icmp", true, map[string]any{"secret": "redis"}), known(reported), ""},
		{"unknown plugin", def("nope", true, map[string]any{"secret": "redis"}), known(reported), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := missingSecret(fakeSecretLookup, tc.d.Plugin, tc.d.Params, tc.d.Enabled, tc.known); got != tc.want {
				t.Fatalf("missingSecret = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMissingSecretUsesTheRegistry: the exported form reads the real
// descriptors: it warns for exactly the plugins that take a secret, an
// agent plugin against the agent's report and a server plugin (snmp)
// against the organization's store.
func TestMissingSecretUsesTheRegistry(t *testing.T) {
	agentOnly := KnownSecrets{Agent: AgentSecrets{Known: true, Names: []string{"redis"}}}
	orgOnly := KnownSecrets{Agent: AgentSecrets{Known: true, Names: []string{}}, Org: []string{"redis"}}
	for _, name := range checkplugins.Names() {
		d := Definition{Key: name, Plugin: name, Enabled: true, Params: map[string]any{"secret": "redis"}}
		desc, _ := checkplugins.Get(name)
		wantAgentOnly, wantOrgOnly := "", ""
		if desc.UsesSecret() && desc.Runs == checkplugins.Server {
			wantAgentOnly = "redis"
		}
		if desc.UsesSecret() && desc.Runs == checkplugins.Agent {
			wantOrgOnly = "redis"
		}
		if got := MissingSecret(d.Plugin, d.Params, d.Enabled, agentOnly); got != wantAgentOnly {
			t.Errorf("%s with the secret on the agent only: MissingSecret = %q, want %q", name, got, wantAgentOnly)
		}
		if got := MissingSecret(d.Plugin, d.Params, d.Enabled, orgOnly); got != wantOrgOnly {
			t.Errorf("%s with the secret in the org store only: MissingSecret = %q, want %q", name, got, wantOrgOnly)
		}
	}
	if got := MissingSecret("snmp", map[string]any{"secret": "sw", "interfaces": true}, true, KnownSecrets{}); got != "sw" {
		t.Fatalf("snmp naming a secret the org store lacks: MissingSecret = %q, want sw", got)
	}
}

// TestNeedOrgSecretNames: only an enabled server-plugin definition naming
// a secret makes the host page read the organization's secret names.
func TestNeedOrgSecretNames(t *testing.T) {
	def := func(plugin string, enabled bool, params map[string]any) Definition {
		return Definition{Key: plugin, Plugin: plugin, Enabled: enabled, Params: params}
	}
	cases := []struct {
		name string
		defs []Definition
		want bool
	}{
		{"none", nil, false},
		{"agent plugin with a secret", []Definition{def("fake-db", true, map[string]any{"secret": "x"})}, false},
		{"server plugin without a secret", []Definition{def("fake-probe", true, map[string]any{"host": "x"})}, false},
		{"disabled server plugin with a secret", []Definition{def("fake-probe", false, map[string]any{"secret": "x"})}, false},
		{"server plugin with a secret", []Definition{def("cpu", true, nil), def("fake-probe", true, map[string]any{"secret": "x"})}, true},
		{"unknown plugin", []Definition{def("nope", true, map[string]any{"secret": "x"})}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needOrgSecretNames(fakeSecretLookup, tc.defs); got != tc.want {
				t.Fatalf("needOrgSecretNames = %v, want %v", got, tc.want)
			}
		})
	}
	if !NeedOrgSecretNames([]Definition{def("snmp", true, map[string]any{"secret": "sw", "interfaces": true})}) {
		t.Fatal("NeedOrgSecretNames(snmp naming a secret) = false, want true")
	}
}
