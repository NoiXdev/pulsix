package checkplugins_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestPostgresAsksForReplicaItems (backlog 3.10b): databases narrows the
// database items only; every replica item is asked whatever it lists.
func TestPostgresAsksForReplicaItems(t *testing.T) {
	listed := map[string]any{"databases": []any{"app"}}
	tests := []struct {
		name   string
		params map[string]any
		item   string
		want   bool
	}{
		{"the server's own item", listed, "", true},
		{"a listed database", listed, "app", true},
		{"an unlisted database", listed, "analytics", false},
		{"no list asks for every database", nil, "analytics", true},
		{"an empty list asks for every database", map[string]any{"databases": []any{}}, "analytics", true},
		{"a replica with a list", listed, checkplugins.PostgresReplicaItemPrefix + "standby1@10.0.0.5", true},
		{"a replica without a list", nil, checkplugins.PostgresReplicaItemPrefix + "walreceiver@local", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkplugins.AsksForItem("postgres", tt.params, tt.item); got != tt.want {
				t.Errorf("AsksForItem(%v, %q) = %v, want %v", tt.params, tt.item, got, tt.want)
			}
		})
	}
}
