package checkplugins_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestSyntheticAssertions (backlog 3.87): tcp, dns and tls take
// assertions in their own dialect, refuse a name used twice, ask for the
// whole-host item and each entry's item, and report the two assertion
// metrics.
func TestSyntheticAssertions(t *testing.T) {
	cases := []struct {
		plugin string
		base   map[string]any
		good   string
		other  string
	}{
		{"tcp", map[string]any{"port": int64(22)}, `ssh=response matches "^SSH-2\\.0"`, "ok=status == 200"},
		{"dns", map[string]any{}, `ip=answer("192.0.2.1") exists`, "ok=connect_ms < 5"},
		{"tls", map[string]any{}, "modern=version >= 1.2", `ok=response contains "x"`},
	}
	for _, tc := range cases {
		t.Run(tc.plugin, func(t *testing.T) {
			with := func(entries ...any) map[string]any {
				p := map[string]any{"assertions": entries}
				for k, v := range tc.base {
					p[k] = v
				}
				return p
			}
			if err := checkplugins.Validate(tc.plugin, with(tc.good)); err != nil {
				t.Fatalf("Validate(%s) = %v", tc.good, err)
			}
			err := checkplugins.Validate(tc.plugin, with(tc.other))
			if err == nil || !strings.Contains(err.Error(), "unknown subject") || !strings.Contains(err.Error(), "(see the docs)") {
				t.Errorf("Validate(%s) = %v, want the dialect's refusal", tc.other, err)
			}
			name := strings.SplitN(tc.good, "=", 2)[0]
			if err := checkplugins.Validate(tc.plugin, with(tc.good, tc.good+" @warn")); err == nil || !strings.Contains(err.Error(), `assertions names "`+name+`" twice`) {
				t.Errorf("Validate(twice) = %v", err)
			}
			params := with(tc.good)
			for item, want := range map[string]bool{"": true, name: true, "other": false} {
				if got := checkplugins.AsksForItem(tc.plugin, params, item); got != want {
					t.Errorf("AsksForItem(%q) = %v, want %v", item, got, want)
				}
			}
			if checkplugins.AsksForItem(tc.plugin, tc.base, "x") {
				t.Error("a check without assertions asks for an item")
			}
			d, _ := checkplugins.Get(tc.plugin)
			kinds := map[string]checkplugins.MetricKind{}
			for _, m := range d.Metrics {
				kinds[m.Name] = m.Kind
			}
			if kinds[tc.plugin+".assertion"] != checkplugins.Discrete || kinds[tc.plugin+".assertion_actual"] != checkplugins.Discrete {
				t.Errorf("metrics = %v", kinds)
			}
		})
	}
}
