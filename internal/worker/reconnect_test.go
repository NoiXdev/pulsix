package worker

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// TestReconnectingClientRedialsAfterRenewal: a certificate renewal must
// not just swap Identity in memory — a connection dialed BEFORE the
// renewal keeps presenting the certificate it handshook with (gRPC does
// not re-run GetClientCertificate on an already-established connection),
// so the worker has to move future calls to a fresh connection for the
// renewed leaf to ever reach the wire. This proves the new connection
// really is authenticated with the RENEWED serial, not just that calls
// keep working (the old certificate would also still work at this point,
// since Renew never invalidates it) — by making the OLD serial stop
// resolving server-side (the same failure mode it eventually hits for
// real, at its own expiry) and checking calls only succeed through the
// reconnected client, never through the stale connection.
func TestReconnectingClientRedialsAfterRenewal(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1", true)
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	oldConn, err := dialIdentity(t.Context(), cfg, id, []grpc.DialOption{h.bufconnDialer()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oldConn.Close() })
	oldSerial := id.Leaf().SerialNumber

	rc := NewReconnectingClient(oldConn, quiet())

	now := id.Leaf().NotAfter.Add(-time.Hour) // due for renewal
	r := NewRenewer(rc, id, func() time.Time { return now }, nil, quiet(), WithOnRenewed(func(ctx context.Context) {
		rc.Redial(ctx, func(ctx context.Context) (*grpc.ClientConn, error) {
			return dialIdentity(ctx, cfg, id, []grpc.DialOption{h.bufconnDialer()})
		})
	}))
	if err := r.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if id.Leaf().SerialNumber.Cmp(oldSerial) == 0 {
		t.Fatal("the certificate was not renewed")
	}

	// Simulate the old serial becoming untrusted, the way it eventually
	// would at its own expiry: the reconnected client must keep working
	// (it is on the new connection, authenticated with the new serial),
	// while a call still made directly on the pre-renewal connection must
	// now fail — proving that connection really was carrying the old one.
	h.store.mu.Lock()
	delete(h.store.bySerial, oldSerial.String())
	h.store.mu.Unlock()

	if _, err := rc.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"}); err != nil {
		t.Fatalf("the reconnected client must authenticate with the renewed serial: %v", err)
	}
	if _, err := workerpb.NewWorkerServiceClient(oldConn).ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"}); err == nil {
		t.Fatal("the pre-renewal connection must still be carrying the old (now untrusted) serial")
	}
}

// TestReconnectingClientRedialFailureKeepsTheOldConnection: a dial
// failure (the manager unreachable right when renewal fires) must not
// break the worker — it keeps using the connection it already has, and
// the next renewal check tries the reconnect again.
func TestReconnectingClientRedialFailureKeepsTheOldConnection(t *testing.T) {
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

	rc := NewReconnectingClient(conn, quiet())
	rc.Redial(t.Context(), func(context.Context) (*grpc.ClientConn, error) {
		return nil, context.DeadlineExceeded
	})
	if _, err := rc.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"}); err != nil {
		t.Fatalf("a failed redial must leave the working connection in place: %v", err)
	}
}
