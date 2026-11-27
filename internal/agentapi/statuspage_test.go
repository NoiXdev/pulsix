package agentapi

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// TestHeartbeatAckCarriesNames: every heartbeat's ack names the agent's
// organization and host (backlog 3.132) and the heartbeat's status page
// mode is stored with the rest of it; an older agent's heartbeat (no
// mode) is stored as unknown. Other messages' acks carry no names.
func TestHeartbeatAckCarriesNames(t *testing.T) {
	h := newHarness(t)
	resp, client := enrollAnAgent(t, h, "status-1")
	hostID := uuid.MustParse(resp.GetHostId())
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stored := func() *string {
		t.Helper()
		h.store.mu.Lock()
		defer h.store.mu.Unlock()
		for _, a := range h.store.agentsR {
			if a.HostID == hostID {
				return a.StatusPage
			}
		}
		t.Fatal("no agent row")
		return nil
	}

	ack := sendAndAck(t, stream, &agentpb.AgentMessage{Body: &agentpb.AgentMessage_Heartbeat{Heartbeat: &agentpb.Heartbeat{
		Version: "v1", StatusPage: agentpb.StatusPageMode_STATUS_PAGE_MODE_LOOPBACK,
	}}})
	names := ack.GetAgentNames()
	if names == nil || names.GetHostName() != "Host "+hostID.String() || names.GetOrgName() == "" {
		t.Fatalf("ack names = %v", names)
	}
	if got := stored(); got == nil || *got != "loopback" {
		t.Fatalf("stored = %v", got)
	}

	sendAndAck(t, stream, heartbeat("v1", ""))
	if got := stored(); got != nil {
		t.Fatalf("an older agent's heartbeat stored %q", *got)
	}

	ack = sendAndAck(t, stream, &agentpb.AgentMessage{Body: &agentpb.AgentMessage_Facts{Facts: &agentpb.Facts{}}})
	if ack.GetAgentNames() != nil {
		t.Fatalf("a facts ack carries names: %v", ack.GetAgentNames())
	}
}

func TestStatusPageModeOf(t *testing.T) {
	for in, want := range map[agentpb.StatusPageMode]string{
		agentpb.StatusPageMode_STATUS_PAGE_MODE_UNSPECIFIED: "",
		agentpb.StatusPageMode_STATUS_PAGE_MODE_OFF:         "off",
		agentpb.StatusPageMode_STATUS_PAGE_MODE_LOOPBACK:    "loopback",
		agentpb.StatusPageMode_STATUS_PAGE_MODE_NETWORK:     "network",
		agentpb.StatusPageMode_STATUS_PAGE_MODE_FAILED:      "failed",
		agentpb.StatusPageMode(99):                          "",
	} {
		if got := string(statusPageModeOf(in)); got != want {
			t.Errorf("statusPageModeOf(%v) = %q, want %q", in, got, want)
		}
	}
}
