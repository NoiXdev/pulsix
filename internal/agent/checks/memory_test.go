// package checks: the memory plugin's tests run entirely against injected
// fixtures — no real mem.VirtualMemory or mem.SwapMemory call.
package checks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shirou/gopsutil/v4/mem"
)

type fixtureVirtualMemory struct {
	stat *mem.VirtualMemoryStat
	err  error
}

func (f fixtureVirtualMemory) VirtualMemory(context.Context) (*mem.VirtualMemoryStat, error) {
	return f.stat, f.err
}

type fixtureSwapMemory struct {
	stat *mem.SwapMemoryStat
	err  error
}

func (f fixtureSwapMemory) SwapMemory(context.Context) (*mem.SwapMemoryStat, error) {
	return f.stat, f.err
}

func TestMemoryCollectHappyPath(t *testing.T) {
	p := memoryPlugin{
		Memory: fixtureVirtualMemory{stat: &mem.VirtualMemoryStat{UsedPercent: 61.2, Available: 4 << 30}},
		Swap:   fixtureSwapMemory{stat: &mem.SwapMemoryStat{UsedPercent: 5.0}},
	}
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(obs) != 3 {
		t.Fatalf("got %d observations, want 3: %+v", len(obs), obs)
	}
	for _, o := range obs {
		if o.Item != "" {
			t.Errorf("memory is whole-host, item must be empty, got %q", o.Item)
		}
	}
	if o := findObs(t, obs, "", "mem.used_pct"); o.Value == nil || *o.Value != 61.2 {
		t.Errorf("mem.used_pct = %v, want 61.2", o.Value)
	}
	if o := findObs(t, obs, "", "mem.available_bytes"); o.Value == nil || *o.Value != float64(4<<30) {
		t.Errorf("mem.available_bytes = %v, want %v", o.Value, float64(4<<30))
	}
	if o := findObs(t, obs, "", "swap.used_pct"); o.Value == nil || *o.Value != 5.0 {
		t.Errorf("swap.used_pct = %v, want 5.0", o.Value)
	}
}

func TestMemoryCollectToleratesProviderFaults(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name             string
		plugin           memoryPlugin
		wantValueMetrics []string
		wantErrMetrics   []string
	}{
		{
			name: "virtual memory fault still reports swap",
			plugin: memoryPlugin{
				Memory: fixtureVirtualMemory{err: boom},
				Swap:   fixtureSwapMemory{stat: &mem.SwapMemoryStat{UsedPercent: 2}},
			},
			wantValueMetrics: []string{"swap.used_pct"},
			wantErrMetrics:   []string{"mem.used_pct", "mem.available_bytes"},
		},
		{
			name: "swap fault still reports memory",
			plugin: memoryPlugin{
				Memory: fixtureVirtualMemory{stat: &mem.VirtualMemoryStat{UsedPercent: 50, Available: 1}},
				Swap:   fixtureSwapMemory{err: boom},
			},
			wantValueMetrics: []string{"mem.used_pct", "mem.available_bytes"},
			wantErrMetrics:   []string{"swap.used_pct"},
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
			if want := len(tc.wantValueMetrics) + len(tc.wantErrMetrics); len(obs) != want {
				t.Fatalf("got %+v, want %d observations (%d values + %d errors)", obs, want, len(tc.wantValueMetrics), len(tc.wantErrMetrics))
			}
			for _, m := range tc.wantValueMetrics {
				o := findObs(t, obs, "", m)
				if o.Value == nil || o.Err != "" {
					t.Errorf("%s = %+v, want a value observation with no error", m, o)
				}
			}
			for _, m := range tc.wantErrMetrics {
				o := findObs(t, obs, "", m)
				if o.Err == "" || o.Value != nil {
					t.Errorf("%s = %+v, want an error observation carrying no value", m, o)
				}
			}
		})
	}
}

// TestMemoryCollectNoSwapReportsZero: a host with no swap configured is not
// an error — gopsutil itself reports UsedPercent 0 rather than NaN when
// swap total is 0, and the plugin passes that straight through.
func TestMemoryCollectNoSwapReportsZero(t *testing.T) {
	p := memoryPlugin{
		Memory: fixtureVirtualMemory{stat: &mem.VirtualMemoryStat{UsedPercent: 10, Available: 1}},
		Swap:   fixtureSwapMemory{stat: &mem.SwapMemoryStat{Total: 0, UsedPercent: 0}},
	}
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if o := findObs(t, obs, "", "swap.used_pct"); o.Value == nil || *o.Value != 0 {
		t.Errorf("swap.used_pct = %v, want 0", o.Value)
	}
}

func TestMemoryCollectNilProvidersContributeNothing(t *testing.T) {
	obs, err := (memoryPlugin{}).Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(obs) != 0 {
		t.Fatalf("obs = %+v, want none", obs)
	}
}

func TestMemoryCollectContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := memoryPlugin{
		Memory: fixtureVirtualMemory{stat: &mem.VirtualMemoryStat{}},
		Swap:   fixtureSwapMemory{stat: &mem.SwapMemoryStat{}},
	}
	obs, err := p.Collect(ctx, nil)
	if obs != nil {
		t.Fatalf("cancelled collection returned %v", obs)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestMemoryName(t *testing.T) {
	if (memoryPlugin{}).Name() != "memory" {
		t.Fatalf("Name() = %q, want memory", (memoryPlugin{}).Name())
	}
}

var (
	_ virtualMemoryStater = fixtureVirtualMemory{}
	_ swapMemoryStater    = fixtureSwapMemory{}
	_ Plugin              = memoryPlugin{}
)
