// AgentStream: the agent's persistent bidirectional connection. The agent
// sends heartbeats (last_seen + version refresh) and facts (forwarded to
// the FactsSink port); the manager acknowledges every processed message.
// A revocation ends the stream with Unauthenticated (StreamRegistry) —
// the same shape the worker RunEvents stream is cut by.
package agentapi

import (
	"context"
	"io"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/agents"
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
	// authoritative build).
	if err := s.deps.Agents.Touch(ctx, agent.Serial, agent.Version); err != nil {
		return status.Errorf(codes.Internal, "touch agent: %v", err)
	}
	tracked, release := s.streams.track(agent.Serial, agent.HostID)
	defer release()
	// Track happens before this second lookup, so either CloseHost sees
	// the stream or the lookup sees the revocation.
	live, err := s.deps.Agents.GetBySerial(ctx, agent.Serial)
	if err != nil || live.Revoked {
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
		return status.Error(codes.Unauthenticated, "agent revoked")
	}
}

// receiveMessages processes every message until the agent closes the
// stream. Messages read after the stream was cut are dropped without
// acknowledgement (the trackedStream gate), and the loop then runs into
// the ended RPC's Recv failure.
func (s *Service) receiveMessages(ctx context.Context, agent agents.AgentIdentity, stream agentpb.AgentService_AgentStreamServer, tracked *trackedStream) error {
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err // transport-level failure: pass the status through
		}
		done, err := tracked.store(func() error { return s.process(ctx, agent, msg) })
		if err != nil {
			return status.Errorf(codes.Internal, "process agent message: %v", err)
		}
		if !done {
			continue
		}
		if err := stream.Send(&agentpb.ManagerMessage{Ack: true}); err != nil {
			return err
		}
	}
}

// process applies one agent message: a heartbeat touches the registry by
// serial, facts flow to the sink (a nil sink drops them with a server log
// — Task 8 wires the facts service). An unknown body (the proto union is
// deliberately open for future manager messages) is a no-op that is still
// acknowledged.
func (s *Service) process(ctx context.Context, agent agents.AgentIdentity, msg *agentpb.AgentMessage) error {
	switch body := msg.GetBody().(type) {
	case *agentpb.AgentMessage_Heartbeat:
		return s.deps.Agents.Touch(ctx, agent.Serial, body.Heartbeat.GetVersion())
	case *agentpb.AgentMessage_Facts:
		if s.deps.Facts == nil {
			slog.WarnContext(ctx, "agent facts received but no facts sink is wired; dropping",
				"serial", agent.Serial, "host_id", agent.HostID)
			return nil
		}
		return s.deps.Facts.RecordFacts(ctx, agent.OrgID, agent.HostID, body.Facts.GetValues())
	default:
		return nil
	}
}
