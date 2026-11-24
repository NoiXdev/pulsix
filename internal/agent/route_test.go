package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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

// routeWait bounds every wait in these tests, so a regression fails the
// test instead of hanging the package until its timeout.
const routeWait = 10 * time.Second

// awaitClosed waits for ch to close, failing the test after routeWait.
// Only from the test's own goroutine: it calls t.Fatalf.
func awaitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(routeWait):
		t.Fatalf("timed out after %s waiting for %s", routeWait, what)
	}
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
					select {
					case <-release:
					case <-time.After(routeWait):
						// The sink's goroutine, not the test's: report
						// and let it finish rather than Fatalf here.
						t.Errorf("timed out after %s waiting for the activation to release the sink", routeWait)
					}
				})
			}
			handled := make(chan struct{})
			go func() {
				defer close(handled)
				tc.handle(l, lt.now())
			}()
			awaitClosed(t, routed, "the sink to finish routing")

			stream := &chunkingStream{}
			l.beforeInstall = func() {
				close(release)
				awaitClosed(t, handled, "the sink to finish buffering")
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

// failingOnceStream refuses every send and runs onFail, when set, inside
// the first one, before the refusal reaches the caller — the window in
// which the session that owned this stream ends and the next one goes
// live. sends counts every attempt.
type failingOnceStream struct {
	once   sync.Once
	onFail func()
	sends  atomic.Int32
}

func (s *failingOnceStream) Send(*agentpb.AgentMessage) error {
	s.sends.Add(1)
	if s.onFail != nil {
		s.once.Do(s.onFail)
	}
	return errors.New("stream is dead")
}

// Recv reports the stream as dead too, rather than blocking forever: no
// test here reads from it, and one that started to would fail, not hang.
func (s *failingOnceStream) Recv() (*agentpb.ManagerMessage, error) {
	return nil, errors.New("stream is dead")
}

func (s *failingOnceStream) CloseSend() error         { return nil }
func (s *failingOnceStream) Context() context.Context { return context.Background() }

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

// TestResultWhoseSendFailedWithNoNewerSenderIsBufferedOnce pins
// routeResults' own guard directly (route.go, `sender == failed`): when a
// send fails and no different sender has gone live — the failed one still
// installed, or none at all — the result goes into its ring once and the
// sink returns. It must neither retry the failed stream in a loop nor
// drop the result.
func TestResultWhoseSendFailedWithNoNewerSenderIsBufferedOnce(t *testing.T) {
	for name, tc := range routeCases() {
		for _, still := range []struct {
			name        string
			deactivated bool
		}{
			{"failed sender still installed", false},
			{"failed sender deactivated, none live", true},
		} {
			t.Run(name+"/"+still.name, func(t *testing.T) {
				lt := newLoopTest(t)
				l := lt.newLoop()

				dying := &failingOnceStream{}
				sender := newStreamSender(dying)
				if err := l.activateSender(sender); err != nil {
					t.Fatalf("activateSender: %v", err)
				}
				if still.deactivated {
					dying.onFail = func() { l.deactivateSender(sender) }
				}

				handled := make(chan struct{})
				go func() {
					defer close(handled)
					tc.handle(l, lt.now())
				}()
				awaitClosed(t, handled, "the sink to return after a failed send")

				if got := dying.sends.Load(); got != 1 {
					t.Errorf("the dead stream saw %d sends, want 1: a failed sender is never retried", got)
				}
				if got := tc.left(l); got != 1 {
					t.Errorf("the ring holds %d results, want the 1 the dead stream refused", got)
				}
			})
		}
	}
}
