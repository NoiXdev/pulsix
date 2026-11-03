// Tests for what the session does with a check-results chunk the agent's
// own gRPC stack refuses to encode (backlog 7.52). The encode failure is
// only real on a real gRPC client stream — a fake Stream would have to
// guess the error's shape — so the replay test runs NewLoop's
// activateSender, handleResults and sendHeartbeat over a bufconn
// connection to a stub AgentService that only counts what reaches it.
package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// countingAgentServer is the manager side of the replay test: it accepts
// every AgentStream and counts the heartbeats and check results that
// actually arrive.
type countingAgentServer struct {
	agentpb.UnimplementedAgentServiceServer

	mu         sync.Mutex
	heartbeats int
	results    []*agentpb.CheckResult
}

func (s *countingAgentServer) AgentStream(stream grpc.BidiStreamingServer[agentpb.AgentMessage, agentpb.ManagerMessage]) error {
	for {
		m, err := stream.Recv()
		if err != nil {
			return nil
		}
		s.mu.Lock()
		if m.GetHeartbeat() != nil {
			s.heartbeats++
		}
		s.results = append(s.results, m.GetCheckResults().GetResults()...)
		s.mu.Unlock()
	}
}

func (s *countingAgentServer) counts() (heartbeats, results int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heartbeats, len(s.results)
}

// startCountingAgentServer serves srv over bufconn and returns a client
// connection to it.
func startCountingAgentServer(t *testing.T, srv *countingAgentServer) agentpb.AgentServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	agentpb.RegisterAgentServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return agentpb.NewAgentServiceClient(conn)
}

// bufferLen counts the disconnect buffer without changing it.
func bufferLen(l *Loop) int {
	held := l.buffer.Drain()
	l.buffer.Add(held)
	return len(held)
}

// TestUnencodableChunkIsDroppedNotRebuffered is backlog 7.52. A result
// whose item is not valid UTF-8 cannot be encoded: gRPC refuses it on the
// client with Internal ("error while marshaling") and finishes the stream.
// The replay used to put that chunk back into the buffer like any other
// failed send, so every reconnect replayed it, died on it again, and never
// reached its heartbeat — the host went dark for good while the buffer
// kept growing. The chunk is now dropped (and logged); the next reconnect
// replays what followed it, the heartbeat goes out, and the buffer does
// not grow across reconnects.
func TestUnencodableChunkIsDroppedNotRebuffered(t *testing.T) {
	lt := newLoopTest(t)
	base := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	lt.now = func() time.Time { return base }
	l := lt.newLoop()
	srv := &countingAgentServer{}
	client := startCountingAgentServer(t, srv)

	// One whole chunk that encodes, then a chunk carrying the poisoned
	// result, then a short tail that encodes again.
	buffered := manyResults(2*maxResultsPerMessage+10, base)
	buffered[maxResultsPerMessage+3].Item = "/home/u/q\"\xffz"
	l.buffer.Add(buffered)

	var activationErrs []error
	before := bufferLen(l)
	for reconnect := 1; reconnect <= 3; reconnect++ {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := client.AgentStream(ctx)
		if err != nil {
			cancel()
			t.Fatalf("reconnect %d: open stream: %v", reconnect, err)
		}
		sender := newStreamSender(stream)
		if err := l.activateSender(sender); err != nil {
			activationErrs = append(activationErrs, err)
		} else if err := l.sendHeartbeat(sender); err != nil {
			t.Fatalf("reconnect %d: the heartbeat after a successful replay failed: %v", reconnect, err)
		}
		// A result the scheduler produced while this session was up (or
		// after it failed), then the session's own teardown.
		l.handleResults([]checks.Result{{CheckKey: "cpu", Metric: "cpu.used_pct",
			Value: floatPtr(float64(reconnect)), At: base}})
		l.deactivateSender(sender)
		_ = sender.closeSend()
		cancel()

		if after := bufferLen(l); after > before {
			t.Fatalf("reconnect %d: the buffer grew from %d to %d results; the unencodable chunk is being replayed for ever",
				reconnect, before, after)
		}
		before = bufferLen(l)
	}

	if len(activationErrs) != 1 {
		t.Fatalf("failed replays = %d (%v), want exactly 1: the one that met the unencodable chunk", len(activationErrs), activationErrs)
	}
	logs := lt.logOutput()
	if !strings.Contains(logs, "could not be encoded") || !strings.Contains(logs, "dropped="+strconv.Itoa(maxResultsPerMessage)) {
		t.Fatalf("the dropped chunk was not logged with its size (%d); log was:\n%s", maxResultsPerMessage, logs)
	}

	// Everything but the dropped chunk reaches the manager: the first
	// chunk and the tail of the buffer, plus the live result of every
	// session that came up (the first one's went to the buffer and out on
	// the second reconnect).
	wantResults := maxResultsPerMessage + 10 + 3
	waitFor(t, 2*time.Second, func() bool {
		hb, res := srv.counts()
		return hb == 2 && res == wantResults
	})
	if n := bufferLen(l); n != 0 {
		t.Fatalf("buffer still holds %d results, want none", n)
	}
}

// staticErrStream is a Stream whose sends succeed until failAfter, then
// fail with err — enough to drive handleResults' choice between buffering
// and dropping without a whole session.
type staticErrStream struct {
	chunkingStream
	err error
}

func (s *staticErrStream) Send(m *agentpb.AgentMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) >= s.failAfter {
		return s.err
	}
	s.sent = append(s.sent, m)
	return nil
}

// TestHandleResultsDropsOnlyTheChunkItCannotEncode: the live path shares
// the rule. Only a client-side encode failure drops the failing chunk;
// any other send error, an Internal one included, still buffers every
// undelivered result.
func TestHandleResultsDropsOnlyTheChunkItCannotEncode(t *testing.T) {
	encodeErr := status.Error(codes.Internal, "grpc: error while marshaling: string field contains invalid UTF-8")
	cases := map[string]struct {
		err        error
		wantBuffer int
	}{
		"encode failure drops the failing chunk":      {err: encodeErr, wantBuffer: maxResultsPerMessage},
		"io.EOF buffers the rest":                     {err: io.EOF, wantBuffer: 2 * maxResultsPerMessage},
		"other Internal buffers the rest":             {err: status.Error(codes.Internal, "stream terminated by RST_STREAM"), wantBuffer: 2 * maxResultsPerMessage},
		"unavailable buffers the rest":                {err: status.Error(codes.Unavailable, "transport is closing"), wantBuffer: 2 * maxResultsPerMessage},
		"plain error naming marshal buffers the rest": {err: errors.New("error while marshaling, but not a gRPC status"), wantBuffer: 2 * maxResultsPerMessage},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lt := newLoopTest(t)
			l := lt.newLoop()
			stream := &staticErrStream{chunkingStream: chunkingStream{failAfter: 1}, err: tc.err}
			l.sender = newStreamSender(stream)

			l.handleResults(manyResults(3*maxResultsPerMessage, lt.now()))

			buffered := l.buffer.Drain()
			if len(buffered) != tc.wantBuffer {
				t.Fatalf("buffered %d results, want %d", len(buffered), tc.wantBuffer)
			}
			if first := buffered[0].Item; first != "/m"+strconv.Itoa(3*maxResultsPerMessage-tc.wantBuffer) {
				t.Fatalf("buffer starts at %q, want the first result after what was sent or dropped", first)
			}
		})
	}
}
