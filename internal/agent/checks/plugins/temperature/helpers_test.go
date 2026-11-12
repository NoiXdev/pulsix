package temperature

import (
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// findObs locates the one observation for (item, metric); see memory's copy.
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

func hasObs(obs []checks.Observation, item, metric string) bool {
	for _, o := range obs {
		if o.Item == item && o.Metric == metric {
			return true
		}
	}
	return false
}

// sensorCount asserts that item "" carries exactly temp.sensors = want.
func sensorCount(t *testing.T, obs []checks.Observation, want float64) {
	t.Helper()
	var got []checks.Observation
	for _, o := range obs {
		if o.Item == "" {
			got = append(got, o)
		}
	}
	if len(got) != 1 || got[0].Metric != "temp.sensors" || got[0].Value == nil || *got[0].Value != want {
		t.Fatalf("item \"\": observations = %+v, want temp.sensors = %v", got, want)
	}
}
