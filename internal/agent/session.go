// The stream session: one AgentStream held open — facts at start,
// heartbeats on the heartbeat ticker, facts again on the facts ticker,
// acknowledgements consumed until the stream ends. A failed session is
// the caller's reconnect-and-backoff signal; a clean end (the manager
// closing its side, the renewal window opening, or shutdown) is nil.
package agent

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// streamSession holds one AgentStream open: facts at start, heartbeats on
// the heartbeat ticker, facts again on the facts ticker, acknowledgements
// consumed until the stream ends. It returns a non-nil error only for a
// failed session (the caller reconnects with backoff); a clean end (the
// manager closing its side, or the renewal window opening) returns nil.
func (l *Loop) streamSession(ctx context.Context, id *Identity) error {
	conn, err := l.dial.DialAgent(ctx, id)
	if err != nil {
		return fmt.Errorf("dial manager: %w", err)
	}
	defer func() { _ = conn.Close() }()
	// The session's own context: cancelling it unblocks the receive loop
	// when the send side ends the session first — and it is the context
	// the RPC itself runs on, so it must exist BEFORE the stream opens.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := conn.AgentStream(streamCtx)
	if err != nil {
		return fmt.Errorf("open agent stream: %w", err)
	}
	recvErr := make(chan error, 1)
	go func() { recvErr <- consumeAcks(stream) }()

	// Facts at start: the manager derives the os/arch/virt automatic
	// tags from them, so they land before the first heartbeat.
	if err := l.pushFacts(streamCtx, stream); err != nil {
		cancel()
		l.drainRecv(recvErr)
		if ctx.Err() != nil {
			return nil
		}
		return err
	}

	hb := l.ticker(l.cfg.HeartbeatInterval)
	defer hb.Stop()
	factsTick := l.ticker(l.cfg.FactsInterval)
	defer factsTick.Stop()

	for {
		select {
		case <-ctx.Done():
			cancel()
			l.drainRecv(recvErr)
			_ = stream.CloseSend()
			return nil
		case <-hb.C():
			if id.NeedsRenewal(l.now()) {
				// Renewal due while the stream runs: end the session
				// cleanly (the manager sees a normal send close) — the
				// outer loop renews and reconnects with the fresh
				// certificate.
				_ = stream.CloseSend()
				cancel()
				l.drainRecv(recvErr)
				return nil
			}
			if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Heartbeat{
				Heartbeat: &agentpb.Heartbeat{Version: l.cfg.Version},
			}}); err != nil {
				cancel()
				l.drainRecv(recvErr)
				return fmt.Errorf("send heartbeat: %w", err)
			}
		case <-factsTick.C():
			if err := l.pushFacts(streamCtx, stream); err != nil {
				cancel()
				l.drainRecv(recvErr)
				return fmt.Errorf("push facts: %w", err)
			}
		case err := <-recvErr:
			// nil is a clean end (the manager closed its side): the
			// caller reconnects without backoff; anything else is the
			// failure it carries.
			return err
		}
	}
}

// consumeAcks reads the manager's acknowledgements until the stream ends.
// The ack carries no payload to apply; its arrival is the confirmation.
func consumeAcks(stream Stream) error {
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// pushFacts collects the host's facts and pushes them. A collection error
// is advisory — whatever map came back is still pushed, because one probe
// hiccup must not blind the manager to the whole host; only a dead
// context (shutdown) fails outright.
func (l *Loop) pushFacts(ctx context.Context, stream Stream) error {
	facts, err := l.facts.Collect(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.log.WarnContext(ctx, "collecting host facts failed; pushing what was collected", "err", err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Facts{
		Facts: &agentpb.Facts{Values: facts},
	}}); err != nil {
		return fmt.Errorf("send facts: %w", err)
	}
	return nil
}

// wallTicker is the production ticker: a time.Ticker.
type wallTicker struct{ t *time.Ticker }

// newWallTicker is the tickerFactory the binary uses; tests inject one
// they drive themselves.
func newWallTicker(d time.Duration) Ticker { return &wallTicker{t: time.NewTicker(d)} }

func (w *wallTicker) C() <-chan time.Time { return w.t.C }
func (w *wallTicker) Stop()               { w.t.Stop() }
