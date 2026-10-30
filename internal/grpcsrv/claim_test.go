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
	if len(calls) != 1 || len(calls[0].Scope.ZoneIDs) != 1 || calls[0].Scope.ZoneIDs[0] != zoneID {
		t.Fatalf("Claim was called with zones %v, want exactly [%s]", calls, zoneID)
	}
}

// TestClaimJobPassesOnWhetherCancelStopsThePlay (backlog 2.113): the
// worker's own answer reaches the claim port unchanged, so the job records
// it; an older worker that never sends the field reads as false.
func TestClaimJobPassesOnWhetherCancelStopsThePlay(t *testing.T) {
	for _, want := range []bool{true, false} {
		h := newHarness(t)
		certPEM, keyPEM, _ := h.issue("fra-1-a", orgID, zoneID, true)
		client := h.client(certPEM, keyPEM)
		h.enqueueClaim(uuid.Must(uuid.NewV7()), orgID, zoneID)

		if _, err := client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "0.2.0", CancelStopsPlay: want}); err != nil {
			t.Fatal(err)
		}
		calls := h.claims.Calls()
		if len(calls) != 1 || calls[0].CancelStopsPlay != want {
			t.Fatalf("Claim calls = %+v, want one with CancelStopsPlay %v", calls, want)
		}
	}
}

// TestClaimJobZoneAuthorization (fix round 1, Critical): a worker claims
// only for the zone its certificate was enrolled for. A request naming any
// other zone — another organization's zone (zone ids are visible through
// that organization's API) or a sibling zone of its own organization — is
// refused with PermissionDenied before the claim port is ever reached, so
// the foreign job (and its revealed inventory and secrets) never ships.
// The claim port always receives the worker's own organization.
func TestClaimJobZoneAuthorization(t *testing.T) {
	otherOrg := uuid.MustParse("33333333-3333-7333-8333-333333333333")
	otherOrgZone := uuid.MustParse("44444444-4444-7444-8444-444444444444")
	sameOrgOtherZone := uuid.MustParse("55555555-5555-7555-8555-555555555555")

	tests := []struct {
		name      string
		zones     []uuid.UUID
		wantCode  codes.Code
		wantClaim bool
	}{
		{name: "own zone", zones: []uuid.UUID{zoneID}, wantCode: codes.OK, wantClaim: true},
		{name: "empty request resolves to own zone", zones: nil, wantCode: codes.OK, wantClaim: true},
		{name: "own zone listed twice", zones: []uuid.UUID{zoneID, zoneID}, wantCode: codes.OK, wantClaim: true},
		{name: "other organization's zone", zones: []uuid.UUID{otherOrgZone}, wantCode: codes.PermissionDenied},
		{name: "own zone plus other organization's zone", zones: []uuid.UUID{zoneID, otherOrgZone}, wantCode: codes.PermissionDenied},
		{name: "same organization, other zone", zones: []uuid.UUID{sameOrgOtherZone}, wantCode: codes.PermissionDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			certPEM, keyPEM, serial := h.issue("fra-1-a", orgID, zoneID, true)
			client := h.client(certPEM, keyPEM)

			// The queue holds a job of the OTHER organization: whatever the
			// port would hand out must never reach a worker that asked for
			// a zone it is not enrolled for.
			foreignJob := uuid.Must(uuid.NewV7())
			h.enqueueClaim(foreignJob, otherOrg, otherOrgZone)

			ids := make([]string, len(tt.zones))
			for i, z := range tt.zones {
				ids[i] = z.String()
			}
			resp, err := client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "0.2.0", ZoneIds: ids})
			if status.Code(err) != tt.wantCode {
				t.Fatalf("code = %s (%v), want %s", status.Code(err), err, tt.wantCode)
			}
			calls := h.claims.Calls()
			if !tt.wantClaim {
				if resp.GetJob() != nil {
					t.Fatalf("denied claim shipped a job: %+v", resp.GetJob())
				}
				if len(calls) != 0 {
					t.Fatalf("denied claim reached the claim port: %+v", calls)
				}
				if len(h.audit.Events()) != 0 {
					t.Fatalf("denied claim audited a job claim: %+v", h.audit.Events())
				}
				return
			}
			if len(calls) != 1 {
				t.Fatalf("claim port calls = %d, want 1", len(calls))
			}
			if row, err := h.store.GetBySerial(t.Context(), serial); err != nil || calls[0].Scope.WorkerID != row.ID {
				t.Fatalf("claim scope worker = %s, want the authenticated worker's row id (%v)", calls[0].Scope.WorkerID, err)
			}
			if calls[0].Scope.OrgID != orgID {
				t.Fatalf("claim scope org = %s, want the worker's own org %s", calls[0].Scope.OrgID, orgID)
			}
			if len(calls[0].Scope.ZoneIDs) != 1 || calls[0].Scope.ZoneIDs[0] != zoneID {
				t.Fatalf("claim scope zones = %v, want exactly [%s]", calls[0].Scope.ZoneIDs, zoneID)
			}
		})
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
