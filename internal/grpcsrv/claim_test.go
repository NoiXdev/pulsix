// ClaimJob: happy path, empty queue, malformed zones, the heartbeat, the
// audit event, the binding-token registration and parallel-claim safety.
package grpcsrv

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// TestClaimJobHappyPath claims the single queued job and checks every
// side effect at once: payload echo, token, audit, heartbeat and the
// registry entry RunEvents will later consume.
func TestClaimJobHappyPath(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM, serial := h.issue("fra-1-a", orgID, zoneID, true)
	client := h.client(certPEM, keyPEM)

	jobID := uuid.Must(uuid.NewV7())
	h.enqueueClaim(jobID, orgID, zoneID)

	resp, err := client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{
		Version: "0.2.0", ZoneIds: []string{zoneID.String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.GetJob().GetId(); got != jobID.String() {
		t.Fatalf("claimed id = %q, want %q", got, jobID)
	}
	if got := resp.GetJob().GetKind(); got != "run" {
		t.Fatalf("kind = %q, want run", got)
	}
	if got := resp.GetJob().GetWorkerToken(); got != testToken(jobID) {
		t.Fatalf("worker_token = %q, want the claim's token", got)
	}

	// The heartbeat: last_seen/version touch with the self-reported build.
	if version := h.store.touched[serial.String()]; version != "0.2.0" {
		t.Fatalf("touched version = %q, want 0.2.0", version)
	}

	// Audit: job.claim with org and job id.
	events := h.audit.Events()
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	if events[0].Operation != "job.claim" || events[0].ObjectID != jobID.String() || events[0].OrgID != orgID.String() {
		t.Fatalf("audit event = %+v", events[0])
	}

	// The binding token is registered for RunEvents.
	bound, ok := h.srv.registry.lookup(jobID)
	if !ok || bound.token != testToken(jobID) {
		t.Fatalf("registry binding = %+v ok=%v, want the claim's token", bound, ok)
	}
	if got := bound.expiresAt.Sub(h.clock()); got > 30*time.Second || got <= 0 {
		t.Fatalf("binding TTL = %v, want within (0, 30s]", got)
	}
}

// TestClaimJobEmptyQueue: no claim → empty response, no audit.
func TestClaimJobEmptyQueue(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM, _ := h.issue("fra-1-a", orgID, zoneID, true)
	client := h.client(certPEM, keyPEM)

	resp, err := client.ClaimJob(t.Context(), claimRequest(zoneID))
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetJob() != nil {
		t.Fatalf("empty queue must yield no job, got %+v", resp.GetJob())
	}
	if len(h.audit.Events()) != 0 {
		t.Fatalf("empty claim must not audit, got %+v", h.audit.Events())
	}
}

// TestClaimJobEmptyZoneIDsFallsBackToWorkerZone (Task 17): a claim request
// with no zone ids at all — internal/worker's documented default when
// PULSIX_WORKER_ZONES is unset (or, equivalently, the embedded worker,
// which never sets it) — must resolve to the calling worker's own
// registered zone rather than matching nothing.
func TestClaimJobEmptyZoneIDsFallsBackToWorkerZone(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM, _ := h.issue("fra-1-a", orgID, zoneID, true)
	client := h.client(certPEM, keyPEM)

	jobID := uuid.Must(uuid.NewV7())
	h.enqueueClaim(jobID, orgID, zoneID)

	resp, err := client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "0.2.0"}) // ZoneIds deliberately empty
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.GetJob().GetId(); got != jobID.String() {
		t.Fatalf("claimed id = %q, want %q — empty ZoneIds must fall back to the worker's registered zone", got, jobID)
	}
	calls := h.claims.Calls()
	if len(calls) != 1 || len(calls[0].Zones) != 1 || calls[0].Zones[0] != zoneID {
		t.Fatalf("Claim was called with zones %v, want exactly [%s]", calls, zoneID)
	}
}

// TestClaimJobInvalidZoneIDs: an unparseable zone id is the caller's error.
func TestClaimJobInvalidZoneIDs(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM, _ := h.issue("fra-1-a", orgID, zoneID, true)
	client := h.client(certPEM, keyPEM)

	_, err := client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{ZoneIds: []string{"not-a-uuid"}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %s (%v), want InvalidArgument", status.Code(err), err)
	}
}

// TestClaimJobParallel: two concurrent claims over one connection must not
// race (the test runs under -race; the fake serves one job, then an empty
// queue).
func TestClaimJobParallel(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM, _ := h.issue("fra-1-a", orgID, zoneID, true)
	client := h.client(certPEM, keyPEM)

	jobID := uuid.Must(uuid.NewV7())
	h.enqueueClaim(jobID, orgID, zoneID)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = client.ClaimJob(t.Context(), claimRequest(zoneID))
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("parallel claim: %v", err)
		}
	}
}

// TestClaimResultLogsCarryNoCredentialMaterial: the one log-capture test —
// with secrets all over the payload, the default logger must not echo a
// single one of them.
func TestClaimResultLogsCarryNoCredentialMaterial(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM, _ := h.issue("fra-1-a", orgID, zoneID, true)
	client := h.client(certPEM, keyPEM)

	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	jobID := uuid.Must(uuid.NewV7())
	secret1, secret2 := "super-secret-password", "kind-payload-secret"
	token := testToken(jobID)
	h.claims.Enqueue(&JobClaim{
		Job: workerpb.Job{
			Id: jobID.String(), Kind: "run", OrgId: orgID.String(), ZoneId: zoneID.String(),
			Credentials: map[string]string{"cred-1": `{"password":"` + secret1 + `"}`},
			KindPayload: `{"bootstrap_password":"` + secret2 + `"}`,
		},
		Token: token,
	})
	if _, err := client.ClaimJob(t.Context(), claimRequest(zoneID)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReportResult(t.Context(), &workerpb.ReportResultRequest{
		JobId: jobID.String(), Status: "failed", Stats: `{"web-1":{"ok":3}}`, Error: "boom",
	}); err != nil {
		t.Fatal(err)
	}

	captured := logs.String()
	for _, forbidden := range []string{secret1, secret2, token, `{"web-1":{"ok":3}}`, "boom"} {
		if strings.Contains(captured, forbidden) {
			t.Fatalf("log output leaked %q:\n%s", forbidden, captured)
		}
	}
}
