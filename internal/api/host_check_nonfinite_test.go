package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
)

// TestHostChecksSurviveANonFiniteStoredValue (backlog 3.11c): ingest refuses
// ±Inf and NaN now, but a manager that ran before that fix may already hold
// one, and JSON has no spelling for it — encoding the host's checks or a
// graph over it would fail the whole response. The current value reads as
// an error instead, and a graph leaves such a point out; a value beyond
// ±1e100 reads as "value is out of range" the same way.
func TestHostChecksSurviveANonFiniteStoredValue(t *testing.T) {
	f := newChecksFixture(t, "host-checks-nonfinite")
	now := time.Now()
	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now.Add(-3 * time.Minute), Value: floatPtr(41)},
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now.Add(-2 * time.Minute), Value: floatPtr(42)},
	)
	// What an older manager stored from a broken agent: the current row and
	// the newest sample hold NaN and +Inf, written past the ingest guard.
	pool := f.s.DB.Pool()
	tag, err := pool.Exec(t.Context(),
		`UPDATE check_current SET value = 'NaN' WHERE host_id = $1 AND check_key = 'cpu' AND metric = 'usage_percent'`, f.web)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("plant NaN current value: %v (%d rows)", err, tag.RowsAffected())
	}
	tag, err = pool.Exec(t.Context(),
		`UPDATE check_samples SET value = 'Infinity' WHERE host_id = $1 AND check_key = 'cpu' AND metric = 'usage_percent' AND value = 42`, f.web)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("plant +Inf sample: %v (%d rows)", err, tag.RowsAffected())
	}

	f.record(t, checks.Observation{HostID: f.web, CheckKey: "memory", Metric: "used_bytes", At: now.Add(-time.Minute), Value: floatPtr(1)})
	tag, err = pool.Exec(t.Context(),
		`UPDATE check_current SET value = 1.5e101 WHERE host_id = $1 AND check_key = 'memory' AND metric = 'used_bytes'`, f.web)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("plant an out-of-range current value: %v (%d rows)", err, tag.RowsAffected())
	}
	mem := f.hostChecks(t, f.viewerC, f.web).byKey(t, "memory").Values
	if len(mem) != 1 || mem[0].Value != nil || mem[0].Error != "value is out of range" {
		t.Fatalf("memory values = %+v, want one row carrying only the error %q", mem, "value is out of range")
	}

	values := f.hostChecks(t, f.viewerC, f.web).byKey(t, "cpu").Values
	if len(values) != 1 || values[0].Value != nil || values[0].Error != "value is not a finite number" {
		t.Fatalf("cpu values = %+v, want one row carrying only the error %q", values, "value is not a finite number")
	}

	resp := requestJSON(t, f.viewerC, http.MethodGet,
		f.hostURL(f.web)+"/metrics?check=cpu&metric=usage_percent&range=1h", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics over a non-finite sample = %d %s, want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Points []struct {
			Value float64 `json:"value"`
		} `json:"points"`
	}
	decodeInto(t, resp, &out)
	if len(out.Points) != 1 || out.Points[0].Value != 41 {
		t.Fatalf("points = %+v, want only the finite sample 41", out.Points)
	}
}

// TestHostMetricsSurviveValuesAtTheIngestBound (backlog 3.11c): ingest keeps
// every value within ±checks.MaxAbsValue, so a graph bucket holding several
// of them must still average. PostgreSQL's avg over float8 squares a scaled
// difference as it goes, which overflows long before the sum does.
func TestHostMetricsSurviveValuesAtTheIngestBound(t *testing.T) {
	f := newChecksFixture(t, "host-metrics-bound")
	now := time.Now()
	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now.Add(-90 * time.Second), Value: floatPtr(checks.MaxAbsValue)},
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now.Add(-80 * time.Second), Value: floatPtr(-checks.MaxAbsValue)},
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now.Add(-70 * time.Second), Value: floatPtr(checks.MaxAbsValue)},
	)
	for _, r := range []string{"1h", "24h", "7d"} {
		resp := requestJSON(t, f.viewerC, http.MethodGet,
			f.hostURL(f.web)+"/metrics?check=cpu&metric=usage_percent&range="+r, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("metrics (%s) over values at the bound = %d %s, want 200", r, resp.StatusCode, mustReadBody(t, resp))
		}
		_ = resp.Body.Close()
	}
}
