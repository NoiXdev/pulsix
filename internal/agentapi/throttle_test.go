// How much resolve work one agent identity can cause (backlog 7.25).
package agentapi

import (
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/checks"
)

// TestHeartbeatsInsideTheIntervalResolveOnce: every heartbeat used to run
// an uncached, organization-wide resolve, so an agent sending them in a
// loop drove that many resolves against the shared database. Inside
// heartbeatResolveInterval only the first one resolves; the others are
// acknowledged and nothing more.
func TestHeartbeatsInsideTheIntervalResolveOnce(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	h.checks.setResolved(hostID, checks.ResolvedSet{HostID: hostID, Version: "v1"})
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		if err := stream.Send(heartbeat("agent-1", "stale")); err != nil {
			t.Fatal(err)
		}
		msg, err := recvWithin(t, stream, recvTimeout)
		if err != nil || !msg.GetAck() {
			t.Fatalf("heartbeat %d ack = %+v, %v", i+1, msg, err)
		}
	}
	if calls := h.checks.resolveHostCalls(); calls != 1 {
		t.Fatalf("ResolveHost calls after 100 heartbeats inside one interval = %d, want 1", calls)
	}

	h.advance(heartbeatResolveInterval)
	if err := stream.Send(heartbeat("agent-1", "stale")); err != nil {
		t.Fatal(err)
	}
	msg, err := recvWithin(t, stream, recvTimeout)
	if err != nil || msg.GetCheckConfig().GetVersion() != "v1" {
		t.Fatalf("heartbeat after the interval = %+v, %v, want the current set", msg, err)
	}
	if calls := h.checks.resolveHostCalls(); calls != 2 {
		t.Fatalf("ResolveHost calls once the interval passed = %d, want 2", calls)
	}
}

// TestReconnectingDoesNotResetTheResolveInterval: the interval belongs to
// the agent, not to one stream, or opening a new stream per heartbeat
// would buy a resolve each time.
func TestReconnectingDoesNotResetTheResolveInterval(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	h.checks.setResolved(hostID, checks.ResolvedSet{HostID: hostID, Version: "v1"})
	for i := range 3 {
		stream, err := (*client).AgentStream(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(heartbeat("agent-1", "stale")); err != nil {
			t.Fatal(err)
		}
		if _, err := recvWithin(t, stream, recvTimeout); err != nil {
			t.Fatalf("stream %d: %v", i+1, err)
		}
		if err := stream.CloseSend(); err != nil {
			t.Fatal(err)
		}
	}
	if calls := h.checks.resolveHostCalls(); calls != 1 {
		t.Fatalf("ResolveHost calls over three streams inside one interval = %d, want 1", calls)
	}
}

// TestAThirdStreamOfOneAgentEndsTheOldest: an agent holds one stream, two
// while a reconnect overlaps a connection the manager has not noticed is
// dead. A third ends the oldest — with its own status, not the
// revocation's — so one identity cannot pile up streams.
func TestAThirdStreamOfOneAgentEndsTheOldest(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	h.checks.setResolved(hostID, checks.ResolvedSet{HostID: hostID, Version: "v1"})

	var streams []receiver
	for i := range 3 {
		stream, err := (*client).AgentStream(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		// One round trip, so the stream is tracked before the next opens.
		if err := stream.Send(heartbeat("agent-1", "v1")); err != nil {
			t.Fatal(err)
		}
		if _, err := recvWithin(t, stream, recvTimeout); err != nil {
			t.Fatalf("stream %d: %v", i+1, err)
		}
		streams = append(streams, stream)
	}
	_, err := recvWithin(t, streams[0], recvTimeout)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("the oldest stream after a third opened: %v, want ResourceExhausted", err)
	}
	if n := openStreams(h.svc.streams, hostID); n != 2 {
		t.Fatalf("open streams of the agent = %d, want 2", n)
	}
}

func openStreams(r *StreamRegistry, hostID uuid.UUID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byHost[hostID])
}
