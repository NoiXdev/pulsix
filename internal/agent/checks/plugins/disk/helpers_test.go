package disk

import (
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// findObs locates the one observation for (item, metric); the test fails
// loudly rather than silently reading a zero value if it's missing. Every
// plugin package keeps its own copy (backlog 3.52): a shared test-helper
// package for a ten-line lookup would cost more than it saves.
func findObs(t *testing.T, obs []checks.Observation, item, metric string) checks.Observation {
	t.Helper()
	for _, o := range obs {
		if o.Item == item && o.Metric == metric {
			return o
		}
	}
	t.Fatalf("no observation for item %q metric %q in %+v", item, metric, obs)
	return checks.Observation{}
}
