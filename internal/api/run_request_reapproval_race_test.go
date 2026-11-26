package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
)

// raceGuard is the real claim guard with hooks, each run once:
// afterWithdrawn right after the real guard answered "approval withdrawn"
// and before the queue acts on that answer — the window of backlog 2.127
// (5); whileConfirmed right after the real guard confirmed the withdrawal
// under the request's lock, inside the transaction that fails the job.
type raceGuard struct {
	*runrequests.Service
	afterWithdrawn func()
	whileConfirmed func()
}

func (g *raceGuard) ConfirmWithdrawn(ctx context.Context, orgID, runGroupID uuid.UUID) (bool, error) {
	ok, err := g.Service.ConfirmWithdrawn(ctx, orgID, runGroupID)
	if ok && err == nil && g.whileConfirmed != nil {
		hook := g.whileConfirmed
		g.whileConfirmed = nil
		hook()
	}
	return ok, err
}

func (g *raceGuard) CheckRunClaim(ctx context.Context, orgID, runGroupID uuid.UUID, run jobs.ApprovedRun) error {
	err := g.Service.CheckRunClaim(ctx, orgID, runGroupID, run)
	if errors.Is(err, jobs.ErrApprovalWithdrawn) && g.afterWithdrawn != nil {
		hook := g.afterWithdrawn
		g.afterWithdrawn = nil
		hook()
	}
	return err
}

// withdrawnFirstEnqueue makes a request over both hosts (one per zone)
// whose approval enqueued zone A's job only and went back to pending, and
// returns the request's id and that job.
func withdrawnFirstEnqueue(t *testing.T, f runRequestFixture) (string, jobs.Job) {
	t.Helper()
	f.targetBothHosts(t)
	created := f.request(t, map[string]any{"reason": "x"})
	lift := refuseJobInserts(t, f.zoneB)
	resp := f.approve(t, f.adminC, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode == http.StatusOK {
		t.Fatalf("approve with zone B's insert failing succeeded: %s", body)
	}
	lift()
	first := f.groupJobs(t, created.Request.ID)
	if len(first) != 1 || first[0].ZoneID != f.zoneA {
		t.Fatalf("jobs after the interrupted enqueue = %+v, want zone A's only", first)
	}
	return created.Request.ID, first[0]
}

// claimByZone claims until the queue is empty and counts the claims by zone.
func (f runRequestFixture) claimByZone(t *testing.T) map[uuid.UUID]int {
	t.Helper()
	claimed := map[uuid.UUID]int{}
	for range 4 {
		c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA, f.zoneB}, "v1")
		if err != nil {
			t.Fatal(err)
		}
		if c == nil {
			break
		}
		j, err := f.s.Deps.Jobs.Get(f.scoped, f.orgID, uuid.MustParse(c.Job.GetId()))
		if err != nil {
			t.Fatal(err)
		}
		claimed[j.ZoneID]++
	}
	return claimed
}

// TestRunRequestReapprovalWhileAClaimIsRefusedRunsTheZone is backlog 2.127
// (5): the claim guard read the request as pending, and before the queue
// failed the job a person approved the request again, whose replay of the
// run group saw the job still running. The job must not be failed as
// withdrawn then — its zone would never run — but run.
func TestRunRequestReapprovalWhileAClaimIsRefusedRunsTheZone(t *testing.T) {
	f := newRunRequestFixture(t, "rr-race-approved")
	id, first := withdrawnFirstEnqueue(t, f)

	guard := &raceGuard{Service: f.s.Deps.RunRequests}
	guard.afterWithdrawn = func() {
		resp := f.approve(t, f.adminC, id, f.detail(t, id).Fingerprint)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("re-approve in the claim's window = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
	}
	f.s.Queue.SetClaimGuard(guard)

	c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA}, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.Job.GetId() != first.ID.String() {
		t.Fatalf("claim of zone A's job re-approved in its window = %v, want the job %s", c, first.ID)
	}
	if guard.afterWithdrawn != nil {
		t.Fatal("the guard never answered withdrawn")
	}
	if got := f.claimByZone(t); len(got) != 1 || got[f.zoneB] != 1 {
		t.Fatalf("further claims by zone = %v, want zone B's job only", got)
	}
	if all := f.groupJobs(t, id); len(all) != 2 {
		t.Fatalf("jobs of the run = %+v, want one per zone", all)
	}
}

// approveAsync approves the request on a goroutine of its own and returns
// the channel its status code (or 0 on a transport error) arrives on.
func (f runRequestFixture) approveAsync(id, fingerprint string) <-chan int {
	done := make(chan int, 1)
	go func() {
		b, _ := json.Marshal(map[string]any{"fingerprint": fingerprint})
		resp, err := f.adminC.Post(f.s.HTTP.URL+f.orgPath()+"/run-requests/"+id+"/approve", "application/json", bytes.NewReader(b))
		if err != nil {
			done <- 0
			return
		}
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()
	return done
}

// waitForBlockedDecide waits until an approval's decision waits for a lock
// in the test database, or the approval ended without waiting.
func waitForBlockedDecide(t *testing.T, approved <-chan int) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%DecideRunRequest%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		select {
		case code := <-approved:
			t.Fatalf("the re-approval did not wait for the withdrawn claim's failure (status %d)", code)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("the re-approval's decision never waited for a lock")
}

// TestRunRequestReapprovalWaitsForAWithdrawnClaimAndRefillsTheZone is the
// other order of backlog 2.127 (5): the claim confirmed the withdrawal
// under the request's lock and is about to fail the job when a person
// approves again. The approval's decision waits for the failure to
// commit, so its replay sees the job failed as withdrawn and enqueues its
// zone again: both zones run.
func TestRunRequestReapprovalWaitsForAWithdrawnClaimAndRefillsTheZone(t *testing.T) {
	f := newRunRequestFixture(t, "rr-race-refill")
	id, first := withdrawnFirstEnqueue(t, f)

	fingerprint := f.detail(t, id).Fingerprint
	var approved <-chan int
	guard := &raceGuard{Service: f.s.Deps.RunRequests}
	guard.whileConfirmed = func() {
		approved = f.approveAsync(id, fingerprint)
		waitForBlockedDecide(t, approved)
	}
	f.s.Queue.SetClaimGuard(guard)

	if c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA}, "v1"); err != nil || c != nil {
		t.Fatalf("claim of the withdrawn job = %v, %v; want refused", c, err)
	}
	if approved == nil {
		t.Fatal("the guard never confirmed the withdrawal")
	}
	if code := <-approved; code != http.StatusOK {
		t.Fatalf("re-approve = %d", code)
	}
	refused, err := f.s.Deps.Jobs.Get(f.scoped, f.orgID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !refused.WithdrawnAtClaim() {
		t.Fatalf("refused job = %s %q, want withdrawn at claim", refused.Status, refused.Error)
	}
	if got := f.claimByZone(t); len(got) != 2 || got[f.zoneA] != 1 || got[f.zoneB] != 1 {
		t.Fatalf("claims of the re-approved run by zone = %v, want one each for zone A and zone B", got)
	}
	if all := f.groupJobs(t, id); len(all) != 3 {
		t.Fatalf("jobs of the run = %d, want the withdrawn one and one per zone", len(all))
	}
}
