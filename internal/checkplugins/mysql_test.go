package checkplugins_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestValidateMySQLParams (backlog 3.10b): schema_sizes is an optional bool.
func TestValidateMySQLParams(t *testing.T) {
	runParamCases(t, []paramCase{
		{"mysql secret only", "mysql", map[string]any{"secret": "db"}, true},
		{"mysql schema sizes on", "mysql", map[string]any{"secret": "db", "schema_sizes": true}, true},
		{"mysql schema sizes off", "mysql", map[string]any{"secret": "db", "schema_sizes": false}, true},
		{"mysql schema sizes not a bool", "mysql", map[string]any{"secret": "db", "schema_sizes": "yes"}, false},
		{"mysql an unknown parameter", "mysql", map[string]any{"secret": "db", "schemas": []any{"app"}}, false},
	})
}

// TestMySQLAsksForSchemaMetrics (backlog 3.10b): the schema metrics are
// asked only while schema_sizes is true, so switching it off reads as
// "not asked", not as a reading that went stale.
func TestMySQLAsksForSchemaMetrics(t *testing.T) {
	on := map[string]any{"secret": "db", "schema_sizes": true}
	off := map[string]any{"secret": "db"}
	tests := []struct {
		name   string
		params map[string]any
		metric string
		want   bool
	}{
		{"size with sizes on", on, "mysql.schema_size_bytes", true},
		{"unreported with sizes on", on, "mysql.schemas_unreported", true},
		{"size with sizes absent", off, "mysql.schema_size_bytes", false},
		{"unreported with sizes absent", off, "mysql.schemas_unreported", false},
		{"size with sizes false", map[string]any{"schema_sizes": false}, "mysql.schema_size_bytes", false},
		{"another metric with sizes absent", off, "mysql.connections", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkplugins.AsksForMetric("mysql", tt.params, nil, tt.metric); got != tt.want {
				t.Errorf("AsksForMetric(%v, %q) = %v, want %v", tt.params, tt.metric, got, tt.want)
			}
		})
	}
	if !checkplugins.AsksForItem("mysql", off, "app") {
		t.Error("a schema item is not asked")
	}
}
