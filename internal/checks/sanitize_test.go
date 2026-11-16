package checks_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

// TestSanitizeObservations pins the observation contract SanitizeObservations
// exports for the probe-result ingest (backlog 3.13a): the same bounds,
// error cleaning, shape and plausibility rules as RecordObservations.
func TestSanitizeObservations(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	host := uuid.New()
	v := func(f float64) *float64 { return &f }
	l := func(s string) *string { return &s }
	obs := []checks.Observation{
		{HostID: host, CheckKey: "web", Metric: "http.up", At: now, Value: v(1)},                                  // valid
		{HostID: host, CheckKey: "web", Metric: "http.code", At: now, Err: "a\x00b"},                              // valid, cleaned
		{HostID: host, CheckKey: "web", Metric: "http.up", At: now, Value: v(1), Label: l("x")},                   // malformed
		{HostID: host, CheckKey: "web", Metric: "http.up", At: now, Err: "\x00"},                                  // malformed once cleaned
		{HostID: host, CheckKey: "web", Metric: "http.up", At: now.Add(-2 * time.Hour), Value: v(0)},              // implausible
		{HostID: host, CheckKey: "web", Metric: "http.up", At: now.Add(3 * time.Minute), Value: v(0)},             // implausible
		{HostID: host, CheckKey: "web", Metric: "http.ms", At: now, Err: strings.Repeat("e", checks.MaxErrLen+5)}, // valid, cut
	}
	valid, res, err := checks.SanitizeObservations(obs, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) != 3 || res.Malformed != 2 || res.Implausible != 2 || res.Accepted != 0 {
		t.Fatalf("valid %d, ingest %+v", len(valid), res)
	}
	if valid[1].Err != "ab" {
		t.Errorf("error text not cleaned: %q", valid[1].Err)
	}
	if len(valid[2].Err) != checks.MaxErrLen {
		t.Errorf("error text not cut: %d bytes", len(valid[2].Err))
	}
	if obs[1].Err != "a\x00b" {
		t.Error("the caller's slice was changed")
	}
}

func TestSanitizeObservationsRefusesWhatTheContractForbids(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ok := checks.Observation{CheckKey: "web", Metric: "http.up", At: now}
	cases := map[string][]checks.Observation{
		"empty key":    {{Metric: "m", At: now}},
		"long metric":  {{CheckKey: "k", Metric: strings.Repeat("m", checks.MaxMetricLen+1), At: now}},
		"line break":   {{CheckKey: "k", Item: "a\nb", Metric: "m", At: now}},
		"over a batch": make([]checks.Observation, checks.MaxObservationsPerBatch+1),
	}
	for i := range cases["over a batch"] {
		cases["over a batch"][i] = ok
	}
	for name, obs := range cases {
		if _, _, err := checks.SanitizeObservations(obs, now); !errors.Is(err, checks.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}
