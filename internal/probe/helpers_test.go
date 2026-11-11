package probe_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/probe"
)

// loopbackGuard allows loopback so tests can reach their own listeners; every
// other always-refused range stays refused.
func loopbackGuard() *probe.Guard { return probe.NewGuardForTest(true, nil) }

// byMetric indexes observations by metric and fails on a duplicate or on an
// observation that carries both a value and an error.
func byMetric(t *testing.T, obs []probe.Observation) map[string]probe.Observation {
	t.Helper()
	m := make(map[string]probe.Observation, len(obs))
	for _, o := range obs {
		if _, dup := m[o.Metric]; dup {
			t.Fatalf("metric %q reported twice: %+v", o.Metric, obs)
		}
		if (o.Value == nil) == (o.Err == "") {
			t.Fatalf("observation %q must carry a value xor an error: %+v", o.Metric, o)
		}
		m[o.Metric] = o
	}
	return m
}

// wantValue fails unless metric is present with a value, and returns it.
func wantValue(t *testing.T, m map[string]probe.Observation, metric string) float64 {
	t.Helper()
	o, ok := m[metric]
	if !ok {
		t.Fatalf("metric %q missing from %+v", metric, m)
	}
	if o.Value == nil {
		t.Fatalf("metric %q has error %q, want a value", metric, o.Err)
	}
	return *o.Value
}

// wantOnly fails unless the observations are exactly the given metric with
// the given value.
func wantOnly(t *testing.T, obs []probe.Observation, metric string, value float64) {
	t.Helper()
	m := byMetric(t, obs)
	if len(m) != 1 {
		t.Fatalf("want only %s=%v, got %+v", metric, value, obs)
	}
	if got := wantValue(t, m, metric); got != value {
		t.Fatalf("%s = %v, want %v", metric, got, value)
	}
}

// wantErrors fails unless the observations are exactly one error per metric,
// each carrying msg, and returns nothing else.
func wantErrors(t *testing.T, obs []probe.Observation, msg string, metrics ...string) {
	t.Helper()
	m := byMetric(t, obs)
	if len(m) != len(metrics) {
		t.Fatalf("want errors for %v, got %+v", metrics, obs)
	}
	for _, metric := range metrics {
		o, ok := m[metric]
		if !ok {
			t.Fatalf("metric %q missing from %+v", metric, obs)
		}
		if o.Err != msg {
			t.Fatalf("metric %q: err %q, value %v; want err %q", metric, o.Err, o.Value, msg)
		}
	}
}
