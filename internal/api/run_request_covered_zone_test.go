package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
)

// TestRunRequestReapprovalRefusesAHostThatJoinedACoveredZone is backlog
// 2.142 with the owner's decision of 2026-11-27: the first approval
// enqueues zone A's job and fails before zone B's; then a host joins
// zone A. Approving again would replay zone A's job, which does not hold
// the new host, so the host would never run: the re-approval is refused
// with a 409 that names the zone and the host, nothing is enqueued (not
// even zone B's job) and the request stays pending.
func TestRunRequestReapprovalRefusesAHostThatJoinedACoveredZone(t *testing.T) {
	f := newRunRequestFixture(t, "rr-covered-zone")
	f.targetBothHosts(t)
	created := f.request(t, map[string]any{"reason": "x"})
	id := created.Request.ID

	lift := refuseJobInserts(t, f.zoneB)
	resp := f.approve(t, f.adminC, id, f.detail(t, id).Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode == http.StatusOK {
		t.Fatalf("approve with zone B's insert failing succeeded: %s", body)
	}
	lift()
	if first := f.groupJobs(t, id); len(first) != 1 || first[0].ZoneID != f.zoneA {
		t.Fatalf("jobs after the interrupted enqueue = %+v, want zone A's only", first)
	}

	joined, err := f.s.Deps.Hosts.CreateHost(f.scoped, f.orgID, hosts.HostInput{Name: "web-1b", Address: "10.0.0.2", ZoneID: f.zoneA, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	cred := f.hostCredential(t)
	if _, err := f.s.Deps.SSH.SetHostSSH(f.scoped, f.orgID, joined.ID, sshconf.HostSSHInput{CredentialID: &cred, Port: 22, BastionMode: sshconf.ModeInherit}); err != nil {
		t.Fatal(err)
	}
	updateTemplate(t, f, func(in *templates.UpdateInput) {
		in.Scope = templates.Scope{HostIDs: []uuid.UUID{f.hostA, f.hostB, joined.ID}}
	})

	fingerprint := f.detail(t, id).Fingerprint
	conn := superConn(t)
	logged := logStatusChanges(t, conn, id)
	resp = f.approve(t, f.adminC, id, fingerprint)
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("re-approve = %d %s, want 409", resp.StatusCode, body)
	}
	var problem struct {
		Detail string `json:"detail"`
		Code   string `json:"code"`
		Zones  []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Hosts []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"hosts"`
		} `json:"zones"`
	}
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "hosts_joined_covered_zone" || len(problem.Zones) != 1 || problem.Zones[0].ID != f.zoneA.String() ||
		len(problem.Zones[0].Hosts) != 1 || problem.Zones[0].Hosts[0].ID != joined.ID.String() || problem.Zones[0].Hosts[0].Name != "web-1b" {
		t.Fatalf("problem = %s", body)
	}
	if !strings.Contains(problem.Detail, `web-1b is not covered by the earlier attempt's run in zone "`+problem.Zones[0].Name+`"`) {
		t.Fatalf("detail = %q, want the zone and the host named", problem.Detail)
	}
	if all := f.groupJobs(t, id); len(all) != 1 {
		t.Fatalf("jobs after the refused re-approval = %d, want zone A's first job only", len(all))
	}
	if st := f.detail(t, id).Request.Status; st != "pending" {
		t.Fatalf("status after the refused re-approval = %s, want pending", st)
	}
	// Review I1: the refusal comes before the decision, so the request
	// never stood approved, not even between a decision and its revert.
	if changes := logged(); len(changes) != 0 {
		t.Fatalf("status writes during the refused re-approval = %v, want none", changes)
	}
	var fp, bound *string
	var decided *time.Time
	if err := conn.QueryRow(context.Background(), `SELECT approved_fingerprint, approved_follow_ups::text, decided_at FROM run_requests WHERE id = $1`, id).
		Scan(&fp, &bound, &decided); err != nil {
		t.Fatal(err)
	}
	if fp != nil || bound != nil || decided != nil {
		t.Fatalf("request after the refusal: fingerprint %v, follow-ups %v, decided %v; want none", fp, bound, decided)
	}
}

// logStatusChanges records every status a write gives the run request id
// from now on; the returned function reads them.
func logStatusChanges(t *testing.T, conn *pgx.Conn, id string) func() []string {
	t.Helper()
	exec := func(sql string) {
		if _, err := conn.Exec(context.Background(), sql); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP TRIGGER IF EXISTS pulsix_test_log_status ON run_requests`)
		_, _ = conn.Exec(context.Background(), `DROP FUNCTION IF EXISTS pulsix_test_log_status()`)
		_, _ = conn.Exec(context.Background(), `DROP TABLE IF EXISTS pulsix_test_status_log`)
	})
	exec(`CREATE TABLE IF NOT EXISTS pulsix_test_status_log (status text NOT NULL)`)
	exec(fmt.Sprintf(`CREATE OR REPLACE FUNCTION pulsix_test_log_status() RETURNS trigger
	      LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN
	        IF NEW.id = '%s' THEN INSERT INTO pulsix_test_status_log VALUES (NEW.status); END IF; RETURN NEW; END $$`, id))
	exec(`CREATE TRIGGER pulsix_test_log_status AFTER UPDATE ON run_requests FOR EACH ROW EXECUTE FUNCTION pulsix_test_log_status()`)
	return func() []string {
		rows, err := conn.Query(context.Background(), `SELECT status FROM pulsix_test_status_log`)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
}
