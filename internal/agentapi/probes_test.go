// Probe vantage points (backlog 3.13a), the manager side of the agent
// protocol: a capable agent's heartbeat is answered with its ProbeConfig
// when its reported version differs, and its ProbeResults are stored as its
// own vantage — only for slots it is assigned and metrics of the slot's
// plugin.
package agentapi

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
	"github.com/noixdev/pulsix/internal/probeconfig"
	"github.com/noixdev/pulsix/internal/probes"
)

// vantageCall is one RecordVantage call.
type vantageCall struct {
	org     uuid.UUID
	vantage string
	obs     []checks.Observation
}

// fakeProbes is the in-memory ProbesPort: assignments per agent host, and
// every RecordVantage call.
type fakeProbes struct {
	mu          sync.Mutex
	assignments map[uuid.UUID][]probes.Assignment
	assignErr   error
	assignCalls int
	recorded    []vantageCall
	recordErr   error
}

func newFakeProbes() *fakeProbes {
	return &fakeProbes{assignments: map[uuid.UUID][]probes.Assignment{}}
}

func (f *fakeProbes) setAssignments(agentHost uuid.UUID, as ...probes.Assignment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assignments[agentHost] = as
}

func (f *fakeProbes) AgentAssignments(_ context.Context, org, agentHost uuid.UUID) ([]probes.Assignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assignCalls++
	if f.assignErr != nil {
		return nil, f.assignErr
	}
	var out []probes.Assignment
	for _, a := range f.assignments[agentHost] {
		if a.OrgID == org {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeProbes) RecordVantage(_ context.Context, org uuid.UUID, vantage string, obs []checks.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recorded = append(f.recorded, vantageCall{org, vantage, slices.Clone(obs)})
	return nil
}

func (f *fakeProbes) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.assignCalls
}

func (f *fakeProbes) vantageCalls() []vantageCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.recorded)
}

func probeHeartbeat(supported bool, probeVersion string) *agentpb.AgentMessage {
	return &agentpb.AgentMessage{Body: &agentpb.AgentMessage_Heartbeat{
		Heartbeat: &agentpb.Heartbeat{Version: "agent-1", ProbesSupported: supported, ProbeConfigVersion: probeVersion},
	}}
}

// probeAgent enrols one agent on a probes harness and opens its stream.
func probeAgent(t *testing.T) (*harness, uuid.UUID, agentpb.AgentService_AgentStreamClient) {
	t.Helper()
	h := newHarnessWithProbes(t)
	resp, client := enrollAnAgent(t, h, "probe-1")
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return h, uuid.MustParse(resp.GetHostId()), stream
}

func sendAndAck(t *testing.T, stream agentpb.AgentService_AgentStreamClient, msg *agentpb.AgentMessage) *agentpb.ManagerMessage {
	t.Helper()
	if err := stream.Send(msg); err != nil {
		t.Fatal(err)
	}
	got, err := recvWithin(t, stream, recvTimeout)
	if err != nil || !got.GetAck() {
		t.Fatalf("ack = %+v, %v", got, err)
	}
	return got
}

var (
	targetA = uuid.MustParse("0198f0a1-7c2d-7000-8000-00000000000a")
	targetB = uuid.MustParse("0198f0a1-7c2d-7000-8000-00000000000b")
)

func httpSlot(agentHost, target uuid.UUID) probes.Assignment {
	return probes.Assignment{
		OrgID: testOrg, AgentHostID: agentHost, TargetHostID: target, CheckKey: "web", Plugin: "http",
		Params:   map[string]any{"url": "https://example.test/", "timeout_ms": float64(3000), "json_values": []any{"status=$.status"}},
		Interval: time.Minute, Address: "10.0.0.10",
	}
}

func snmpSlot(agentHost, target uuid.UUID) probes.Assignment {
	return probes.Assignment{
		OrgID: testOrg, AgentHostID: agentHost, TargetHostID: target, CheckKey: "switch", Plugin: "snmp",
		Params: map[string]any{"interfaces": true}, Interval: time.Minute, Address: "10.0.0.12",
	}
}

func tcpSlot(agentHost, target uuid.UUID) probes.Assignment {
	return probes.Assignment{
		OrgID: testOrg, AgentHostID: agentHost, TargetHostID: target, CheckKey: "ssh", Plugin: "tcp",
		Params: map[string]any{"port": float64(22)}, Interval: 30 * time.Second, Address: "10.0.0.11",
	}
}

// TestHeartbeatProbeConfigRidesTheAckOnlyOnMismatch: a capable agent with
// no version gets its set; reporting that version, it gets nothing; a
// changed assignment set is sent again.
func TestHeartbeatProbeConfigRidesTheAckOnlyOnMismatch(t *testing.T) {
	h, agentHost, stream := probeAgent(t)
	h.probes.setAssignments(agentHost, tcpSlot(agentHost, targetB), httpSlot(agentHost, targetA))

	cfg := sendAndAck(t, stream, probeHeartbeat(true, "")).GetProbeConfig()
	if cfg == nil {
		t.Fatal("a capable agent without a probe version must receive its set")
	}
	want := []probeconfig.Spec{
		{TargetHostID: targetA, CheckKey: "web", Plugin: "http", ParamsJSON: `{"json_values":["status=$.status"],"timeout_ms":3000,"url":"https://example.test/"}`, IntervalSeconds: 60, Address: "10.0.0.10"},
		{TargetHostID: targetB, CheckKey: "ssh", Plugin: "tcp", ParamsJSON: `{"port":22}`, IntervalSeconds: 30, Address: "10.0.0.11"},
	}
	if cfg.GetVersion() != probeconfig.Version(want) {
		t.Fatalf("version = %s, want %s", cfg.GetVersion(), probeconfig.Version(want))
	}
	got, err := probeconfig.FromProto(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("specs = %+v\nwant %+v", got, want)
	}

	h.advance(heartbeatResolveInterval)
	if msg := sendAndAck(t, stream, probeHeartbeat(true, cfg.GetVersion())); msg.GetProbeConfig() != nil {
		t.Fatalf("an agent at the current version got %+v", msg.GetProbeConfig())
	}

	h.probes.setAssignments(agentHost, tcpSlot(agentHost, targetB))
	h.advance(heartbeatResolveInterval)
	next := sendAndAck(t, stream, probeHeartbeat(true, cfg.GetVersion())).GetProbeConfig()
	if next == nil || len(next.GetProbes()) != 1 || next.GetVersion() == cfg.GetVersion() {
		t.Fatalf("after a change = %+v, want the one remaining slot", next)
	}

	// No assignment at all is a set too: the empty one, with its version.
	h.probes.setAssignments(agentHost)
	h.advance(heartbeatResolveInterval)
	empty := sendAndAck(t, stream, probeHeartbeat(true, next.GetVersion())).GetProbeConfig()
	if empty == nil || len(empty.GetProbes()) != 0 || empty.GetVersion() != probeconfig.Version(nil) {
		t.Fatalf("emptied set = %+v", empty)
	}
}

// TestHeartbeatWithoutProbesSupportedGetsNoProbeConfig: an agent that does
// not report probes_supported is never sent a ProbeConfig and costs no
// assignment read, even when rows name it.
func TestHeartbeatWithoutProbesSupportedGetsNoProbeConfig(t *testing.T) {
	h, agentHost, stream := probeAgent(t)
	h.probes.setAssignments(agentHost, httpSlot(agentHost, targetA))
	if msg := sendAndAck(t, stream, probeHeartbeat(false, "")); msg.GetProbeConfig() != nil {
		t.Fatalf("an incapable agent got %+v", msg.GetProbeConfig())
	}
	if n := h.probes.calls(); n != 0 {
		t.Fatalf("assignment reads = %d, want none", n)
	}
}

// TestHeartbeatRecordsProbesSupported: every heartbeat hands its flag to the
// registry (which writes only a change), keyed by the agent's host.
func TestHeartbeatRecordsProbesSupported(t *testing.T) {
	h, agentHost, stream := probeAgent(t)
	sendAndAck(t, stream, probeHeartbeat(true, ""))
	sendAndAck(t, stream, probeHeartbeat(false, ""))
	want := []probesSupportedCall{{agentHost, true}, {agentHost, false}}
	if got := h.store.probesSupportedCalls(); !slices.Equal(got, want) {
		t.Fatalf("SetProbesSupported calls = %+v, want %+v", got, want)
	}
}

// TestHeartbeatProbeFaultsKeepTheStream: a failing assignment read or flag
// write is logged; the ack goes out (with the CheckConfig the heartbeat
// earned), without a ProbeConfig, and the stream lives on.
func TestHeartbeatProbeFaultsKeepTheStream(t *testing.T) {
	h, agentHost, stream := probeAgent(t)
	h.checks.setResolved(agentHost, checks.ResolvedSet{HostID: agentHost, Version: "v1"})
	h.probes.setAssignments(agentHost, httpSlot(agentHost, targetA))
	h.probes.assignErr = errors.New("db down")
	h.store.mu.Lock()
	h.store.probesSupportedErr = errors.New("db down")
	h.store.mu.Unlock()

	msg := sendAndAck(t, stream, probeHeartbeat(true, ""))
	if msg.GetProbeConfig() != nil {
		t.Fatalf("a failed read sent %+v", msg.GetProbeConfig())
	}
	if msg.GetCheckConfig().GetVersion() != "v1" {
		t.Fatalf("check config = %+v, want v1 despite the probe fault", msg.GetCheckConfig())
	}
	h.advance(heartbeatResolveInterval)
	sendAndAck(t, stream, probeHeartbeat(true, ""))
	if !strings.Contains(h.logs.String(), "probe") {
		t.Fatalf("the probe faults were not logged: %s", h.logs.String())
	}
}

func probeValue(target uuid.UUID, key, item, metric string, at time.Time, v float64) *agentpb.ProbeResult {
	return &agentpb.ProbeResult{TargetHostId: target.String(), CheckKey: key, Item: item, Metric: metric,
		At: timestamppb.New(at), Observation: &agentpb.ProbeResult_Value{Value: v}}
}

func probeResultsMsg(rs ...*agentpb.ProbeResult) *agentpb.AgentMessage {
	return &agentpb.AgentMessage{Body: &agentpb.AgentMessage_ProbeResults{ProbeResults: &agentpb.ProbeResults{Results: rs}}}
}

// TestProbeResultsStoredAsTheAgentsVantage: results of assigned slots are
// stored per target host with the agent's host id as the vantage, the
// observation keyed by the target; all three observation arms survive.
func TestProbeResultsStoredAsTheAgentsVantage(t *testing.T) {
	h, agentHost, stream := probeAgent(t)
	h.probes.setAssignments(agentHost, httpSlot(agentHost, targetA), tcpSlot(agentHost, targetB))
	at := h.clock().Truncate(time.Second)
	sendAndAck(t, stream, probeResultsMsg(
		probeValue(targetA, "web", "", "http.up", at, 1),
		probeValue(targetB, "ssh", "", "tcp.up", at, 0),
		&agentpb.ProbeResult{TargetHostId: targetA.String(), CheckKey: "web", Item: "status", Metric: "http.json.text",
			At: timestamppb.New(at), Observation: &agentpb.ProbeResult_Label{Label: "ok"}},
		&agentpb.ProbeResult{TargetHostId: targetB.String(), CheckKey: "ssh", Metric: "tcp.connect_ms",
			At: timestamppb.New(at), Observation: &agentpb.ProbeResult_Error{Error: "refused"}},
	))
	calls := h.probes.vantageCalls()
	if len(calls) != 1 {
		t.Fatalf("RecordVantage calls = %d, want one per message", len(calls))
	}
	if c := calls[0]; c.org != testOrg || c.vantage != agentHost.String() {
		t.Fatalf("call org %s vantage %q, want %s %q", c.org, c.vantage, testOrg, agentHost)
	}
	byTarget := map[uuid.UUID][]checks.Observation{}
	for _, o := range calls[0].obs {
		byTarget[o.HostID] = append(byTarget[o.HostID], o)
	}
	a, b := byTarget[targetA], byTarget[targetB]
	if len(a) != 2 || a[0].Metric != "http.up" || *a[0].Value != 1 || !a[0].At.Equal(at) ||
		a[1].Label == nil || *a[1].Label != "ok" || a[1].Item != "status" {
		t.Fatalf("target A observations = %+v", a)
	}
	if len(b) != 2 || *b[0].Value != 0 || b[1].Err != "refused" || b[0].CheckKey != "ssh" {
		t.Fatalf("target B observations = %+v", b)
	}
}

// TestProbeResultsOnlyForAskedItems: an item is kept only when the slot's
// plugin asks for it — "" for a plugin without items, a json_values name
// for http — and nothing of an snmp slot is ever kept (snmp never goes to
// an agent, and its interfaces option asks for any item).
func TestProbeResultsOnlyForAskedItems(t *testing.T) {
	h := newHarnessWithProbes(t)
	agentHost := uuid.New()
	targetC := uuid.MustParse("0198f0a1-7c2d-7000-8000-00000000000c")
	h.probes.setAssignments(agentHost, httpSlot(agentHost, targetA), tcpSlot(agentHost, targetB), snmpSlot(agentHost, targetC))
	agent := agents.AgentIdentity{OrgID: testOrg, HostID: agentHost, Serial: "s1"}
	now := h.clock()
	label := func(target uuid.UUID, key, item, metric, l string) *agentpb.ProbeResult {
		return &agentpb.ProbeResult{TargetHostId: target.String(), CheckKey: key, Item: item, Metric: metric,
			At: timestamppb.New(now), Observation: &agentpb.ProbeResult_Label{Label: l}}
	}
	res, err := h.svc.recordProbeResults(t.Context(), agent, &agentpb.ProbeResults{Results: []*agentpb.ProbeResult{
		probeValue(targetB, "ssh", "", "tcp.up", now, 1),                // kept
		probeValue(targetB, "ssh", "x", "tcp.up", now, 1),               // tcp has no items
		label(targetA, "web", "status", "http.json.text", "ok"),         // kept: a json_values name
		probeValue(targetA, "web", "status", "http.json.value", now, 1), // kept
		probeValue(targetA, "web", "other", "http.json.value", now, 1),  // not a json_values name
		probeValue(targetA, "web", "", "http.up", now, 1),               // kept: the whole-check item
		probeValue(targetC, "switch", "", "snmp.up", now, 1),            // snmp never from an agent
		probeValue(targetC, "switch", "eth0", "snmp.if.in_octets", now, 1),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (probeIngest{Accepted: 4, UnaskedItem: 2, Unassigned: 2}); res != want {
		t.Fatalf("ingest = %+v, want %+v", res, want)
	}
	for _, o := range h.probes.vantageCalls()[0].obs {
		if o.Item == "x" || o.Item == "other" || o.HostID == targetC {
			t.Fatalf("recorded an unasked item: %+v", o)
		}
	}
}

// TestProbeResultsOfTheWrongKindAreDropped: a numeric metric carries a
// value or an error, a discrete one a label or an error; anything else is
// dropped and counted.
func TestProbeResultsOfTheWrongKindAreDropped(t *testing.T) {
	h := newHarnessWithProbes(t)
	agentHost := uuid.New()
	h.probes.setAssignments(agentHost, httpSlot(agentHost, targetA))
	agent := agents.AgentIdentity{OrgID: testOrg, HostID: agentHost, Serial: "s1"}
	now := h.clock()
	res, err := h.svc.recordProbeResults(t.Context(), agent, &agentpb.ProbeResults{Results: []*agentpb.ProbeResult{
		{TargetHostId: targetA.String(), CheckKey: "web", Metric: "http.up", At: timestamppb.New(now),
			Observation: &agentpb.ProbeResult_Label{Label: "up"}}, // numeric with a label
		probeValue(targetA, "web", "status", "http.json.text", now, 1), // discrete with a value
		{TargetHostId: targetA.String(), CheckKey: "web", Item: "status", Metric: "http.json.text", At: timestamppb.New(now),
			Observation: &agentpb.ProbeResult_Error{Error: "no body"}}, // kept: an error fits either kind
		{TargetHostId: targetA.String(), CheckKey: "web", Metric: "http.up", At: timestamppb.New(now),
			Observation: &agentpb.ProbeResult_Error{Error: "refused"}}, // kept
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (probeIngest{Accepted: 2, WrongKind: 2}); res != want {
		t.Fatalf("ingest = %+v, want %+v", res, want)
	}
}

// TestProbeResultsOutsideTheAssignmentAreDroppedAndCounted is Review Focus
// 1: a slot the agent is not (or no longer) assigned, a metric of another
// plugin, a target that names nothing — never recorded, each counted, and
// the rest of the batch kept. One assignment read serves the message.
func TestProbeResultsOutsideTheAssignmentAreDroppedAndCounted(t *testing.T) {
	h := newHarnessWithProbes(t)
	agentHost := uuid.New()
	otherAgent := uuid.New()
	h.probes.setAssignments(agentHost, httpSlot(agentHost, targetA))
	h.probes.setAssignments(otherAgent, tcpSlot(otherAgent, targetB))
	agent := agents.AgentIdentity{OrgID: testOrg, HostID: agentHost, Serial: "s1"}
	now := h.clock()
	stale := now.Add(-2 * time.Hour)

	res, err := h.svc.recordProbeResults(t.Context(), agent, &agentpb.ProbeResults{Results: []*agentpb.ProbeResult{
		probeValue(targetA, "web", "", "http.up", now, 1),      // stored
		probeValue(targetB, "ssh", "", "tcp.up", now, 1),       // another agent's slot
		probeValue(targetA, "ssh", "", "tcp.up", now, 1),       // a key not assigned for this target
		probeValue(targetA, "web", "", "tcp.up", now, 1),       // a metric of another plugin
		probeValue(targetA, "web", "", "cpu.used_pct", now, 1), // an agent plugin's metric
		{TargetHostId: "not-a-uuid", CheckKey: "web", Metric: "http.up", At: timestamppb.New(now), Observation: &agentpb.ProbeResult_Value{Value: 1}},
		{TargetHostId: strings.ToUpper(targetA.String()), CheckKey: "web", Metric: "http.up", At: timestamppb.New(now), Observation: &agentpb.ProbeResult_Value{Value: 1}},
		probeValue(targetA, "web", "", "http.response_ms", stale, 3),                                   // implausible
		{TargetHostId: targetA.String(), CheckKey: "web", Metric: "http.up", At: timestamppb.New(now)}, // malformed
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := probeIngest{Accepted: 1, Unassigned: 4, ForeignMetric: 2, Implausible: 1, Malformed: 1}
	if res != want {
		t.Fatalf("ingest = %+v, want %+v", res, want)
	}
	calls := h.probes.vantageCalls()
	if len(calls) != 1 || len(calls[0].obs) != 1 || calls[0].obs[0].Metric != "http.up" || calls[0].obs[0].HostID != targetA {
		t.Fatalf("recorded = %+v, want only the assigned http.up", calls)
	}
	if n := h.probes.calls(); n != 1 {
		t.Fatalf("assignment reads = %d, want one per message", n)
	}
}

// TestProbeResultsFromAnAgentWithoutAssignments: an agent that never
// reported probes_supported has no assignments, so its results are all
// dropped — acknowledged, never recorded.
func TestProbeResultsFromAnAgentWithoutAssignments(t *testing.T) {
	h, _, stream := probeAgent(t)
	sendAndAck(t, stream, probeResultsMsg(probeValue(targetA, "web", "", "http.up", h.clock(), 1)))
	if calls := h.probes.vantageCalls(); len(calls) != 0 {
		t.Fatalf("recorded %+v for an agent with no assignment", calls)
	}
}

// TestProbeResultsContractRefusedOnTheWire: the check-results contract —
// the batch bound and the identifying-string bounds — refuses a probe batch
// with InvalidArgument exactly as it refuses a check batch.
func TestProbeResultsContractRefusedOnTheWire(t *testing.T) {
	big := make([]*agentpb.ProbeResult, checks.MaxObservationsPerBatch+1)
	for i := range big {
		big[i] = probeValue(targetA, "web", "", "http.up", time.Now(), 1)
	}
	cases := map[string][]*agentpb.ProbeResult{
		"one result too many": big,
		"key too long":        {probeValue(targetA, strings.Repeat("k", checks.MaxCheckKeyLen+1), "", "http.up", time.Now(), 1)},
		"item with a newline": {probeValue(targetA, "web", "a\nb", "http.up", time.Now(), 1)},
	}
	for name, rs := range cases {
		t.Run(name, func(t *testing.T) {
			h, agentHost, stream := probeAgent(t)
			h.probes.setAssignments(agentHost, httpSlot(agentHost, targetA))
			if err := stream.Send(probeResultsMsg(rs...)); err != nil {
				t.Fatal(err)
			}
			if _, err := recvWithin(t, stream, recvTimeout); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("err = %v, want InvalidArgument", err)
			}
			if calls := h.probes.vantageCalls(); len(calls) != 0 {
				t.Fatalf("a refused batch recorded %+v", calls)
			}
		})
	}
}

// TestProbeResultsShareTheIngestBudget: probe results draw on the same
// per-agent budget as check results, so an agent that spent it on check
// results is paced before its probe results are stored.
func TestProbeResultsShareTheIngestBudget(t *testing.T) {
	h, agentHost, stream := probeAgent(t)
	h.probes.setAssignments(agentHost, httpSlot(agentHost, targetA))
	for range ingestBurst / checks.MaxObservationsPerBatch {
		sendResultsBatch(t, stream, checks.MaxObservationsPerBatch, h.clock())
	}
	if slept := h.sleptTotal(); slept != 0 {
		t.Fatalf("the burst itself waited %s", slept)
	}
	rs := make([]*agentpb.ProbeResult, checks.MaxObservationsPerBatch)
	for i := range rs {
		rs[i] = probeValue(targetA, "web", "", "http.up", h.clock(), 1)
	}
	sendAndAck(t, stream, probeResultsMsg(rs...))
	if h.sleptTotal() == 0 {
		t.Fatal("probe results over the spent budget were not paced")
	}
	if calls := h.probes.vantageCalls(); len(calls) != 1 || len(calls[0].obs) != checks.MaxObservationsPerBatch {
		t.Fatal("paced probe results were not stored in full")
	}
}

// TestProbeResultsStoreFaultFailsTheStream: a store failure is the
// manager's, answered Internal like a check-results store failure; the
// agent's replay ring sends the batch again after the reconnect.
func TestProbeResultsStoreFaultFailsTheStream(t *testing.T) {
	h, agentHost, stream := probeAgent(t)
	h.probes.setAssignments(agentHost, httpSlot(agentHost, targetA))
	h.probes.recordErr = errors.New("db down")
	if err := stream.Send(probeResultsMsg(probeValue(targetA, "web", "", "http.up", h.clock(), 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := recvWithin(t, stream, recvTimeout); status.Code(err) != codes.Internal {
		t.Fatalf("err = %v, want Internal", err)
	}
}

// TestProbeResultsForAGoneTargetAreDroppedNotFatal: a target host deleted
// between the assignment read and the store write (probes.ErrTargetGone)
// drops the message, counting its results as unassigned, and never fails
// the stream: the agent's next refresh no longer names the host.
func TestProbeResultsForAGoneTargetAreDroppedNotFatal(t *testing.T) {
	h := newHarnessWithProbes(t)
	agentHost := uuid.New()
	h.probes.setAssignments(agentHost, httpSlot(agentHost, targetA), tcpSlot(agentHost, targetB))
	agent := agents.AgentIdentity{OrgID: testOrg, HostID: agentHost, Serial: "s1"}
	msg := &agentpb.ProbeResults{Results: []*agentpb.ProbeResult{
		probeValue(targetA, "web", "", "http.up", h.clock(), 1),
		probeValue(targetB, "ssh", "", "tcp.up", h.clock(), 1),
	}}
	h.probes.recordErr = fmt.Errorf("record: %w", probes.ErrTargetGone)
	res, err := h.svc.recordProbeResults(t.Context(), agent, msg)
	if err != nil {
		t.Fatalf("a gone target failed the message: %v", err)
	}
	if res.Accepted != 0 || res.Unassigned != 2 {
		t.Fatalf("ingest = %+v, want both results dropped as unassigned", res)
	}
}

// TestProbeResultsForAGoneTargetKeepTheStream: over the stream, the same
// message is acknowledged.
func TestProbeResultsForAGoneTargetKeepTheStream(t *testing.T) {
	h, agentHost, stream := probeAgent(t)
	h.probes.setAssignments(agentHost, httpSlot(agentHost, targetA))
	h.probes.recordErr = probes.ErrTargetGone
	sendAndAck(t, stream, probeResultsMsg(probeValue(targetA, "web", "", "http.up", h.clock(), 1)))
}

// TestProbeResultsAreStoredInOneCall: a message's kept results, across
// targets, reach the store in one RecordVantage call — one transaction —
// so a store fault records none of them and the replay cannot duplicate
// samples of a part that was stored.
func TestProbeResultsAreStoredInOneCall(t *testing.T) {
	h := newHarnessWithProbes(t)
	agentHost := uuid.New()
	h.probes.setAssignments(agentHost, httpSlot(agentHost, targetA), tcpSlot(agentHost, targetB))
	agent := agents.AgentIdentity{OrgID: testOrg, HostID: agentHost, Serial: "s1"}
	msg := &agentpb.ProbeResults{Results: []*agentpb.ProbeResult{
		probeValue(targetA, "web", "", "http.up", h.clock(), 1),
		probeValue(targetB, "ssh", "", "tcp.up", h.clock(), 1),
	}}
	h.probes.recordErr = errors.New("db down")
	if _, err := h.svc.recordProbeResults(t.Context(), agent, msg); err == nil {
		t.Fatal("a store fault was swallowed")
	}
	if calls := h.probes.vantageCalls(); len(calls) != 0 {
		t.Fatalf("a failed message recorded %+v", calls)
	}
	h.probes.recordErr = nil
	if _, err := h.svc.recordProbeResults(t.Context(), agent, msg); err != nil {
		t.Fatal(err)
	}
	if calls := h.probes.vantageCalls(); len(calls) != 1 || len(calls[0].obs) != 2 {
		t.Fatalf("calls = %+v, want one call with both targets", calls)
	}
}

// TestProbeResultsWithoutAProbesPortAreAcknowledged: a manager wired
// without the probes store drops probe results with a log and still
// acknowledges them, the posture of the other optional ports.
func TestProbeResultsWithoutAProbesPortAreAcknowledged(t *testing.T) {
	h := newHarnessWithChecks(t)
	_, client := enrollAnAgent(t, h, "probe-1")
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sendAndAck(t, stream, probeResultsMsg(probeValue(targetA, "web", "", "http.up", h.clock(), 1)))
	if msg := sendAndAck(t, stream, probeHeartbeat(true, "")); msg.GetProbeConfig() != nil {
		t.Fatalf("no probes port, yet a ProbeConfig: %+v", msg.GetProbeConfig())
	}
}
