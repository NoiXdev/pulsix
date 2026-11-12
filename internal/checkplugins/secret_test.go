package checkplugins_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// withSecret is a test-only descriptor: no real plugin takes a secret yet
// (3.10's are the first), so the helpers are exercised against this one.
func withSecret() checkplugins.Descriptor {
	return checkplugins.Descriptor{
		Name: "fake-db", Runs: checkplugins.Agent,
		Params:  []checkplugins.ParamSpec{{Name: "host", Type: checkplugins.TypeString}, checkplugins.SecretParam()},
		Metrics: []checkplugins.MetricSpec{{Name: "fake-db.up", Kind: checkplugins.Numeric}},
	}
}

func TestSecretParamShape(t *testing.T) {
	p := checkplugins.SecretParam()
	if p.Name != "secret" || checkplugins.SecretParamName != "secret" {
		t.Fatalf("SecretParam().Name = %q, want %q", p.Name, "secret")
	}
	if p.Type != checkplugins.TypeString || p.Format != checkplugins.FormatSecretName || p.MaxBytes != 64 || !p.NonEmpty {
		t.Fatalf("SecretParam() = %+v, want a non-empty string of format secretName and at most 64 bytes", p)
	}
}

func TestUsesSecret(t *testing.T) {
	if !withSecret().UsesSecret() {
		t.Fatal("a descriptor with the secret parameter must use a secret")
	}
	// The plugins that take a secret (backlog 3.10); add each database
	// plugin here as it lands.
	want := map[string]bool{"postgres": true}
	for _, d := range checkplugins.All() {
		if d.UsesSecret() != want[d.Name] {
			t.Errorf("%s: UsesSecret = %v, want %v", d.Name, d.UsesSecret(), want[d.Name])
		}
	}
	// A parameter merely called "secret" but not of the shared shape is
	// not the convention: only SecretParam's spelling counts.
	odd := withSecret()
	odd.Params[1].Format = checkplugins.FormatNoControl
	if odd.UsesSecret() {
		t.Fatal("a secret parameter without the secretName format must not count")
	}
}

// TestDescriptorsSpellTheSecretParamOneWay: a real descriptor that names a
// parameter "secret" must use SecretParam's shape (Required may differ),
// so the manager's missing-secret warning and the agent's resolution read
// the same parameter.
func TestDescriptorsSpellTheSecretParamOneWay(t *testing.T) {
	want := checkplugins.SecretParam()
	for _, d := range checkplugins.All() {
		for _, p := range d.Params {
			if p.Name != checkplugins.SecretParamName {
				continue
			}
			p.Required = false
			if p.Type != want.Type || p.Format != want.Format || p.MaxBytes != want.MaxBytes || p.NonEmpty != want.NonEmpty {
				t.Errorf("%s: secret parameter %+v, want SecretParam()'s shape %+v", d.Name, p, want)
			}
		}
	}
}

func TestSecretNameOf(t *testing.T) {
	d := withSecret()
	cases := []struct {
		name   string
		params map[string]any
		want   string
		ok     bool
	}{
		{"named", map[string]any{"secret": "mysql-prod"}, "mysql-prod", true},
		{"absent", map[string]any{"host": "db"}, "", false},
		{"nil params", nil, "", false},
		{"empty", map[string]any{"secret": ""}, "", false},
		{"not a string", map[string]any{"secret": 42}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := d.SecretNameOf(tc.params)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("SecretNameOf(%v) = %q, %v; want %q, %v", tc.params, got, ok, tc.want, tc.ok)
			}
		})
	}
	// A descriptor without the parameter never names a secret, whatever
	// the params carry.
	cpu, _ := checkplugins.Get("cpu")
	if got, ok := cpu.SecretNameOf(map[string]any{"secret": "x"}); ok || got != "" {
		t.Fatalf("cpu.SecretNameOf = %q, %v; want none", got, ok)
	}
}
