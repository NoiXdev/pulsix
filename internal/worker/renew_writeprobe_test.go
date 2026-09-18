package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// countingRenewClient is a workerpb.WorkerServiceClient whose only usable
// method is RenewCertificate; every other method panics, so any test
// using it fails loudly if Check ever calls one of them. failErr, when
// set, makes every RenewCertificate call fail with it instead of
// succeeding.
type countingRenewClient struct {
	mu      sync.Mutex
	calls   int
	failErr error
}

func (c *countingRenewClient) ClaimJob(context.Context, *workerpb.ClaimJobRequest, ...grpc.CallOption) (*workerpb.ClaimJobResponse, error) {
	panic("not used by Check")
}

func (c *countingRenewClient) RunEvents(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[workerpb.RunEvent, workerpb.ManagerDirective], error) {
	panic("not used by Check")
}

func (c *countingRenewClient) ReportResult(context.Context, *workerpb.ReportResultRequest, ...grpc.CallOption) (*workerpb.ReportResultResponse, error) {
	panic("not used by Check")
}

func (c *countingRenewClient) Enroll(context.Context, *workerpb.EnrollRequest, ...grpc.CallOption) (*workerpb.EnrollResponse, error) {
	panic("not used by Check")
}

func (c *countingRenewClient) RenewCertificate(context.Context, *workerpb.RenewCertificateRequest, ...grpc.CallOption) (*workerpb.RenewCertificateResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.failErr != nil {
		return nil, c.failErr
	}
	return &workerpb.RenewCertificateResponse{}, nil
}

func (c *countingRenewClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// TestRenewerRenewsEvenWithReadOnlyDirectory: a renewal due against a
// read-only identity directory must still happen — the worker keeps
// working on the renewed certificate in memory, because a worker that
// simply refused to renew would die for good once the certificate on
// disk expires (fix round 2, Important A). The failure to persist is
// reported as a single actionable ERROR naming the directory, the uid
// that needs write access, the ON-DISK certificate's own expiry (not the
// new one now only in memory) and the consequence of a restart before
// the directory is fixed.
func TestRenewerRenewsEvenWithReadOnlyDirectory(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1", true)
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(cfg.CertPath)
	if err := probeWritable(dir); err != nil {
		t.Skip("cannot simulate a read-only directory with the current privileges")
	}
	onDiskExpiry := id.Leaf().NotAfter
	before := id.Leaf().SerialNumber

	conn, err := dialIdentity(t.Context(), cfg, id, []grpc.DialOption{h.bufconnDialer()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := workerpb.NewWorkerServiceClient(conn)

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	now := id.Leaf().NotAfter.Add(-time.Hour) // due for renewal
	var logs bytes.Buffer
	r := NewRenewer(client, id, func() time.Time { return now }, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	if err := r.Check(t.Context()); err != nil {
		t.Fatalf("Check must still renew (in memory) even when the directory is read-only: %v", err)
	}
	if id.Leaf().SerialNumber.Cmp(before) == 0 {
		t.Fatal("the certificate must be renewed and swapped into memory despite the read-only directory")
	}

	out := logs.String()
	for _, want := range []string{dir, "65532", onDiskExpiry.Format(time.RFC3339), "restart"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the log must mention %q (path, uid, on-disk expiry, restart consequence):\n%s", want, out)
		}
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(cfg.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	onDiskLeaf, err := ca.ParseLeaf(onDisk)
	if err != nil {
		t.Fatal(err)
	}
	if onDiskLeaf.SerialNumber.Cmp(before) != 0 {
		t.Fatal("the on-disk certificate must be unchanged: the write never succeeded")
	}
}

// TestIdentityReplaceSwapsInMemoryEvenWhenDiskWriteFails: when a renewed
// certificate cannot be written to disk (a race after Check's own probe,
// or a caller that skipped it), Replace must still swap it into memory —
// the worker keeps working on it until its next restart rather than keep
// presenting one closer to expiry — while reporting the write failure so
// the caller can log it.
func TestIdentityReplaceSwapsInMemoryEvenWhenDiskWriteFails(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1", true)
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	before := id.Leaf().SerialNumber

	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, id.Signer())
	if err != nil {
		t.Fatal(err)
	}
	renewed, _, err := h.ca.SignCSR(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}), orgID, zoneID, "fra-1")
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Dir(cfg.CertPath)
	if err := probeWritable(dir); err != nil {
		t.Skip("cannot simulate a read-only directory with the current privileges")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err = id.Replace(renewed)
	if err == nil {
		t.Fatal("a write to a read-only directory must report an error")
	}
	if id.Leaf().SerialNumber.Cmp(before) == 0 {
		t.Fatal("the in-memory certificate must be swapped despite the write failure")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	onDisk, _ := os.ReadFile(cfg.CertPath)
	if string(onDisk) == string(renewed) {
		t.Fatal("the on-disk file must be unchanged: the write genuinely failed")
	}
}

// TestRenewerRunContinuesAfterFailedChecks: Run must keep retrying (not
// stop, not panic) when Check keeps failing, with a real injected sleep
// standing in for the 6h interval (the only prior cancel test used an
// already-cancelled context and never exercised this loop with sleep
// actually returning nil).
func TestRenewerRunContinuesAfterFailedChecks(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1", true)
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	client := &countingRenewClient{failErr: context.DeadlineExceeded}
	now := id.Leaf().NotAfter.Add(-time.Hour) // always due

	var mu sync.Mutex
	sleeps := 0
	sleep := func(context.Context, time.Duration) error {
		mu.Lock()
		sleeps++
		n := sleeps
		mu.Unlock()
		if n >= 3 {
			return context.Canceled // stop the loop once we've seen enough
		}
		return nil
	}
	var logs bytes.Buffer
	r := NewRenewer(client, id, func() time.Time { return now }, sleep, slog.New(slog.NewTextHandler(&logs, nil)))
	r.Run(t.Context())

	if n := client.callCount(); n < 3 {
		t.Fatalf("RenewCertificate was called %d time(s); Run must keep retrying after a failed Check", n)
	}
	if !strings.Contains(logs.String(), "certificate renewal reported a problem") {
		t.Fatalf("a failed Check must be logged each time:\n%s", logs.String())
	}
}
