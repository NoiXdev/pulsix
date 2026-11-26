package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

// superConn is a superuser connection to the test database, closed at the
// test's end.
func superConn(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// setManaged turns a host's managed flag, behind every service's back.
func setManaged(t *testing.T, conn *pgx.Conn, host uuid.UUID, managed bool) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), `UPDATE hosts SET managed = $2 WHERE id = $1`, host, managed); err != nil {
		t.Fatal(err)
	}
}

// manageOnApproval makes host managed in the transaction that records a
// request's approval: after the approval read the run picture, before the
// enqueue resolves the template's hosts. It returns what lifts it.
func manageOnApproval(t *testing.T, conn *pgx.Conn, host uuid.UUID) (lift func()) {
	t.Helper()
	exec := func(sql string) {
		if _, err := conn.Exec(context.Background(), sql); err != nil {
			t.Fatal(err)
		}
	}
	lift = func() {
		_, _ = conn.Exec(context.Background(), `DROP TRIGGER IF EXISTS pulsix_test_manage_on_approval ON run_requests`)
		_, _ = conn.Exec(context.Background(), `DROP FUNCTION IF EXISTS pulsix_test_manage_on_approval()`)
	}
	t.Cleanup(lift)
	exec(fmt.Sprintf(`CREATE OR REPLACE FUNCTION pulsix_test_manage_on_approval() RETURNS trigger
	      LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN UPDATE hosts SET managed = true WHERE id = '%s'; RETURN NEW; END $$`, host))
	exec(`CREATE TRIGGER pulsix_test_manage_on_approval AFTER UPDATE ON run_requests FOR EACH ROW
	      WHEN (NEW.status = 'approved' AND OLD.status <> 'approved') EXECUTE FUNCTION pulsix_test_manage_on_approval()`)
	return lift
}

// TestRunRequestClaimRefusesAHostTheApprovalDidNotInclude is backlog 2.141:
// a host joins the template's hosts after the approval read the run
// picture and before the enqueue resolved them, and leaves again before
// the claim. The live picture then matches the approved one again, but
// the job froze the host at enqueue: its claim must fail visibly instead
// of rendering a host nobody approved. The approved zone's job runs.
func TestRunRequestClaimRefusesAHostTheApprovalDidNotInclude(t *testing.T) {
	f := newRunRequestFixture(t, "rr-hosts-changed")
	f.targetBothHosts(t)
	conn := superConn(t)
	setManaged(t, conn, f.hostB, false)
	created := f.request(t, map[string]any{"reason": "x"})
	fingerprint := f.detail(t, created.Request.ID).Fingerprint

	lift := manageOnApproval(t, conn, f.hostB)
	resp := f.approve(t, f.adminC, created.Request.ID, fingerprint)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	lift()
	setManaged(t, conn, f.hostB, false)
	if all := f.groupJobs(t, created.Request.ID); len(all) != 2 {
		t.Fatalf("jobs of the run = %+v, want zone B's joined host enqueued too", all)
	}
	if got := f.detail(t, created.Request.ID).Fingerprint; got != fingerprint {
		t.Fatal("the picture differs from the approved one after the host left again")
	}

	if c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneB}, "v1"); err != nil || c != nil {
		t.Fatalf("claim of the job with the unapproved host = %v, %v; want refused", c, err)
	}
	var refused jobs.Job
	for _, j := range f.groupJobs(t, created.Request.ID) {
		if j.ZoneID == f.zoneB {
			refused = j
		}
	}
	if refused.Status != jobs.StatusError || !strings.HasPrefix(refused.Error, "refused at claim: the run's hosts changed after it was approved") ||
		refused.WithdrawnAtClaim() {
		t.Fatalf("zone B's job = %s %q, want refused because its hosts changed", refused.Status, refused.Error)
	}
	if c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA}, "v1"); err != nil || c == nil {
		t.Fatalf("claim of the approved zone's job = %v, %v; want it claimed", c, err)
	}
}
