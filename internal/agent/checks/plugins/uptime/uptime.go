// Package uptime is the agent's uptime check plugin: uptime.seconds for
// the whole host. The Plugin contract and the shared observation helpers
// it builds on are in internal/agent/checks.
package uptime

import (
	"context"
	"fmt"

	"github.com/shirou/gopsutil/v4/host"

	"github.com/noixdev/pulsix/internal/agent/checks"
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

// New returns the uptime plugin wired to gopsutil.
func New() checks.Plugin { return uptimePlugin{Uptime: gopsutilUptime{}} }

func (uptimePlugin) Name() string { return "uptime" }

// Collect ignores params — uptime takes none. A provider fault becomes the
// metric's own error observation plus the advisory error.
func (p uptimePlugin) Collect(ctx context.Context, _ map[string]any) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Uptime == nil {
		return nil, nil
	}
	secs, err := p.Uptime.Uptime(ctx)
	if err != nil {
		wrapped := fmt.Errorf("uptime: %w", err)
		return []checks.Observation{checks.ErrObservation("", "uptime.seconds", wrapped)}, wrapped
	}
	return []checks.Observation{checks.ValueObservation("", "uptime.seconds", float64(secs))}, nil
}

var _ checks.Plugin = uptimePlugin{}
