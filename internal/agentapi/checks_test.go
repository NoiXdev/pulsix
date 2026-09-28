// Configuration delivery and sample ingest: the heartbeat-driven
// convergence between what an agent reports and what ResolveHost
// computes today, the out-of-band PushHost write-path hook, and
// CheckResults mapping into checks.Observation.
package agentapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	msg, err := recvWithin(t, stream, recvTimeout)
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
	msg, err := recvWithin(t, stream, recvTimeout)
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
	if msg, err := recvWithin(t, stream, recvTimeout); err != nil || msg.GetCheckConfig() != nil {
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
	msg, err := recvWithin(t, stream, recvTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if cfg := msg.GetCheckConfig(); cfg == nil || cfg.GetVersion() != "v2" {
		t.Fatalf("stale version after a definition change = %+v, want a push to v2", cfg)
	}

	if err := stream.Send(heartbeat("agent-1", "v2")); err != nil {
		t.Fatal(err)
	}
	if msg, err := recvWithin(t, stream, recvTimeout); err != nil || msg.GetCheckConfig() != nil {
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
	first, err := recvWithin(t, stream, recvTimeout)
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
	second, err := recvWithin(t, stream, recvTimeout)
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
	third, err := recvWithin(t, stream, recvTimeout)
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
	msg, err := recvWithin(t, stream, recvTimeout)
	if err != nil || !msg.GetAck() {
		t.Fatalf("heartbeat without a checks port = %+v, %v", msg, err)
	}
	if msg.GetCheckConfig() != nil {
		t.Fatal("no checks port means never pushing a CheckConfig")
	}
}

// TestHeartbeatResolveFaultKeepsTheStreamAlive pins the contract that
// replaced TestHeartbeatResolveFaultIsInternal.
//
// The heartbeat used to answer a ResolveHost fault with Internal, which
// ends the RPC. Before the check pipeline the heartbeat touched one row;
// it now reads the organization's hosts, definitions, groups and one
// selector query per group, several times a minute per agent — so a
// statement timeout, pool exhaustion or a failover blip cut EVERY stream
// in the installation at the same instant, and the fleet then reconnected
// into the database that was already failing.
//
// The push path has always logged and dropped the identical failure
// ("the agents' own re-assert will repair it", internal/api's
// pushAffected), precisely because convergence heals. This path now
// agrees: the heartbeat is still acknowledged, no configuration rides
// along, the stream survives — and the very next heartbeat, once the
// fault clears, delivers the set. Nothing is lost that the next
// heartbeat would not have delivered anyway.
func TestHeartbeatResolveFaultKeepsTheStreamAlive(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	h.checks.setResolved(hostID, checks.ResolvedSet{
		HostID:  hostID,
		Checks:  []checks.Resolved{{Key: "cpu", Plugin: "cpu", Interval: 30 * time.Second}},
		Version: "v1",
	})
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
	msg, err := recvWithin(t, stream, recvTimeout)
	if err != nil {
		t.Fatalf("a resolve fault must not end the stream: %v", err)
	}
	if !msg.GetAck() {
		t.Fatalf("heartbeat during a resolve fault = %+v, want an ack", msg)
	}
	if msg.GetCheckConfig() != nil {
		t.Fatalf("nothing was resolved, so nothing may be pushed: %+v", msg.GetCheckConfig())
	}

	// The fault clears; the next heartbeat heals, on the same stream.
	h.checks.mu.Lock()
	h.checks.resolveErr = nil
	h.checks.mu.Unlock()
	if err := stream.Send(heartbeat("agent-1", "")); err != nil {
		t.Fatal(err)
	}
	healed, err := recvWithin(t, stream, recvTimeout)
	if err != nil || !healed.GetAck() {
		t.Fatalf("heartbeat after the fault cleared = %+v, %v", healed, err)
	}
	if cfg := healed.GetCheckConfig(); cfg == nil || cfg.GetVersion() != "v1" {
		t.Fatalf("the next heartbeat must deliver the set: %+v", cfg)
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
	msg, err := recvWithin(t, stream, recvTimeout)
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
	msg, err := recvWithin(t, stream, recvTimeout)
	if err != nil || !msg.GetAck() {
		t.Fatalf("results for an unrecognised host = %+v, %v, want a plain ack, not a stream error", msg, err)
	}
	obs := h.checks.observations()
	if len(obs) != 1 || obs[0].HostID != hostID {
		t.Fatalf("the unrecognised-host observation must still reach RecordObservations to be counted: got %+v, want one for %s", obs, hostID)
	}
}

// lockedBuffer is a slog sink a test can read while the server's own
// goroutine is still writing to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestCheckResultsRejectionLogNamesTheCause: the one thing that happens
// to a rejected observation in this milestone is a log line — there is no
// metrics endpoint anywhere in Pulsix yet (backlog 3.27) — so that line
// has to carry what an operator would act on. A batch nothing survived is
// not "partially" anything, and "rejected: 1" that could equally mean a
// skewed clock, a broken agent build or a deleted host sends whoever
// reads it looking in three places.
func TestCheckResultsRejectionLogNamesTheCause(t *testing.T) {
	h := newHarnessWithChecks(t)
	_, client := enrollAnAgent(t, h, "web-1")
	// Every observation in the batch is for a host the store does not
	// recognise, so nothing is accepted at all.
	h.checks.knownHosts = map[uuid.UUID]bool{}

	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_CheckResults{CheckResults: &agentpb.CheckResults{
		Results: []*agentpb.CheckResult{
			checkResultValue("cpu", "", "used_percent", at, 1),
			checkResultValue("cpu", "", "load1", at, 2),
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	if msg, err := recvWithin(t, stream, recvTimeout); err != nil || !msg.GetAck() {
		t.Fatalf("check results ack = %+v, %v", msg, err)
	}

	captured := logs.String()
	if strings.Contains(captured, "partially") {
		t.Errorf("nothing was accepted, so the batch was not PARTIALLY rejected:\n%s", captured)
	}
	for _, want := range []string{
		"agent check results rejected in full",
		"accepted=0",
		"rejected=2",
		"rejected_unknown_host=2",
		// The other two causes are named as well, at zero: a line that
		// omits them cannot be read as "not the clock, not the agent".
		"rejected_implausible_at=0",
		"rejected_malformed=0",
	} {
		if !strings.Contains(captured, want) {
			t.Errorf("log does not carry %q:\n%s", want, captured)
		}
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
	if _, err := recvWithin(t, stream, recvTimeout); status.Code(err) != codes.Internal {
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
	if _, err := recvWithin(t, stream, recvTimeout); status.Code(err) != codes.InvalidArgument {
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
	if msg, err := recvWithin(t, stream, recvTimeout); err != nil || !msg.GetAck() {
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
	msg, err := recvWithin(t, stream, recvTimeout)
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

// TestHeartbeatChecksInvalidInputNeverBlamesTheAgent: checks.ErrInvalidInput
// surfacing from the heartbeat path (ResolveHost, e.g. via a persisted
// monitoring group whose selector no longer parses under the current
// grammar) is the manager's own data being wrong, not anything this agent
// did — so it must never be answered InvalidArgument, and since
// TestHeartbeatResolveFaultKeepsTheStreamAlive it must not end the stream
// either. Same sentinel, opposite verdict from the check-results path
// TestCheckResultsInvalidInputIsInvalidArgument pins: there the agent
// really is the one at fault.
func TestHeartbeatChecksInvalidInputNeverBlamesTheAgent(t *testing.T) {
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
	msg, err := recvWithin(t, stream, recvTimeout)
	if err != nil {
		t.Fatalf("checks.ErrInvalidInput from the heartbeat path ended the stream with %v (code %s); "+
			"one unparseable stored selector must not disconnect the organization's whole fleet", err, status.Code(err))
	}
	if !msg.GetAck() || msg.GetCheckConfig() != nil {
		t.Fatalf("heartbeat = %+v, want a bare ack", msg)
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
	msg, err := recvWithin(t, stream, recvTimeout)
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
	msg, err := recvWithin(t, stream, recvTimeout)
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
	msg, err := recvWithin(t, stream, recvTimeout)
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

// TestPushHostsResolvesOnceForTheBatch: the many-hosts push — what a
// monitoring group's selector change triggers, where the affected set is
// every host the old and the new selector match — resolves the batch in
// one pass, exactly like PushOrg does for a whole organization. A
// per-host loop would re-read the definitions, the groups and every
// group's expansion once per host, inside the request.
func TestPushHostsResolvesOnceForTheBatch(t *testing.T) {
	h := newHarnessWithChecks(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	other := uuid.Must(uuid.NewV7())
	h.checks.setResolved(hostID, checks.ResolvedSet{
		HostID: hostID, Checks: []checks.Resolved{{Key: "cpu", Plugin: "cpu", Interval: 30 * time.Second}}, Version: "batch-pushed",
	})
	h.checks.setResolved(other, checks.ResolvedSet{HostID: other, Version: "batch-pushed-2"})
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

	if err := h.svc.PushHosts(t.Context(), testOrg, []uuid.UUID{hostID, other}); err != nil {
		t.Fatal(err)
	}
	msg, err := recvWithin(t, stream, recvTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if cfg := msg.GetCheckConfig(); cfg == nil || cfg.GetVersion() != "batch-pushed" {
		t.Fatalf("pushed config = %+v", cfg)
	}
	if calls := h.checks.resolveHostsCalls(); calls != 1 {
		t.Fatalf("ResolveHosts calls = %d, want exactly 1 for the batch", calls)
	}
	if calls := h.checks.resolveHostCalls(); calls != 0 {
		t.Fatalf("ResolveHost calls = %d, want 0: a batch push must not fan out per host", calls)
	}
}

// TestPushHostIsOnePathWithPushHosts: the single-host push is the batch of
// one, so there is one resolve path to keep correct, not two that could
// answer differently for the same host.
func TestPushHostIsOnePathWithPushHosts(t *testing.T) {
	h := newHarnessWithChecks(t)
	hostID := uuid.Must(uuid.NewV7())
	h.checks.setResolved(hostID, checks.ResolvedSet{HostID: hostID, Version: "v1"})
	if err := h.svc.PushHost(t.Context(), testOrg, hostID); err != nil {
		t.Fatal(err)
	}
	if calls := h.checks.resolveHostsCalls(); calls != 1 {
		t.Fatalf("PushHost made %d ResolveHosts calls, want 1 (it is the batch of one)", calls)
	}
}

// TestPushHostsEmptyBatchIsANoOp: nothing to push must not resolve
// anything — a group write that affects no host at all is the common case
// (a group nothing binds to), and it must cost nothing.
func TestPushHostsEmptyBatchIsANoOp(t *testing.T) {
	h := newHarnessWithChecks(t)
	if err := h.svc.PushHosts(t.Context(), testOrg, nil); err != nil {
		t.Fatal(err)
	}
	if calls := h.checks.resolveHostsCalls(); calls != 0 {
		t.Fatalf("an empty batch resolved %d times, want 0", calls)
	}
}

// TestPushKeepsGoingAfterAnEncodeFault: one definition with parameters
// that cannot be encoded must not decide, by map iteration order, which
// hosts get their new configuration and which are silently left behind.
// Every host is attempted; the fault is reported afterwards.
func TestPushKeepsGoingAfterAnEncodeFault(t *testing.T) {
	for _, tc := range []struct {
		name string
		push func(h *harness, ids []uuid.UUID) error
	}{
		{"org", func(h *harness, _ []uuid.UUID) error { return h.svc.PushOrg(t.Context(), testOrg) }},
		{"batch", func(h *harness, ids []uuid.UUID) error { return h.svc.PushHosts(t.Context(), testOrg, ids) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessWithChecks(t)
			var ids []uuid.UUID
			var streams []agentpb.AgentService_AgentStreamClient
			for _, name := range []string{"web-1", "web-2", "web-3"} {
				resp, client := enrollAnAgent(t, h, name)
				id := uuid.MustParse(resp.GetHostId())
				ids = append(ids, id)
				h.checks.setResolved(id, checks.ResolvedSet{HostID: id, Version: "v-" + name})
				stream, err := (*client).AgentStream(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				streams = append(streams, stream)
				deadline := time.Now().Add(5 * time.Second)
				for trackedStreams(h.svc.Streams(), id) == 0 {
					if time.Now().After(deadline) {
						t.Fatal("the stream never registered")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			// The middle host carries parameters json.Marshal refuses (a
			// channel is not encodable), whichever order the map is
			// walked in.
			h.checks.setResolved(ids[1], checks.ResolvedSet{
				HostID: ids[1], Version: "broken",
				Checks: []checks.Resolved{{Key: "cpu", Plugin: "cpu", Params: map[string]any{"bad": make(chan int)}}},
			})
			h.checks.setOrgResolved(testOrg, map[uuid.UUID]checks.ResolvedSet{
				ids[0]: {HostID: ids[0], Version: "v-web-1"},
				ids[1]: {HostID: ids[1], Version: "broken", Checks: []checks.Resolved{{Key: "cpu", Plugin: "cpu", Params: map[string]any{"bad": make(chan int)}}}},
				ids[2]: {HostID: ids[2], Version: "v-web-3"},
			})

			if err := tc.push(h, ids); err == nil {
				t.Fatal("an encode fault must be reported")
			}
			// Both healthy hosts were still pushed, whatever order the
			// fault was met in.
			for _, i := range []int{0, 2} {
				msg, err := recvWithin(t, streams[i], recvTimeout)
				if err != nil {
					t.Fatalf("host %d never received its configuration: %v", i, err)
				}
				if cfg := msg.GetCheckConfig(); cfg == nil {
					t.Fatalf("host %d got %+v, want a CheckConfig", i, msg)
				}
			}
		})
	}
}

// acceptingCheckStore is a checks.Store that accepts every batch and is
// asked nothing else. It exists so a test can run the REAL
// checks.Service as the stream's ChecksPort: the claim under test is that
// the domain service's own contract bounds are what the wire refuses, not
// merely that a canned ErrInvalidInput would be mapped. Every other Store
// method comes from the embedded nil interface — calling one panics,
// which is exactly right, since nothing here may reach them.
type acceptingCheckStore struct{ checks.Store }

func (acceptingCheckStore) RecordObservations(_ context.Context, _ uuid.UUID, obs []checks.Observation) (int, int, error) {
	return len(obs), 0, nil
}

// resultBatch builds n well-shaped numeric results, all for the same key.
func resultBatch(n int, key, item, metric string, at time.Time) []*agentpb.CheckResult {
	out := make([]*agentpb.CheckResult, n)
	for i := range out {
		out[i] = checkResultValue(key, item, metric, at, float64(i))
	}
	return out
}

// TestCheckResultsContractBoundsRefusedOnTheWire runs the real
// checks.Service behind the stream and proves the ingest contract's
// bounds are reachable from the wire: a batch one over
// MaxObservationsPerBatch, and a single result whose check_key, item or
// metric is one byte over its own bound, are each answered
// InvalidArgument — which is also what makes errCheckResultsRejected
// reachable in production at all. The control case (a batch exactly at
// the count bound, with every string exactly at its length bound) is
// acknowledged, so the bounds cannot be satisfied by refusing everything.
func TestCheckResultsContractBoundsRefusedOnTheWire(t *testing.T) {
	at := time.Now()
	maxKey := strings.Repeat("k", checks.MaxCheckKeyLen)
	maxItem := strings.Repeat("i", checks.MaxItemLen)
	maxMetric := strings.Repeat("m", checks.MaxMetricLen)

	cases := []struct {
		name    string
		results []*agentpb.CheckResult
		wantErr bool
	}{
		{"at every bound", resultBatch(checks.MaxObservationsPerBatch, maxKey, maxItem, maxMetric, at), false},
		{"one result too many", resultBatch(checks.MaxObservationsPerBatch+1, "cpu", "", "cpu.used_pct", at), true},
		{"check_key one byte too long", resultBatch(1, maxKey+"x", "", "cpu.used_pct", at), true},
		{"item one byte too long", resultBatch(1, "cpu", maxItem+"x", "cpu.used_pct", at), true},
		{"metric one byte too long", resultBatch(1, "cpu", "", maxMetric+"x", at), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessPort(t, true, nil, checks.NewService(acceptingCheckStore{}))
			_, client := enrollAnAgent(t, h, "web-1")
			stream, err := (*client).AgentStream(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_CheckResults{
				CheckResults: &agentpb.CheckResults{Results: tc.results},
			}}); err != nil {
				t.Fatal(err)
			}
			msg, err := recvWithin(t, stream, recvTimeout)
			if !tc.wantErr {
				if err != nil || !msg.GetAck() {
					t.Fatalf("a batch at every bound must be accepted: %+v, %v", msg, err)
				}
				return
			}
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("over the contract: %+v, %v, want InvalidArgument", msg, err)
			}
		})
	}
}

// TestCheckResultsRejectionLogAttributesEachCauseSeparately runs the REAL
// checks.Service behind the stream so the clock and shape rules — which
// live in that service, not in a port fake — actually fire, and pins that
// each count reaches the log field that names its own cause.
//
// TestCheckResultsRejectionLogNamesTheCause above exercises only the
// unknown-host cause and asserts the other two at zero, so swapping
// res.Implausible and res.Malformed in the warning passed it. Attribution
// is the whole point of that log line ("rejected_implausible_at" sends an
// operator to the host's NTP, "rejected_malformed" to its agent build),
// and two thirds of it could be mislabelled silently.
//
// The counts are deliberately DIFFERENT — one malformed against two
// implausible — because a swap between two equal numbers is invisible.
func TestCheckResultsRejectionLogAttributesEachCauseSeparately(t *testing.T) {
	h := newHarnessPort(t, true, nil, checks.NewService(acceptingCheckStore{}))
	_, client := enrollAnAgent(t, h, "web-1")

	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	stale := now.Add(-2 * time.Hour) // maxPast is one hour
	// No observation arm set at all: neither a value, nor a label, nor an
	// error — a shape neither table can represent, which is what a broken
	// agent build produces and a skewed clock never does.
	malformed := &agentpb.CheckResult{CheckKey: "cpu", Metric: "cpu.used_pct", At: timestamppb.New(now)}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_CheckResults{CheckResults: &agentpb.CheckResults{
		Results: []*agentpb.CheckResult{
			checkResultValue("cpu", "", "cpu.used_pct", now, 1), // accepted
			malformed,
			checkResultValue("cpu", "", "cpu.load1", stale, 2),
			checkResultValue("cpu", "", "cpu.load5", stale, 3),
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	if msg, err := recvWithin(t, stream, recvTimeout); err != nil || !msg.GetAck() {
		t.Fatalf("check results ack = %+v, %v", msg, err)
	}

	captured := logs.String()
	for _, want := range []string{
		"agent check results partially rejected",
		"accepted=1",
		"rejected=3",
		"rejected_malformed=1",
		"rejected_implausible_at=2",
		"rejected_unknown_host=0",
	} {
		if !strings.Contains(captured, want) {
			t.Errorf("log does not carry %q:\n%s", want, captured)
		}
	}
}
