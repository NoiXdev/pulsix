// More failure-path coverage: dial errors, stream-open errors, persist
// failures under a read-only data dir and the renewal verification's
// garbage-DER branch.
package agent

import (
	"context"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDialAgentServerNameError(t *testing.T) {
	dir := t.TempDir()
	id, _ := enrollDataDir(t)
	if err := writeFileAtomic(dir, caFileName, []byte("garbage-ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &grpcDialer{cfg: Config{ManagerAddr: "no-host-port", DataDir: dir}}
	if _, err := d.DialAgent(t.Context(), id); err == nil {
		t.Fatal("DialAgent without a usable server name = nil error, want an error")
	}
}

func TestLoadOrGenerateKeyUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, keyFileName)
	// A DIRECTORY where the key should be: a real I/O error, not absence.
	if err := os.Mkdir(keyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrGenerateKey(keyPath); err == nil {
		t.Fatal("LoadOrGenerateKey on a directory path = nil error, want an error")
	}
}

func TestLoadOrGenerateKeyPersistFailsReadOnly(t *testing.T) {
	dir := t.TempDir()
	ro := filepath.Join(dir, "data")
	if err := os.Mkdir(ro, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	if _, err := LoadOrGenerateKey(filepath.Join(ro, keyFileName)); err == nil {
		t.Fatal("LoadOrGenerateKey into a read-only dir = nil error, want an error")
	}
}

func TestStreamOpenFailureReconnects(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}, streamErr: errors.New("stream refused")}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(lt.recordedSleeps()) >= 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if lt.dialer.enrollDials != 0 {
		t.Errorf("enroll dials = %d, want 0", lt.dialer.enrollDials)
	}
}

func TestFactsTickSendFailureReconnects(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	stream1 := newFakeStream()
	stream2 := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream1, stream2}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(stream1.sentMessages()) > 0 })
	// The send side dies; the next FACTS tick fails.
	stream1.failSends(errors.New("transport dead"))
	_, tickers := lt.waitForTickers(2)
	tickers[1].c <- time.Now()
	waitFor(t, 2*time.Second, func() bool { return len(stream2.sentMessages()) > 0 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if len(lt.recordedSleeps()) != 1 {
		t.Fatalf("sleeps = %v, want exactly the one reconnect backoff", lt.recordedSleeps())
	}
}

func TestEnrollDialErrorRetries(t *testing.T) {
	lt := newLoopTest(t)
	caInst := newTestCA(t)
	lt.withToken(caInst)
	lt.dialer.enrollConn = &fakeEnrollConn{respond: enrollResponder(caInst, nil, nil)}
	// The pinned dial itself fails (the manager unreachable): a raw
	// transport error, so the loop waits it out and retries.
	lt.dialer.enrollDialErr = errors.New("connection refused")
	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(lt.recordedSleeps()) >= 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

func TestEnrollPersistFailsReadOnly(t *testing.T) {
	lt := newLoopTest(t)
	caInst := newTestCA(t)
	lt.withToken(caInst)
	lt.dialer.enrollConn = &fakeEnrollConn{respond: enrollResponder(caInst, nil, nil)}
	// agent.ca.pem as a DIRECTORY: the write sequence (key, ca, cert)
	// fails at the rename — the answer arrived and the key was written,
	// but the identity cannot complete.
	if err := os.Mkdir(filepath.Join(lt.dir, caFileName), 0o700); err != nil {
		t.Fatal(err)
	}

	l := lt.newLoop()
	err := l.Run(context.Background())
	if err == nil || !errors.Is(err, ErrSetupFailed) {
		t.Fatalf("Run = %v, want ErrSetupFailed (the answer arrived but could not be persisted)", err)
	}
	if !strings.Contains(err.Error(), "persist agent identity") {
		t.Errorf("the error must name the persist failure: %v", err)
	}
	if lt.dialer.enrollDials != 1 {
		t.Errorf("enroll dials = %d, want 1 (the attempt was made, the persist is what failed)", lt.dialer.enrollDials)
	}
}

func TestIdentityReplaceRejectsGarbageDER(t *testing.T) {
	id, _ := enrollDataDir(t)
	before := id.Leaf()
	// A well-formed PEM CERTIFICATE block whose DER is garbage: the parse
	// error branch, not the missing-PEM branch.
	garbage := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage DER")})
	if err := id.Replace(garbage); err == nil {
		t.Fatal("Replace with a garbage-DER certificate = nil error, want an error")
	}
	if !id.Leaf().Equal(before) {
		t.Error("a refused Replace must leave the identity unchanged")
	}
}

// A session opens with a fixed sequence before it reaches its steady
// state: facts, then the buffer replay, then the first heartbeat. Every
// step of it can meet a transport that died between the stream opening
// and the send — gRPC opens a stream lazily, so nothing before the first
// send has touched the network — and each must end the session the same
// way: cancel the receive goroutine, drain it, and hand the caller an
// error it reconnects on. Neither step had a test, which is part of why
// internal/agent's coverage sat close enough to its floor for an
// unrelated timing race to push it under (backlog 3.29).
func TestSessionOpeningSendFailuresReconnect(t *testing.T) {
	tests := []struct {
		name string
		// arm makes the chosen step the failing one.
		arm func(*fakeStream, error)
		// wantSent is how many messages reached the manager before the
		// failing send, on the stream that dies.
		wantSent int
	}{
		{"the facts push at connect", func(s *fakeStream, err error) { s.failSends(err) }, 0},
		{"the first heartbeat", func(s *fakeStream, err error) { s.failSendsAfter(1, err) }, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lt := newLoopTest(t)
			lt.withIdentity()
			dead, live := newFakeStream(), newFakeStream()
			lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{dead, live}}
			tc.arm(dead, errors.New("transport dead"))

			l := lt.newLoop()
			cancel, done := runLoop(t, l)
			// The second stream getting anything at all is the proof the
			// first session ended and the loop reconnected, rather than
			// blocking somewhere in its opening sequence.
			waitFor(t, 2*time.Second, func() bool { return len(live.sentMessages()) > 0 })

			if got := len(dead.sentMessages()); got != tc.wantSent {
				t.Fatalf("the dead stream carried %d messages, want %d — a different step failed than the test armed", got, tc.wantSent)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run = %v, want nil", err)
			}
			// One reconnect, at the ladder's first rung: the session
			// ended as a failure (backoff), not as a clean end (no
			// backoff).
			if sleeps := lt.recordedSleeps(); len(sleeps) != 1 || sleeps[0] != time.Second {
				t.Fatalf("sleeps = %v, want exactly one 1s reconnect backoff", sleeps)
			}
			// And the replacement session really did start from the top:
			// facts before anything else.
			if first := live.sentMessages()[0]; first.GetFacts() == nil {
				t.Fatalf("the new session's first message = %+v, want facts", first)
			}
		})
	}
}
