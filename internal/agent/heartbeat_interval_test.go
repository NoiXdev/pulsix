package agent

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agents"
)

// TestManagerMirrorsTheAgentsBounds ties the manager's copies of the
// agent's own constants (internal/agents, which the agent binary never
// imports) to the agent's: the bounds the manager clamps a reported
// heartbeat interval to, and the disconnect buffer window that caps the
// gap restarting an online period (backlog 3.58). A raised maximum the
// manager did not follow would narrow that agent's online window and read
// it offline between its contacts.
func TestManagerMirrorsTheAgentsBounds(t *testing.T) {
	if agents.MinReportedHeartbeatInterval != minHeartbeatInterval {
		t.Errorf("agents.MinReportedHeartbeatInterval = %v, the agent's minimum is %v",
			agents.MinReportedHeartbeatInterval, minHeartbeatInterval)
	}
	if agents.MaxReportedHeartbeatInterval != maxHeartbeatInterval {
		t.Errorf("agents.MaxReportedHeartbeatInterval = %v, the agent's maximum is %v",
			agents.MaxReportedHeartbeatInterval, maxHeartbeatInterval)
	}
	if agents.DisconnectBufferWindow != bufferWindow {
		t.Errorf("agents.DisconnectBufferWindow = %v, the agent's buffer window is %v",
			agents.DisconnectBufferWindow, bufferWindow)
	}
}

// TestReportedHeartbeatInterval: the interval goes out in whole seconds,
// rounded up, so a reported interval is never shorter than the real one
// and the manager's online window never too narrow.
func TestReportedHeartbeatInterval(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want int32
	}{
		{30 * time.Second, 30},
		{10 * time.Minute, 600},
		{7500 * time.Millisecond, 8},
		{20 * time.Millisecond, 1},
		{0, 0},
	} {
		if got := reportedHeartbeatInterval(tc.in); got != tc.want {
			t.Errorf("reportedHeartbeatInterval(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
