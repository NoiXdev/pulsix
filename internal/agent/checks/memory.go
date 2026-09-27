package checks

import (
	"context"
	"errors"
	"fmt"

	"github.com/shirou/gopsutil/v4/mem"
)

// virtualMemoryStater is the mem.VirtualMemory probe (RAM usage).
type virtualMemoryStater interface {
	VirtualMemory(ctx context.Context) (*mem.VirtualMemoryStat, error)
}

// swapMemoryStater is the mem.SwapMemory probe.
type swapMemoryStater interface {
	SwapMemory(ctx context.Context) (*mem.SwapMemoryStat, error)
}

type gopsutilVirtualMemory struct{}

func (gopsutilVirtualMemory) VirtualMemory(ctx context.Context) (*mem.VirtualMemoryStat, error) {
	return mem.VirtualMemoryWithContext(ctx)
}

type gopsutilSwapMemory struct{}

func (gopsutilSwapMemory) SwapMemory(ctx context.Context) (*mem.SwapMemoryStat, error) {
	return mem.SwapMemoryWithContext(ctx)
}

// memoryPlugin reports whole-host memory metrics (item ""): mem.used_pct
// and mem.available_bytes share the one VirtualMemory call; swap.used_pct
// comes from a separate SwapMemory call, so a host with no swap (or a
// platform fault reading it) never costs the RAM numbers.
type memoryPlugin struct {
	Memory virtualMemoryStater
	Swap   swapMemoryStater
}

func newMemoryPlugin() Plugin {
	return memoryPlugin{Memory: gopsutilVirtualMemory{}, Swap: gopsutilSwapMemory{}}
}

func (memoryPlugin) Name() string { return "memory" }

// Collect ignores params — memory takes none (internal/checks.validateParams
// rejects any at definition time).
func (p memoryPlugin) Collect(ctx context.Context, _ map[string]any) ([]Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var obs []Observation
	var errs []error

	if p.Memory != nil {
		if vm, err := p.Memory.VirtualMemory(ctx); err != nil {
			errs = append(errs, fmt.Errorf("memory: virtual memory: %w", err))
		} else if vm != nil {
			obs = append(obs,
				valueObservation("", "mem.used_pct", vm.UsedPercent),
				valueObservation("", "mem.available_bytes", float64(vm.Available)),
			)
		}
	}

	if p.Swap != nil {
		if sw, err := p.Swap.SwapMemory(ctx); err != nil {
			errs = append(errs, fmt.Errorf("memory: swap: %w", err))
		} else if sw != nil {
			obs = append(obs, valueObservation("", "swap.used_pct", sw.UsedPercent))
		}
	}

	return obs, errors.Join(errs...)
}

var _ Plugin = memoryPlugin{}
