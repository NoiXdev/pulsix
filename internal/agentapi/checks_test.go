// Configuration delivery and sample ingest: the heartbeat-driven
// convergence between what an agent reports and what ResolveHost
// computes today, the out-of-band PushHost write-path hook, and
// CheckResults mapping into checks.Observation.
package agentapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
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

// TestHeartbeatChecksInvalidInputStaysInternal: checks.ErrInvalidInput
// surfacing from the heartbeat path (ResolveHost, e.g. via a persisted
// monitoring group whose selector no longer parses under the current
// grammar) must never become InvalidArgument — that mapping exists only
// for the check-results path (errCheckResultsRejected), because an
// InvalidArgument here would reject every heartbeat from every agent in
// the organization over the manager's own bad data, not anything any one
// agent did. This is the mirror image of the same rule
// TestCheckResultsInvalidInputIsInvalidArgument pins for the path where
// InvalidArgument is in fact correct.
func TestHeartbeatChecksInvalidInputStaysInternal(t *testing.T) {
	h := newHarnessWithChecks(t)
	_, client := enrollAnAgent(t, h, "web-1")
	h.checks.mu.Lock()
	h.checks.resolveErr = checks.ErrInvalidInput
	h.checks.mu.Unlock()
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(heartbeat("agent-1", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.Internal {
		t.Fatalf("checks.ErrInvalidInput from the heartbeat path: %v, want Internal (never InvalidArgument)", err)
	}
}

// TestPushHostOrgMismatchNeverReachesAnotherTenantsAgent: PushToHost keys
// on the org a stream actually authenticated as (trackedStream.orgID, set
// once at track time from the connecting agent's own certificate
// identity — never trusted from a caller-supplied argument alone). A
// caller confusing (orgA, hostB) must not arm hostB's real org's agent
// with a CheckConfig it never asked for. The two messages carry distinct,
// recognisable versions specifically so a wrongly-delivered mismatched
// push cannot hide behind a correctly-delivered one that happens to carry
// identical content.
func TestPushHostOrgMismatchNeverReachesAnotherTenantsAgent(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
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

	wrongOrg := uuid.Must(uuid.NewV7())
	reached := h.svc.Streams().PushToHost(wrongOrg, hostID,
		&agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{Version: "should-never-arrive"}})
	if reached != 0 {
		t.Fatalf("PushToHost with a mismatched org reached %d streams, want 0", reached)
	}

	// The real org still reaches it — proving the 0 above is the org
	// check working, not a registry that has forgotten the stream.
	reached = h.svc.Streams().PushToHost(testOrg, hostID,
		&agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{Version: "correct-org"}})
	if reached != 1 {
		t.Fatalf("PushToHost with the matching org reached %d streams, want 1", reached)
	}
	msg, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg := msg.GetCheckConfig(); cfg == nil || cfg.GetVersion() != "correct-org" {
		t.Fatalf("received = %+v, want only the matching-org push ever delivered", cfg)
	}
}

// recomputeVersion re-derives a version from a pushed CheckConfig's own
// specs using internal/checks's documented, pinned canonical encoding
// (resolver.go: each check as
// "key\x00plugin\x00interval_seconds\x00canonical-json(params)\x00",
// concatenated in key order, then SHA-256 hex — the exact format Task 3's
// own TestVersionCanonicalEncodingIsPinned pins byte-for-byte). It is
// implemented completely independently of checkConfigFor/canonicalParamsJSON
// (no shared code, no shared constant, and ParamsJson is taken verbatim
// from the wire spec rather than re-derived from a params map) — the same
// discipline Task 3's own encoding test uses — so it cannot pass merely
// because both sides called the same, possibly buggy, helper.
func recomputeVersion(specs []*agentpb.CheckSpec) string {
	sorted := append([]*agentpb.CheckSpec(nil), specs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].GetKey() < sorted[j].GetKey() })
	var buf bytes.Buffer
	for _, s := range sorted {
		buf.WriteString(s.GetKey())
		buf.WriteByte(0)
		buf.WriteString(s.GetPlugin())
		buf.WriteByte(0)
		buf.WriteString(strconv.FormatInt(int64(s.GetIntervalSeconds()), 10))
		buf.WriteByte(0)
		buf.WriteString(s.GetParamsJson())
		buf.WriteByte(0)
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

// TestPushedNestedParamsReproduceTheVersion is Review Focus 3's params
// half: canonicalParamsJSON is a deliberate, documented duplicate of
// internal/checks's own unexported canonicalParams, and the only params
// assertion before this test was the trivial empty-params case ("{}") —
// the json.Marshal branch (anything with actual parameters, especially
// nested ones) was never exercised, so a divergence between the two
// encoders — the exact failure that turns into a permanent push loop,
// since an agent that re-hashes what it was handed would never converge
// — could have shipped unnoticed.
//
// wantVersion is computed by hand here, over the *original* checks.Resolved
// params map, using nothing but the documented canonical-JSON rule
// (sorted map keys — encoding/json's own behaviour, not anything
// canonicalParamsJSON does) and a hand-written expected JSON literal, so
// it does not call canonicalParamsJSON either. The test then recomputes a
// version from the *pushed* CheckSpec (via recomputeVersion, which reads
// only the wire fields — never the original params map) and asserts it
// equals wantVersion: the only way that can hold is if
// checkConfigFor/canonicalParamsJSON rendered the nested params into
// exactly the same canonical bytes the documented format promises.
func TestPushedNestedParamsReproduceTheVersion(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())

	params := map[string]any{
		"mounts": []any{"/data", "/"},
		"z":      1,
	}
	// Hand-written, not produced by calling json.Marshal in this test:
	// "mounts" sorts before "z"; the slice's own order is preserved
	// (only map keys are sorted, at every nesting level).
	const wantParamsJSON = `{"mounts":["/data","/"],"z":1}`
	wantEncoded := "disk\x00disk\x0090\x00" + wantParamsJSON + "\x00"
	sum := sha256.Sum256([]byte(wantEncoded))
	wantVersion := hex.EncodeToString(sum[:])

	h.checks.setResolved(hostID, checks.ResolvedSet{
		HostID: hostID,
		Checks: []checks.Resolved{{Key: "disk", Plugin: "disk", Interval: 90 * time.Second, Params: params}},
		// Version is deliberately the same hand-computed value a real
		// checks.Service would have produced for this exact Resolved —
		// not asserted directly (the fake passes it through verbatim
		// regardless), but what makes the recompute-from-pushed check
		// below meaningful rather than circular.
		Version: wantVersion,
	})
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(heartbeat("agent-1", "")); err != nil {
		t.Fatal(err)
	}
	msg, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	cfg := msg.GetCheckConfig()
	if cfg == nil || len(cfg.GetChecks()) != 1 {
		t.Fatalf("pushed config = %+v", cfg)
	}
	spec := cfg.GetChecks()[0]
	if spec.GetParamsJson() != wantParamsJSON {
		t.Fatalf("pushed params_json = %q, want %q", spec.GetParamsJson(), wantParamsJSON)
	}
	if cfg.GetVersion() != wantVersion {
		t.Fatalf("pushed version = %q, want %q", cfg.GetVersion(), wantVersion)
	}
	if got := recomputeVersion(cfg.GetChecks()); got != wantVersion {
		t.Fatalf("version recomputed from the pushed specs = %q, want %q (an agent re-hashing what it was handed would never converge)", got, wantVersion)
	}
}

// TestPushOrgResolvesOnceForEveryMonitoredHost: the organization-wide
// push — what an organization-bound definition change triggers — computes
// every monitored host's configuration in ONE resolver pass (ResolveOrg,
// which expands each monitoring group's selector once for the whole
// organization) rather than one full ResolveHost per host. On a fleet of
// any size that difference is the whole point of the method: the per-host
// path re-reads the definitions, the groups and every group's selector
// from scratch for each host.
func TestPushOrgResolvesOnceForEveryMonitoredHost(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	other := uuid.Must(uuid.NewV7())
	h.checks.setOrgResolved(testOrg, map[uuid.UUID]checks.ResolvedSet{
		hostID: {HostID: hostID, Checks: []checks.Resolved{{Key: "cpu", Plugin: "cpu", Interval: 30 * time.Second}}, Version: "org-pushed"},
		// A second monitored host with no open stream: pushed to nobody,
		// and not an error (PushHost's own "not connected is a no-op").
		other: {HostID: other, Version: "org-pushed-2"},
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

	if err := h.svc.PushOrg(t.Context(), testOrg); err != nil {
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
	if cfg == nil || cfg.GetVersion() != "org-pushed" || len(cfg.GetChecks()) != 1 {
		t.Fatalf("pushed config = %+v", cfg)
	}
	if calls := h.checks.resolveOrgCalls(); calls != 1 {
		t.Fatalf("ResolveOrg calls = %d, want exactly 1 for the whole organization", calls)
	}
	if calls := h.checks.resolveHostCalls(); calls != 0 {
		t.Fatalf("ResolveHost calls = %d, want 0: an org push must not fan out per host", calls)
	}
}

// TestPushOrgWithoutChecksPortIsNoOp mirrors PushHost's own nil-tolerance:
// the checks domain can wire in independently of this package shipping.
func TestPushOrgWithoutChecksPortIsNoOp(t *testing.T) {
	h := newHarness(t)
	if err := h.svc.PushOrg(t.Context(), testOrg); err != nil {
		t.Fatalf("PushOrg without a checks port = %v, want nil", err)
	}
}

// TestPushOrgResolveFaultPropagates: like PushHost, an org push reports a
// resolver fault to its caller rather than silently pushing nothing — the
// caller logs it, and the agents' own re-assert is what repairs the miss.
func TestPushOrgResolveFaultPropagates(t *testing.T) {
	h := newHarnessWithChecks(t)
	h.checks.resolveErr = errors.New("resolver down")
	if err := h.svc.PushOrg(t.Context(), testOrg); err == nil {
		t.Fatal("a resolve fault must propagate to PushOrg's own caller")
	}
}
