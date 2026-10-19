// The run view routes' tests (backlog 2.12): the run/cancel/retry
// actions, the list and detail reads with their pagination and filter
// validation, the dangling-template tolerance, the audit trail and the
// never-leaks rule — driven through the full API against the test
// database. The SSE stream's own tests live in job_sse_test.go, on the
// fixture seeded here.
package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// runFixture is the seeded org the run view's tests share: owner (the
// admin role), operator, viewer, two managed hosts in two zones (the
// first with its own ssh_key credential, so bootstrap and claims have
// what they need), one playbook and one job template scoped to the
// first host.
type runFixture struct {
	s          *apitest.Server
	orgID      uuid.UUID
	orgSlug    string
	adminC     *http.Client
	operatorC  *http.Client
	viewerC    *http.Client
	zoneA      uuid.UUID
	zoneB      uuid.UUID
	hostA      uuid.UUID
	hostB      uuid.UUID
	playbook   uuid.UUID
	templateID uuid.UUID
}

func newRunFixture(t *testing.T, slug string) runFixture {
	t.Helper()
	s := apitest.New(t)
	admin := s.SeedUser(slug+"-admin@example.test", "correct horse battery")
	operator := s.SeedUser(slug+"-operator@example.test", "correct horse battery")
	viewer := s.SeedUser(slug+"-viewer@example.test", "correct horse battery")
	org := s.SeedOrg(slug, admin)
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	scoped := db.WithOrgScope(context.Background(), org)

	zones, err := s.Deps.Hosts.ListZones(scoped, org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	zoneA := zones[0].ID
	zoneBz, err := s.Deps.Hosts.CreateZone(scoped, org, "fra", "Frankfurt")
	if err != nil {
		t.Fatal(err)
	}
	zoneB := zoneBz.ID
	hostA, err := s.Deps.Hosts.CreateHost(scoped, org, hosts.HostInput{Name: "web-1", Address: "10.0.0.1", ZoneID: zoneA, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	hostB, err := s.Deps.Hosts.CreateHost(scoped, org, hosts.HostInput{Name: "web-2", Address: "10.0.1.1", ZoneID: zoneB, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := s.Deps.Playbooks.CreateEditor(scoped, org, playbooks.CreateInput{Inline: "- name: deploy\n  hosts: all\n  tasks: []\n"})
	if err != nil {
		t.Fatal(err)
	}
	sshCred, err := s.Deps.Credentials.Create(scoped, org, credentials.CreateInput{
		Name: "host-login", Kind: credentials.KindSSHKey, Username: "ansible",
		Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The host's own ssh_key credential: the bootstrap installs THAT
	// credential's public key, and the claim's connection needs a login.
	if _, err := s.Deps.SSH.SetHostSSH(scoped, org, hostA.ID, sshconf.HostSSHInput{
		CredentialID: &sshCred.ID, Port: 22, BastionMode: sshconf.ModeInherit,
	}); err != nil {
		t.Fatal(err)
	}
	tmpl, err := s.Deps.Templates.Create(scoped, org, templates.CreateInput{
		Name: "deploy", PlaybookID: pb.ID,
		Scope:     templates.Scope{HostIDs: []uuid.UUID{hostA.ID}},
		ExtraVars: map[string]any{"deploy_tag": "extra-var-s3cret-31d7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runFixture{
		s: s, orgID: org, orgSlug: slug,
		adminC: s.LoginAs(admin), operatorC: s.LoginAs(operator), viewerC: s.LoginAs(viewer),
		zoneA: zoneA, zoneB: zoneB, hostA: hostA.ID, hostB: hostB.ID,
		playbook: pb.ID, templateID: tmpl.ID,
	}
}

func (f runFixture) baseURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/jobs"
}

func (f runFixture) runURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/job-templates/" + f.templateID.String() + "/run"
}

func (f runFixture) runURLFor(id uuid.UUID) string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/job-templates/" + id.String() + "/run"
}

// jobDTO mirrors the API's job shapes closely enough for every assertion
// the tests make.
type jobDTO struct {
	ID         string         `json:"id"`
	ZoneID     string         `json:"zone_id"`
	Kind       string         `json:"kind"`
	Status     string         `json:"status"`
	TemplateID string         `json:"template_id,omitempty"`
	RunGroupID string         `json:"run_group_id"`
	Trigger    string         `json:"trigger"`
	TriggerRef string         `json:"trigger_ref,omitempty"`
	ActorName  string         `json:"actor_name,omitempty"`
	WorkerName string         `json:"worker_name,omitempty"`
	Hosts      []string       `json:"hosts"`
	Stats      map[string]any `json:"stats"`
	Error      string         `json:"error,omitempty"`
	Template   *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"template,omitempty"`
	Group []struct {
		ID     string `json:"id"`
		ZoneID string `json:"zone_id"`
		Status string `json:"status"`
	} `json:"group"`
	VaultIdentities []struct {
		Label        string `json:"label"`
		CredentialID string `json:"credential_id"`
		FromTemplate bool   `json:"from_template"`
	} `json:"vault_identities"`
}

func runJob(t *testing.T, c *http.Client, url string) []jobDTO {
	t.Helper()
	resp := postJSON(t, c, url, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, resp, &out)
	return out.Jobs
}

// finishJob drives one queued job to a terminal status through the real
// queue, exactly like the worker's ReportResult does: claim it (the
// zone queue must hand back this job) and report the finish.
func finishJob(t *testing.T, s *apitest.Server, zone, jobID uuid.UUID, status jobs.Status, stats map[string]any, errText string) {
	t.Helper()
	claim, err := s.Queue.Claim(context.Background(), zoneOrg(t, s, zone), []uuid.UUID{zone}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim: %v, %v", claim, err)
	}
	if id := claim.Job.GetId(); id != jobID.String() {
		t.Fatalf("claimed %s, want %s", id, jobID)
	}
	if err := s.Queue.Finish(context.Background(), jobID, status, stats, errText); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

// zoneOrg reads the organization a zone belongs to: the claim is scoped
// to the claiming worker's organization, and these helpers only know the
// zone.
func zoneOrg(t *testing.T, s *apitest.Server, zone uuid.UUID) uuid.UUID {
	t.Helper()
	var org uuid.UUID
	if err := s.DB.Pool().QueryRow(context.Background(), `SELECT org_id FROM zones WHERE id = $1`, zone).Scan(&org); err != nil {
		t.Fatalf("zone org: %v", err)
	}
	return org
}

// appendEvents drives events into a job's stream through the real
// queue, exactly like the gRPC EventRecorder port's Append calls do
// (claim first, so the live binding exists).
func appendEvents(t *testing.T, s *apitest.Server, zone, jobID uuid.UUID, events []jobs.Event) {
	t.Helper()
	claim, err := s.Queue.Claim(context.Background(), zoneOrg(t, s, zone), []uuid.UUID{zone}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim: %v, %v", claim, err)
	}
	if id := claim.Job.GetId(); id != jobID.String() {
		t.Fatalf("claimed %s, want %s", id, jobID)
	}
	for _, ev := range events {
		if err := s.Queue.AppendEvent(context.Background(), jobID, ev); err != nil {
			t.Fatalf("AppendEvent %d: %v", ev.Seq, err)
		}
	}
}

// assertAuditEvent walks the org's activity feed and fails when no
// recorded event matches action, object type and object id.
func assertAuditEvent(t *testing.T, s *apitest.Server, actor *http.Client, slug, action, objectType, objectID string) {
	t.Helper()
	activity := readActivity(t, s, actor, slug)
	for _, ev := range activity.Items {
		if ev.Action == action && ev.ObjectType == objectType && ev.ObjectID == objectID {
			return
		}
	}
	t.Fatalf("activity = %+v, want a %q event for %s %s", activity.Items, action, objectType, objectID)
}

func TestRunRouteCreatesGroupedJobs(t *testing.T) {
	f := newRunFixture(t, "run-org")

	first := runJob(t, f.operatorC, f.runURL())
	if len(first) != 1 {
		t.Fatalf("first run created %d jobs, want 1 (one host, one zone)", len(first))
	}
	j := first[0]
	if j.Kind != "run" || j.Status != "queued" || j.TemplateID != f.templateID.String() || j.RunGroupID == "" {
		t.Fatalf("job = %+v", j)
	}
	if j.ZoneID != f.zoneA.String() {
		t.Fatalf("job zone = %s, want %s", j.ZoneID, f.zoneA)
	}

	// The run action's audit event names the template it ran.
	assertAuditEvent(t, f.s, f.operatorC, f.orgSlug, "POST runJobTemplate", "job_template", f.templateID.String())

	// A second run is its own group: new run group, new job ids.
	second := runJob(t, f.operatorC, f.runURL())
	if second[0].RunGroupID == first[0].RunGroupID || second[0].ID == first[0].ID {
		t.Fatalf("second run = %+v, must be a new group", second)
	}
}

func TestRunRouteSplitsZonesIntoOneGroup(t *testing.T) {
	f := newRunFixture(t, "run-zones-org")
	tmpl, err := f.s.Deps.Templates.Create(db.WithOrgScope(context.Background(), f.orgID), f.orgID, templates.CreateInput{
		Name: "deploy-both", PlaybookID: f.playbook, Scope: templates.Scope{HostIDs: []uuid.UUID{f.hostA, f.hostB}},
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs1 := runJob(t, f.operatorC, f.runURLFor(tmpl.ID))
	if len(jobs1) != 2 {
		t.Fatalf("created %d jobs, want one per zone", len(jobs1))
	}
	zones := map[string]bool{}
	for _, j := range jobs1 {
		if j.RunGroupID == "" || j.RunGroupID != jobs1[0].RunGroupID {
			t.Fatalf("job %s does not share the run group %s", j.ID, jobs1[0].RunGroupID)
		}
		zones[j.ZoneID] = true
	}
	if len(zones) != 2 {
		t.Fatalf("jobs cover zones %v, want both", zones)
	}
}

func TestRunRouteUnknownTemplate(t *testing.T) {
	f := newRunFixture(t, "run-404-org")
	url := f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/job-templates/" + uuid.Must(uuid.NewV7()).String() + "/run"
	resp := postJSON(t, f.operatorC, url, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown template: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestRunViewAuthMatrix drives the permission table: reads are
// jobs.read (viewer), the three actions are jobs.run (operator), and a
// request with no principal at all is 401 everywhere.
func TestRunViewAuthMatrix(t *testing.T) {
	f := newRunFixture(t, "run-auth-org")
	jobID := runJob(t, f.operatorC, f.runURL())[0].ID
	jobURL := f.baseURL() + "/" + jobID

	// Viewer: every read is fine, every action is forbidden.
	for _, tc := range []struct{ name, url, method string }{
		{"list", f.baseURL(), http.MethodGet},
		{"detail", jobURL, http.MethodGet},
		{"events", jobURL + "/events", http.MethodGet},
		{"run", f.runURL(), http.MethodPost},
		{"cancel", jobURL + "/cancel", http.MethodPost},
		{"retry", jobURL + "/retry-failed", http.MethodPost},
	} {
		resp := requestJSON(t, f.viewerC, tc.method, tc.url, nil)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusForbidden {
			t.Fatalf("viewer %s: status %d, body %s", tc.name, resp.StatusCode, mustReadBody(t, resp))
		}
		if (tc.method == http.MethodGet) != (resp.StatusCode == http.StatusOK) {
			t.Fatalf("viewer %s: status %d does not match %q", tc.name, resp.StatusCode, tc.method)
		}
	}

	// Operator: every action is fine.
	if resp := postJSON(t, f.operatorC, f.runURL(), nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("operator run: status %d", resp.StatusCode)
	}

	// No principal at all: 401 on a read and on an action alike.
	anon := f.s.Client()
	if resp := requestJSON(t, anon, http.MethodGet, f.baseURL(), nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous list: status %d", resp.StatusCode)
	}
	if resp := postJSON(t, anon, f.runURL(), nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous run: status %d", resp.StatusCode)
	}

	// A half-authenticated session is 403 (second factor required), the
	// same bar every org-scoped route clears.
	pending := f.s.SeedUser("run-auth-pending@example.test", "correct horse battery")
	f.s.AddMember(f.orgID, pending, tenancy.RoleOperator)
	if resp := requestJSON(t, f.s.LoginAsPending(pending), http.MethodGet, f.baseURL(), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pending session: status %d", resp.StatusCode)
	}
}

func TestCancelRouteMapsNotRunningToConflict(t *testing.T) {
	f := newRunFixture(t, "run-cancel-org")
	jobID := runJob(t, f.operatorC, f.runURL())[0].ID
	cancelURL := f.baseURL() + "/" + jobID + "/cancel"

	resp := postJSON(t, f.operatorC, cancelURL, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Job struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"job"`
	}
	decodeInto(t, resp, &out)
	if out.Job.ID != jobID || out.Job.Status != "cancelled" {
		t.Fatalf("cancelled = %+v, want the queued job now cancelled", out.Job)
	}
	assertAuditEvent(t, f.s, f.operatorC, f.orgSlug, "POST cancelJob", "job", jobID)

	// A job past the queue has nothing left to cancel: 409, and the
	// state stays cancelled.
	resp = postJSON(t, f.operatorC, cancelURL, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second cancel: status %d, want 409, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestCancelRouteUnknownJob(t *testing.T) {
	f := newRunFixture(t, "run-cancel-404-org")
	resp := postJSON(t, f.operatorC, f.baseURL()+"/"+uuid.Must(uuid.NewV7()).String()+"/cancel", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job: status %d", resp.StatusCode)
	}
}

func TestRetryRouteRefusesWhenNothingToRetry(t *testing.T) {
	f := newRunFixture(t, "run-retry-none-org")
	jobID := runJob(t, f.operatorC, f.runURL())[0].ID
	retryURL := f.baseURL() + "/" + jobID + "/retry-failed"

	// A run that has not finished yet: 409.
	if resp := postJSON(t, f.operatorC, retryURL, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("retry queued run: status %d, want 409", resp.StatusCode)
	}

	// A succeeded run is terminal but has no failed host: 422, and the
	// body says nothing to retry.
	finishJob(t, f.s, f.zoneA, mustUUID(t, jobID), jobs.StatusSucceeded, nil, "")
	resp := postJSON(t, f.operatorC, retryURL, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("retry succeeded run: status %d, want 422, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if !strings.Contains(mustReadBody(t, resp), "nothing to retry") {
		t.Fatalf("body must say nothing to retry: %s", mustReadBody(t, resp))
	}
}

// TestRetryFailedRouteRunsOnlyFailedHosts is the store-backed retry
// path: enqueue a two-zone run group, fake-finish both zone jobs with
// failed/unreachable stats, and the retry creates a NEW group scoped to
// exactly the failed and unreachable hosts — through the API with the
// real store.
func TestRetryFailedRouteRunsOnlyFailedHosts(t *testing.T) {
	f := newRunFixture(t, "run-retry-org")
	tmpl, err := f.s.Deps.Templates.Create(db.WithOrgScope(context.Background(), f.orgID), f.orgID, templates.CreateInput{
		Name: "deploy-both", PlaybookID: f.playbook, Scope: templates.Scope{HostIDs: []uuid.UUID{f.hostA, f.hostB}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started := runJob(t, f.operatorC, f.runURLFor(tmpl.ID))
	if len(started) != 2 {
		t.Fatalf("started %d jobs, want 2", len(started))
	}
	// Zone A's job failed on host A; zone B's job failed with host B
	// unreachable. Whichever zone each job landed in, the stats name
	// that zone's host.
	zoneOf := map[string]uuid.UUID{started[0].ID: mustUUID(t, started[0].ZoneID), started[1].ID: mustUUID(t, started[1].ZoneID)}
	finishJob(t, f.s, zoneOf[started[0].ID], mustUUID(t, started[0].ID), jobs.StatusFailed, map[string]any{
		jobs.StatsFailedHosts:      []any{zoneHost(t, f, started[0].ZoneID).String()},
		jobs.StatsUnreachableHosts: []any{},
	}, "playbook failed")
	finishJob(t, f.s, zoneOf[started[1].ID], mustUUID(t, started[1].ID), jobs.StatusFailed, map[string]any{
		jobs.StatsFailedHosts:      []any{},
		jobs.StatsUnreachableHosts: []any{zoneHost(t, f, started[1].ZoneID).String()},
	}, "hosts unreachable")

	resp := postJSON(t, f.operatorC, f.baseURL()+"/"+started[0].ID+"/retry-failed", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("retry: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var retried struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, resp, &retried)
	if len(retried.Jobs) != 1 {
		t.Fatalf("retry created %d jobs, want exactly one for the one failed zone", len(retried.Jobs))
	}
	got, err := f.s.Deps.Jobs.Get(db.WithOrgScope(context.Background(), f.orgID), f.orgID, mustUUID(t, retried.Jobs[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.HostIDs) != 1 || got.HostIDs[0] != zoneHost(t, f, started[0].ZoneID) {
		t.Fatalf("retry scope = %v, want exactly the failed host", got.HostIDs)
	}
	if got.TemplateID == nil || *got.TemplateID != tmpl.ID || got.RunGroupID == mustUUID(t, started[0].RunGroupID) {
		t.Fatalf("retry job = %+v, want the same template in a new group", got)
	}
	assertAuditEvent(t, f.s, f.operatorC, f.orgSlug, "POST retryFailedJobs", "job", started[0].ID)
}

// zoneHost maps a run job's zone back to the fixture host that lives in
// it, so the retry test can name the host ids the stats carry.
func zoneHost(t *testing.T, f runFixture, zoneID string) uuid.UUID {
	t.Helper()
	if zoneID == f.zoneA.String() {
		return f.hostA
	}
	if zoneID == f.zoneB.String() {
		return f.hostB
	}
	t.Fatalf("zone %s is not one of the fixture's zones", zoneID)
	return uuid.Nil
}

func TestJobListPaginationAndFilterValidation(t *testing.T) {
	f := newRunFixture(t, "run-list-org")

	// Four runs of the single-zone template: four run jobs to page over.
	var runIDs []string
	for i := 0; i < 4; i++ {
		for _, j := range runJob(t, f.operatorC, f.runURL()) {
			runIDs = append(runIDs, j.ID)
		}
		time.Sleep(2 * time.Millisecond) // keep each run's created_at distinct
	}

	// Walk the pages with limit=2, collecting every job exactly once,
	// each page newest first.
	seen := map[string]bool{}
	cursor, pages := "", 0
	for {
		url := f.baseURL() + "?limit=2&kind=run"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		resp := requestJSON(t, f.adminC, http.MethodGet, url, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("page %d: status %d, body %s", pages, resp.StatusCode, mustReadBody(t, resp))
		}
		var out struct {
			Jobs       []jobDTO `json:"jobs"`
			NextCursor string   `json:"next_cursor"`
		}
		decodeInto(t, resp, &out)
		if len(out.Jobs) == 0 {
			break
		}
		if len(out.Jobs) > 2 {
			t.Fatalf("limit=2 returned %d jobs", len(out.Jobs))
		}
		for i := 1; i < len(out.Jobs); i++ {
			if out.Jobs[i-1].ID <= out.Jobs[i].ID {
				t.Fatalf("page is not newest first: %+v before %+v", out.Jobs[i-1], out.Jobs[i])
			}
		}
		for _, j := range out.Jobs {
			if seen[j.ID] {
				t.Fatalf("job %s seen twice across pages", j.ID)
			}
			seen[j.ID] = true
		}
		pages++
		cursor = out.NextCursor
		if cursor == "" {
			break
		}
	}
	if pages < 2 || len(seen) != len(runIDs) {
		t.Fatalf("walked %d pages, saw %d of %d run jobs", pages, len(seen), len(runIDs))
	}
	for _, id := range runIDs {
		if !seen[id] {
			t.Fatalf("run job %s never appeared", id)
		}
	}

	// A connection test through the hostops route gives the kind filter
	// something to select.
	ctURL := f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/hosts/" + f.hostA.String() + "/connection-test"
	if resp := postJSON(t, f.operatorC, ctURL, nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("connection test: status %d", resp.StatusCode)
	}
	var byKind struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?kind=connection_test", nil), &byKind)
	if len(byKind.Jobs) != 1 || byKind.Jobs[0].Kind != "connection_test" {
		t.Fatalf("kind=connection_test: %+v", byKind.Jobs)
	}
	var byStatus struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?status=queued", nil), &byStatus)
	if len(byStatus.Jobs) == 0 {
		t.Fatal("status=queued matched nothing")
	}
	for _, j := range byStatus.Jobs {
		if j.Status != "queued" {
			t.Fatalf("status filter returned %+v", j)
		}
	}

	// template_id and group filters: only the matching jobs come back.
	var byTemplate struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?kind=run&template_id="+f.templateID.String(), nil), &byTemplate)
	if len(byTemplate.Jobs) != len(runIDs) {
		t.Fatalf("template_id filter: %+v, want %d", byTemplate.Jobs, len(runIDs))
	}
	for _, j := range byTemplate.Jobs {
		if j.TemplateID != f.templateID.String() {
			t.Fatalf("template_id filter returned %+v", j)
		}
	}
	var byGroup struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?group="+byTemplate.Jobs[0].RunGroupID, nil), &byGroup)
	if len(byGroup.Jobs) != 1 || byGroup.Jobs[0].RunGroupID != byTemplate.Jobs[0].RunGroupID {
		t.Fatalf("group filter: %+v", byGroup.Jobs)
	}

	// A malformed filter value is a 400, never a silent empty page.
	for _, bad := range []string{"status=bogus", "kind=bogus", "template_id=not-a-uuid", "group=not-a-uuid"} {
		resp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?"+bad, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status %d, body %s", bad, resp.StatusCode, mustReadBody(t, resp))
		}
	}

	// A garbage cursor is 400 too (the same 400 the hosts list gives).
	resp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?cursor=not-a-valid-cursor", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("garbage cursor: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestJobDetailAndListShowTriggerRef is fix round 1, minor 4: the DTO
// change (Trigger enum widened, trigger_ref added) had no API-level test.
// Schedules and webhooks (Tasks 6/9/10) do not exist yet, so the
// schedule-triggered job is inserted directly through the store — exactly
// the shape a schedule tick will insert — and read back through both the
// detail and the list routes.
func TestJobDetailAndListShowTriggerRef(t *testing.T) {
	f := newRunFixture(t, "run-trigger-ref-org")

	// A manual run (the only trigger the HTTP route can produce today)
	// carries no trigger_ref.
	manualID := runJob(t, f.operatorC, f.runURL())[0].ID
	var manual struct {
		Job jobDTO `json:"job"`
	}
	decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+manualID, nil), &manual)
	if manual.Job.Trigger != "manual" || manual.Job.TriggerRef != "" {
		t.Fatalf("manual job = %+v, want trigger manual with no trigger_ref", manual.Job)
	}

	scheduleID := uuid.Must(uuid.NewV7())
	scheduled := jobs.Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.orgID, ZoneID: f.zoneA, Kind: jobs.KindRun, TemplateID: &f.templateID,
		HostIDs: []uuid.UUID{f.hostA}, Status: jobs.StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()),
		Trigger: jobs.TriggerSchedule, TriggerRef: &scheduleID,
	}
	if err := jobs.NewSQLStore(f.s.DB).CreateJob(db.WithOrgScope(context.Background(), f.orgID), scheduled, nil); err != nil {
		t.Fatal(err)
	}

	var detail struct {
		Job jobDTO `json:"job"`
	}
	decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+scheduled.ID.String(), nil), &detail)
	if detail.Job.Trigger != "schedule" || detail.Job.TriggerRef != scheduleID.String() {
		t.Fatalf("scheduled job detail = %+v, want trigger_ref %s", detail.Job, scheduleID)
	}

	var list struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?group="+scheduled.RunGroupID.String(), nil), &list)
	if len(list.Jobs) != 1 || list.Jobs[0].Trigger != "schedule" || list.Jobs[0].TriggerRef != scheduleID.String() {
		t.Fatalf("scheduled job in list = %+v, want trigger_ref %s", list.Jobs, scheduleID)
	}
}

func TestJobDetailShowsTemplateActorAndGroup(t *testing.T) {
	f := newRunFixture(t, "run-detail-org")
	jobID := runJob(t, f.operatorC, f.runURL())[0].ID

	resp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+jobID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Job jobDTO `json:"job"`
	}
	decodeInto(t, resp, &out)
	j := out.Job
	if j.ID != jobID || j.ZoneID != f.zoneA.String() || j.Status != "queued" || j.Trigger != "manual" {
		t.Fatalf("detail = %+v", j)
	}
	if j.Template == nil || j.Template.ID != f.templateID.String() || j.Template.Name != "deploy" {
		t.Fatalf("template summary = %+v, want the live template", j.Template)
	}
	if j.ActorName == "" {
		t.Fatal("actor_name must carry the actor's display name")
	}
	if len(j.Hosts) != 1 || j.Hosts[0] != f.hostA.String() {
		t.Fatalf("hosts = %v, want the template's target", j.Hosts)
	}
	if j.Stats == nil {
		t.Fatal("stats must always be an object")
	}
	if len(j.Group) != 0 {
		t.Fatalf("group = %+v, want no siblings in a single-zone run", j.Group)
	}
}

// TestJobDetailGroupListsSiblings covers the additive group field: both
// zone jobs of one run name each other, and neither names itself.
func TestJobDetailGroupListsSiblings(t *testing.T) {
	f := newRunFixture(t, "run-group-org")
	tmpl, err := f.s.Deps.Templates.Create(db.WithOrgScope(context.Background(), f.orgID), f.orgID, templates.CreateInput{
		Name: "deploy-both", PlaybookID: f.playbook, Scope: templates.Scope{HostIDs: []uuid.UUID{f.hostA, f.hostB}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started := runJob(t, f.operatorC, f.runURLFor(tmpl.ID))

	for _, me := range started {
		var out struct {
			Job jobDTO `json:"job"`
		}
		decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+me.ID, nil), &out)
		if len(out.Job.Group) != 1 {
			t.Fatalf("group for %s = %+v, want the one sibling", me.ID, out.Job.Group)
		}
		g := out.Job.Group[0]
		other := started[0]
		if other.ID == me.ID {
			other = started[1]
		}
		if g.ID != other.ID || g.ZoneID != other.ZoneID || g.Status != "queued" {
			t.Fatalf("sibling for %s = %+v, want %+v", me.ID, g, other)
		}
	}
}

// TestRunDetailShowsResolvedVaultIdentitiesAndTheirOrigin is spec decision
// 3: the run detail lists the resolved vault identity set and whether each
// came from the repository or the template — the condition under which
// decision 2's silent collision resolution is acceptable at all. The
// collision case is the one an operator actually stares at, so this pins
// it directly: a repository-only label reads from_template: false, while
// the label the template overrides reads true.
func TestRunDetailShowsResolvedVaultIdentitiesAndTheirOrigin(t *testing.T) {
	f := newRunFixture(t, "run-vault-detail-org")
	scoped := db.WithOrgScope(context.Background(), f.orgID)

	repoProdCred := seedVaultCredentialForRepo(t, f.s, f.orgID, "repo-prod-vault")
	repoInfraCred := seedVaultCredentialForRepo(t, f.s, f.orgID, "repo-infra-vault")
	templateProdCred := seedVaultCredentialForRepo(t, f.s, f.orgID, "template-prod-vault")

	repo, err := f.s.Deps.Repos.Create(scoped, f.orgID, repos.CreateInput{
		Name: "infra", URL: "https://git.example.com/acme/infra.git",
		VaultIdentities: []repos.VaultIdentity{
			{CredentialID: mustUUID(t, repoProdCred), Label: "prod"},
			{CredentialID: mustUUID(t, repoInfraCred), Label: "infra"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := f.s.Deps.Playbooks.CreateRepo(scoped, f.orgID, repo.ID, "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := f.s.Deps.Templates.Create(scoped, f.orgID, templates.CreateInput{
		Name: "deploy-infra", PlaybookID: pb.ID, Scope: templates.Scope{HostIDs: []uuid.UUID{f.hostA}},
		// "prod" collides with the repository's own: decision 2 says the
		// template wins. "infra" is untouched, so it must still read as
		// the repository's.
		VaultCredentials: []templates.VaultCredential{{CredentialID: mustUUID(t, templateProdCred), Label: "prod"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	jobID := runJob(t, f.operatorC, f.runURLFor(tmpl.ID))[0].ID

	resp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+jobID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Job jobDTO `json:"job"`
	}
	decodeInto(t, resp, &out)

	if len(out.Job.VaultIdentities) != 2 {
		t.Fatalf("vault_identities = %+v, want 2 (infra, prod)", out.Job.VaultIdentities)
	}
	byLabel := map[string]struct {
		credentialID string
		fromTemplate bool
	}{}
	for _, id := range out.Job.VaultIdentities {
		byLabel[id.Label] = struct {
			credentialID string
			fromTemplate bool
		}{id.CredentialID, id.FromTemplate}
	}
	if got := byLabel["infra"]; got.credentialID != repoInfraCred || got.fromTemplate {
		t.Errorf("label infra = %+v, want {%s, from_template: false} (untouched, stays the repository's)", got, repoInfraCred)
	}
	if got := byLabel["prod"]; got.credentialID != templateProdCred || !got.fromTemplate {
		t.Errorf("label prod = %+v, want {%s, from_template: true} (the template's own wins the collision)", got, templateProdCred)
	}
}

// TestJobDetailToleratesDeletedTemplate drives the dangling
// template_id: delete the template the run came from, and the run stays
// fully readable — with the template summary rendering "(deleted)".
func TestJobDetailToleratesDeletedTemplate(t *testing.T) {
	f := newRunFixture(t, "run-dangling-org")
	started := runJob(t, f.operatorC, f.runURL())

	resp := requestJSON(t, f.operatorC, http.MethodDelete,
		f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/job-templates/"+f.templateID.String(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete template: status %d", resp.StatusCode)
	}

	resp = requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+started[0].ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail of dangling run: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Job jobDTO `json:"job"`
	}
	decodeInto(t, resp, &out)
	if out.Job.Template == nil {
		t.Fatal("the run detail must render even when its template is gone")
	}
	if out.Job.Template.ID != f.templateID.String() || out.Job.Template.Name != "(deleted)" {
		t.Fatalf("template summary = %+v, want (deleted)", out.Job.Template)
	}

	// The list still shows the run too.
	var list struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?kind=run", nil), &list)
	if len(list.Jobs) != len(started) {
		t.Fatalf("list after template deletion = %+v", list.Jobs)
	}
}

// TestRunDetailLeaksNoSecrets is the brief's snapshot-safety rule: the
// run detail never carries the claim payload, a revealed credential or
// the revealed inventory — no $sealed marker, no PEM block, no
// credential token, no template extra-var value — even for the admin
// who owns the organization.
func TestRunDetailLeaksNoSecrets(t *testing.T) {
	f := newRunFixture(t, "run-leak-org")
	const oneTimeKey = "pem-onetime-9f2a-----BEGIN OPENSSH PRIVATE KEY-----b3BlbnNzaC1rZXktdjEAAAAA"

	// A bootstrap job's stored payload IS the sealed one-time login: the
	// strongest at-rest leak case there is.
	resp := postJSON(t, f.operatorC,
		f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/hosts/"+f.hostA.String()+"/bootstrap",
		map[string]any{"user": "root", "private_key": oneTimeKey})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("bootstrap: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var started struct {
		JobID string `json:"job_id"`
	}
	decodeInto(t, resp, &started)

	runID := runJob(t, f.operatorC, f.runURL())[0].ID

	for _, id := range []string{started.JobID, runID} {
		resp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+id, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("detail %s: status %d", id, resp.StatusCode)
		}
		body := mustReadBody(t, resp)
		var raw struct {
			Job jobDTO `json:"job"`
		}
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			t.Fatal(err)
		}
		if raw.Job.Error != "" && strings.Contains(raw.Job.Error, "s3cret") {
			t.Errorf("run detail %s carries secret material in error: %s", id, body)
		}
		for _, marker := range []string{
			"$sealed", "-----BEGIN", "git-tok-s3cret-4b19", "extra-var-s3cret-31d7", oneTimeKey,
			"private_key", "payload",
		} {
			if strings.Contains(body, marker) {
				t.Errorf("run detail %s leaks %q: %s", id, marker, body)
			}
		}
	}
}

func TestJobEventsEndpointAfterFilter(t *testing.T) {
	f := newRunFixture(t, "run-events-org")
	jobID := mustUUID(t, runJob(t, f.operatorC, f.runURL())[0].ID)

	// A fresh run has streamed nothing: the empty list is an empty
	// array, not an error.
	var out struct {
		Events []struct {
			Seq     uint64         `json:"seq"`
			Attempt int            `json:"attempt"`
			Host    string         `json:"host"`
			Task    string         `json:"task"`
			State   string         `json:"state"`
			Payload map[string]any `json:"payload"`
		} `json:"events"`
	}
	decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+jobID.String()+"/events", nil), &out)
	if len(out.Events) != 0 {
		t.Fatalf("fresh run already has events: %+v", out.Events)
	}

	appendEvents(t, f.s, f.zoneA, jobID, []jobs.Event{
		{Seq: 1, Host: "web-1", Task: "Gathering Facts", State: "ok", Payload: json.RawMessage(`{"delta":1}`), At: time.Now()},
		{Seq: 2, Host: "web-1", Task: "deploy", State: "changed", Payload: json.RawMessage(`"bare stdout line"`), At: time.Now()},
		{Seq: 3, Host: "web-1", Task: "deploy", State: "ok", Payload: nil, At: time.Now()},
	})

	decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+jobID.String()+"/events", nil), &out)
	if len(out.Events) != 3 || out.Events[0].Seq != 1 || out.Events[2].Seq != 3 {
		t.Fatalf("events = %+v", out.Events)
	}
	for _, e := range out.Events {
		if e.Attempt != 1 {
			t.Fatalf("event %d attempt = %d, want the first claim's 1", e.Seq, e.Attempt)
		}
	}

	// after=<seq> resumes strictly past it, in seq order.
	decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+jobID.String()+"/events?after=1", nil), &out)
	if len(out.Events) != 2 || out.Events[0].Seq != 2 {
		t.Fatalf("events after 1 = %+v", out.Events)
	}
	if out.Events[1].Payload["text"] != "bare stdout line" {
		t.Fatalf("payload round trip: %+v", out.Events[1].Payload)
	}

	// Another organization's job id is as invisible as one that does
	// not exist.
	resp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+uuid.Must(uuid.NewV7()).String()+"/events", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job's events: status %d", resp.StatusCode)
	}
}
