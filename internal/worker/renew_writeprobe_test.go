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
	r := NewRenewer(client, id, func() time.Time { return now }, nil, slog.New(slog.NewTextHandler(&logs, nil)), h.renewRoots(cfg))
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

// TestRenewerRetriesPendingWriteEveryCheckUntilDirectoryIsFixed: a
// renewal that could only be swapped into memory must not be lost once
// the directory is fixed — the worker must not wait for its next
// scheduled renewal (~60 days later) to notice. Every Check while a
// write is pending retries it and logs the same actionable ERROR with
// the TRUE on-disk expiry (not the in-memory one, which by then is a
// fresh ~90-day certificate that makes things look fine when they are
// not). Once the directory is writable, the very next Check — due for a
// new renewal or not — persists the pending certificate, and a fresh
// LoadIdentity from disk afterwards sees the new serial (fix round 3,
// closing New Breakage 1).
func TestRenewerRetriesPendingWriteEveryCheckUntilDirectoryIsFixed(t *testing.T) {
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

	now := id.Leaf().NotAfter.Add(-time.Hour) // due once, for this first Check only

	var logs1 bytes.Buffer
	r1 := NewRenewer(client, id, func() time.Time { return now }, nil, slog.New(slog.NewTextHandler(&logs1, nil)), h.renewRoots(cfg))
	if err := r1.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	newSerial := id.Leaf().SerialNumber
	if newSerial.Cmp(before) == 0 {
		t.Fatal("the certificate must be renewed in memory")
	}
	if !strings.Contains(logs1.String(), onDiskExpiry.Format(time.RFC3339)) {
		t.Fatalf("the first check's warning must name the true on-disk expiry:\n%s", logs1.String())
	}

	// A second check, at a realistic "now" the fresh in-memory certificate
	// is nowhere near due at (unlike the fixed, far-future "now" used to
	// force the first renewal, which would look due against ANY
	// freshly-issued 90-day certificate too, real time having barely
	// moved during the test) — must still retry the write and still warn,
	// with the SAME on-disk expiry as before (nothing has been saved
	// yet), not the in-memory one's ~90-days-out expiry.
	var logs2 bytes.Buffer
	r2 := NewRenewer(client, id, time.Now, nil, slog.New(slog.NewTextHandler(&logs2, nil)), h.renewRoots(cfg))
	if err := r2.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if id.Leaf().SerialNumber.Cmp(newSerial) != 0 {
		t.Fatal("a retry-only check must not mint another certificate")
	}
	if !strings.Contains(logs2.String(), onDiskExpiry.Format(time.RFC3339)) {
		t.Fatalf("every check while a write is pending must warn with the true on-disk expiry:\n%s", logs2.String())
	}
	// DiskExpiry must keep tracking the file that is ACTUALLY on disk
	// (still the original, pre-renewal one) — otherwise the warning would
	// eventually claim far more time is left than a restart would
	// actually find. (Not compared against Leaf().NotAfter directly: two
	// certificates minted less than a second apart, as these are, encode
	// to the identical whole-second X.509 validity timestamp, so that
	// comparison would not reliably distinguish anything here — DiskExpiry
	// matching the true captured on-disk value is the meaningful proof.)
	if !id.DiskExpiry().Equal(onDiskExpiry) {
		t.Fatalf("DiskExpiry = %s, want the original on-disk expiry %s", id.DiskExpiry(), onDiskExpiry)
	}

	// Fix the directory: the next check, even though no new renewal is
	// due, must persist the pending certificate.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r3 := NewRenewer(client, id, time.Now, nil, quiet(), h.renewRoots(cfg))
	if err := r3.Check(t.Context()); err != nil {
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
	if onDiskLeaf.SerialNumber.Cmp(newSerial) != 0 {
		t.Fatal("the pending certificate must be persisted once the directory is writable again")
	}
	reloaded, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Leaf().SerialNumber.Cmp(newSerial) != 0 {
		t.Fatal("a fresh LoadIdentity from disk must see the new serial")
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
	r := NewRenewer(client, id, func() time.Time { return now }, sleep, slog.New(slog.NewTextHandler(&logs, nil)), h.renewRoots(cfg))
	r.Run(t.Context())

	if n := client.callCount(); n < 3 {
		t.Fatalf("RenewCertificate was called %d time(s); Run must keep retrying after a failed Check", n)
	}
	if !strings.Contains(logs.String(), "certificate renewal reported a problem") {
		t.Fatalf("a failed Check must be logged each time:\n%s", logs.String())
	}
}

// TestPendingWriteConsequenceDependsOnDiskExpiry: the pending-write ERROR
// tells the operator what a restart would do. While the certificate on
// disk is still valid, a restart only loses the renewal; once it has
// expired, a restart fails right away and the worker then needs to be
// enrolled again (task 14 re-review N2).
func TestPendingWriteConsequenceDependsOnDiskExpiry(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1", true)
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	expiry := id.DiskExpiry()
	for name, tc := range map[string]struct {
		now        time.Time
		want, lack string
	}{
		"on-disk certificate still valid": {expiry.Add(-time.Hour), "will stop working once that certificate expires", "already expired"},
		"on-disk certificate expired":     {expiry.Add(time.Hour), "already expired", "will stop working once"},
	} {
		var logs bytes.Buffer
		r := NewRenewer(nil, id, func() time.Time { return tc.now }, nil, slog.New(slog.NewTextHandler(&logs, nil)), h.renewRoots(cfg))
		r.logPendingWrite(t.Context(), os.ErrPermission)
		out := logs.String()
		if !strings.Contains(out, tc.want) || strings.Contains(out, tc.lack) {
			t.Fatalf("%s: the consequence must say %q, not %q:\n%s", name, tc.want, tc.lack, out)
		}
		if name == "on-disk certificate expired" && (!strings.Contains(out, "enroll") || !strings.Contains(out, "restart")) {
			t.Fatalf("%s: the consequence must name the restart failure and re-enrollment:\n%s", name, out)
		}
	}
}

// TestRenewRefusesACertificateTheCADidNotIssue is backlog 2.64, the
// worker half of the check the agent already had. The renewal applied
// whatever the manager answered as long as it parsed and matched the
// worker's key — so a leaf from anywhere else would have been written
// over a WORKING identity, and the worker would have found out at its
// next handshake, as an opaque TLS error, with nothing to fall back on.
func TestRenewRefusesACertificateTheCADidNotIssue(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1", true)
	pool, err := CAPool(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// The real CA's answer passes.
	good, _, _, err := h.ca.Issue(context.Background(), &orgID, zoneID, "fra-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyRenewedChain(good, pool); err != nil {
		t.Fatalf("a certificate from the worker's own CA was refused: %v", err)
	}

	// A different CA's does not, however well-formed.
	other := newHarness(t)
	foreign, _, _, err := other.ca.Issue(context.Background(), &orgID, zoneID, "fra-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyRenewedChain(foreign, pool); err == nil {
		t.Fatal("a certificate the worker's CA did not issue was accepted")
	}

	// And no roots at all is a refusal, not a skip: an unchecked
	// renewal is exactly what this exists to prevent, so a caller that
	// forgot WithRoots hears about it at the first renewal instead of
	// quietly losing the check.
	if err := verifyRenewedChain(good, nil); err == nil {
		t.Fatal("a renewer with no CA renewed anyway")
	}
	if err := verifyRenewedChain([]byte("not a certificate"), pool); err == nil {
		t.Fatal("garbage was accepted")
	}
}
