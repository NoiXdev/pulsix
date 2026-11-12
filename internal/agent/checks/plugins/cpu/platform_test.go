package cpu

import (
	"context"
	"testing"

	"github.com/shirou/gopsutil/v4/load"
)

func TestCPUOnWindowsReportsNoLoad(t *testing.T) {
	p := newCPUPluginFor("windows")
	if p.Load != nil {
		t.Fatal("windows cpu plugin has a load probe")
	}
	p.Percent = fixturePercent{pct: []float64{12.5}}
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 || obs[0].Metric != "cpu.used_pct" {
		t.Fatalf("obs = %+v, want only cpu.used_pct", obs)
	}
}

func TestCPUOnDarwinKeepsLoad(t *testing.T) {
	p := newCPUPluginFor("darwin")
	if p.Load == nil {
		t.Fatal("darwin cpu plugin has no load probe")
	}
	p.Load = fixtureLoad{avg: &load.AvgStat{Load1: 1, Load5: 2, Load15: 3}}
	p.Percent = fixturePercent{pct: []float64{5}}
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 4 {
		t.Fatalf("obs = %+v, want load1/5/15 and used_pct", obs)
	}
}
