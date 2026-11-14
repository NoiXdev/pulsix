package checkplugins_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestValidateScriptParams: the script plugin (backlog 3.6g) takes only
// command names; what each name runs is defined on the host. The bounds
// are the agent's own (internal/agent/checks/plugins/script).
func TestValidateScriptParams(t *testing.T) {
	runParamCases(t, []paramCase{
		{"accepts one command", "script", map[string]any{"commands": []any{"backup_age"}}, true},
		{"accepts a Go string list", "script", map[string]any{"commands": []string{"a.b_c-1"}}, true},
		{"accepts 32 commands", "script", map[string]any{"commands": anyList(32, "x")}, true},
		{"accepts a 64-byte name", "script", map[string]any{"commands": []any{strings.Repeat("c", 64)}}, true},
		{"accepts nagios", "script", map[string]any{"commands": []any{"x"}, "format": "nagios"}, true},
		{"accepts json", "script", map[string]any{"commands": []any{"x"}, "format": "json"}, true},
		{"accepts timeout 1", "script", map[string]any{"commands": []any{"x"}, "timeout_seconds": 1.0}, true},
		{"accepts timeout 60", "script", map[string]any{"commands": []any{"x"}, "timeout_seconds": 60.0}, true},

		{"refuses no params", "script", nil, false},
		{"refuses no commands", "script", map[string]any{"format": "nagios"}, false},
		{"refuses an empty list", "script", map[string]any{"commands": []any{}}, false},
		{"refuses 33 commands", "script", map[string]any{"commands": anyList(33, "x")}, false},
		{"refuses a 65-byte name", "script", map[string]any{"commands": []any{strings.Repeat("c", 65)}}, false},
		{"refuses an empty name", "script", map[string]any{"commands": []any{""}}, false},
		{"refuses a path", "script", map[string]any{"commands": []any{"/usr/bin/true"}}, false},
		{"refuses a space", "script", map[string]any{"commands": []any{"rm -rf"}}, false},
		{"refuses a colon", "script", map[string]any{"commands": []any{"a:b"}}, false},
		{"refuses a non-string entry", "script", map[string]any{"commands": []any{1.0}}, false},
		{"refuses a string for the list", "script", map[string]any{"commands": "x"}, false},
		{"refuses an unknown format", "script", map[string]any{"commands": []any{"x"}, "format": "perl"}, false},
		{"refuses an upper-case format", "script", map[string]any{"commands": []any{"x"}, "format": "JSON"}, false},
		{"refuses timeout 0", "script", map[string]any{"commands": []any{"x"}, "timeout_seconds": 0.0}, false},
		{"refuses timeout 61", "script", map[string]any{"commands": []any{"x"}, "timeout_seconds": 61.0}, false},
		{"refuses a fractional timeout", "script", map[string]any{"commands": []any{"x"}, "timeout_seconds": 1.5}, false},
		{"refuses a string timeout", "script", map[string]any{"commands": []any{"x"}, "timeout_seconds": "10"}, false},
		{"refuses an argv", "script", map[string]any{"commands": []any{"x"}, "argv": []any{"/bin/sh"}}, false},
	})
}

// TestScriptDescriptor pins the parts of the descriptor a parameter case
// cannot see: where it runs, the item list, the defaults and the metrics.
func TestScriptDescriptor(t *testing.T) {
	d, ok := checkplugins.Get("script")
	if !ok {
		t.Fatal("Get(script) found nothing")
	}
	if d.Runs != checkplugins.Agent || d.ItemParam != "commands" || d.HostItemAlwaysAsked || d.ItemEntry == nil {
		t.Fatalf("script descriptor = %+v", d)
	}
	defaults := map[string]any{}
	for _, p := range d.Params {
		defaults[p.Name] = p.Default
	}
	if defaults["format"] != "nagios" || defaults["timeout_seconds"] != int64(10) || defaults["commands"] != nil {
		t.Fatalf("defaults = %v", defaults)
	}
	if d.Params[0].Format != checkplugins.FormatCommandName {
		t.Fatalf("commands format = %q, want %q", d.Params[0].Format, checkplugins.FormatCommandName)
	}
	want := map[string]checkplugins.MetricKind{
		"script.status":           checkplugins.Discrete,
		"script.output":           checkplugins.Discrete,
		"script.exit_code":        checkplugins.Numeric,
		"script.duration_seconds": checkplugins.Numeric,
		"script.value":            checkplugins.Numeric,
	}
	if len(d.Metrics) != len(want) {
		t.Fatalf("metrics = %v, want %v", d.Metrics, want)
	}
	for _, m := range d.Metrics {
		if want[m.Name] != m.Kind {
			t.Errorf("metric %s kind %q, want %q", m.Name, m.Kind, want[m.Name])
		}
	}
}
