package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/noixdev/pulsix/internal/grpcsrv"
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

// TestReconnectingClientKeepsAnOpenStreamAcrossRedial: a RunEvents stream
// opened before Redial must keep working after it — Redial only changes
// which generation NEW calls go to — and the connection it replaced must
// stay open the whole time the stream is (fix round 2, Important B: a
// blind close timer previously cut any run still going at the time of a
// redial). Only once the stream itself ends does the superseded
// connection become eligible to close, proven here by a direct call on it
// starting to fail only after that point.
func TestReconnectingClientKeepsAnOpenStreamAcrossRedial(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1", true)
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	connA, err := dialIdentity(t.Context(), cfg, id, []grpc.DialOption{h.bufconnDialer()})
	if err != nil {
		t.Fatal(err)
	}
	rc := NewReconnectingClient(connA, quiet())

	jobID := uuid.Must(uuid.NewV7())
	h.claims.Enqueue(&grpcsrv.JobClaim{
		Job:   workerpb.Job{Id: jobID.String(), Kind: "run", OrgId: orgID.String(), ZoneId: zoneID.String()},
		Token: "tok-" + jobID.String(),
	})
	claimResp, err := rc.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"})
	if err != nil || claimResp.GetJob() == nil {
		t.Fatalf("claim: resp=%v err=%v", claimResp, err)
	}
	job := claimResp.GetJob()

	streamCtx := metadata.AppendToOutgoingContext(t.Context(), "job_id", job.GetId(), "worker_token", job.GetWorkerToken())
	stream, err := rc.RunEvents(streamCtx)
	if err != nil {
		t.Fatal(err)
	}

	// A redial while the stream above is still open — as a certificate
	// renewal mid-run would trigger.
	rc.Redial(t.Context(), func(ctx context.Context) (*grpc.ClientConn, error) {
		return dialIdentity(ctx, cfg, id, []grpc.DialOption{h.bufconnDialer()})
	})

	if err := stream.Send(&workerpb.RunEvent{JobId: job.GetId(), Seq: 1, Host: "web-1", Task: "t", State: "ok"}); err != nil {
		t.Fatalf("a stream opened before Redial must keep working after it: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := stream.Recv(); err != nil {
			break // the manager ended the stream; connA is now idle
		}
	}

	waitFor(t, 2*time.Second, func() bool {
		_, err := workerpb.NewWorkerServiceClient(connA).ClaimJob(context.Background(), &workerpb.ClaimJobRequest{Version: "dev"})
		return err != nil // connA is closed once the stream truly ended, not before
	})
}

// TestReconnectingClientRaceStress: concurrent callers hammering every
// method while Redial fires repeatedly underneath them, run under -race
// (and normally with -count=20 in CI for this package, per the fix round
// 3 report). It asserts nothing about outcomes — RunEvents opened with no
// legitimate claim is expected to error immediately, same as some
// ClaimJob/ReportResult/RenewCertificate calls landing between redials —
// the only thing under test is that acquire() (the current generation
// plus its inFlight.Add) can never race Redial's swap and the resulting
// closeWhenIdle's Wait/Close: no data race, and no `sync: WaitGroup
// misuse` panic (fix round 3, closing the New Breakage 2 race).
func TestReconnectingClientRaceStress(t *testing.T) {
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
	rc := NewReconnectingClient(conn, quiet())
	t.Cleanup(func() { _ = rc.Close() })

	stop := make(chan struct{})
	var wg sync.WaitGroup

	caller := func(call func()) {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			call()
		}
	}

	wg.Add(4)
	go caller(func() {
		_, _ = rc.ClaimJob(context.Background(), &workerpb.ClaimJobRequest{Version: "dev"})
	})
	go caller(func() {
		_, _ = rc.ReportResult(context.Background(), &workerpb.ReportResultRequest{JobId: uuid.Must(uuid.NewV7()).String(), Status: "succeeded"})
	})
	go caller(func() {
		_, _ = rc.RenewCertificate(context.Background(), &workerpb.RenewCertificateRequest{})
	})
	go caller(func() {
		stream, err := rc.RunEvents(context.Background())
		if err != nil {
			return
		}
		_ = stream.CloseSend()
		for {
			if _, err := stream.Recv(); err != nil {
				return
			}
		}
	})

	for i := 0; i < 200; i++ {
		rc.Redial(context.Background(), func(ctx context.Context) (*grpc.ClientConn, error) {
			return dialIdentity(ctx, cfg, id, []grpc.DialOption{h.bufconnDialer()})
		})
	}
	close(stop)
	wg.Wait()
}
