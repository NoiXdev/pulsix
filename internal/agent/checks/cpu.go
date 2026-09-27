package checks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
)

// defaultCPUSampleInterval is how long the cpu plugin blocks measuring
// cpu.used_pct. gopsutil's non-blocking mode (interval 0) reports the
// delta since the last call, which is either package-global state shared
// with anything else in the process calling cpu.Percent or, on the very
// first call, meaningless — a short blocking sample keeps the plugin
// self-contained and correct on every call, at the cost of this much wall
// time per collection.
const defaultCPUSampleInterval = 200 * time.Millisecond

// loadAverager is the load.Avg probe (1/5/15-minute load averages).
type loadAverager interface {
	Avg(ctx context.Context) (*load.AvgStat, error)
}

// cpuPercenter is the cpu.Percent probe, mirroring gopsutil's own signature
// (percpu is always false here — the plugin wants one whole-host number).
type cpuPercenter interface {
	Percent(ctx context.Context, interval time.Duration, percpu bool) ([]float64, error)
}

type gopsutilLoad struct{}

func (gopsutilLoad) Avg(ctx context.Context) (*load.AvgStat, error) {
	return load.AvgWithContext(ctx)
}

type gopsutilCPUPercent struct{}

func (gopsutilCPUPercent) Percent(ctx context.Context, interval time.Duration, percpu bool) ([]float64, error) {
	return cpu.PercentWithContext(ctx, interval, percpu)
}

// cpuPlugin reports whole-host processor metrics (item ""): cpu.load1/5/15
// from one load.Avg call, cpu.used_pct from a separate cpu.Percent sample.
// The two calls are independent so a fault in one never costs the other.
type cpuPlugin struct {
	Load     loadAverager
	Percent  cpuPercenter
	Interval time.Duration
}

func newCPUPlugin() Plugin {
	return cpuPlugin{Load: gopsutilLoad{}, Percent: gopsutilCPUPercent{}, Interval: defaultCPUSampleInterval}
}

func (cpuPlugin) Name() string { return "cpu" }

// Collect ignores params — cpu takes none (internal/checks.validateParams
// rejects any at definition time).
func (p cpuPlugin) Collect(ctx context.Context, _ map[string]any) ([]Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var obs []Observation
	var errs []error

	if p.Load != nil {
		if avg, err := p.Load.Avg(ctx); err != nil {
			wrapped := fmt.Errorf("cpu: load average: %w", err)
			errs = append(errs, wrapped)
			obs = append(obs,
				errObservation("", "cpu.load1", wrapped),
				errObservation("", "cpu.load5", wrapped),
				errObservation("", "cpu.load15", wrapped),
			)
		} else if avg != nil {
			obs = append(obs,
				valueObservation("", "cpu.load1", avg.Load1),
				valueObservation("", "cpu.load5", avg.Load5),
				valueObservation("", "cpu.load15", avg.Load15),
			)
		}
	}

	if p.Percent != nil {
		if pct, err := p.Percent.Percent(ctx, p.Interval, false); err != nil {
			wrapped := fmt.Errorf("cpu: used percent: %w", err)
			errs = append(errs, wrapped)
			obs = append(obs, errObservation("", "cpu.used_pct", wrapped))
		} else if len(pct) > 0 {
			obs = append(obs, valueObservation("", "cpu.used_pct", pct[0]))
		}
	}

	return obs, errors.Join(errs...)
}

var _ Plugin = cpuPlugin{}
