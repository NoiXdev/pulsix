package api_test

import (
	"context"
	"encoding/json"
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

// renameHost renames a host, behind every service's back.
func renameHost(t *testing.T, conn *pgx.Conn, host uuid.UUID, name string) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), `UPDATE hosts SET name = $2 WHERE id = $1`, host, name); err != nil {
		t.Fatal(err)
	}
}

// renameOnApproval renames host to name in the transaction that records a
// request's approval: after the approval read the run picture, before the
// enqueue resolves the run's hosts. It returns what lifts it.
func renameOnApproval(t *testing.T, conn *pgx.Conn, host uuid.UUID, name string) (lift func()) {
	t.Helper()
	exec := func(sql string) {
		if _, err := conn.Exec(context.Background(), sql); err != nil {
			t.Fatal(err)
		}
	}
	lift = func() {
		_, _ = conn.Exec(context.Background(), `DROP TRIGGER IF EXISTS pulsix_test_rename_on_approval ON run_requests`)
		_, _ = conn.Exec(context.Background(), `DROP FUNCTION IF EXISTS pulsix_test_rename_on_approval()`)
	}
	t.Cleanup(lift)
	exec(fmt.Sprintf(`CREATE OR REPLACE FUNCTION pulsix_test_rename_on_approval() RETURNS trigger
	      LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN UPDATE hosts SET name = '%s' WHERE id = '%s'; RETURN NEW; END $$`, name, host))
	exec(`CREATE TRIGGER pulsix_test_rename_on_approval AFTER UPDATE ON run_requests FOR EACH ROW
	      WHEN (NEW.status = 'approved' AND OLD.status <> 'approved') EXECUTE FUNCTION pulsix_test_rename_on_approval()`)
	return lift
}

// hostJoinedInTheWindow approves a request limited to web-1* over both
// hosts while host B (web-2, zone B, managed throughout) is renamed to
// match the limit inside the approval, after the picture was read and
// before the enqueue narrowed the run to the matching hosts; B is renamed
// back before anything is claimed. The approved picture holds host A only,
// and the live one is identical again, but zone B's job froze host B. It
// returns zone B's job.
func hostJoinedInTheWindow(t *testing.T, f runRequestFixture) jobs.Job {
	t.Helper()
	f.targetBothHosts(t)
	conn := superConn(t)
	created := f.tryRequest(t, map[string]any{"limit": "web-1*"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	var out runRequestDetail
	if err := json.Unmarshal(created.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	id := out.Request.ID
	fingerprint := f.detail(t, id).Fingerprint

	lift := renameOnApproval(t, conn, f.hostB, "web-1b")
	resp := f.approve(t, f.adminC, id, fingerprint)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	lift()
	renameHost(t, conn, f.hostB, "web-2")
	if got := f.detail(t, id).Fingerprint; got != fingerprint {
		t.Fatal("the picture differs from the approved one after the host left again")
	}
	var zoneB []jobs.Job
	for _, j := range f.groupJobs(t, id) {
		if j.ZoneID == f.zoneB {
			zoneB = append(zoneB, j)
		}
	}
	if len(zoneB) != 1 || len(zoneB[0].HostIDs) != 1 || zoneB[0].HostIDs[0] != f.hostB {
		t.Fatalf("zone B's jobs = %+v, want one that froze host B", zoneB)
	}
	return zoneB[0]
}

// TestRunRequestClaimRefusesAHostTheApprovalDidNotInclude is backlog 2.141:
// a host joins the run's hosts after the approval read the run picture and
// before the enqueue resolved them, and leaves them again before the claim
// while it stays managed. The live picture then matches the approved one
// again, but the job froze the host at enqueue. Without the claim's check
// the host is rendered and played on although nobody approved it; with
// it, the claim fails visibly and the approved zone's job still runs.
func TestRunRequestClaimRefusesAHostTheApprovalDidNotInclude(t *testing.T) {
	t.Run("without the check the host ships", func(t *testing.T) {
		f := newRunRequestFixture(t, "rr-hosts-leak")
		hostJoinedInTheWindow(t, f)
		f.s.Queue.SetClaimGuard(nil)
		c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneB}, "v1")
		if err != nil || c == nil {
			t.Fatalf("unguarded claim of zone B's job = %v, %v; want it claimed", c, err)
		}
		if doc := c.Job.GetInventoryDoc(); !strings.Contains(doc, `"web-2"`) || c.Job.GetLimit() != "" {
			t.Fatalf("unguarded claim rendered %s with limit %q; want host B played on", doc, c.Job.GetLimit())
		}
	})
	t.Run("the claim refuses it", func(t *testing.T) {
		f := newRunRequestFixture(t, "rr-hosts-changed")
		job := hostJoinedInTheWindow(t, f)
		if c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneB}, "v1"); err != nil || c != nil {
			t.Fatalf("claim of the job with the unapproved host = %v, %v; want refused", c, err)
		}
		refused, err := f.s.Deps.Jobs.Get(f.scoped, f.orgID, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if refused.Status != jobs.StatusError || !strings.HasPrefix(refused.Error, "refused at claim: the run's hosts changed after it was approved") ||
			refused.WithdrawnAtClaim() {
			t.Fatalf("zone B's job = %s %q, want refused because its hosts changed", refused.Status, refused.Error)
		}
		if c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA}, "v1"); err != nil || c == nil {
			t.Fatalf("claim of the approved zone's job = %v, %v; want it claimed", c, err)
		}
	})
}
