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
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
)

// refuseJobInserts makes every job insert for zone fail until the returned
// function lifts it (and at the test's end at the latest): an enqueue
// interrupted between two zones, as a manager shutdown mid-loop would.
func refuseJobInserts(t *testing.T, zone uuid.UUID) (lift func()) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	exec := func(sql string) error {
		_, err := conn.Exec(context.Background(), sql)
		return err
	}
	lift = func() {
		_ = exec(`DROP TRIGGER IF EXISTS pulsix_test_refuse_zone_job ON jobs`)
		_ = exec(`DROP FUNCTION IF EXISTS pulsix_test_refuse_zone_job()`)
	}
	t.Cleanup(lift)
	if err := exec(`CREATE OR REPLACE FUNCTION pulsix_test_refuse_zone_job() RETURNS trigger
	      LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected job insert failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	if err := exec(fmt.Sprintf(`CREATE TRIGGER pulsix_test_refuse_zone_job BEFORE INSERT ON jobs FOR EACH ROW
	      WHEN (NEW.zone_id = '%s') EXECUTE FUNCTION pulsix_test_refuse_zone_job()`, zone)); err != nil {
		t.Fatal(err)
	}
	return lift
}

// groupJobs lists the jobs of the request's run group (its own id).
func (f runRequestFixture) groupJobs(t *testing.T, requestID string) []jobs.Job {
	t.Helper()
	group := uuid.MustParse(requestID)
	list, _, err := f.s.Deps.Jobs.List(f.scoped, f.orgID, jobs.Filter{RunGroupID: &group}, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// targetBothHosts gives the second host the first one's login and points
// the template at both, one per zone.
func (f runRequestFixture) targetBothHosts(t *testing.T) {
	t.Helper()
	cred := f.hostCredential(t)
	if _, err := f.s.Deps.SSH.SetHostSSH(f.scoped, f.orgID, f.hostB, sshconf.HostSSHInput{CredentialID: &cred, Port: 22, BastionMode: sshconf.ModeInherit}); err != nil {
		t.Fatal(err)
	}
	updateTemplate(t, f, func(in *templates.UpdateInput) { in.Scope = templates.Scope{HostIDs: []uuid.UUID{f.hostA, f.hostB}} })
}

// TestRunRequestReapprovalRefillsZonesRefusedAtClaim: the first approval
// enqueues zone A's job and fails before zone B's, so the request goes
// back to pending; a worker then claims zone A's job and the claim guard
// refuses it. Approving again must give both zones a job that can be
// claimed: the refused job ran nothing anywhere, so its zone is enqueued
// again instead of replayed.
func TestRunRequestReapprovalRefillsZonesRefusedAtClaim(t *testing.T) {
	f := newRunRequestFixture(t, "rr-refill")
	f.targetBothHosts(t)
	created := f.request(t, map[string]any{"reason": "x"})

	lift := refuseJobInserts(t, f.zoneB)
	resp := f.approve(t, f.adminC, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode == http.StatusOK {
		t.Fatalf("approve with zone B's insert failing succeeded: %s", body)
	}
	if st := f.detail(t, created.Request.ID).Request.Status; st != "pending" {
		t.Fatalf("status after the failed enqueue = %s, want pending", st)
	}
	first := f.groupJobs(t, created.Request.ID)
	if len(first) != 1 || first[0].ZoneID != f.zoneA {
		t.Fatalf("jobs after the interrupted enqueue = %+v, want zone A's only", first)
	}
	lift()

	if c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA, f.zoneB}, "v1"); err != nil || c != nil {
		t.Fatalf("claim of the pending request's job = %v, %v; want refused", c, err)
	}
	refused, err := f.s.Deps.Jobs.Get(f.scoped, f.orgID, first[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if refused.Status != jobs.StatusError || refused.Error != "refused at claim: the run's request is not approved" {
		t.Fatalf("refused job = %s %q", refused.Status, refused.Error)
	}

	resp = f.approve(t, f.adminC, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	claimed := map[uuid.UUID]int{}
	for range 3 {
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
	if len(claimed) != 2 || claimed[f.zoneA] != 1 || claimed[f.zoneB] != 1 {
		t.Fatalf("claims of the re-approved run by zone = %v, want one each for zone A and zone B", claimed)
	}
	if all := f.groupJobs(t, created.Request.ID); len(all) != 3 {
		t.Fatalf("jobs of the run = %d, want the refused one and one per zone", len(all))
	}
}

// TestRunRequestPictureTargetsFollowTheTemplateLimit: the picture shows the
// hosts the run reaches under the template's limit, not every host of its
// scope.
func TestRunRequestPictureTargetsFollowTheTemplateLimit(t *testing.T) {
	f := newRunRequestFixture(t, "rr-limited-picture")
	f.targetBothHosts(t)
	updateTemplate(t, f, func(in *templates.UpdateInput) { in.Limit = "web-2" })
	created := f.request(t, map[string]any{"reason": "x"})

	var p struct {
		Targets []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(f.detail(t, created.Request.ID).Picture, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Targets) != 1 || p.Targets[0].ID != f.hostB.String() || p.Targets[0].Name != "web-2" {
		t.Fatalf("picture targets = %+v, want web-2 only", p.Targets)
	}
	if strings.Contains(string(f.detail(t, created.Request.ID).Picture), f.hostA.String()) {
		t.Fatal("the picture names the host the limit leaves out")
	}
}

// TestRunRequestReapprovalOfARefusedRunFinishesWithTheRefill is backlog
// 2.126's final review, Important 1: the first approval plans a follow-up,
// enqueues zone A's job and fails before zone B's; the claim guard then
// refuses A's job as withdrawn. That job is no part of the run's outcome,
// so once the re-approval's refill of A and B succeeds the run group
// reports success once — its follow-up starts on both hosts — and its
// summary is the refill's outcome, not the withdrawn job's error.
func TestRunRequestReapprovalOfARefusedRunFinishesWithTheRefill(t *testing.T) {
	f := newRunRequestFixture(t, "rr-refill-finish")
	fu := f.withFollowUp(t, "listmonk")
	f.setFollowUpsStart(t, true)
	f.targetBothHosts(t)
	created := f.request(t, map[string]any{"reason": "x"})
	group := uuid.MustParse(created.Request.ID)

	lift := refuseJobInserts(t, f.zoneB)
	resp := f.approve(t, f.adminC, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode == http.StatusOK {
		t.Fatalf("approve with zone B's insert failing succeeded: %s", body)
	}
	lift()
	withdrawn := f.groupJobs(t, created.Request.ID)
	if len(withdrawn) != 1 {
		t.Fatalf("jobs after the interrupted enqueue = %d, want zone A's only", len(withdrawn))
	}
	if c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA, f.zoneB}, "v1"); err != nil || c != nil {
		t.Fatalf("claim of the pending request's job = %v, %v; want refused", c, err)
	}

	resp = f.approve(t, f.adminC, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if rows := f.runFollowUps(t, created.Request.ID); rows[fu.String()] != "pending" {
		t.Fatalf("follow-ups after the re-approval = %v, want listmonk pending", rows)
	}
	hostOf := map[uuid.UUID]uuid.UUID{f.zoneA: f.hostA, f.zoneB: f.hostB}
	var refill []uuid.UUID
	for range 2 {
		c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA, f.zoneB}, "v1")
		if err != nil || c == nil {
			t.Fatalf("claim of the re-approved run = %v, %v", c, err)
		}
		id := uuid.MustParse(c.Job.GetId())
		j, err := f.s.Deps.Jobs.Get(f.scoped, f.orgID, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.s.Queue.Finish(context.Background(), id, jobs.StatusSucceeded,
			map[string]any{"ok_hosts": []string{hostOf[j.ZoneID].String()}}, ""); err != nil {
			t.Fatal(err)
		}
		refill = append(refill, id)
	}

	sums, err := f.s.Deps.Jobs.GroupSummaries(f.scoped, f.orgID, []uuid.UUID{group})
	if err != nil {
		t.Fatal(err)
	}
	if sum := sums[group]; sum.Status != jobs.StatusSucceeded || sum.FirstJobID == withdrawn[0].ID {
		t.Fatalf("run summary = %+v, want succeeded and not anchored on the withdrawn job %s", sum, withdrawn[0].ID)
	}
	var reported int
	if err := f.s.DB.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM jobs WHERE run_group_id = $1 AND run_group_reported AND id = ANY($2)`, group, refill).Scan(&reported); err != nil {
		t.Fatal(err)
	}
	if reported != 1 {
		t.Fatalf("finish reports claimed on the refilled jobs = %d, want 1", reported)
	}

	if err := f.s.ChainStarter.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	fus := f.chainDetail(t, refill[0].String()).Job.Chain.FollowUps
	if len(fus) != 1 || fus[0].TemplateID != fu.String() || fus[0].State != "started" || len(fus[0].Hosts) != 2 {
		t.Fatalf("follow-ups after the refill succeeded = %+v, want listmonk started on both hosts", fus)
	}
	if p := f.chainDetail(t, fus[0].ChildFirstJobID).Job.Chain.Parent; p == nil || p.FirstJobID == withdrawn[0].ID.String() {
		t.Fatalf("the follow-up's parent = %+v, want a refilled job, not the withdrawn %s", p, withdrawn[0].ID)
	}
}
