// The production dialer's pinned verification and server-name rules, the
// wall-clock sleep and the refusal classifier — the pieces the bufconn
// integration test exercises only on their happy paths.
package agent

import (
	"context"
	"encoding/pem"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/enrolltoken"
)

// serverChain mints a server-shaped certificate: the DNS SAN is the CA
// config's ListenHost ("manager" here), exactly like grpcsrv's
// serverCertSource issues it. Returns the leaf DER and the CA DER — the
// exact shape a pinned verifier sees on the wire.
func serverChain(t *testing.T, caInst *ca.CA) ([]byte, []byte) {
	t.Helper()
	certPEM, _, _, err := caInst.Issue(t.Context(), nil, uuid.Nil, "")
	if err != nil {
		t.Fatalf("issue server certificate: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM CERTIFICATE in the issued server certificate")
	}
	_, caCert := caInst.Pool()
	return block.Bytes, caCert.Raw
}

func TestVerifyPinned(t *testing.T) {
	caInst := newTestCA(t)
	leafDER, caDER := serverChain(t, caInst)
	pin := enrolltoken.Pin(caDER)

	if err := verifyPinned(nil, pin, "manager"); err == nil {
		t.Fatal("verifyPinned with no presented CA = nil, want an error")
	}
	if err := verifyPinned([][]byte{leafDER}, pin, "manager"); err == nil {
		t.Fatal("verifyPinned with a chainless leaf = nil, want an error")
	}
	if err := verifyPinned([][]byte{leafDER, caDER}, [32]byte{}, "manager"); err == nil {
		t.Fatal("verifyPinned with a wrong pin = nil, want an error")
	}
	if err := verifyPinned([][]byte{leafDER, caDER}, pin, "manager"); err != nil {
		t.Fatalf("verifyPinned: %v", err)
	}
	// The leaf must verify for the SERVER NAME, not just chain.
	if err := verifyPinned([][]byte{leafDER, caDER}, pin, "other-manager"); err == nil {
		t.Fatal("verifyPinned with a mismatched server name = nil, want an error")
	}
	// A foreign server certificate must not verify against the pinned CA.
	otherLeaf, otherCA := serverChain(t, newTestCA(t))
	if err := verifyPinned([][]byte{otherLeaf, caDER}, pin, "manager"); err == nil {
		t.Fatal("a foreign server certificate must not verify against the pinned CA")
	}
	if err := verifyPinned([][]byte{otherLeaf, otherCA}, pin, "manager"); err == nil {
		t.Fatal("a foreign CA chain must not match the pinned fingerprint")
	}
	// Garbage DERs are parse errors, never silent passes.
	if err := verifyPinned([][]byte{[]byte("garbage"), caDER}, pin, "manager"); err == nil {
		t.Fatal("verifyPinned with a garbage leaf = nil, want an error")
	}
	if err := verifyPinned([][]byte{leafDER, []byte("garbage")}, enrolltoken.Pin([]byte("garbage")), "manager"); err == nil {
		t.Fatal("verifyPinned with a garbage CA block = nil, want an error")
	}
}

// TestServerNameOf pins the ServerName derivation: the override wins,
// otherwise the dial address's host is used.
func TestServerNameOf(t *testing.T) {
	cfg := Config{ManagerAddr: "manager.example:8444"}
	got, err := serverNameOf(cfg)
	if err != nil || got != "manager.example" {
		t.Fatalf("serverNameOf = %q, %v; want manager.example", got, err)
	}
	cfg.ServerName = "pulsix-manager"
	got, err = serverNameOf(cfg)
	if err != nil || got != "pulsix-manager" {
		t.Fatalf("serverNameOf override = %q, %v; want pulsix-manager", got, err)
	}
	cfg = Config{ManagerAddr: "no-host-port"}
	if _, err := serverNameOf(cfg); err == nil {
		t.Fatal("serverNameOf on an address without a host must fail")
	}
}

func TestDialAgentRejectsUnreadableCAChain(t *testing.T) {
	dir := t.TempDir()
	d := &grpcDialer{cfg: Config{ManagerAddr: "manager:8444", DataDir: dir}}
	id, _ := enrollDataDir(t)
	// No CA chain file at all.
	if _, err := d.DialAgent(t.Context(), id); err == nil {
		t.Fatal("DialAgent without a CA chain = nil error, want an error")
	}
	if err := writeFileAtomic(dir, caFileName, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DialAgent(t.Context(), id); err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Fatalf("DialAgent with a garbage CA chain = %v, want a PEM error", err)
	}
}

func TestDialEnrollRefusesABadServerNameSource(t *testing.T) {
	d := &grpcDialer{cfg: Config{ManagerAddr: "no-host-port"}}
	if _, err := d.DialEnroll(t.Context(), enrolltoken.Token{}); err == nil {
		t.Fatal("DialEnroll without a usable server name = nil error, want an error")
	}
}

func TestNewDialerBuilds(t *testing.T) {
	if NewDialer(Config{ManagerAddr: "manager:8444"}) == nil {
		t.Fatal("NewDialer = nil")
	}
}

func TestIsEnrollUnreachable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"manager down", status.Error(codes.Unavailable, "connection refused"), true},
		{"no status at all", errors.New("raw dial error"), true},
		{"deadline", status.Error(codes.DeadlineExceeded, "timed out"), true},
		{"unknown code", status.Error(codes.Unknown, "boom"), true},
		{"refusal", status.Error(codes.Unauthenticated, "enrollment refused"), false},
		{"name taken", status.Error(codes.AlreadyExists, "a host with this name already exists"), false},
		{"rate limited", status.Error(codes.ResourceExhausted, "too many enrollment attempts"), false},
		{"pin mismatch", errors.New("the manager's CA does not match the fingerprint in the enrollment token"), false},
		{"no pin presented", errors.New("the manager presented no CA certificate to pin against"), false},
		{"pinned CA mismatch", errors.New("the manager returned a CA that does not match the pinned CA fingerprint"), false},
	}
	for _, tt := range tests {
		if got := isEnrollUnreachable(tt.err); got != tt.want {
			t.Errorf("isEnrollUnreachable(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestInterruptibleSleepReturnsOnContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if err := interruptibleSleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("interruptibleSleep = %v, want context.Canceled", err)
	}
	if err := interruptibleSleep(context.Background(), 5*time.Millisecond); err != nil {
		t.Fatalf("interruptibleSleep short = %v, want nil", err)
	}
}

func TestLogStreamErrorDistinguishesRevocation(t *testing.T) {
	var logs strings.Builder
	l := NewLoop(Config{}, &fakeDialer{}, &fakeCollector{},
		WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	id, _ := enrollDataDir(t)
	l.logStreamError(t.Context(), errors.New("transport hiccup"), time.Second, id)
	if !strings.Contains(logs.String(), "backing off") {
		t.Errorf("a transport error must log the backoff:\n%s", logs.String())
	}
	logs.Reset()
	l.logStreamError(t.Context(), status.Error(codes.Unauthenticated, "unknown or revoked certificate"), time.Second, id)
	out := logs.String()
	if !strings.Contains(out, "revoked, replaced or expired") || !strings.Contains(out, "host-1") {
		t.Errorf("an Unauthenticated stream error must name the revocation remediation and host:\n%s", out)
	}
}
