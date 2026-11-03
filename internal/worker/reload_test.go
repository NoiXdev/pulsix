// A rewritten identity bundle (final review I2b): `worker add --replace`
// can rewrite the bundle under a running worker — after a restore older
// than the worker's last renewal, or on a new server that was set up
// before the restore. The worker re-reads its certificate, key and CA
// before every handshake, so the next connection uses the new bundle
// without recreating the container.
package worker

import (
	"bytes"
	"context"
	"crypto"
	"crypto/x509"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// copyBundle copies the certificate, key and CA files of from over the
// ones to points at, the way `worker add --replace` rewrites the bundle
// volume.
func copyBundle(t *testing.T, from, to Config) {
	t.Helper()
	for _, p := range [][2]string{{from.KeyPath, to.KeyPath}, {from.CAPath, to.CAPath}, {from.CertPath, to.CertPath}} {
		b, err := os.ReadFile(p[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p[1], b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestIdentityReload: an unchanged file is no change; a rewritten bundle
// (new key and certificate) is installed; a broken one is an error that
// keeps the current identity; a renewal still waiting to be written is
// newer than the file and is never overwritten by it.
func TestIdentityReload(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1", true)
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	first := id.Leaf().SerialNumber

	if changed, err := id.Reload(); changed || err != nil {
		t.Fatalf("reload of an unchanged bundle = %v, %v; want no change", changed, err)
	}

	h.issue("fra-1", true) // rewrites the same three files with a new key
	changed, err := id.Reload()
	if !changed || err != nil {
		t.Fatalf("reload of a rewritten bundle = %v, %v; want a change", changed, err)
	}
	if id.Leaf().SerialNumber.Cmp(first) == 0 {
		t.Fatal("the rewritten certificate is not in use")
	}
	diskLeaf, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !id.Signer().Public().(interface{ Equal(crypto.PublicKey) bool }).Equal(diskLeaf.Signer().Public()) {
		t.Fatal("the rewritten key is not in use")
	}
	if !id.DiskExpiry().Equal(diskLeaf.Leaf().NotAfter) {
		t.Fatal("the disk expiry does not describe the rewritten file")
	}

	second := id.Leaf().SerialNumber
	if err := os.WriteFile(cfg.CertPath, []byte("half-written"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := id.Reload(); changed || err == nil {
		t.Fatalf("reload of a broken bundle = %v, %v; want an error and no change", changed, err)
	}
	if id.Leaf().SerialNumber.Cmp(second) != 0 {
		t.Fatal("a broken bundle replaced the working identity")
	}

	h.issue("fra-1", true)
	id.mu.Lock()
	id.pending = []byte("a renewal the read-only directory could not take yet")
	id.mu.Unlock()
	if changed, err := id.Reload(); changed || err != nil {
		t.Fatalf("reload with a pending write = %v, %v; want no change", changed, err)
	}
	if id.Leaf().SerialNumber.Cmp(second) != 0 {
		t.Fatal("the file replaced a renewal that is still waiting to be written")
	}
}

// TestRedialOnNewIdentityAfterARewrittenBundle: the restore case. The
// manager no longer knows the worker's serial, so the open connection's
// calls fail with Unauthenticated; once `worker add --replace` has
// rewritten the bundle, the hook the loop calls on Unauthenticated
// reconnects with the new certificate and claims succeed again. Before
// the bundle changes, the hook does nothing.
func TestRedialOnNewIdentityAfterARewrittenBundle(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1", true)
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	dial := func(ctx context.Context) (*grpc.ClientConn, error) {
		return dialIdentity(ctx, cfg, id, []grpc.DialOption{h.bufconnDialer()})
	}
	conn, err := dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rc := NewReconnectingClient(conn, quiet())
	t.Cleanup(func() { _ = rc.Close() })
	if _, err := rc.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"}); err != nil {
		t.Fatal(err)
	}
	old := id.Leaf().SerialNumber

	// The restored database does not know the serial.
	h.store.mu.Lock()
	delete(h.store.bySerial, old.String())
	h.store.mu.Unlock()
	if _, err := rc.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("claim with the unknown serial = %v, want Unauthenticated", err)
	}
	hook := RedialOnNewIdentity(id, rc, dial, quiet())
	if hook(t.Context()) {
		t.Fatal("the hook reconnected although the bundle did not change")
	}

	h.issue("fra-1", true) // worker add --replace rewrites the bundle
	if !hook(t.Context()) {
		t.Fatal("the hook did not pick up the rewritten bundle")
	}
	if id.Leaf().SerialNumber.Cmp(old) == 0 {
		t.Fatal("the identity still holds the old certificate")
	}
	if _, err := rc.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"}); err != nil {
		t.Fatalf("claim after the reconnect: %v", err)
	}
}

// TestHandshakeRereadsTheBundleBetweenDials: a new server whose CA is not
// the one the worker started with. The worker's first handshakes fail (it
// does not trust the server); once the bundle is rewritten with the new
// CA, certificate and key, gRPC's own next reconnect reads the files
// again and succeeds — on the same connection object, without a restart.
func TestHandshakeRereadsTheBundleBetweenDials(t *testing.T) {
	oldServer := newHarness(t)
	cfg := oldServer.issue("fra-1", true)
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	newServer := newHarness(t)
	conn, err := dialIdentity(t.Context(), cfg, id, []grpc.DialOption{newServer.bufconnDialer()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := workerpb.NewWorkerServiceClient(conn)
	if _, err := client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("claim against a server of another CA = %v, want Unavailable", err)
	}

	copyBundle(t, newServer.issue("fra-1", true), cfg)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := client.ClaimJob(ctx, &workerpb.ClaimJobRequest{Version: "dev"}, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("claim after the bundle was rewritten: %v", err)
	}
	newLeaf, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if id.Leaf().SerialNumber.Cmp(newLeaf.Leaf().SerialNumber) != 0 {
		t.Fatal("the identity does not hold the rewritten certificate")
	}
}

// TestLoopUnauthenticatedCallsTheIdentityHook: every claim refused with
// Unauthenticated asks the hook; when it reconnected with a new identity
// the loop says so instead of diagnosing a revocation.
func TestLoopUnauthenticatedCallsTheIdentityHook(t *testing.T) {
	for _, reloaded := range []bool{false, true} {
		cfg := cfgFor(t.TempDir())
		client := &fakeWorkerClient{}
		client.setClaimErr(status.Error(codes.Unauthenticated, "denied"))
		var logs bytes.Buffer
		calls := make(chan struct{}, 100)
		l := NewLoop(cfg, client, &scriptExecutor{},
			WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
			WithSleep(func(context.Context, time.Duration) error { return nil }),
			WithOnUnauthenticated(func(context.Context) bool {
				select {
				case calls <- struct{}{}:
				default:
				}
				return reloaded
			}))
		cancel, done := runLoop(t, l)
		waitFor(t, 2*time.Second, func() bool { return len(client.claimsNow()) >= 2 })
		cancel()
		if err := <-done; err != nil {
			t.Errorf("done: %v", err)
		}
		if len(calls) == 0 {
			t.Fatalf("reloaded=%v: the hook was never called", reloaded)
		}
		got := logs.String()
		if reloaded != strings.Contains(got, "identity files changed") {
			t.Errorf("reloaded=%v: log = %s", reloaded, got)
		}
		if reloaded == strings.Contains(got, "revoked") {
			t.Errorf("reloaded=%v: the revocation diagnosis must appear only without a new identity: %s", reloaded, got)
		}
	}
}

// TestRenewerReadsItsRootsOnEveryCheck: a renewer given a roots loader
// checks a renewed certificate against what the loader returns at that
// moment, so a rewritten CA file is honoured without a restart.
func TestRenewerReadsItsRootsOnEveryCheck(t *testing.T) {
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
	client := workerpb.NewWorkerServiceClient(conn)
	now := id.Leaf().NotAfter.Add(-time.Hour)

	other := newHarness(t)
	foreign, err := CAPool(other.issue("x", false))
	if err != nil {
		t.Fatal(err)
	}
	roots := foreign
	r := NewRenewer(client, id, func() time.Time { return now }, nil, quiet(),
		WithRootsFrom(func() (*x509.CertPool, error) { return roots, nil }))
	before := id.Leaf().SerialNumber
	if err := r.Check(t.Context()); err == nil || id.Leaf().SerialNumber.Cmp(before) != 0 {
		t.Fatalf("a renewal from a CA the loader does not return = %v; want refused", err)
	}

	if roots, err = CAPool(cfg); err != nil {
		t.Fatal(err)
	}
	if err := r.Check(t.Context()); err != nil {
		t.Fatalf("renewal with the current roots: %v", err)
	}
	if id.Leaf().SerialNumber.Cmp(before) == 0 {
		t.Fatal("the certificate was not renewed")
	}
}
