// Server assembly and ReportResult: the terminal write and its audit, the
// closed status set, and the malformed-input cases.
package grpcsrv

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// TestNewRequiresClockAndCA guards the wiring contract: a Deps without the
// pieces the server cannot build must fail at construction, not at the
// first RPC.
func TestNewRequiresClockAndCA(t *testing.T) {
	if _, err := newServer(Deps{}); err == nil {
		t.Fatal("New without a Clock must fail")
	}
}

// TestReportResultHappyPath: the terminal write lands with the recorder
// and one job.result audit event.
func TestReportResultHappyPath(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM, _ := h.issue("fra-1-a", orgID, zoneID, true)
	client := h.client(certPEM, keyPEM)

	jobID := uuid.Must(uuid.NewV7())
	if _, err := client.ReportResult(t.Context(), &workerpb.ReportResultRequest{
		JobId: jobID.String(), Status: "succeeded", Stats: `{"web-1":{"ok":2,"changed":1}}`,
	}); err != nil {
		t.Fatal(err)
	}
	results := h.results.Results()
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].JobID != jobID.String() || results[0].Status != "succeeded" || results[0].Stats["web-1"] == nil {
		t.Fatalf("result = %+v", results[0])
	}
	events := h.audit.Events()
	if len(events) != 1 || events[0].Operation != "job.result" || events[0].ObjectID != jobID.String() {
		t.Fatalf("audit events = %+v, want one job.result", events)
	}
}

// TestReportResultUnknownStatus: the status set is closed.
func TestReportResultUnknownStatus(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM, _ := h.issue("fra-1-a", orgID, zoneID, true)
	client := h.client(certPEM, keyPEM)

	jobID := uuid.Must(uuid.NewV7())
	_, err := client.ReportResult(t.Context(), &workerpb.ReportResultRequest{JobId: jobID.String(), Status: "somehow-fine"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %s (%v), want InvalidArgument", status.Code(err), err)
	}
	if len(h.results.Results()) != 0 {
		t.Fatalf("a rejected result must not be recorded")
	}
}

// TestReportResultBadIDs: empty and unparseable job ids are the caller's
// error.
func TestReportResultBadIDs(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM, _ := h.issue("fra-1-a", orgID, zoneID, true)
	client := h.client(certPEM, keyPEM)

	for _, jobID := range []string{"", "not-a-uuid"} {
		_, err := client.ReportResult(t.Context(), &workerpb.ReportResultRequest{JobId: jobID, Status: "failed"})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("job id %q: code = %s (%v), want InvalidArgument", jobID, status.Code(err), err)
		}
	}
}

// TestReportResultBadStats: present stats must be a JSON object.
func TestReportResultBadStats(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM, _ := h.issue("fra-1-a", orgID, zoneID, true)
	client := h.client(certPEM, keyPEM)

	jobID := uuid.Must(uuid.NewV7())
	_, err := client.ReportResult(t.Context(), &workerpb.ReportResultRequest{
		JobId: jobID.String(), Status: "failed", Stats: "[]",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %s (%v), want InvalidArgument", status.Code(err), err)
	}
}

// TestRegistryTTLBound: the registry's expiry rides the injected clock, so
// a claim made "now" stays valid for exactly the 30 s TTL — verified here
// on the registry itself (the stream-level expiry is the expired-token test
// in events_test.go).
func TestRegistryTTLBound(t *testing.T) {
	h := newHarness(t)
	jobID := uuid.Must(uuid.NewV7())
	h.srv.registry.register(jobID, "tok")
	if ok := h.srv.registry.consume(jobID, "tok"); !ok {
		t.Fatal("a fresh binding must consume")
	}

	// Re-register and jump past the TTL.
	h.srv.registry.register(jobID, "tok")
	h.advance(31 * time.Second)
	if ok := h.srv.registry.consume(jobID, "tok"); ok {
		t.Fatal("an expired binding must not consume")
	}
}
