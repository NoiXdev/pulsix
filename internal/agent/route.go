package agent

// routeResults is the routing both result sinks share (handleResults for
// check results, handleProbeResults for probe results): results go out
// through the live sender, or into their ring through buffer when there
// is none.
//
// Deciding to buffer and buffering are one step under routeMu (backlog
// 3.80). activateSender drains the rings, replays them and installs its
// sender under that same lock, so a result is either in a ring before the
// drain, and replayed, or routed after the install, and sent live. Before
// this, the sink read the sender under the lock and buffered after
// releasing it, and a result routed while no sender was live could be
// added to a ring the activation had just drained, then wait behind a live
// stream for the next reconnect.
//
// The send itself stays outside the lock: one slow stream must not hold
// every other check's and probe's goroutine, the activation and the
// session's teardown behind it. That leaves the second way to the same
// strand: a send failing on a dying stream while the session ends and the
// next one drains the rings and goes live. So after a failed send the
// routing is decided again under the lock: when the failed sender is
// still installed, or none is, the undelivered rest goes into the ring —
// the next activation drains it, because it cannot drain before this
// buffer write releases the lock; when a different sender went live in
// the meantime, the rest is sent through that one instead. Each retry is
// on a newer session's sender, so the loop ends with the reconnects.
//
// Only what did not go out is buffered or retried (unsentAfter): a
// delivered chunk must not be written twice, and one that cannot be
// encoded is dropped rather than carried from stream to stream.
func routeResults[T any](l *Loop, kind string, results []T,
	send func(*streamSender, []T) (int, error), buffer func([]T),
) {
	var failed *streamSender
	for len(results) > 0 {
		l.routeMu.Lock()
		sender := l.sender
		if sender == nil || sender == failed {
			buffer(results)
			l.routeMu.Unlock()
			l.routed()
			return
		}
		l.routeMu.Unlock()
		l.routed()
		sent, err := send(sender, results)
		if err == nil {
			return
		}
		results = unsentAfter(l.log, kind, results, sent, err)
		failed = sender
	}
}

// routed runs the afterRoute test seam, if a test set one.
func (l *Loop) routed() {
	if l.afterRoute != nil {
		l.afterRoute()
	}
}
