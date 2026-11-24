package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// routeCase is one of the two result sinks that share the routing rule
// (backlog 3.80): check results through handleResults and the check
// buffer, probe results through handleProbeResults and the probe ring.
type routeCase struct {
	// handle routes one result through the sink under test.
	handle func(l *Loop, at time.Time)
	// delivered counts the results of this kind stream received.
	delivered func(stream *chunkingStream) int
	// left drains this kind's ring and counts what was still in it.
	left func(l *Loop) int
}

func routeCases() map[string]routeCase {
	return map[string]routeCase{
		"check results": {
			handle: func(l *Loop, at time.Time) { l.handleResults(manyResults(1, at)) },
			delivered: func(stream *chunkingStream) int {
				n := 0
				for _, b := range stream.batches() {
					n += len(b)
				}
				return n
			},
			left: func(l *Loop) int { return len(l.buffer.Drain()) },
		},
		"probe results": {
			handle: func(l *Loop, at time.Time) { l.handleProbeResults(manyProbeResults(1, at)) },
			delivered: func(stream *chunkingStream) int {
				n := 0
				for _, m := range probeResultMessages(stream) {
					n += len(m.GetResults())
				}
				return n
			},
			left: func(l *Loop) int { return len(l.probeBuffer.Drain()) },
		},
	}
}

// TestResultRoutedWhileDisconnectedIsNotStrandedByTheActivation (backlog
// 3.80): a sink routes a result while no sender is live, and its buffer
// write lands after activateSender drained the ring for the sender that
// is becoming live but before that sender is installed. The result must
// reach the new stream, not sit in the ring until the next reconnect.
//
// The seams force exactly that interleaving: the sink stops once its
// routing step is over and routeMu is released (afterRoute), and the
// activation lets it finish only after both replays, just before the
// install (beforeInstall).
func TestResultRoutedWhileDisconnectedIsNotStrandedByTheActivation(t *testing.T) {
	for name, tc := range routeCases() {
		t.Run(name, func(t *testing.T) {
			lt := newLoopTest(t)
			l := lt.newLoop()

			routed, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			l.afterRoute = func() {
				once.Do(func() {
					close(routed)
					<-release
				})
			}
			handled := make(chan struct{})
			go func() {
				defer close(handled)
				tc.handle(l, lt.now())
			}()
			<-routed

			stream := &chunkingStream{}
			l.beforeInstall = func() {
				close(release)
				<-handled
			}
			if err := l.activateSender(newStreamSender(stream)); err != nil {
				t.Fatalf("activateSender: %v", err)
			}

			if got := tc.delivered(stream); got != 1 {
				t.Errorf("the new stream received %d results, want the 1 routed while disconnected", got)
			}
			if got := tc.left(l); got != 0 {
				t.Errorf("%d results stranded in the ring behind a live sender, want 0", got)
			}
		})
	}
}

// failingOnceStream refuses every send and runs onFail inside the first
// one, before the refusal reaches the caller — the window in which the
// session that owned this stream ends and the next one goes live.
type failingOnceStream struct {
	once   sync.Once
	onFail func()
}

func (s *failingOnceStream) Send(*agentpb.AgentMessage) error {
	s.once.Do(s.onFail)
	return errors.New("stream is dead")
}

func (s *failingOnceStream) Recv() (*agentpb.ManagerMessage, error) { select {} }
func (s *failingOnceStream) CloseSend() error                       { return nil }
func (s *failingOnceStream) Context() context.Context               { return context.Background() }

// TestResultWhoseSendFailedIsNotStrandedByAReconnect (backlog 3.80): a
// sink's send fails on a dying stream, and while it fails the session
// ends and the next one drains the rings and goes live. The undelivered
// result must reach the new stream rather than be put into a ring that
// was already drained for it.
func TestResultWhoseSendFailedIsNotStrandedByAReconnect(t *testing.T) {
	for name, tc := range routeCases() {
		t.Run(name, func(t *testing.T) {
			lt := newLoopTest(t)
			l := lt.newLoop()

			dying := &failingOnceStream{}
			old := newStreamSender(dying)
			if err := l.activateSender(old); err != nil {
				t.Fatalf("activateSender (old stream): %v", err)
			}
			fresh := &chunkingStream{}
			dying.onFail = func() {
				l.deactivateSender(old)
				if err := l.activateSender(newStreamSender(fresh)); err != nil {
					t.Errorf("activateSender (new stream): %v", err)
				}
			}

			tc.handle(l, lt.now())

			if got := tc.delivered(fresh); got != 1 {
				t.Errorf("the new stream received %d results, want the 1 the dead stream refused", got)
			}
			if got := tc.left(l); got != 0 {
				t.Errorf("%d results stranded in the ring behind a live sender, want 0", got)
			}
		})
	}
}
