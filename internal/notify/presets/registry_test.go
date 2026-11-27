package presets_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

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
