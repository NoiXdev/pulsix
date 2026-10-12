package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// racyStream is a Stream whose send-side calls touch one unguarded field.
// That is the point: a gRPC client stream may not have SendMsg and
// CloseSend running at once, but a fake that locks internally would hide
// exactly the overlap this test exists to catch. With the field bare, the
// race detector flags every concurrent pair rather than the lucky ones —
// which is what turns an intermittent CI finding into a test that fails
// the same way every run.
type racyStream struct {
	sendSide int // deliberately unguarded
}

func (s *racyStream) Send(*agentpb.AgentMessage) error { s.sendSide++; return nil }
func (s *racyStream) CloseSend() error                 { s.sendSide++; return nil }
func (s *racyStream) Recv() (*agentpb.ManagerMessage, error) {
	return &agentpb.ManagerMessage{}, nil
}
func (s *racyStream) Context() context.Context { return context.Background() }

// Backlog 1.37. streamSender exists because the check scheduler sends
// results from its own goroutines while the session loop sends heartbeats
// from another, and gRPC forbids two concurrent SendMsg calls on one
// stream. Its mutex makes those safe.
//
// Closing the send side is the same kind of operation and needs the same
// mutex, which the session teardown did not take: it called CloseSend
// straight on the stream, so an in-flight result send raced it. The race
// detector caught that in CI once, on an e2e test, and not in the two
// runs on either side of it — which is why this test drives the overlap
// directly instead of waiting for it to happen again.
func TestSenderSerialisesCloseWithSends(t *testing.T) {
	stream := &racyStream{}
	sender := newStreamSender(stream)

	const senders = 8
	var wg sync.WaitGroup
	start := make(chan struct{})

	for range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 50 {
				if err := sender.send(&agentpb.AgentMessage{}); err != nil {
					t.Errorf("send: %v", err)
					return
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		// The teardown, running while results are still going out — the
		// exact overlap the session loop produces when its context ends
		// mid-batch.
		if err := sender.closeSend(); err != nil {
			t.Errorf("closeSend: %v", err)
		}
	}()

	close(start)
	wg.Wait()
}
