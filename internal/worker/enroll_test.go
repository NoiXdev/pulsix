package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/workers"
)

type oneZone struct{}

func (oneZone) ListZones(_ context.Context, org uuid.UUID) ([]hosts.Zone, error) {
	return []hosts.Zone{{ID: zoneID, OrgID: org, Slug: "default"}}, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func (h *harness) token(t *testing.T, name string) string {
	t.Helper()
	svc := workers.NewService(h.store, workers.WithSigner(h.ca), workers.WithZones(oneZone{}))
	_, token, err := svc.CreateEnrollment(context.Background(), orgID, nil, zoneID, name)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func tokenConfig(t *testing.T, token string) Config {
	t.Helper()
	cfg, err := Load([]string{"PULSIX_MANAGER_ADDR=manager:8444", "PULSIX_WORKER_DATA_DIR=" + t.TempDir(), "PULSIX_ENROLL_TOKEN=" + token})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestEnrollWritesTheBundleAndClaims(t *testing.T) {
	h := newHarness(t)
	cfg := tokenConfig(t, h.token(t, "fra-7"))
	if err := ensureIdentity(t.Context(), cfg, quiet(), time.Now, []grpc.DialOption{h.bufconnDialer()}); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{cfg.KeyPath: 0o600, cfg.CertPath: 0o644, cfg.CAPath: 0o644} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v, want mode %v", path, st, err, mode)
		}
	}
	if filepath.Dir(cfg.CertPath) != filepath.Join(cfg.DataDir, "identity") {
		t.Fatalf("identity dir = %s", filepath.Dir(cfg.CertPath))
	}
	conn, err := h.dialWith(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := workerpb.NewWorkerServiceClient(conn).ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"}); err != nil {
		t.Fatalf("the enrolled worker must authenticate: %v", err)
	}
	// Restart: the valid bundle wins, the (now used) token is ignored.
	if err := ensureIdentity(t.Context(), cfg, quiet(), time.Now, []grpc.DialOption{h.bufconnDialer()}); err != nil {
		t.Fatalf("restart with a valid bundle: %v", err)
	}
}

func TestEnrollRefusesAForeignCA(t *testing.T) {
	h := newHarness(t)
	tok, _ := enrolltoken.Parse(h.token(t, "fra-8"))
	tok.CAPin[0] ^= 0xff // a token for some other manager
	cfg := tokenConfig(t, tok.String())
	err := ensureIdentity(t.Context(), cfg, quiet(), time.Now, []grpc.DialOption{h.bufconnDialer()})
	if err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("a wrong CA pin must fail the handshake: %v", err)
	}
	if _, statErr := os.Stat(cfg.KeyPath); statErr == nil {
		t.Fatal("nothing may be written after a refused handshake")
	}
}

func TestEnsureIdentityWithoutBundleOrToken(t *testing.T) {
	cfg := tokenConfig(t, "")
	if err := ensureIdentity(t.Context(), cfg, quiet(), time.Now, nil); err == nil || !strings.Contains(err.Error(), "PULSIX_ENROLL_TOKEN") {
		t.Fatalf("err = %v", err)
	}
	cfg.EnrollToken = "pxw1.garbage"
	if err := ensureIdentity(t.Context(), cfg, quiet(), time.Now, nil); err == nil {
		t.Fatal("a malformed token must fail")
	}
}

func TestRenewerRenewsWhenDue(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1", true)
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialIdentity(t.Context(), cfg, id, []grpc.DialOption{h.bufconnDialer()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	now := time.Now()
	r := NewRenewer(workerpb.NewWorkerServiceClient(conn), id, func() time.Time { return now }, nil, quiet())
	before := id.Leaf().SerialNumber
	if err := r.Check(t.Context()); err != nil || id.Leaf().SerialNumber.Cmp(before) != 0 {
		t.Fatalf("a fresh certificate must not renew: %v", err)
	}
	now = id.Leaf().NotAfter.Add(-time.Hour)
	if err := r.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if id.Leaf().SerialNumber.Cmp(before) == 0 {
		t.Fatal("the certificate was not renewed")
	}
	// Run stops on cancel.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	NewRenewer(workerpb.NewWorkerServiceClient(conn), id, func() time.Time { return now }, nil, quiet()).Run(ctx)
}

// TestEnsureIdentityWrapsUnreachableManager: a failure to reach the
// manager for the Enroll call itself (as opposed to a bad token or a
// malformed config) is reported through ErrEnrollUnreachable, so
// cmd/worker/main.go can exit 1 (transient, a restart policy retries)
// instead of 2 (a configuration mistake the operator must fix).
func TestEnsureIdentityWrapsUnreachableManager(t *testing.T) {
	h := newHarness(t)
	cfg := tokenConfig(t, h.token(t, "fra-9"))
	unreachable := grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	})
	err := ensureIdentity(t.Context(), cfg, quiet(), time.Now, []grpc.DialOption{unreachable})
	if !errors.Is(err, ErrEnrollUnreachable) {
		t.Fatalf("err = %v, want ErrEnrollUnreachable", err)
	}
}

// TestEnsureIdentityClassifiesServerRefusals: only a genuine failure to
// reach the manager (dialing, a transport error) is ErrEnrollUnreachable.
// A token the server actively refused (used, expired, unknown) and a
// pin-mismatched CA (the handshake itself failing, before any RPC status
// exists) are both configuration problems an operator has to fix — a
// restart policy that treats them as transient would loop until the
// per-address enrollment rate limit trips (fix round 2, minor).
func TestEnsureIdentityClassifiesServerRefusals(t *testing.T) {
	h := newHarness(t)

	// A pin mismatch never reaches an RPC status at all (the TLS handshake
	// itself fails) — still not "unreachable".
	tok, _ := enrolltoken.Parse(h.token(t, "fra-p1"))
	tok.CAPin[0] ^= 0xff
	err := ensureIdentity(t.Context(), tokenConfig(t, tok.String()), quiet(), time.Now, []grpc.DialOption{h.bufconnDialer()})
	if err == nil {
		t.Fatal("a pin mismatch must fail")
	}
	if errors.Is(err, ErrEnrollUnreachable) {
		t.Fatalf("a pin mismatch must not be reported as unreachable: %v", err)
	}

	// An already-spent token: the server answers Unauthenticated.
	spent := h.token(t, "fra-p2")
	firstCfg := tokenConfig(t, spent)
	if err := ensureIdentity(t.Context(), firstCfg, quiet(), time.Now, []grpc.DialOption{h.bufconnDialer()}); err != nil {
		t.Fatal(err)
	}
	err = ensureIdentity(t.Context(), tokenConfig(t, spent), quiet(), time.Now, []grpc.DialOption{h.bufconnDialer()})
	if err == nil {
		t.Fatal("a spent token must be refused")
	}
	if errors.Is(err, ErrEnrollUnreachable) {
		t.Fatalf("a spent token must not be reported as unreachable: %v", err)
	}
}

// TestIsEnrollUnreachableNarrowlyMatchesPinFailures: fix round 3, New
// Breakage 3. "authentication handshake failed" is grpc-go's generic
// prefix for EVERY TLS handshake error, not just a pin mismatch — a
// transient one (an EOF or reset while the manager is still starting up
// behind, say, Docker's userland proxy, which accepts TCP on a published
// port before the real listener is ready) carries that same prefix. Only
// pin.go's own specific wording may classify a handshake failure as a
// non-transient refusal; anything else with that prefix must still count
// as unreachable, or a transient failure gets misdiagnosed as "the
// manager refused enrollment" (exit 2) instead of retried (exit 1).
func TestIsEnrollUnreachableNarrowlyMatchesPinFailures(t *testing.T) {
	pinMismatch := errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: authentication handshake failed: the manager's CA does not match the fingerprint in the enrollment token"`)
	if isEnrollUnreachable(pinMismatch) {
		t.Fatalf("a genuine pin mismatch must not be reported as unreachable: %v", pinMismatch)
	}

	transient := errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: authentication handshake failed: EOF"`)
	if !isEnrollUnreachable(transient) {
		t.Fatalf("a transient handshake failure unrelated to pinning must be reported as unreachable: %v", transient)
	}
	reset := errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: authentication handshake failed: read tcp 127.0.0.1:5555->127.0.0.1:8444: read: connection reset by peer"`)
	if !isEnrollUnreachable(reset) {
		t.Fatalf("a connection reset during the handshake must be reported as unreachable: %v", reset)
	}
}
