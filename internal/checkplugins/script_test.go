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

		// Values for placeholders (backlog 3.6j): the shape only; the host's
		// rules judge them.
		{"accepts args", "script", map[string]any{"commands": []any{"tcp", "disk"}, "args": []any{"tcp:port=8080", "tcp:host=db 1", "disk:mount=/var/a=b"}}, true},
		{"accepts shell text as data", "script", map[string]any{"commands": []any{"x"}, "args": []any{"x:a=;rm -rf / && $(id)"}}, true},
		{"accepts a leading dash (the host decides)", "script", map[string]any{"commands": []any{"x"}, "args": []any{"x:offset=-5"}}, true},
		{"accepts an empty args list", "script", map[string]any{"commands": []any{"x"}, "args": []any{}}, true},
		{"accepts 16 values for one command", "script", map[string]any{"commands": []any{"x"}, "args": argEntries("x", 16)}, true},
		{"accepts a 512-byte value", "script", map[string]any{"commands": []any{"x"}, "args": []any{"x:a=" + strings.Repeat("v", 512)}}, true},
		{"refuses 17 values for one command", "script", map[string]any{"commands": []any{"x"}, "args": argEntries("x", 17)}, false},
		{"refuses a 513-byte value", "script", map[string]any{"commands": []any{"x"}, "args": []any{"x:a=" + strings.Repeat("v", 513)}}, false},
		{"refuses an empty value", "script", map[string]any{"commands": []any{"x"}, "args": []any{"x:a="}}, false},
		{"refuses a newline", "script", map[string]any{"commands": []any{"x"}, "args": []any{"x:a=1\n2"}}, false},
		{"refuses a NUL", "script", map[string]any{"commands": []any{"x"}, "args": []any{"x:a=1\x002"}}, false},
		{"refuses no placeholder", "script", map[string]any{"commands": []any{"x"}, "args": []any{"x=1"}}, false},
		{"refuses an upper-case placeholder", "script", map[string]any{"commands": []any{"x"}, "args": []any{"x:Port=1"}}, false},
		{"refuses a command not listed", "script", map[string]any{"commands": []any{"x"}, "args": []any{"y:port=1"}}, false},
		{"refuses a placeholder twice", "script", map[string]any{"commands": []any{"x"}, "args": []any{"x:port=1", "x:port=2"}}, false},
		{"refuses a string for args", "script", map[string]any{"commands": []any{"x"}, "args": "x:port=1"}, false},
	})
}

func argEntries(command string, n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = command + ":p" + strings.Repeat("x", i) + "=1"
	}
	return out
}

// TestScriptArgsErrorsNeverQuoteValues: a refused args entry is described,
// never repeated.
func TestScriptArgsErrorsNeverQuoteValues(t *testing.T) {
	const value = "Va1ue-Secret"
	for _, args := range [][]any{
		{"x:a=" + value + "\n"},
		{"x:a=" + strings.Repeat(value, 60)},
		{"y:a=" + value},
		{"x:a=" + value, "x:a=" + value},
		{"x" + value},
	} {
		err := checkplugins.Validate("script", map[string]any{"commands": []any{"x"}, "args": args})
		if err == nil || strings.Contains(err.Error(), value) {
			t.Errorf("Validate(%q) = %v", args, err)
		}
	}
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
	if a := d.Params[1]; a.Name != "args" || a.Type != checkplugins.TypeList || a.Required || a.MaxEntries != 64 || a.MaxBytes != 640 || a.Format != checkplugins.FormatScriptArg {
		t.Fatalf("args param = %+v", a)
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
