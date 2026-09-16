// The ad-hoc command route's tests (backlog 2.14): the enqueue and its
// per-zone split, the validation 422s, the invisible-host 404s, the
// permission matrix and the audit trail — driven through the full API
// against the test database, on the same fixture the run view's tests
// seed (job_handlers_test.go).
package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/tenancy"
)

func (f runFixture) adhocURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/hosts/command"
}

func postAdhoc(t *testing.T, c *http.Client, url, module, argv string, hostIDs ...string) *http.Response {
	t.Helper()
	body := map[string]any{"host_ids": hostIDs, "module": module, "argv": argv}
	return postJSON(t, c, url, body)
}

func TestAdhocRouteSplitsZonesIntoOneGroup(t *testing.T) {
	f := newRunFixture(t, "adhoc-org")

	resp := postAdhoc(t, f.operatorC, f.adhocURL(), "shell", "uptime", f.hostA.String(), f.hostB.String())
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("adhoc: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, resp, &out)
	if len(out.Jobs) != 2 {
		t.Fatalf("created %d jobs, want one per zone", len(out.Jobs))
	}
	zones := map[string]bool{}
	for _, j := range out.Jobs {
		if j.Kind != "adhoc" || j.Status != "queued" || j.Trigger != "manual" {
			t.Fatalf("job = %+v", j)
		}
		if j.RunGroupID == "" || j.RunGroupID != out.Jobs[0].RunGroupID {
			t.Fatalf("job %s does not share the run group %s", j.ID, out.Jobs[0].RunGroupID)
		}
		zones[j.ZoneID] = true
	}
	if len(zones) != 2 {
		t.Fatalf("jobs cover zones %v, want both", zones)
	}

	// The audit event names the run group, and its summary carries the
	// module and the operator's command — the operator typed it and it is
	// not secret material, so it is traceability, not a leak.
	group := out.Jobs[0].RunGroupID
	activity := readActivity(t, f.s, f.adminC, f.orgSlug)
	for _, ev := range activity.Items {
		if ev.Action == "POST runAdhocCommand" && ev.ObjectType == "host" && ev.ObjectID == group {
			if !strings.Contains(ev.Summary, "shell") || !strings.Contains(ev.Summary, "uptime") {
				t.Fatalf("audit summary = %q, want module and argv", ev.Summary)
			}
			return
		}
	}
	t.Fatalf("activity = %+v, want a runAdhocCommand event for %s", activity.Items, group)
}

// TestAdhocRouteClaimCarriesTheRunnerPayload drives the sealed payload's
// round trip through the real queue: the claim a worker would receive
// carries kind adhoc and the plain {"module","argv"} payload the runner's
// ad-hoc parser (internal/worker/runner/playbook.go) expects.
func TestAdhocRouteClaimCarriesTheRunnerPayload(t *testing.T) {
	f := newRunFixture(t, "adhoc-claim-org")
	resp := postAdhoc(t, f.operatorC, f.adhocURL(), "shell", "echo hello", f.hostA.String())
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("adhoc: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, resp, &out)

	zone := mustUUID(t, out.Jobs[0].ZoneID)
	claim, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{zone}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim: %v, %v", claim, err)
	}
	if id := claim.Job.GetId(); id != out.Jobs[0].ID {
		t.Fatalf("claimed %s, want %s", id, out.Jobs[0].ID)
	}
	if kind := claim.Job.GetKind(); kind != "adhoc" {
		t.Fatalf("claim kind = %q, want adhoc", kind)
	}
	if payload := claim.Job.GetKindPayload(); payload != `{"module":"shell","argv":"echo hello"}` {
		t.Fatalf("claim kind payload = %q, want the runner's module/argv shape", payload)
	}
}

func TestAdhocRouteValidatesModuleAndArgv(t *testing.T) {
	f := newRunFixture(t, "adhoc-validation-org")
	url := f.adhocURL()

	cases := []struct {
		name   string
		module string
		argv   string
		hosts  []string
	}{
		{"module raw", "raw", "uptime", []string{f.hostA.String()}},
		{"module empty", "", "uptime", []string{f.hostA.String()}},
		{"argv empty", "shell", "", []string{f.hostA.String()}},
		{"argv whitespace", "shell", "   ", []string{f.hostA.String()}},
		{"argv oversized", "shell", strings.Repeat("x", 8<<10+1), []string{f.hostA.String()}},
		{"no hosts", "shell", "uptime", []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := postAdhoc(t, f.operatorC, url, c.module, c.argv, c.hosts...)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422, body %s", resp.StatusCode, mustReadBody(t, resp))
			}
			// The offending value never comes back in the error body
			// (redactErrorValues): location and message say what is wrong.
			if strings.Contains(mustReadBody(t, resp), strings.Repeat("x", 8<<10+1)) {
				t.Fatal("error body echoes the oversized argv")
			}
		})
	}
}

func TestAdhocRouteRejectsMalformedHostID(t *testing.T) {
	f := newRunFixture(t, "adhoc-malformed-org")
	resp := postAdhoc(t, f.operatorC, f.adhocURL(), "shell", "uptime", "not-a-uuid")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed host id: status %d, want 400, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestAdhocRouteRejectsUnknownUnmanagedAndForeignHosts(t *testing.T) {
	f := newRunFixture(t, "adhoc-hosts-org")

	// An unmanaged host is as invisible as an unknown one (404, not a
	// validation error): it cannot run anything.
	unmanaged, err := f.s.Deps.Hosts.CreateHost(db.WithOrgScope(context.Background(), f.orgID), f.orgID,
		hosts.HostInput{Name: "bare", Address: "10.9.9.9", ZoneID: f.zoneA, Managed: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"unknown host", uuid.Must(uuid.NewV7()).String()},
		{"unmanaged host", unmanaged.ID.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := postAdhoc(t, f.operatorC, f.adhocURL(), "shell", "uptime", tc.id)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("%s: status %d, want 404, body %s", tc.name, resp.StatusCode, mustReadBody(t, resp))
			}
		})
	}
}

// TestAdhocRouteForeignHostIsInvisible seeds a second organization with
// its own host and proves the ad-hoc route answers 404 for its id — the
// same org-scoped invisibility every other resource shows.
func TestAdhocRouteForeignHostIsInvisible(t *testing.T) {
	f := newRunFixture(t, "adhoc-foreign-org")
	foreigner := f.s.SeedUser("adhoc-foreign-owner@example.test", "correct horse battery")
	foreignOrg := f.s.SeedOrg("adhoc-foreign", foreigner)
	scoped := db.WithOrgScope(context.Background(), foreignOrg)
	zones, err := f.s.Deps.Hosts.ListZones(scoped, foreignOrg)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	foreignHost, err := f.s.Deps.Hosts.CreateHost(scoped, foreignOrg, hosts.HostInput{Name: "elsewhere", Address: "10.8.8.8", ZoneID: zones[0].ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}

	resp := postAdhoc(t, f.operatorC, f.adhocURL(), "shell", "uptime", foreignHost.ID.String())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign host: status %d, want 404, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestAdhocRouteAuthMatrix(t *testing.T) {
	f := newRunFixture(t, "adhoc-auth-org")
	url := f.adhocURL()

	// Viewer: jobs.run is beyond their role → 403.
	if resp := postAdhoc(t, f.viewerC, url, "shell", "uptime", f.hostA.String()); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer: status %d, want 403", resp.StatusCode)
	}
	// No principal at all → 401.
	anon := f.s.Client()
	if resp := postAdhoc(t, anon, url, "shell", "uptime", f.hostA.String()); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: status %d, want 401", resp.StatusCode)
	}
	// A half-authenticated session is 403, the same bar every org-scoped
	// route clears.
	pending := f.s.SeedUser("adhoc-auth-pending@example.test", "correct horse battery")
	f.s.AddMember(f.orgID, pending, tenancy.RoleOperator)
	if resp := postAdhoc(t, f.s.LoginAsPending(pending), url, "shell", "uptime", f.hostA.String()); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pending session: status %d, want 403", resp.StatusCode)
	}
	// Operator: 202.
	if resp := postAdhoc(t, f.operatorC, url, "shell", "uptime", f.hostA.String()); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("operator: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}
