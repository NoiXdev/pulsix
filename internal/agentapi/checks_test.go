// Configuration delivery and sample ingest: the heartbeat-driven
// convergence between what an agent reports and what ResolveHost
// computes today, the out-of-band PushHost write-path hook, and
// CheckResults mapping into checks.Observation.
package agentapi

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

func heartbeat(version, configVersion string) *agentpb.AgentMessage {
	return &agentpb.AgentMessage{Body: &agentpb.AgentMessage_Heartbeat{
		Heartbeat: &agentpb.Heartbeat{Version: version, ConfigVersion: configVersion},
	}}
}

// TestHeartbeatEmptyConfigVersionReceivesFullSet: an agent that has never
// converged (config_version "") gets the whole resolved set on its very
// first heartbeat — "on connect, the agent reports its version before
// anything else, so a push missed during a disconnect is repaired
// immediately" (design, "Configuration delivery").
func TestHeartbeatEmptyConfigVersionReceivesFullSet(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	h.checks.setResolved(hostID, checks.ResolvedSet{
		HostID:  hostID,
		Checks:  []checks.Resolved{{Key: "cpu", Plugin: "cpu", Interval: 30 * time.Second}},
		Version: "v1",
	})
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(heartbeat("agent-1", "")); err != nil {
		t.Fatal(err)
	}
	msg, err := stream.Recv()
	if err != nil || !msg.GetAck() {
		t.Fatalf("heartbeat ack = %+v, %v", msg, err)
	}
	cfg := msg.GetCheckConfig()
	if cfg == nil {
		t.Fatal("an empty config_version must receive the full set")
	}
	if cfg.GetVersion() != "v1" || len(cfg.GetChecks()) != 1 {
		t.Fatalf("pushed config = %+v", cfg)
	}
	spec := cfg.GetChecks()[0]
	if spec.GetKey() != "cpu" || spec.GetPlugin() != "cpu" || spec.GetIntervalSeconds() != 30 || spec.GetParamsJson() != "{}" {
		t.Fatalf("pushed spec = %+v", spec)
	}
}

// TestHeartbeatSameVersionReceivesNothing: once the agent reports exactly
// the version ResolveHost computes today, the heartbeat is acknowledged
// with no CheckConfig at all.
func TestHeartbeatSameVersionReceivesNothing(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	h.checks.setResolved(hostID, checks.ResolvedSet{HostID: hostID, Version: "v1"})
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(heartbeat("agent-1", "v1")); err != nil {
		t.Fatal(err)
	}
	msg, err := stream.Recv()
	if err != nil || !msg.GetAck() {
		t.Fatalf("heartbeat ack = %+v, %v", msg, err)
	}
	if msg.GetCheckConfig() != nil {
		t.Fatalf("a heartbeat already at the current version must not be pushed to: %+v", msg.GetCheckConfig())
	}
}

// TestHeartbeatChangedDefinitionPushesOnce: the host converges at v1,
// then a definition edit moves the resolved version to v2 — the very next
// heartbeat still reporting v1 gets pushed v2 exactly once, and the
// heartbeat after that (now reporting v2) gets nothing further.
func TestHeartbeatChangedDefinitionPushesOnce(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	h.checks.setResolved(hostID, checks.ResolvedSet{HostID: hostID, Version: "v1"})
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(heartbeat("agent-1", "v1")); err != nil {
		t.Fatal(err)
	}
	if msg, err := stream.Recv(); err != nil || msg.GetCheckConfig() != nil {
		t.Fatalf("converged heartbeat = %+v, %v, want no push", msg, err)
	}

	// The definition changes underneath the host.
	h.checks.setResolved(hostID, checks.ResolvedSet{
		HostID:  hostID,
		Checks:  []checks.Resolved{{Key: "disk", Plugin: "disk", Interval: 60 * time.Second}},
		Version: "v2",
	})
	if err := stream.Send(heartbeat("agent-1", "v1")); err != nil {
		t.Fatal(err)
	}
	msg, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg := msg.GetCheckConfig(); cfg == nil || cfg.GetVersion() != "v2" {
		t.Fatalf("stale version after a definition change = %+v, want a push to v2", cfg)
	}

	if err := stream.Send(heartbeat("agent-1", "v2")); err != nil {
		t.Fatal(err)
	}
	if msg, err := stream.Recv(); err != nil || msg.GetCheckConfig() != nil {
		t.Fatalf("heartbeat after converging on v2 = %+v, %v, want no further push", msg, err)
	}
}

// TestConvergenceUnrecognisedVersionPushesOnceThenConverges is Review
// Focus 3: the manager keeps no record of what it last sent, so a version
// it does not recognise at all — invented, or left over from a
// definition the manager has since deleted — is answered exactly the way
// any other mismatch is: the current set, once. The agent's own next
// reported version (whatever the manager just sent) is what ends the
// exchange; nothing here could loop even if the same unrecognised version
// somehow arrived twice, since each comparison is independent and stateless.
func TestConvergenceUnrecognisedVersionPushesOnceThenConverges(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	// The resolved set never changes in this test — only the agent's own
	// reported version does. The manager's answer is the same "current
	// set" either way, proving it never special-cases "this version is
	// unfamiliar" versus "this version is merely stale".
	h.checks.setResolved(hostID, checks.ResolvedSet{
		HostID:  hostID,
		Checks:  []checks.Resolved{{Key: "cpu", Plugin: "cpu", Interval: 30 * time.Second}},
		Version: "the-only-version-that-ever-existed",
	})
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// An invented version: not "", not anything ResolveHost ever
	// produced, not anything this manager process has ever seen before —
	// e.g. a definition the agent still remembers that has since been
	// deleted entirely.
	if err := stream.Send(heartbeat("agent-1", "invented-"+uuid.Must(uuid.NewV7()).String())); err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || !first.GetAck() {
		t.Fatalf("first heartbeat ack = %+v, %v", first, err)
	}
	cfg := first.GetCheckConfig()
	if cfg == nil || cfg.GetVersion() != "the-only-version-that-ever-existed" {
		t.Fatalf("unrecognised version = %+v, want exactly one push of the current set", cfg)
	}

	// The agent now reports precisely the version that push established.
	if err := stream.Send(heartbeat("agent-1", cfg.GetVersion())); err != nil {
		t.Fatal(err)
	}
	second, err := stream.Recv()
	if err != nil || !second.GetAck() {
		t.Fatalf("second heartbeat ack = %+v, %v", second, err)
	}
	if second.GetCheckConfig() != nil {
		t.Fatalf("the heartbeat that converges must not be pushed to again: %+v", second.GetCheckConfig())
	}

	// A third heartbeat, still reporting the converged version, must also
	// receive nothing — convergence is not a one-shot fluke of round two.
	if err := stream.Send(heartbeat("agent-1", cfg.GetVersion())); err != nil {
		t.Fatal(err)
	}
	third, err := stream.Recv()
	if err != nil || !third.GetAck() || third.GetCheckConfig() != nil {
		t.Fatalf("third (still converged) heartbeat = %+v, %v, want ack only", third, err)
	}
	if calls := h.checks.resolveHostCalls(); calls != 3 {
		t.Fatalf("ResolveHost calls = %d, want 3 (one per heartbeat: convergence is compared fresh every time, never cached)", calls)
	}
}

// TestHeartbeatWithoutChecksPortNeverPushes: a nil Checks port (this
// package shipping ahead of the checks domain being wired) leaves every
// heartbeat a plain ack, regardless of what version the agent reports.
func TestHeartbeatWithoutChecksPortNeverPushes(t *testing.T) {
	h := newHarness(t) // no checks wired
	_, client := enrollAnAgent(t, h, "web-1")
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(heartbeat("agent-1", "anything-at-all")); err != nil {
		t.Fatal(err)
	}
	msg, err := stream.Recv()
	if err != nil || !msg.GetAck() {
		t.Fatalf("heartbeat without a checks port = %+v, %v", msg, err)
	}
	if msg.GetCheckConfig() != nil {
		t.Fatal("no checks port means never pushing a CheckConfig")
	}
}

// TestHeartbeatResolveFaultIsInternal: a checks-service fault while
// resolving is a server fault, matching every other server-fault branch
// on this stream.
func TestHeartbeatResolveFaultIsInternal(t *testing.T) {
	h := newHarnessWithChecks(t)
	_, client := enrollAnAgent(t, h, "web-1")
	h.checks.mu.Lock()
	h.checks.resolveErr = errors.New("db down")
	h.checks.mu.Unlock()
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(heartbeat("agent-1", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.Internal {
		t.Fatalf("resolve fault: %v, want Internal", err)
	}
}

// checkResult builds one wire result; exactly one of value, label, errText
// is meaningful, mirroring checks.Observation's own contract.
func checkResultValue(key, item, metric string, at time.Time, value float64) *agentpb.CheckResult {
	return &agentpb.CheckResult{CheckKey: key, Item: item, Metric: metric, At: timestamppb.New(at),
		Observation: &agentpb.CheckResult_Value{Value: value}}
}

func checkResultLabel(key, item, metric string, at time.Time, label string) *agentpb.CheckResult {
	return &agentpb.CheckResult{CheckKey: key, Item: item, Metric: metric, At: timestamppb.New(at),
		Observation: &agentpb.CheckResult_Label{Label: label}}
}

func checkResultError(key, item, metric string, at time.Time, errText string) *agentpb.CheckResult {
	return &agentpb.CheckResult{CheckKey: key, Item: item, Metric: metric, At: timestamppb.New(at),
		Observation: &agentpb.CheckResult_Error{Error: errText}}
}

// TestCheckResultsRecorded: a batch mapping a numeric, a discrete and an
// error observation each land on RecordObservations with the connected
// agent's own host id and the right shape, and the message is
// acknowledged.
func TestCheckResultsRecorded(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	h.checks.knownHosts = map[uuid.UUID]bool{hostID: true}
	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_CheckResults{CheckResults: &agentpb.CheckResults{
		Results: []*agentpb.CheckResult{
			checkResultValue("cpu", "", "used_percent", at, 12.5),
			checkResultLabel("service", "nginx", "state", at, "running"),
			checkResultError("disk", "/data", "used_percent", at, "collection failed"),
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	msg, err := stream.Recv()
	if err != nil || !msg.GetAck() {
		t.Fatalf("check results ack = %+v, %v", msg, err)
	}

	obs := h.checks.observations()
	if len(obs) != 3 {
		t.Fatalf("recorded observations = %d, want 3", len(obs))
	}
	for _, o := range obs {
		if o.HostID != hostID {
			t.Fatalf("observation host id = %s, want the connected agent's own %s", o.HostID, hostID)
		}
		if !o.At.Equal(at) {
			t.Fatalf("observation at = %s, want %s", o.At, at)
		}
	}
	if obs[0].CheckKey != "cpu" || obs[0].Metric != "used_percent" || obs[0].Value == nil || *obs[0].Value != 12.5 || obs[0].Label != nil || obs[0].Err != "" {
		t.Fatalf("value observation = %+v", obs[0])
	}
	if obs[1].CheckKey != "service" || obs[1].Item != "nginx" || obs[1].Label == nil || *obs[1].Label != "running" || obs[1].Value != nil || obs[1].Err != "" {
		t.Fatalf("label observation = %+v", obs[1])
	}
	if obs[2].CheckKey != "disk" || obs[2].Item != "/data" || obs[2].Err != "collection failed" || obs[2].Value != nil || obs[2].Label != nil {
		t.Fatalf("error observation = %+v", obs[2])
	}
}

// TestCheckResultsUnknownHostRejectedCountsNotError: RecordObservations's
// own "counted, never turned into an error for the batch" contract
// applies here too — a host the checks store does not recognise (its
// knownHosts is empty) is refused (rejected) without costing the agent
// its connection: the message is still forwarded and still acknowledged.
func TestCheckResultsUnknownHostRejectedCountsNotError(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	// knownHosts is non-nil and does not contain hostID: every observation
	// for this host is counted rejected by the fake, the same shape the
	// real store's own unknown-host lookup produces.
	h.checks.knownHosts = map[uuid.UUID]bool{}
	at := time.Now()

	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_CheckResults{CheckResults: &agentpb.CheckResults{
		Results: []*agentpb.CheckResult{checkResultValue("cpu", "", "used_percent", at, 1)},
	}}}); err != nil {
		t.Fatal(err)
	}
	msg, err := stream.Recv()
	if err != nil || !msg.GetAck() {
		t.Fatalf("results for an unrecognised host = %+v, %v, want a plain ack, not a stream error", msg, err)
	}
	obs := h.checks.observations()
	if len(obs) != 1 || obs[0].HostID != hostID {
		t.Fatalf("the unrecognised-host observation must still reach RecordObservations to be counted: got %+v, want one for %s", obs, hostID)
	}
}

// TestCheckResultsStoreFaultIsInternal: a genuine store failure (not a
// counted rejection) is a server fault.
func TestCheckResultsStoreFaultIsInternal(t *testing.T) {
	h := newHarnessWithChecks(t)
	_, client := enrollAnAgent(t, h, "web-1")
	h.checks.mu.Lock()
	h.checks.recordErr = errors.New("db down")
	h.checks.mu.Unlock()
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_CheckResults{CheckResults: &agentpb.CheckResults{
		Results: []*agentpb.CheckResult{checkResultValue("cpu", "", "used_percent", time.Now(), 1)},
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.Internal {
		t.Fatalf("store fault: %v, want Internal", err)
	}
}

// TestCheckResultsInvalidInputIsInvalidArgument: checks.ErrInvalidInput
// becomes InvalidArgument, matching the facts path's own
// agents.ErrInvalidInput mapping — the caller's fault, not the server's.
func TestCheckResultsInvalidInputIsInvalidArgument(t *testing.T) {
	h := newHarnessWithChecks(t)
	_, client := enrollAnAgent(t, h, "web-1")
	h.checks.mu.Lock()
	h.checks.recordErr = checks.ErrInvalidInput
	h.checks.mu.Unlock()
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_CheckResults{CheckResults: &agentpb.CheckResults{
		Results: []*agentpb.CheckResult{checkResultValue("cpu", "", "used_percent", time.Now(), 1)},
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid input: %v, want InvalidArgument", err)
	}
}

// TestCheckResultsDroppedWithoutChecksPort: a nil Checks port drops
// results with a server log and still acknowledges, the same posture
// FactsSink takes.
func TestCheckResultsDroppedWithoutChecksPort(t *testing.T) {
	h := newHarness(t) // no checks wired
	_, client := enrollAnAgent(t, h, "web-1")
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_CheckResults{CheckResults: &agentpb.CheckResults{
		Results: []*agentpb.CheckResult{checkResultValue("cpu", "", "used_percent", time.Now(), 1)},
	}}}); err != nil {
		t.Fatal(err)
	}
	if msg, err := stream.Recv(); err != nil || !msg.GetAck() {
		t.Fatalf("dropped results must still be acknowledged: %+v, %v", msg, err)
	}
}

// TestPushHostDeliversToConnectedAgent: the write-path hook — a
// definition, group or tag change calling PushHost — reaches an already
// open stream without waiting for its next heartbeat, and carries no Ack
// (it answers nothing the agent sent).
func TestPushHostDeliversToConnectedAgent(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	h.checks.setResolved(hostID, checks.ResolvedSet{
		HostID:  hostID,
		Checks:  []checks.Resolved{{Key: "cpu", Plugin: "cpu", Interval: 30 * time.Second}},
		Version: "pushed-version",
	})
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for trackedStreams(h.svc.Streams(), hostID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stream never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := h.svc.PushHost(t.Context(), testOrg, hostID); err != nil {
		t.Fatal(err)
	}
	msg, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.GetAck() {
		t.Fatal("an out-of-band push must not carry Ack")
	}
	cfg := msg.GetCheckConfig()
	if cfg == nil || cfg.GetVersion() != "pushed-version" || len(cfg.GetChecks()) != 1 {
		t.Fatalf("pushed config = %+v", cfg)
	}
}

// TestPushHostNotConnectedIsNoOp: a host with no open stream is exactly
// that — a no-op, not an error; the design's own words are "a host that
// is not connected is a no-op, because the agent's own re-assert heals it
// within the configuration interval".
func TestPushHostNotConnectedIsNoOp(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, _ := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	h.checks.setResolved(hostID, checks.ResolvedSet{HostID: hostID, Version: "v1"})
	if err := h.svc.PushHost(t.Context(), testOrg, hostID); err != nil {
		t.Fatalf("push to a disconnected host = %v, want nil (a no-op)", err)
	}
}

// TestPushHostWithoutChecksPortIsNoOp: PushHost is nil-tolerant exactly
// like the heartbeat path — a manager that has not wired a checks service
// yet must not panic or error when something calls PushHost anyway.
func TestPushHostWithoutChecksPortIsNoOp(t *testing.T) {
	h := newHarness(t) // no checks wired
	resp, _ := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	if err := h.svc.PushHost(t.Context(), testOrg, hostID); err != nil {
		t.Fatalf("push without a checks port = %v, want nil", err)
	}
}

// TestPushHostResolveFaultPropagates: unlike the heartbeat path (which
// only ever logs and answers a plain ack on the next reconnect), PushHost
// is called synchronously from an API write path and reports a resolve
// fault back to its caller rather than swallowing it.
func TestPushHostResolveFaultPropagates(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, _ := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	h.checks.mu.Lock()
	h.checks.resolveErr = errors.New("db down")
	h.checks.mu.Unlock()
	if err := h.svc.PushHost(t.Context(), testOrg, hostID); err == nil {
		t.Fatal("a resolve fault must propagate to PushHost's own caller")
	}
}
