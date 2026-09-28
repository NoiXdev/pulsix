// The agent loop's test fakes: channel-driven dialer/conn/stream/ticker
// implementations of the loop's ports (real gRPC is exercised separately
// by the bufconn integration test).
package agent

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// fakeTicker is a channel-driven ticker the test controls.
type fakeTicker struct {
	c chan time.Time
}

func newFakeTicker() *fakeTicker { return &fakeTicker{c: make(chan time.Time)} }

func (f *fakeTicker) C() <-chan time.Time { return f.c }
func (f *fakeTicker) Stop()               {}

// fakeTickers hands out fake tickers, recording their intervals in
// hand-out order (the loop builds the heartbeat ticker before the facts
// ticker in each session).
type fakeTickers struct {
	mu        sync.Mutex
	intervals []time.Duration
	tickers   []*fakeTicker
}

func (f *fakeTickers) factory() tickerFactory {
	return func(d time.Duration) Ticker {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.intervals = append(f.intervals, d)
		ft := newFakeTicker()
		f.tickers = append(f.tickers, ft)
		return ft
	}
}

func (f *fakeTickers) recorded() ([]time.Duration, []*fakeTicker) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.intervals...), append([]*fakeTicker(nil), f.tickers...)
}

// fakeStream is a channel-driven bidi stream. Send records and (while the
// manager side is open) queues an ack for Recv to consume; the test ends
// the manager side with closeRecv (Recv then reads io.EOF) or fails it
// with failRecv. Recv also unblocks when the session's context (set by
// fakeAgentConn.AgentStream, exactly as the real gRPC stream carries it)
// is cancelled — that is what lets the loop's drain be instant.
type fakeStream struct {
	mu     sync.Mutex
	sent   []*agentpb.AgentMessage
	closed bool

	ctx     context.Context
	recvC   chan *agentpb.ManagerMessage
	done    chan struct{} // closed on a manager-side death (failRecv)
	recvErr error
	sendErr error
	// failSendsAfter's arming: sends beyond sendErrAfter fail with
	// sendErrLater. Zero means unarmed, which is why the count is
	// compared against len(sent) only when an error is set.
	sendErrAfter int
	sendErrLater error
}

func newFakeStream() *fakeStream {
	return &fakeStream{recvC: make(chan *agentpb.ManagerMessage, 64), done: make(chan struct{})}
}

func (s *fakeStream) Send(m *agentpb.AgentMessage) error {
	s.mu.Lock()
	if s.sendErr != nil {
		err := s.sendErr
		s.mu.Unlock()
		return err
	}
	if s.sendErrLater != nil && len(s.sent) >= s.sendErrAfter {
		err := s.sendErrLater
		s.mu.Unlock()
		return err
	}
	s.sent = append(s.sent, m)
	if s.recvC != nil {
		select {
		case s.recvC <- &agentpb.ManagerMessage{Ack: true}:
		default:
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *fakeStream) Recv() (*agentpb.ManagerMessage, error) {
	s.mu.Lock()
	recvC := s.recvC
	s.mu.Unlock()
	if s.ctx != nil {
		select {
		case <-s.dead():
			return nil, s.deadErr()
		case <-s.ctx.Done():
			return nil, s.ctx.Err() // the session was cancelled from the send side
		case m, ok := <-recvC:
			if !ok {
				return nil, io.EOF
			}
			return m, nil
		}
	}
	select {
	case <-s.dead():
		return nil, s.deadErr()
	case m, ok := <-recvC:
		if !ok {
			return nil, io.EOF
		}
		return m, nil
	}
}

// Context is the stream's own context (the loop cancels it to end the
// session; the fake mirrors the real gRPC stream's contract).
func (s *fakeStream) Context() context.Context { return s.ctx }

// dead is the manager-side death signal, closed exactly once.
func (s *fakeStream) dead() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

func (s *fakeStream) deadErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recvErr != nil {
		return s.recvErr
	}
	return io.EOF // a closed manager side with no scripted error: clean end
}

func (s *fakeStream) CloseSend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// pushManagerMessage delivers m to the loop's own Recv, exactly like an
// out-of-band manager push (agentapi's PushHost) or a heartbeat's own
// answer would arrive — used by tests that script a CheckConfig without
// going through Send's own auto-ack.
func (s *fakeStream) pushManagerMessage(m *agentpb.ManagerMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recvC != nil {
		select {
		case s.recvC <- m:
		default:
		}
	}
}

func (s *fakeStream) closeRecv() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recvC != nil {
		close(s.recvC)
		s.recvC = nil
	}
}

// failRecv makes the manager side die with err (the loop reconnects with
// backoff).
func (s *fakeStream) failRecv(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recvErr = err
	select {
	case <-s.done: // already dead
	default:
		close(s.done)
	}
}

func (s *fakeStream) failSends(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendErr = err
}

// failSendsAfter lets the first n sends through and fails every one after
// them, so a test can pick which step of a session's fixed opening
// sequence — facts, buffer replay, the first heartbeat — is the one that
// meets a dead transport.
func (s *fakeStream) failSendsAfter(n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendErrAfter, s.sendErrLater = n, err
}

func (s *fakeStream) sentMessages() []*agentpb.AgentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*agentpb.AgentMessage(nil), s.sent...)
}

func (s *fakeStream) closeSendCalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// fakeEnrollConn records Enroll calls; respond scripts the answer.
type fakeEnrollConn struct {
	mu       sync.Mutex
	requests []*agentpb.EnrollRequest
	respond  func(req *agentpb.EnrollRequest) (*agentpb.EnrollResponse, error)
	closed   bool
}

func (f *fakeEnrollConn) Enroll(_ context.Context, req *agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.respond == nil {
		return nil, fmt.Errorf("fakeEnrollConn: no responder")
	}
	return f.respond(req)
}

func (f *fakeEnrollConn) Close() error { f.closed = true; return nil }

func (f *fakeEnrollConn) recorded() []*agentpb.EnrollRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*agentpb.EnrollRequest(nil), f.requests...)
}

// fakeAgentConn records Renew calls and hands out scripted streams.
type fakeAgentConn struct {
	mu        sync.Mutex
	renews    []*agentpb.RenewRequest
	streams   []*fakeStream
	streamIdx int
	renewResp *agentpb.EnrollResponse
	renewErr  error
	streamErr error
	closed    bool
}

func (f *fakeAgentConn) Renew(_ context.Context, req *agentpb.RenewRequest) (*agentpb.EnrollResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renews = append(f.renews, req)
	if f.renewErr != nil {
		return nil, f.renewErr
	}
	return f.renewResp, nil
}

func (f *fakeAgentConn) AgentStream(ctx context.Context) (Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	if f.streamIdx >= len(f.streams) {
		return nil, fmt.Errorf("fakeAgentConn: no stream prepared")
	}
	s := f.streams[f.streamIdx]
	f.streamIdx++
	s.ctx = ctx
	return s, nil
}

func (f *fakeAgentConn) Close() error { f.closed = true; return nil }

func (f *fakeAgentConn) renewed() []*agentpb.RenewRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*agentpb.RenewRequest(nil), f.renews...)
}

// fakeDialer hands out the scripted connections and records which kind
// was dialed with which identity.
type fakeDialer struct {
	mu           sync.Mutex
	enrollTokens []enrolltoken.Token
	enrollDials  int
	agentDials   int
	agentLeaves  []*x509.Certificate // the identity leaf each agent dial presented

	enrollConn *fakeEnrollConn
	agentConn  *fakeAgentConn

	enrollDialErr error
	agentDialErr  error
}

func (f *fakeDialer) DialEnroll(_ context.Context, tok enrolltoken.Token) (EnrollClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enrollDials++
	f.enrollTokens = append(f.enrollTokens, tok)
	if f.enrollDialErr != nil {
		return nil, f.enrollDialErr
	}
	return f.enrollConn, nil
}

func (f *fakeDialer) DialAgent(_ context.Context, id *Identity) (AgentClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agentDials++
	if id != nil && id.Leaf() != nil {
		f.agentLeaves = append(f.agentLeaves, id.Leaf())
	}
	if f.agentDialErr != nil {
		return nil, f.agentDialErr // sticky until the test clears it
	}
	return f.agentConn, nil
}

// fakeCollector is the facts Collector port stand-in.
type fakeCollector struct {
	mu    sync.Mutex
	calls int
	facts map[string]string
	err   error
}

func (f *fakeCollector) Collect(context.Context) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.facts, f.err
}

func (f *fakeCollector) collectCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// signAgentCSRFor is what a real manager does with an agent CSR: parse,
// issue for the SAME key, under the test org and name.
func signAgentCSRFor(caInst *ca.CA, csrPEM []byte, name string) (*agentpb.EnrollResponse, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, fmt.Errorf("test: no CSR PEM")
	}
	certPEM, _, err := caInst.SignAgentCSR(csrPEM, testOrgID, name)
	if err != nil {
		return nil, err
	}
	_, caCert := caInst.Pool()
	return &agentpb.EnrollResponse{
		CertPem: string(certPEM),
		CaPem:   string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})),
		HostId:  "host-1",
	}, nil
}
