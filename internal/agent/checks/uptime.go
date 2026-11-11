package checks

import (
	"context"
	"fmt"

	"github.com/shirou/gopsutil/v4/host"
)

// uptimeReader is the host.Uptime probe (seconds since boot).
type uptimeReader interface {
	Uptime(ctx context.Context) (uint64, error)
}

type gopsutilUptime struct{}

func (gopsutilUptime) Uptime(ctx context.Context) (uint64, error) {
	return host.UptimeWithContext(ctx)
}

// uptimePlugin reports one whole-host metric (item ""): uptime.seconds.
type uptimePlugin struct {
	Uptime uptimeReader
}

func newUptimePlugin() Plugin { return uptimePlugin{Uptime: gopsutilUptime{}} }

func (uptimePlugin) Name() string { return "uptime" }

// Collect ignores params — uptime takes none. A provider fault becomes the
// metric's own error observation plus the advisory error.
func (p uptimePlugin) Collect(ctx context.Context, _ map[string]any) ([]Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Uptime == nil {
		return nil, nil
	}
	secs, err := p.Uptime.Uptime(ctx)
	if err != nil {
		wrapped := fmt.Errorf("uptime: %w", err)
		return []Observation{errObservation("", "uptime.seconds", wrapped)}, wrapped
	}
	return []Observation{valueObservation("", "uptime.seconds", float64(secs))}, nil
}

var _ Plugin = uptimePlugin{}
