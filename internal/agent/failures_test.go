// The disk-failure and manager-lies paths: the enroll/renew/verify error
// branches, atomic write failures and the read-only data dir — the
// recovery half of the loop's contract.
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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// TestWriteFileAtomicFailures pins the atomic write's error paths: a
// directory path that is a file, and a read-only directory.
func TestWriteFileAtomicFailures(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(blocker, "f", []byte("x"), 0o600); err == nil {
		t.Fatal("writeFileAtomic into a file path = nil error, want an error")
	}
	ro := filepath.Join(dir, "readonly")
	if err := os.Mkdir(ro, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	if err := writeFileAtomic(ro, "f", []byte("x"), 0o600); err == nil {
		t.Fatal("writeFileAtomic into a read-only dir = nil error, want an error")
	}
}

func TestSaveEnrollmentRefusesReadOnlyDataDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	key, csrPEM := newTestKey(t)
	certPEM, caPEM := issueAgentCert(t, csrPEM)
	if err := SaveEnrollment(dir, key, certPEM, caPEM, "host-1"); err == nil {
		t.Fatal("SaveEnrollment into a read-only dir = nil error, want an error")
	}
}

func TestEnrollRefusesReadOnlyDataDir(t *testing.T) {
	lt := newLoopTest(t)
	caInst := newTestCA(t)
	lt.withToken(caInst)
	lt.dialer.enrollConn = &fakeEnrollConn{respond: enrollResponder(caInst, nil, nil)}
	// A read-only SUB-directory of the temp root (the sandbox may deny
	// chmod on the temp root itself): the data dir exists but accepts no
	// files, which is exactly what the probe must catch before any
	// enrollment attempt is spent.
	ro := filepath.Join(lt.dir, "data")
	if err := os.Mkdir(ro, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	lt.cfg.DataDir = ro
	lt.cfg.CertPath = filepath.Join(ro, certFileName)
	lt.cfg.KeyPath = filepath.Join(ro, keyFileName)
	lt.cfg.MetaPath = filepath.Join(ro, metaFileName)

	l := lt.newLoop()
	err := l.Run(context.Background())
	if err == nil || !errors.Is(err, ErrSetupFailed) {
		t.Fatalf("Run on a read-only data dir = %v, want ErrSetupFailed", err)
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("the error must name the read-only fix: %v", err)
	}
	if lt.dialer.enrollDials != 0 {
		t.Errorf("enroll dials = %d, want 0 (the data dir is checked first)", lt.dialer.enrollDials)
	}
}

// TestEnrollVerifiesResponse pins the enrollment's response verification:
// whatever the manager answers is checked before a byte is persisted, so
// a broken answer cannot strand a spent token use.
func TestEnrollVerifiesResponse(t *testing.T) {
	tests := []struct {
		name string
		// respond builds the manager's answer from the loop's own request
		// and the harness CA the loop's token pins.
		respond func(t *testing.T, caInst *ca.CA) func(*agentpb.EnrollRequest) (*agentpb.EnrollResponse, error)
		wantErr string
	}{
		{
			name: "CA does not match the token's pin",
			respond: func(_ *testing.T, _ *ca.CA) func(*agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
				// A DIFFERENT CA signs the (real) CSR: the pin check must
				// refuse the whole answer.
				return func(req *agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
					return signAgentCSRFor(newTestCA(t), []byte(req.GetCsrPem()), testAgentName)
				}
			},
			wantErr: "does not match the enrollment token",
		},
		{
			name: "no usable certificate",
			respond: func(t *testing.T, caInst *ca.CA) func(*agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
				_, caCert := caInst.Pool()
				caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
				return func(*agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
					return &agentpb.EnrollResponse{CertPem: "not a pem", CaPem: string(caPEM), HostId: "host-1"}, nil
				}
			},
			wantErr: "no usable certificate",
		},
		{
			name: "certificate does not match the key just generated",
			respond: func(t *testing.T, caInst *ca.CA) func(*agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
				// A certificate for a DIFFERENT key: the loop's CSR is
				// ignored and the answer carries someone else's leaf.
				_, foreignCSR := newTestKey(t)
				resp, err := signAgentCSRFor(caInst, foreignCSR, testAgentName)
				if err != nil {
					t.Fatal(err)
				}
				return func(*agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) { return resp, nil }
			},
			wantErr: "does not match the key just generated",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lt := newLoopTest(t)
			caInst := newTestCA(t)
			lt.withToken(caInst)
			lt.dialer.enrollConn = &fakeEnrollConn{respond: tt.respond(t, caInst)}
			l := lt.newLoop()
			err := l.Run(context.Background())
			if !errors.Is(err, ErrEnrollRefused) {
				t.Fatalf("Run = %v, want ErrEnrollRefused", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("the refusal must carry the cause: %v", err)
			}
			// Nothing was persisted: the next start would re-enroll with a
			// fresh token instead of trusting broken material.
			if _, statErr := os.Stat(filepath.Join(lt.dir, certFileName)); !os.IsNotExist(statErr) {
				t.Errorf("the data dir must stay empty on a refused enrollment (stat err = %v)", statErr)
			}
		})
	}
}

func TestRenewFailureBacksOff(t *testing.T) {
	lt := newLoopTest(t)
	id := lt.withIdentity()
	leaf := id.Leaf()
	lifetime := leaf.NotAfter.Sub(leaf.NotBefore)
	lt.now = func() time.Time { return leaf.NotBefore.Add(2 * lifetime / 3).Add(time.Hour) }

	// The manager is restarting during the renewal: the loop backs off and
	// retries — never a re-enrollment, never an error exit.
	lt.dialer.agentConn = &fakeAgentConn{renewErr: status.Error(codes.Unavailable, "manager restarting")}
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

func TestRenewRejectsWrongKeyCertificate(t *testing.T) {
	lt := newLoopTest(t)
	id := lt.withIdentity()
	leaf := id.Leaf()
	lifetime := leaf.NotAfter.Sub(leaf.NotBefore)
	lt.now = func() time.Time { return leaf.NotBefore.Add(2 * lifetime / 3).Add(time.Hour) }

	// The manager returns a certificate for a DIFFERENT key: the loop
	// refuses it and keeps the still-valid current certificate.
	_, foreignCSR := newTestKey(t)
	caInst := newTestCA(t)
	foreignPEM, _, err := caInst.SignAgentCSR(foreignCSR, testOrgID, testAgentName)
	if err != nil {
		t.Fatal(err)
	}
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream},
		renewResp: &agentpb.EnrollResponse{CertPem: string(foreignPEM)}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(lt.recordedSleeps()) >= 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if id.Leaf().SerialNumber.Cmp(leaf.SerialNumber) != 0 {
		t.Error("a refused renewal must leave the identity unchanged")
	}
}

func TestStreamSendFailureReconnects(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	stream1 := newFakeStream()
	stream2 := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream1, stream2}}

	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(stream1.sentMessages()) > 0 })
	// The send side dies (the manager's transport dropped mid-push): the
	// next heartbeat tick fails and the loop reconnects with backoff.
	stream1.failSends(errors.New("transport dead"))
	intervals, tickers := lt.ticks.recorded()
	if len(intervals) != 3 {
		t.Fatalf("ticker intervals = %v, want 2", intervals)
	}
	tickers[0].c <- time.Now()
	waitFor(t, 2*time.Second, func() bool { return len(stream2.sentMessages()) > 0 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if len(lt.recordedSleeps()) != 1 || lt.recordedSleeps()[0] != time.Second {
		t.Fatalf("sleeps = %v, want exactly one 1s reconnect backoff", lt.recordedSleeps())
	}
}

func TestRunRefusesUnreadableIdentityFiles(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	// agent.crt replaced by a directory: a real I/O error (not absence).
	if err := os.Remove(filepath.Join(lt.dir, certFileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(lt.dir, certFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	l := lt.newLoop()
	err := l.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "load agent identity") {
		t.Fatalf("Run = %v, want the identity load error", err)
	}
}

func TestRunCleanShutdownBeforeAnything(t *testing.T) {
	lt := newLoopTest(t)
	l := lt.newLoop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Run(ctx); err != nil {
		t.Fatalf("Run with a cancelled context = %v, want nil", err)
	}
	if lt.dialer.agentDials != 0 {
		t.Errorf("agent dials = %d, want 0", lt.dialer.agentDials)
	}
}

func TestLoadOrGenerateKeyRejectsGarbagePEM(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, keyFileName)
	if err := os.WriteFile(keyPath, []byte("-----BEGIN EC PRIVATE KEY-----\ngarbage\n-----END EC PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrGenerateKey(keyPath); err == nil {
		t.Fatal("LoadOrGenerateKey on a garbage PEM block = nil error, want an error")
	}
}
