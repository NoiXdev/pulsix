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
// No packet leaves the host: the context is already cancelled, the params
// are empty and the address is TEST-NET-1. tcp (no port), http (no url)
// and dns (its default name is the address, an IP literal it refuses to
// look up) fail on their empty params before any dial; tls dials with the
// cancelled context, which the dialer checks before connecting; icmp opens
// a local socket and closes it again without sending, because its send
// loop checks the context first.
func TestRunKnowsExactlyTheServerPlugins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := probe.NewGuard(nil, nil)
	servers := 0
	for _, d := range checkplugins.All() {
		if d.Runs == checkplugins.Server {
			servers++
		}
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
	// Without a server plugin the loop above would only check that Run
	// refuses agent plugins, and pass whatever Run's switch says.
	if servers == 0 {
		t.Fatal("no descriptor runs on the server")
	}
}
