package presets_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/notify/presets"
)

// TestRegistryMatchesTheSchema: every registered preset is a value the
// notification_channels CHECK allows, and every value it allows is
// registered — a preset missing on one side either cannot be stored or
// fails every delivery.
func TestRegistryMatchesTheSchema(t *testing.T) {
	var schema []string
	files, err := os.ReadDir("../../db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	check := regexp.MustCompile(`CHECK \(preset IN \(([^)]*)\)\)`)
	for _, f := range files {
		b, err := os.ReadFile("../../db/migrations/" + f.Name())
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(b), "-- +goose Down")
		for _, m := range check.FindAllStringSubmatch(up, -1) {
			schema = nil // the last migration that sets the CHECK wins
			for _, v := range strings.Split(m[1], ",") {
				if v = strings.Trim(strings.TrimSpace(v), "'"); v != "" {
					schema = append(schema, v)
				}
			}
		}
	}
	var registered []string
	for name := range presets.Registry() {
		registered = append(registered, name)
	}
	slices.Sort(schema)
	slices.Sort(registered)
	if !slices.Equal(schema, registered) {
		t.Fatalf("schema allows %v, registry has %v", schema, registered)
	}
}

// TestRegistryAtPointsFixedHostsAtBase: every preset that builds its URL
// builds it under the given base, and under the real service otherwise.
func TestRegistryAtPointsFixedHostsAtBase(t *testing.T) {
	const base = "http://127.0.0.1:9"
	s := notify.PresetSettings{AccountSID: "AC0123456789abcdef0123456789abcdef", Region: "us"}
	for name, p := range presets.RegistryAt(base) {
		ep, ok := p.(notify.Endpointed)
		if !ok {
			continue
		}
		if got := ep.Endpoint(s); !strings.HasPrefix(got, base+"/") {
			t.Errorf("%s: %s", name, got)
		}
		if got := presets.Registry()[name].(notify.Endpointed).Endpoint(s); !strings.HasPrefix(got, "https://") || strings.Contains(got, "127.0.0.1") {
			t.Errorf("%s: production %s", name, got)
		}
	}
}
