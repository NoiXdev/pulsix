// The configuration check the heartbeat records (backlog 3.13c): every
// comparison processHeartbeat makes between the version the agent reports
// and the one it resolves is handed to the registry, and only those.
package agentapi

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

// serialOf is the certificate serial the registry holds for hostID.
func serialOf(t *testing.T, h *harness, hostID uuid.UUID) string {
	t.Helper()
	a, err := h.store.GetByHost(context.Background(), testOrg, hostID)
	if err != nil {
		t.Fatal(err)
	}
	return a.CertSerial
}

// TestHeartbeatRecordsEachComparison: a heartbeat whose resolve ran
// records what the agent reported against what was resolved — a mismatch
// (which is also answered with the set) and a match alike.
func TestHeartbeatRecordsEachComparison(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	serial := serialOf(t, h, hostID)
	h.checks.setResolved(hostID, checks.ResolvedSet{HostID: hostID, Version: "v1"})
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if err := stream.Send(heartbeat("agent-1", "v0")); err != nil {
		t.Fatal(err)
	}
	if msg, err := recvWithin(t, stream, recvTimeout); err != nil || msg.GetCheckConfig().GetVersion() != "v1" {
		t.Fatalf("mismatched heartbeat = %+v, %v; want the set pushed", msg, err)
	}
	h.advance(heartbeatResolveInterval)
	if err := stream.Send(heartbeat("agent-1", "v1")); err != nil {
		t.Fatal(err)
	}
	if msg, err := recvWithin(t, stream, recvTimeout); err != nil || msg.GetCheckConfig() != nil {
		t.Fatalf("converged heartbeat = %+v, %v; want an ack alone", msg, err)
	}

	want := []configCheckCall{
		{serial: serial, reported: "v0", expected: "v1"},
		{serial: serial, reported: "v1", expected: "v1"},
	}
	if got := h.store.recordedConfigChecks(); !slices.Equal(got, want) {
		t.Fatalf("recorded checks = %+v, want %+v", got, want)
	}
}

// TestHeartbeatWithoutAComparisonRecordsNothing: a heartbeat inside the
// resolve throttle, one whose resolve failed, and one on a manager without
// a checks service compared nothing, so they record nothing — the stored
// check keeps saying what the last real comparison found.
func TestHeartbeatWithoutAComparisonRecordsNothing(t *testing.T) {
	t.Run("throttled", func(t *testing.T) {
		h := newHarnessWithChecks(t)
		resp, client := enrollAnAgent(t, h, "web-1")
		hostID := uuid.MustParse(resp.GetHostId())
		h.checks.setResolved(hostID, checks.ResolvedSet{HostID: hostID, Version: "v1"})
		stream, err := (*client).AgentStream(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{"v1", "v0", "v1"} {
			if err := stream.Send(heartbeat("agent-1", v)); err != nil {
				t.Fatal(err)
			}
			if _, err := recvWithin(t, stream, recvTimeout); err != nil {
				t.Fatal(err)
			}
		}
		if got := h.store.recordedConfigChecks(); len(got) != 1 || got[0].reported != "v1" {
			t.Fatalf("recorded checks inside one resolve interval = %+v, want the first heartbeat's only", got)
		}
	})
	t.Run("resolve failed", func(t *testing.T) {
		h := newHarnessWithChecks(t)
		resp, client := enrollAnAgent(t, h, "web-1")
		hostID := uuid.MustParse(resp.GetHostId())
		h.checks.setResolved(hostID, checks.ResolvedSet{HostID: hostID, Version: "v1"})
		h.checks.mu.Lock()
		h.checks.resolveErr = errors.New("db down")
		h.checks.mu.Unlock()
		stream, err := (*client).AgentStream(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(heartbeat("agent-1", "v0")); err != nil {
			t.Fatal(err)
		}
		if _, err := recvWithin(t, stream, recvTimeout); err != nil {
			t.Fatal(err)
		}
		if got := h.store.recordedConfigChecks(); len(got) != 0 {
			t.Fatalf("recorded checks after a failed resolve = %+v, want none", got)
		}
	})
	t.Run("no checks service", func(t *testing.T) {
		h := newHarness(t)
		_, client := enrollAnAgent(t, h, "web-1")
		stream, err := (*client).AgentStream(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(heartbeat("agent-1", "v0")); err != nil {
			t.Fatal(err)
		}
		if _, err := recvWithin(t, stream, recvTimeout); err != nil {
			t.Fatal(err)
		}
		if got := h.store.recordedConfigChecks(); len(got) != 0 {
			t.Fatalf("recorded checks without a checks service = %+v, want none", got)
		}
	})
}

// TestHeartbeatConfigCheckFaultKeepsTheStream: failing to store the check
// is logged and nothing more — the heartbeat is acknowledged, a mismatch
// is still answered with the set, and the stream lives on. The check is
// display data; the next comparison writes it again.
func TestHeartbeatConfigCheckFaultKeepsTheStream(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	h.checks.setResolved(hostID, checks.ResolvedSet{HostID: hostID, Version: "v1"})
	h.store.mu.Lock()
	h.store.configCheckErr = errors.New("db down")
	h.store.mu.Unlock()
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(heartbeat("agent-1", "v0")); err != nil {
		t.Fatal(err)
	}
	msg, err := recvWithin(t, stream, recvTimeout)
	if err != nil || !msg.GetAck() || msg.GetCheckConfig().GetVersion() != "v1" {
		t.Fatalf("heartbeat while the check cannot be stored = %+v, %v; want an ack with the set", msg, err)
	}
	if !strings.Contains(h.logs.String(), "record agent configuration check") {
		t.Fatalf("the failed write was not logged:\n%s", h.logs.String())
	}

	h.store.mu.Lock()
	h.store.configCheckErr = nil
	h.store.mu.Unlock()
	h.advance(heartbeatResolveInterval)
	if err := stream.Send(heartbeat("agent-1", "v1")); err != nil {
		t.Fatal(err)
	}
	if msg, err := recvWithin(t, stream, recvTimeout); err != nil || !msg.GetAck() {
		t.Fatalf("heartbeat after the fault cleared = %+v, %v", msg, err)
	}
	if got := h.store.recordedConfigChecks(); len(got) != 1 || got[0].reported != "v1" {
		t.Fatalf("recorded checks after the fault cleared = %+v, want the second heartbeat's", got)
	}
}
