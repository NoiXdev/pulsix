// package checks: the cpu plugin's tests run entirely against injected
// fixtures — no real /proc load average and no real cpu.Percent sample —
// mirroring internal/agent/facts' provider seam.
package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/load"
)

type fixtureLoad struct {
	avg *load.AvgStat
	err error
}

func (f fixtureLoad) Avg(context.Context) (*load.AvgStat, error) { return f.avg, f.err }

type fixturePercent struct {
	pct []float64
	err error
}

func (f fixturePercent) Percent(context.Context, time.Duration, bool) ([]float64, error) {
	return f.pct, f.err
}

// findObs locates the one observation for (item, metric); the test fails
// loudly rather than silently reading a zero value if it's missing.
func findObs(t *testing.T, obs []Observation, item, metric string) Observation {
	t.Helper()
	for _, o := range obs {
		if o.Item == item && o.Metric == metric {
			return o
		}
	}
	t.Fatalf("no observation for item %q metric %q in %+v", item, metric, obs)
	return Observation{}
}

func TestCPUCollectHappyPath(t *testing.T) {
	p := cpuPlugin{
		Load:    fixtureLoad{avg: &load.AvgStat{Load1: 0.5, Load5: 0.75, Load15: 1.0}},
		Percent: fixturePercent{pct: []float64{37.5}},
	}
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(obs) != 4 {
		t.Fatalf("got %d observations, want 4: %+v", len(obs), obs)
	}
	for _, o := range obs {
		if o.Item != "" {
			t.Errorf("cpu is whole-host, item must be empty, got %q", o.Item)
		}
	}
	cases := []struct {
		metric string
		want   float64
	}{
		{"cpu.load1", 0.5},
		{"cpu.load5", 0.75},
		{"cpu.load15", 1.0},
		{"cpu.used_pct", 37.5},
	}
	for _, tc := range cases {
		o := findObs(t, obs, "", tc.metric)
		if o.Value == nil || *o.Value != tc.want {
			t.Errorf("%s = %v, want %v", tc.metric, o.Value, tc.want)
		}
		if o.Label != nil {
			t.Errorf("%s must be a value, not a label", tc.metric)
		}
	}
}

func TestCPUCollectToleratesProviderFaults(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name        string
		plugin      cpuPlugin
		wantMetrics []string
	}{
		{
			name: "load average fault still reports used_pct",
			plugin: cpuPlugin{
				Load:    fixtureLoad{err: boom},
				Percent: fixturePercent{pct: []float64{10}},
			},
			wantMetrics: []string{"cpu.used_pct"},
		},
		{
			name: "percent fault still reports the load averages",
			plugin: cpuPlugin{
				Load:    fixtureLoad{avg: &load.AvgStat{Load1: 1, Load5: 2, Load15: 3}},
				Percent: fixturePercent{err: boom},
			},
			wantMetrics: []string{"cpu.load1", "cpu.load5", "cpu.load15"},
		},
		{
			name: "both fault: no observations, both errors joined",
			plugin: cpuPlugin{
				Load:    fixtureLoad{err: boom},
				Percent: fixturePercent{err: boom},
			},
			wantMetrics: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs, err := tc.plugin.Collect(context.Background(), nil)
			if err == nil {
				t.Fatal("a provider fault must surface through the joined error")
			}
			if !strings.Contains(err.Error(), "boom") {
				t.Errorf("joined error %v must carry the piece fault", err)
			}
			if len(obs) != len(tc.wantMetrics) {
				t.Fatalf("got %+v, want metrics %v", obs, tc.wantMetrics)
			}
			for _, m := range tc.wantMetrics {
				findObs(t, obs, "", m)
			}
		})
	}
}

// TestCPUCollectNilProvidersContributeNothing: a partially built plugin is
// legal — nil providers are silent, not errors (facts.Collector's stance).
func TestCPUCollectNilProvidersContributeNothing(t *testing.T) {
	obs, err := (cpuPlugin{}).Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(obs) != 0 {
		t.Fatalf("obs = %+v, want none", obs)
	}
}

func TestCPUCollectContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := cpuPlugin{
		Load:    fixtureLoad{avg: &load.AvgStat{}},
		Percent: fixturePercent{pct: []float64{1}},
	}
	obs, err := p.Collect(ctx, nil)
	if obs != nil {
		t.Fatalf("cancelled collection returned %v", obs)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestCPUName(t *testing.T) {
	if (cpuPlugin{}).Name() != "cpu" {
		t.Fatalf("Name() = %q, want cpu", (cpuPlugin{}).Name())
	}
}

var (
	_ loadAverager = fixtureLoad{}
	_ cpuPercenter = fixturePercent{}
	_ Plugin       = cpuPlugin{}
)
