// The stream session: one AgentStream held open — facts at start, an
// immediate heartbeat (so the configuration version reaches the manager
// without waiting for the first tick), heartbeats on the heartbeat
// ticker, a re-assert heartbeat on the shorter config-interval ticker,
// facts again on the facts ticker, manager messages (acknowledgements
// and configuration pushes) consumed until the stream ends. A failed
// session is the caller's reconnect-and-backoff signal; a clean end (the
// manager closing its side, the renewal window opening, or shutdown) is
// nil.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// maxResultsPerMessage caps how many results one CheckResults message
// carries — both a reconnect's replay of the disconnect buffer
// (internal/agent/checks.Buffer) and a single Collect's live output.
// The number is the design's own "Sample delivery" count threshold
// rather than a new one.
//
// It is a correctness bound on both paths, not merely a tidiness one.
// The manager refuses a CheckResults message over
// internal/checks.MaxObservationsPerBatch (1000) with InvalidArgument,
// and that refusal ends the RPC — so an unsplit batch from a host the
// disk plugin fans out far enough on (three metrics per mount) would be
// refused, the stream would drop, the agent would reconnect and send the
// identical batch, for ever. 200 sits five times under that bound, which
// is the headroom that makes the manager's limit unreachable for any
// agent that behaves.
const maxResultsPerMessage = 200

// streamSender serializes every outbound message on one AgentStream.
// gRPC forbids two goroutines calling SendMsg on the same stream
// concurrently; once the scheduler's own per-check goroutines can push
// CheckResults asynchronously alongside this session's own heartbeat and
// facts sends, a shared mutex is the only thing keeping that promise —
// the same rule agentapi's trackedStream exists to keep on the other end
// of this same connection.
type streamSender struct {
	mu     sync.Mutex
	stream Stream
}

func newStreamSender(stream Stream) *streamSender { return &streamSender{stream: stream} }

func (s *streamSender) send(msg *agentpb.AgentMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(msg)
}

// sendResults renders one batch of the scheduler's Results into a single
// CheckResults message — one message per batch, never one per metric, so
// a host running several checks does not turn into a message per host per
// metric per interval ("Sample delivery").
func (s *streamSender) sendResults(results []checks.Result) error {
	return s.send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_CheckResults{
		CheckResults: &agentpb.CheckResults{Results: toCheckResultProtos(results)},
	}})
}

// sendResultsChunked sends results as one or more CheckResults messages
// of at most maxResultsPerMessage each, and reports how many results
// actually went out: all of them on success, everything before the
// failing chunk otherwise. Both senders — the reconnect replay and the
// live scheduler sink — go through it, so neither can produce a message
// the manager's ingest contract would refuse.
func (s *streamSender) sendResultsChunked(results []checks.Result) (sent int, err error) {
	for sent < len(results) {
		n := len(results) - sent
		if n > maxResultsPerMessage {
			n = maxResultsPerMessage
		}
		if err := s.sendResults(results[sent : sent+n]); err != nil {
			return sent, err
		}
		sent += n
	}
	return sent, nil
}

func toCheckResultProtos(results []checks.Result) []*agentpb.CheckResult {
	out := make([]*agentpb.CheckResult, 0, len(results))
	for _, r := range results {
		cr := &agentpb.CheckResult{
			CheckKey: r.CheckKey,
			Item:     r.Item,
			Metric:   r.Metric,
			At:       timestamppb.New(r.At),
		}
		switch {
		case r.Value != nil:
			cr.Observation = &agentpb.CheckResult_Value{Value: *r.Value}
		case r.Label != nil:
			cr.Observation = &agentpb.CheckResult_Label{Label: *r.Label}
		case r.Err != "":
			cr.Observation = &agentpb.CheckResult_Error{Error: r.Err}
		}
		out = append(out, cr)
	}
	return out
}

// streamSession holds one AgentStream open: facts at start, an immediate
// heartbeat, then heartbeats/config-reasserts/facts on their own tickers,
// manager messages consumed until the stream ends. It returns a non-nil
// error only for a failed session (the caller reconnects with backoff); a
// clean end (the manager closing its side, or the renewal window
// opening) returns nil.
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
	sender := newStreamSender(stream)
	recvErr := make(chan error, 1)
	go func() { recvErr <- l.consumeManagerMessages(stream) }()

	// Facts at start: the manager derives the os/arch/virt automatic
	// tags from them, so they land before the first heartbeat.
	if err := l.pushFacts(streamCtx, sender); err != nil {
		cancel()
		l.drainRecv(recvErr)
		if ctx.Err() != nil {
			return nil
		}
		return err
	}

	// Replay whatever the disconnect buffer accumulated, in order, with
	// its original timestamps, before this session starts routing newly
	// collected results — the whole point of buffering ("Sample
	// delivery"). This also makes sender the scheduler's live
	// destination; both happen under one lock so no result produced
	// during the replay can slip into the buffer and sit there
	// undelivered until the NEXT reconnect (see activateSender).
	if err := l.activateSender(sender); err != nil {
		cancel()
		l.drainRecv(recvErr)
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer l.deactivateSender(sender)

	// The configuration version reaches the manager before anything else
	// in the steady-state exchange — a push missed during the disconnect
	// is repaired as soon as the manager sees this heartbeat, rather than
	// waiting out a full heartbeat or config interval first
	// ("Configuration delivery": "the agent reports its version before
	// anything else").
	if err := l.sendHeartbeat(sender); err != nil {
		cancel()
		l.drainRecv(recvErr)
		return fmt.Errorf("send heartbeat: %w", err)
	}

	hb := l.ticker(l.cfg.HeartbeatInterval)
	defer hb.Stop()
	factsTick := l.ticker(l.cfg.FactsInterval)
	defer factsTick.Stop()
	// The config-interval re-assert exists for ONE arrangement: a
	// configuration interval shorter than the heartbeat interval. The
	// version rides every heartbeat, so when the heartbeat is already at
	// least as frequent (the production defaults — 30 s heartbeat, 60 s
	// config — and anything an operator sets that way), this ticker's
	// heartbeat is pure duplicate. It is not free duplicate either: the
	// manager resolves the whole organization's definitions, groups and
	// per-group selector expansions on every heartbeat it receives, so
	// one heartbeat in three used to buy one organization-wide resolve in
	// three for nothing. A nil channel blocks for ever, which is exactly
	// what the select below should do with a re-assert that cannot beat
	// the heartbeat to the manager.
	var configC <-chan time.Time
	if l.cfg.ConfigInterval < l.cfg.HeartbeatInterval {
		configTick := l.ticker(l.cfg.ConfigInterval)
		defer configTick.Stop()
		configC = configTick.C()
	}

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
			if err := l.sendHeartbeat(sender); err != nil {
				cancel()
				l.drainRecv(recvErr)
				return fmt.Errorf("send heartbeat: %w", err)
			}
		case <-configC:
			// Only ever reached when PULSIX_CONFIG_INTERVAL is shorter
			// than the heartbeat interval (see configC above): the extra
			// heartbeat exists purely to keep the manager's convergence
			// check from waiting out a much longer heartbeat cadence.
			if id.NeedsRenewal(l.now()) {
				_ = stream.CloseSend()
				cancel()
				l.drainRecv(recvErr)
				return nil
			}
			if err := l.sendHeartbeat(sender); err != nil {
				cancel()
				l.drainRecv(recvErr)
				return fmt.Errorf("send config re-assert heartbeat: %w", err)
			}
		case <-factsTick.C():
			if err := l.pushFacts(streamCtx, sender); err != nil {
				cancel()
				l.drainRecv(recvErr)
				return fmt.Errorf("push facts: %w", err)
			}
		case err := <-recvErr:
			if ctx.Err() != nil {
				// A genuine race, not a stream fault: streamCtx is
				// derived from ctx (so the receive goroutine's own
				// Recv() unblocks the instant the OUTER ctx is
				// cancelled, independently of this select noticing
				// <-ctx.Done() itself), and Go's select picks
				// pseudo-randomly among whichever cases are ready —
				// this one can win instead of ctx.Done()'s own case on
				// any shutdown. Both must reach the identical outcome
				// (a clean end, the stream closed) regardless of which
				// one fires, or a shutdown would non-deterministically
				// sometimes skip CloseSend. No drainRecv here: the
				// receive goroutine already returned (that is exactly
				// how err reached this case), so there is nothing left
				// to wait for.
				cancel()
				_ = stream.CloseSend()
				return nil
			}
			// nil is a clean end (the manager closed its side): the
			// caller reconnects without backoff; anything else is the
			// failure it carries.
			return err
		}
	}
}

// sendHeartbeat sends the agent's build version and the scheduler's
// currently-applied configuration version — what agentapi's
// processHeartbeat compares against the set it resolves, to decide
// whether to push.
func (l *Loop) sendHeartbeat(sender *streamSender) error {
	return sender.send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Heartbeat{
		Heartbeat: &agentpb.Heartbeat{Version: l.cfg.Version, ConfigVersion: l.scheduler.Version()},
	}})
}

// activateSender drains the disconnect buffer and replays it over sender
// before making sender the scheduler's live destination — both under
// routeMu, so a result produced by a scheduler goroutine between the
// drain and the activation cannot be added to the (by-then-empty) buffer
// and left there undelivered until the next reconnect: handleResults
// blocks on the same lock and only ever observes the fully-settled
// state, either "buffer, no sender yet" or "sender is live," never a
// state in between.
func (l *Loop) activateSender(sender *streamSender) error {
	l.routeMu.Lock()
	defer l.routeMu.Unlock()
	pending := l.buffer.Drain()
	if sent, err := sender.sendResultsChunked(pending); err != nil {
		// Only what did NOT go out goes back: the chunks already
		// delivered must not be replayed again on the next reconnect.
		l.buffer.Add(pending[sent:])
		return fmt.Errorf("replay buffered check results: %w", err)
	}
	l.sender = sender
	return nil
}

// deactivateSender clears sender as the scheduler's live destination —
// called via defer as the session ends, so results produced after this
// point fall back to the buffer until the next reconnect activates a
// fresh sender. The identity check guards against a session that never
// successfully activated (activateSender returned an error before
// setting l.sender) clearing a DIFFERENT, already-active sender that a
// concurrent reconnect may have installed.
func (l *Loop) deactivateSender(sender *streamSender) {
	l.routeMu.Lock()
	defer l.routeMu.Unlock()
	if l.sender == sender {
		l.sender = nil
	}
}

// handleResults is the scheduler's sink: it routes one completed check's
// results to the current session's sender, or, when no stream is up (or
// the send just failed), to the disconnect buffer. This is the one
// function called from the scheduler's own per-check goroutines, never
// from the session's main select loop.
func (l *Loop) handleResults(results []checks.Result) {
	l.routeMu.Lock()
	sender := l.sender
	l.routeMu.Unlock()
	if sender != nil {
		sent, err := sender.sendResultsChunked(results)
		if err == nil {
			return
		}
		// The send failed partway — the session's own recv/send paths
		// will notice the dead stream independently and reconnect. Only
		// the results that did NOT go out fall back to the buffer:
		// re-buffering a delivered chunk would write those samples twice.
		results = results[sent:]
	}
	if dropped := l.buffer.Add(results); dropped > 0 {
		l.log.Warn("check result buffer overflowed while disconnected; oldest samples dropped",
			"dropped", dropped)
	}
}

// consumeManagerMessages reads the manager's messages until the stream
// ends: an acknowledgement carries no payload to apply (its arrival is
// the confirmation); a CheckConfig, when present, is applied to the
// scheduler. Ack and CheckConfig are independent fields on the wire (the
// proto's documented fallback, since ManagerMessage could not become a
// oneof without breaking compatibility) — this tests for CheckConfig's
// presence and never switches on which arm is set.
func (l *Loop) consumeManagerMessages(stream Stream) error {
	for {
		m, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if cc := m.GetCheckConfig(); cc != nil {
			if unrunnable := l.scheduler.Apply(l.decodeCheckConfig(cc)); len(unrunnable) > 0 {
				// The scheduler has no logger of its own, and a check
				// that silently does nothing for ever is exactly what an
				// operator needs to be able to find. The version is not
				// reported as converged either (Scheduler.Apply), so the
				// manager keeps pushing until the set is runnable.
				l.log.Warn("check configuration push named plugins this agent build does not have; "+
					"those checks are not running and this configuration is not reported as converged",
					"keys", unrunnable, "version", cc.GetVersion())
			}
		}
	}
}

// decodeCheckConfig renders the wire CheckConfig into the scheduler's own
// Config, decoding each check's canonical params JSON back into a
// map[string]any. A check whose params fail to decode (never expected
// from a well-behaved manager — checkConfigFor only ever emits what its
// own canonicalParamsJSON produced) is skipped rather than losing the
// whole push: running most of the set beats running none of it.
//
// What such a push does NOT get is its version. The manager stamps one
// over the FULL set it intends, so echoing it back while running less
// would tell processHeartbeat this agent has converged, and it would
// never push again — permanent divergence, not the self-healing
// convergence the delivery design rests on. An empty version is what an
// agent that has never converged reports, and it makes the manager answer
// every heartbeat until the configuration is one this agent can decode.
func (l *Loop) decodeCheckConfig(cc *agentpb.CheckConfig) checks.Config {
	specs := make([]checks.Spec, 0, len(cc.GetChecks()))
	complete := true
	for _, cs := range cc.GetChecks() {
		params, err := decodeParamsJSON(cs.GetParamsJson())
		if err != nil {
			l.log.Warn("check configuration push carried unparsable params; skipping this check, "+
				"and this configuration is not reported as converged",
				"key", cs.GetKey(), "plugin", cs.GetPlugin(), "version", cc.GetVersion(), "err", err)
			complete = false
			continue
		}
		specs = append(specs, checks.Spec{
			Key:      cs.GetKey(),
			Plugin:   cs.GetPlugin(),
			Params:   params,
			Interval: time.Duration(cs.GetIntervalSeconds()) * time.Second,
		})
	}
	if !complete {
		return checks.Config{Checks: specs}
	}
	return checks.Config{Version: cc.GetVersion(), Checks: specs}
}

// decodeParamsJSON parses a CheckSpec's canonical params_json back into a
// map[string]any; "" and "{}" both decode to a nil map, matching how an
// empty params map already reads on the agent side (paramStrings treats
// an absent key exactly like a nil one).
func decodeParamsJSON(raw string) (map[string]any, error) {
	if raw == "" || raw == "{}" {
		return nil, nil
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, err
	}
	return params, nil
}

// pushFacts collects the host's facts and pushes them. A collection error
// is advisory — whatever map came back is still pushed, because one probe
// hiccup must not blind the manager to the whole host; only a dead
// context (shutdown) fails outright.
func (l *Loop) pushFacts(ctx context.Context, sender *streamSender) error {
	facts, err := l.facts.Collect(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.log.WarnContext(ctx, "collecting host facts failed; pushing what was collected", "err", err)
	}
	if err := sender.send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Facts{
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
