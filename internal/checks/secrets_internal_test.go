package checks

import (
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// fakeSecretLookup knows one test-only agent plugin that takes a secret
// and one server plugin that (nonsensically) does: no real plugin takes a
// secret before 3.10.
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
	def := func(plugin string, enabled bool, params map[string]any) Definition {
		return Definition{Key: plugin, Plugin: plugin, Enabled: enabled, Params: params}
	}
	cases := []struct {
		name  string
		d     Definition
		agent AgentSecrets
		want  string
	}{
		{"reported", def("fake-db", true, map[string]any{"secret": "mysql-prod"}), reported, ""},
		{"not reported", def("fake-db", true, map[string]any{"secret": "redis"}), reported, "redis"},
		{"agent reported none", def("fake-db", true, map[string]any{"secret": "redis"}), AgentSecrets{Known: true, Names: []string{}}, "redis"},
		{"unknown is not missing", def("fake-db", true, map[string]any{"secret": "redis"}), AgentSecrets{}, ""},
		{"names no secret", def("fake-db", true, map[string]any{"host": "db"}), reported, ""},
		{"disabled check does not run", def("fake-db", false, map[string]any{"secret": "redis"}), reported, ""},
		{"server plugin has no agent", def("fake-probe", true, map[string]any{"secret": "redis"}), reported, ""},
		{"plugin without the parameter", def("cpu", true, map[string]any{"secret": "redis"}), reported, ""},
		{"unknown plugin", def("nope", true, map[string]any{"secret": "redis"}), reported, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := missingSecret(fakeSecretLookup, tc.d, tc.agent); got != tc.want {
				t.Fatalf("missingSecret = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMissingSecretUsesTheRegistry: the exported form reads the real
// descriptors, none of which takes a secret yet — so it never warns.
func TestMissingSecretUsesTheRegistry(t *testing.T) {
	for _, name := range checkplugins.Names() {
		d := Definition{Key: name, Plugin: name, Enabled: true, Params: map[string]any{"secret": "redis"}}
		if got := MissingSecret(d, AgentSecrets{Known: true, Names: []string{}}); got != "" {
			t.Errorf("%s: MissingSecret = %q, want none (no plugin takes a secret yet)", name, got)
		}
	}
}
