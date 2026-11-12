package probe_test

import (
	"context"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/probe"
)

// TestRunKnowsExactlyTheServerPlugins pins Run's switch to the descriptors:
// every plugin with Runs == Server must be handled, and no agent plugin may be.
// The context is already cancelled and the address is TEST-NET-1, so no
// attempt reaches the network.
func TestRunKnowsExactlyTheServerPlugins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := probe.NewGuard(nil, nil)
	for _, d := range checkplugins.All() {
		t.Run(d.Name, func(t *testing.T) {
			obs := probe.Run(ctx, g, d.Name, map[string]any{}, "192.0.2.1")
			unknown := false
			for _, o := range obs {
				if strings.Contains(o.Err, "unknown probe plugin") {
					unknown = true
				}
			}
			if want := d.Runs != checkplugins.Server; unknown != want {
				t.Fatalf("plugin %s (runs %s): unknown probe plugin = %v, want %v; observations %+v", d.Name, d.Runs, unknown, want, obs)
			}
		})
	}
}
