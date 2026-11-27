// AgentStream: the agent's persistent bidirectional connection. The agent
// sends heartbeats (last_seen + version refresh), facts (forwarded to the
// FactsSink port) and check results (forwarded to the ChecksPort); the
// manager acknowledges every processed message and, on a heartbeat whose
// reported configuration version disagrees with what the host resolves
// to today, rides the current CheckConfig along with that same
// acknowledgement — and a capable agent's ProbeConfig the same way, while
// its probe results are stored as its own vantage (probes.go). A
// revocation ends the stream with Unauthenticated (StreamRegistry) — the
// same shape the worker RunEvents stream is cut by.
package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/grpcsrv"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// AgentStream implements agentpb.AgentServiceServer.AgentStream. Opening
// the stream is itself a heartbeat (Touch at open); the handler tracks the
// stream for revocation before re-checking the serial once — a revocation
// that commits between the interceptor's lookup and the track is caught by
// the re-check, one or the other always sees it (the worker RunEvents
// pattern).
func (s *Service) AgentStream(stream agentpb.AgentService_AgentStreamServer) error {
	ctx := stream.Context()
	agent, ok := grpcsrv.AgentIdentityOf(ctx)
	if !ok {
		return status.Error(codes.PermissionDenied, "unauthenticated call")
	}
	// The stream's open counts as its first heartbeat: last_seen moves,
	// the stored version is kept (the agent's heartbeats carry the
	// authoritative build). A fault recording it is logged, not fatal —
	// the heartbeats that follow retry at their own cadence.
	if err := s.deps.Agents.Touch(ctx, agent.Serial, agent.Version); err != nil {
		slog.ErrorContext(ctx, "touch agent at stream open", "serial", agent.Serial, "err", err)
	}
	// track attaches stream atomically with registering the entry (see its
	// own doc comment): PushHost can already reach this stream the instant
	// it is tracked, with no window where the entry exists but has
	// nothing to send on.
	tracked, release := s.streams.track(agent.Serial, agent.OrgID, agent.HostID, stream)
	defer release()
	// Track happens before this second lookup, so either CloseHost sees
	// the stream or the lookup sees the revocation. A registry fault here
	// is a server fault; a revoked serial is the revocation speaking.
	live, err := s.deps.Agents.GetBySerial(ctx, agent.Serial)
	if err != nil {
		slog.ErrorContext(ctx, "re-check agent after track", "serial", agent.Serial, "err", err)
		return status.Error(codes.Internal, "agent re-check failed")
	}
	if live.Revoked {
		return status.Error(codes.Unauthenticated, "agent revoked")
	}

	// Recv and Send are each single-goroutine, but a cut can land while
	// the loop blocks in Recv — so the receive loop runs on its own and
	// the handler waits for it or the cut, whichever comes first.
	recvErr := make(chan error, 1)
	go func() { recvErr <- s.receiveMessages(ctx, agent, stream, tracked) }()
	select {
	case err := <-recvErr:
		return err
	case <-tracked.revoked:
		if tracked.superseded.Load() {
			return status.Error(codes.ResourceExhausted, "superseded by a newer stream of this agent")
		}
		return status.Error(codes.Unauthenticated, "agent revoked")
	}
}

// receiveMessages processes every message until the agent closes the
// stream. Messages read after the stream was cut are dropped without
// acknowledgement (the trackedStream gate), and the loop then runs into
// the ended RPC's Recv failure. tracked.attach(stream) is called here too
// (not only by AgentStream) so a caller that builds a trackedStream
// directly, without going through AgentStream's own attach, still gets a
// working send — the fault-path unit tests do exactly that.
func (s *Service) receiveMessages(ctx context.Context, agent agents.AgentIdentity, stream agentpb.AgentService_AgentStreamServer, tracked *trackedStream) error {
	tracked.attach(stream)
	pacer := newIngestPacer(s.deps.Clock())
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err // transport-level failure: pass the status through
		}
		// Check and probe results are paced against the agent's ingest
		// budget of their kind before they are stored (backlog 7.111,
		// 3.13a, 3.78), outside the processing lock so a revocation never
		// waits on the pacing.
		if kind, n, ok := s.pacedResults(msg); ok {
			if err := s.pace(ctx, agent, pacer, kind, n); err != nil {
				if errors.Is(err, errCheckResultsRejected) {
					return status.Errorf(codes.InvalidArgument, "agent message rejected: %v", err)
				}
				return status.FromContextError(err).Err()
			}
		}
		var rep reply
		done, err := tracked.store(func() error {
			var procErr error
			rep, procErr = s.process(ctx, agent, msg)
			return procErr
		})
		if err != nil {
			if errors.Is(err, agents.ErrInvalidInput) || errors.Is(err, errCheckResultsRejected) {
				// A facts or check-results push over the respective
				// contract's bounds is the caller's fault, not the
				// server's — the agent's own collector normalizes, so
				// this refusal means the agent lied.
				return status.Errorf(codes.InvalidArgument, "agent message rejected: %v", err)
			}
			return status.Errorf(codes.Internal, "process agent message: %v", err)
		}
		if !done {
			continue
		}
		// ack and the two configurations are independent fields on
		// ManagerMessage (not a oneof): a heartbeat's response always
		// acknowledges and may additionally carry a CheckConfig, a
		// ProbeConfig or both; nothing here ever needs to choose.
		if err := tracked.send(&agentpb.ManagerMessage{
			Ack: true, CheckConfig: rep.checkConfig, ProbeConfig: rep.probeConfig, AgentNames: rep.names,
		}); err != nil {
			return err
		}
	}
}

// reply is what processing one message adds to its acknowledgement: the
// configurations a heartbeat answers a mismatch with, each nil when the
// agent's reported version is current (or nothing was compared).
type reply struct {
	checkConfig *agentpb.CheckConfig
	probeConfig *agentpb.ProbeConfig
	// names is the agent's organization and host names, on every
	// heartbeat's ack (backlog 3.132); nil for other messages and for an
	// unknown serial.
	names *agentpb.AgentNames
}

// pacedResults reports whether msg is a results message charged against
// an ingest budget, which kind's, and how many observations it carries:
// check results when a checks port is wired, probe results when a probes
// port is.
func (s *Service) pacedResults(msg *agentpb.AgentMessage) (ingestKind, int, bool) {
	if r := msg.GetCheckResults(); r != nil && s.deps.Checks != nil {
		return ingestChecks, len(r.GetResults()), true
	}
	if r := msg.GetProbeResults(); r != nil && s.deps.Probes != nil {
		return ingestProbes, len(r.GetResults()), true
	}
	return ingestChecks, 0, false
}

// process applies one agent message: a heartbeat touches the registry and
// answers a configuration mismatch (processHeartbeat); facts record the
// agent's heartbeat interval in the registry and flow to the FactsSink (a
// nil sink drops them with a server log — Task 8 wires the facts
// service); check results flow to the ChecksPort and probe results
// to the ProbesPort (a nil port drops them with a server log the same way,
// so this package can ship ahead of or without it). An unknown body (the
// proto union is deliberately open for future manager messages) is a no-op
// that is still acknowledged.
func (s *Service) process(ctx context.Context, agent agents.AgentIdentity, msg *agentpb.AgentMessage) (reply, error) {
	switch body := msg.GetBody().(type) {
	case *agentpb.AgentMessage_Heartbeat:
		return s.processHeartbeat(ctx, agent, body.Heartbeat)
	case *agentpb.AgentMessage_Facts:
		// The facts open every stream, so the heartbeat interval they
		// carry sizes the agent's online window (backlog 3.58) from the
		// start of the stream on; facts without it store "not reported".
		// A failed write is a registry fault, like a failed heartbeat
		// record: the stream ends and the agent's next one repeats it.
		if err := s.deps.Agents.RecordHeartbeatInterval(ctx, agent.Serial, body.Facts.GetHeartbeatIntervalSeconds()); err != nil {
			return reply{}, err
		}
		if s.deps.Facts == nil {
			slog.WarnContext(ctx, "agent facts received but no facts sink is wired; dropping",
				"serial", agent.Serial, "host_id", agent.HostID)
			return reply{}, nil
		}
		return reply{}, s.deps.Facts.RecordFacts(ctx, agent.OrgID, agent.HostID, body.Facts.GetValues())
	case *agentpb.AgentMessage_CheckResults:
		return reply{}, s.recordCheckResults(ctx, agent, body.CheckResults)
	case *agentpb.AgentMessage_ProbeResults:
		_, err := s.recordProbeResults(ctx, agent, body.ProbeResults)
		return reply{}, err
	default:
		return reply{}, nil
	}
}

// noteVersion logs, once per agent and process, an agent whose reported
// build is outside the supported range against the manager's. The heartbeat
// carries the authoritative version, so this is the one place it is judged.
func (s *Service) noteVersion(ctx context.Context, agent agents.AgentIdentity, version string) {
	st, first := s.skew.Observe("agent/"+agent.HostID.String(), s.deps.Version, version)
	if !first {
		return
	}
	log := s.deps.Log
	if log == nil {
		log = slog.Default()
	}
	log.WarnContext(ctx, "agent version outside the supported range",
		"agent", agent.Name, "org", agent.OrgID, "host_id", agent.HostID, "version", version,
		"manager_version", s.deps.Version, "status", string(st))
}

// processHeartbeat records the heartbeat in the registry — last_seen, the
// agent's reported build version, its secrets report (names and file
// state, which the agents service bounds; an agent older than agent.yaml
// sends no state and is stored as unknown) and its local checks report
// (the checks defined in agent.yaml it runs and their problems, which the
// agents service validates again) — then — if a checks service is wired — resolves the host's
// configuration and compares its version against what the agent reports
// in the very same heartbeat (Heartbeat.config_version). A mismatch is
// answered with the current set, always, regardless of why the versions
// differ: a genuine change since the agent last converged, or a version
// the manager does not recognise at all (the agent's own record of a
// definition that has since been edited or deleted). Nothing here keeps a
// record of what was pushed — the agent's own next reported version is
// the only thing that ends the exchange, which is exactly what makes an
// unrecognised or stale version self-correcting in one round trip rather
// than a loop: whatever the manager sends becomes, by construction, what
// ResolveHost computes, so the very next heartbeat already agrees. Each
// comparison is also stored for the GUI (recordConfigCheck). The agent's
// configuration re-assert (PULSIX_CONFIG_INTERVAL) is an ordinary
// heartbeat, so it reaches this same comparison and is recorded the same
// way. The heartbeat's probes_supported flag is recorded with the rest of
// the heartbeat (backlog 3.79), and a capable
// agent's probe_config_version is compared the same way against its probe
// assignments (probeConfigFor, backlog 3.13a), whose ProbeConfig rides the
// same acknowledgement on a mismatch. Every heartbeat's acknowledgement
// names the agent's organization and host, read by the heartbeat's own
// registry write, for the agent's local status page (backlog 3.132).
func (s *Service) processHeartbeat(ctx context.Context, agent agents.AgentIdentity, hb *agentpb.Heartbeat) (reply, error) {
	names, err := s.deps.Agents.RecordHeartbeat(ctx, agent.Serial, heartbeatReportOf(hb))
	if err != nil {
		return reply{}, err
	}
	var rep reply
	if names != (agents.Names{}) {
		// The agent's status page shows them (backlog 3.132); the same
		// statement that recorded the heartbeat read them.
		rep.names = &agentpb.AgentNames{OrgName: names.Org, HostName: names.Host}
	}
	s.noteVersion(ctx, agent, hb.GetVersion())
	wantsProbes := s.deps.Probes != nil && hb.GetProbesSupported()
	if s.deps.Checks == nil && !wantsProbes {
		return rep, nil
	}
	// At most one resolve per heartbeatResolveInterval and agent (backlog
	// 7.25): inside it the heartbeat is acknowledged and nothing more. A
	// mismatch it would have answered is answered by the next heartbeat
	// outside it, which is the same convergence a failed resolve below
	// relies on; a cached answer instead could be older than a push the
	// agent already applied, and would move it back. The probe
	// assignment read shares the interval: it is one indexed query, but a
	// heartbeat loop must not buy one per message either.
	if !s.streams.allowResolve(agent.Serial, s.deps.Clock()) {
		return rep, nil
	}
	if wantsProbes {
		rep.probeConfig = s.probeConfigFor(ctx, agent, hb.GetProbeConfigVersion())
	}
	if s.deps.Checks == nil {
		return rep, nil
	}
	resolved, err := s.deps.Checks.ResolveHost(ctx, agent.OrgID, agent.HostID)
	if err != nil {
		// Logged and dropped, never fatal to the stream — the same
		// verdict the push path reaches for the identical failure
		// (internal/api's pushAffected: "the agents' own re-assert will
		// repair it"), and for the same reason: convergence heals, so
		// the next heartbeat delivers whatever this one could not. The
		// asymmetry mattered. This resolve reads the organization's
		// hosts, its definitions, its groups and one selector query per
		// group, twice a minute per agent at the defaults (the version
		// rides every heartbeat and the comparison happens after this
		// resolve, never instead of it — backlog 3.19), so a statement
		// timeout, pool exhaustion or a failover blip would otherwise
		// end EVERY stream in the installation at once and send the
		// whole fleet reconnecting into the database that is already
		// failing. The heartbeat's own record above still decides the
		// stream's fate: that one is a single row, and failing it means
		// the registry itself is gone.
		slog.ErrorContext(ctx, "resolve host checks for heartbeat; the agent keeps its stream and the next heartbeat retries",
			"serial", agent.Serial, "org_id", agent.OrgID, "host_id", agent.HostID, "err", err)
		return rep, nil
	}
	s.recordConfigCheck(ctx, agent, hb.GetConfigVersion(), resolved.Version)
	if resolved.Version == hb.GetConfigVersion() {
		return rep, nil
	}
	cfg, err := checkConfigFor(resolved)
	if err != nil {
		return reply{}, fmt.Errorf("encode check configuration: %w", err)
	}
	rep.checkConfig = cfg
	return rep, nil
}

// heartbeatReportOf hands a heartbeat's fields to the agents service as
// sent; validating and bounding them is the service's job.
func heartbeatReportOf(hb *agentpb.Heartbeat) agents.HeartbeatReport {
	local := make([]agents.LocalCheckInput, len(hb.GetLocalChecks()))
	for i, c := range hb.GetLocalChecks() {
		local[i] = agents.LocalCheckInput{Key: c.GetKey(), Plugin: c.GetPlugin(), ParamsJSON: c.GetParamsJson(), IntervalSeconds: c.GetIntervalSeconds()}
	}
	return agents.HeartbeatReport{
		Version: hb.GetVersion(), SecretNames: hb.GetSecretNames(), SecretsState: hb.GetSecretsState(),
		LocalChecks: local, LocalCheckProblems: hb.GetLocalCheckProblems(),
		ProbesSupported: hb.GetProbesSupported(), StatusPage: statusPageModeOf(hb.GetStatusPage()),
	}
}

// statusPageModeOf maps the heartbeat's status page mode (backlog 3.132)
// to the registry's; unspecified (an older agent) and unknown values are
// the empty mode, stored as unknown.
func statusPageModeOf(m agentpb.StatusPageMode) agents.StatusPageMode {
	switch m {
	case agentpb.StatusPageMode_STATUS_PAGE_MODE_OFF:
		return agents.StatusPageOff
	case agentpb.StatusPageMode_STATUS_PAGE_MODE_LOOPBACK:
		return agents.StatusPageLoopback
	case agentpb.StatusPageMode_STATUS_PAGE_MODE_NETWORK:
		return agents.StatusPageNetwork
	case agentpb.StatusPageMode_STATUS_PAGE_MODE_FAILED:
		return agents.StatusPageFailed
	}
	return ""
}

// recordConfigCheck stores the comparison processHeartbeat just made —
// reported against expected — for the Agents page and the host's agent
// card (backlog 3.13c). Only a comparison that ran is stored: a heartbeat
// inside the resolve throttle or with a failed resolve compared nothing,
// and the stored check keeps what the last real one found. The store
// writes the row only when something changed or the stored check has aged
// (agents' configCheckRewrite), so a steady agent adds no write per
// heartbeat. A failed write is logged and dropped, never fatal to the
// stream: the check is display data, and the next comparison writes it
// again — the same posture as the failed resolve above.
func (s *Service) recordConfigCheck(ctx context.Context, agent agents.AgentIdentity, reported, expected string) {
	if err := s.deps.Agents.RecordConfigCheck(ctx, agent.Serial, reported, expected); err != nil {
		log := s.deps.Log
		if log == nil {
			log = slog.Default()
		}
		log.WarnContext(ctx, "record agent configuration check; the next heartbeat writes it again",
			"serial", agent.Serial, "org_id", agent.OrgID, "host_id", agent.HostID, "err", err)
	}
}

// errCheckResultsRejected marks a RecordObservations failure that is this
// specific agent's own fault over the check-results contract
// (internal/checks' MaxObservationsPerBatch and the check_key/item/metric
// length bounds) — the one sentinel receiveMessages tests for to answer
// InvalidArgument instead of Internal.
//
// It is a marker rather than a direct test for checks.ErrInvalidInput so
// that the mapping stays tied to the single call that can mean "this
// agent's own batch is malformed". processHeartbeat can also produce that
// sentinel, from a persisted monitoring group whose selector no longer
// parses; that one is the manager's own data being wrong, and answering
// the agent InvalidArgument for it would blame the wrong party. Today it
// cannot reach here at all — a heartbeat's resolve failure is logged and
// dropped (see processHeartbeat) — so this is defence against a future
// path, not a live distinction.
var errCheckResultsRejected = errors.New("check results rejected")

// recordCheckResults maps one CheckResults batch into checks.Observation
// and hands it to RecordObservations. What was accepted and what was
// rejected is logged, never turned into a stream error — RecordObservations'
// own contract counts a bad item (an implausible timestamp, a malformed
// shape, a host id it does not recognise) rather than failing the batch,
// and this handler passes that contract through rather than second-
// guessing it: one bad result must not cost the agent its connection or
// the rest of the batch its data.
//
// The log line names WHICH rule rejected what, because the four have
// nothing in common as remedies: an implausible timestamp is the host's
// clock (NTP), a malformed shape is a broken agent build, an unknown host
// is a stale enrolment, and an observation over the per-host tuple cap
// (checks.MaxTuplesPerHost) is a host whose checks report too many items
// or an agent inventing keys. A single "rejected" count sends whoever
// reads it looking in all four places. This is a log line and not a counter
// because Pulsix has no metrics endpoint at all yet (backlog 3.27) —
// naming the cause is what can be done here without inventing one.
func (s *Service) recordCheckResults(ctx context.Context, agent agents.AgentIdentity, results *agentpb.CheckResults) error {
	if s.deps.Checks == nil {
		slog.WarnContext(ctx, "agent check results received but no checks service is wired; dropping",
			"serial", agent.Serial, "host_id", agent.HostID)
		return nil
	}
	items := results.GetResults()
	obs := make([]checks.Observation, 0, len(items))
	for _, r := range items {
		o := checks.Observation{
			HostID:   agent.HostID,
			CheckKey: r.GetCheckKey(),
			Item:     r.GetItem(),
			Metric:   r.GetMetric(),
			At:       r.GetAt().AsTime(),
		}
		switch v := r.GetObservation().(type) {
		case *agentpb.CheckResult_Value:
			val := v.Value
			o.Value = &val
		case *agentpb.CheckResult_Label:
			lbl := v.Label
			o.Label = &lbl
		case *agentpb.CheckResult_Error:
			o.Err = v.Error
		}
		obs = append(obs, o)
	}
	res, err := s.deps.Checks.RecordObservations(ctx, agent.OrgID, obs)
	if err != nil {
		if errors.Is(err, checks.ErrInvalidInput) {
			// Marked with errCheckResultsRejected — never checks.ErrInvalidInput
			// tested for directly by receiveMessages — so that a
			// coincidentally identical sentinel from a wholly different
			// call (processHeartbeat's own ResolveHost) can never be
			// mistaken for this path's own caller-fault mapping. See
			// errCheckResultsRejected's own doc comment.
			return fmt.Errorf("%w: %v", errCheckResultsRejected, err)
		}
		return fmt.Errorf("record check observations: %w", err)
	}
	if res.Rejected() > 0 {
		// "partially" is a claim about the batch, and it is false when
		// nothing at all survived — the case an operator most needs to
		// recognise, since a fully rejected batch is a host reporting
		// into a void.
		msg := "agent check results partially rejected"
		if res.Accepted == 0 {
			msg = "agent check results rejected in full"
		}
		slog.WarnContext(ctx, msg,
			"serial", agent.Serial, "org_id", agent.OrgID, "host_id", agent.HostID,
			"accepted", res.Accepted, "rejected", res.Rejected(),
			"rejected_implausible_at", res.Implausible,
			"rejected_malformed", res.Malformed,
			"rejected_unknown_host", res.UnknownHost,
			"rejected_over_cap", res.OverCap)
	} else {
		slog.DebugContext(ctx, "agent check results recorded",
			"serial", agent.Serial, "host_id", agent.HostID, "accepted", res.Accepted)
	}
	return nil
}

// PushHost recomputes hostID's resolved check configuration and pushes it
// to every currently open AgentStream of that host — the write-path hook
// internal/api calls after a check definition, monitoring group, host tag
// or monitored-flag change commits (v0.3b-checks design, "Configuration
// delivery": "the manager recomputes the affected hosts and pushes to
// those currently connected"). A host with no open stream, or a Checks
// port left nil, is a no-op: nothing here remembers the miss, because the
// agent's own heartbeat re-assert (at most PULSIX_CONFIG_INTERVAL later,
// clamped 30-600s) repairs it through the ordinary convergence path in
// processHeartbeat regardless. The pushed message carries no Ack — it
// answers nothing the agent sent, unlike the heartbeat's own response.
//
// It is the batch of one (PushHosts), not a second implementation, so
// the single-host and many-host paths can never answer differently about
// the same host.
func (s *Service) PushHost(ctx context.Context, orgID, hostID uuid.UUID) error {
	return s.PushHosts(ctx, orgID, []uuid.UUID{hostID})
}

// PushHosts recomputes the resolved configuration of every host in
// hostIDs in ONE resolver pass and pushes each host's to its own open
// streams — what a write affecting a known set of hosts triggers: a
// host- or group-bound definition change, or a monitoring group whose
// selector now matches a different set.
//
// The single pass is the point. checks.ResolveHost loads the
// organization's definitions, its monitoring groups and every group's
// selector expansion to answer for one host, so pushing n hosts one at a
// time costs n of those bundles — on a group covering a couple of hundred
// hosts, thousands of serial queries inside one HTTP request. ResolveHosts
// loads that bundle once and answers for the whole batch, by the same
// merge and the same version computation (see its own doc comment), so the
// two can never disagree.
//
// An empty batch resolves nothing at all: a write that turns out to affect
// no host — a monitoring group nothing binds to, the common case — must
// cost nothing. Otherwise the contract is PushHost's, per host.
func (s *Service) PushHosts(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) error {
	if s.deps.Checks == nil || len(hostIDs) == 0 {
		return nil
	}
	sets, err := s.deps.Checks.ResolveHosts(ctx, orgID, hostIDs)
	if err != nil {
		return fmt.Errorf("resolve hosts checks for push: %w", err)
	}
	return s.pushResolved(orgID, sets)
}

// PushOrg recomputes every monitored host's check configuration in orgID
// in one resolver pass and pushes each host's to its own open streams —
// what an organization-bound definition change triggers, where the
// affected set is "every monitored host".
//
// It exists for the same reason PushHosts does, one step further: the
// affected set is not even enumerated by the caller. ResolveOrg loads the
// bundle once and resolves every monitored host against it, which is the
// same answer by construction (both go through the same resolve /
// toResolvedSet path — see checks.ResolveOrg's own doc comment) at a
// fraction of the work.
//
// Everything else matches PushHost: a host with no open stream is a
// no-op, a nil Checks port is a no-op, queueing never blocks on a slow
// agent, and the message carries no Ack.
func (s *Service) PushOrg(ctx context.Context, orgID uuid.UUID) error {
	if s.deps.Checks == nil {
		return nil
	}
	sets, err := s.deps.Checks.ResolveOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("resolve org checks for push: %w", err)
	}
	return s.pushResolved(orgID, sets)
}

// pushResolved queues each resolved set to its host's open streams.
//
// A host whose configuration cannot be encoded does not stop the others.
// Map iteration order is random, so returning at the first fault would
// hand the new configuration to a nondeterministic subset of the fleet and
// silently leave the rest on the old one — from one definition with
// unencodable parameters, reported as a single generic error. Every host
// is attempted, and the faults are reported together afterwards; the
// hosts that could not be encoded are exactly the ones the agents' own
// re-assert will keep asking about, which is the best-effort contract
// every push here already promises.
func (s *Service) pushResolved(orgID uuid.UUID, sets map[uuid.UUID]checks.ResolvedSet) error {
	var faults []error
	for hostID, resolved := range sets {
		cfg, err := checkConfigFor(resolved)
		if err != nil {
			faults = append(faults, fmt.Errorf("encode check configuration for host %s: %w", hostID, err))
			continue
		}
		s.streams.PushToHost(orgID, hostID, &agentpb.ManagerMessage{CheckConfig: cfg})
	}
	return errors.Join(faults...)
}

// checkConfigFor renders a resolved set into the wire CheckConfig: the
// version an agent compares its own against, and each check's key,
// plugin, canonical params JSON and interval.
func checkConfigFor(resolved checks.ResolvedSet) (*agentpb.CheckConfig, error) {
	specs := make([]*agentpb.CheckSpec, 0, len(resolved.Checks))
	for _, c := range resolved.Checks {
		paramsJSON, err := canonicalParamsJSON(c.Params)
		if err != nil {
			return nil, fmt.Errorf("encode params for key %q: %w", c.Key, err)
		}
		specs = append(specs, &agentpb.CheckSpec{
			Key:             c.Key,
			Plugin:          c.Plugin,
			ParamsJson:      paramsJSON,
			IntervalSeconds: int32(c.Interval / time.Second),
		})
	}
	return &agentpb.CheckConfig{Version: resolved.Version, Checks: specs}, nil
}

// canonicalParamsJSON mirrors internal/checks's own unexported
// canonicalParams byte-for-byte: "{}" for nil or empty (never the bare
// "null" json.Marshal(map[string]any(nil)) would otherwise produce),
// sorted-key JSON otherwise — encoding/json sorts map keys at every
// nesting level, so marshalling map[string]any is already canonical. It
// is duplicated rather than imported because canonicalParams is an
// implementation detail of ResolvedSet.Version's hash, private to
// internal/checks; this package needs the identical encoding for a
// different reason — what actually rides the wire in CheckSpec.params_json,
// which an agent may re-hash to verify the version it was just handed.
// The two must never disagree about what "no parameters" or a given
// params map encodes to, which is why both packages pin the same literal
// encoding in their own tests.
func canonicalParamsJSON(params map[string]any) (string, error) {
	if len(params) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
