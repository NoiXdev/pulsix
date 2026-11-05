// The run route narrowed to one host (backlog 7.76): POST
// .../job-templates/{id}/run with the optional body {host_id} runs the
// template on that host only, refuses a host outside the template's
// targets, and leaves a run without a body exactly as it was.
package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
)

// bothHostsTemplate is a template scoped to both fixture hosts, one per zone.
func bothHostsTemplate(t *testing.T, f runFixture) uuid.UUID {
	t.Helper()
	tmpl, err := f.s.Deps.Templates.Create(db.WithOrgScope(context.Background(), f.orgID), f.orgID, templates.CreateInput{
		Name: "deploy-both", PlaybookID: f.playbook, Scope: templates.Scope{HostIDs: []uuid.UUID{f.hostA, f.hostB}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tmpl.ID
}

func TestRunRouteNarrowsToOneHost(t *testing.T) {
	f := newRunFixture(t, "run-host-org")
	tmplID := bothHostsTemplate(t, f)

	created := runJobWith(t, f.operatorC, f.runURLFor(tmplID), map[string]string{"host_id": f.hostB.String()})
	if len(created) != 1 {
		t.Fatalf("created %d jobs, want 1", len(created))
	}
	// The start answer carries no host list; the run's detail does.
	var detail struct {
		Job jobDTO `json:"job"`
	}
	decodeInto(t, requestJSON(t, f.operatorC, http.MethodGet, f.baseURL()+"/"+created[0].ID, nil), &detail)
	j := detail.Job
	if j.ZoneID != f.zoneB.String() || len(j.Hosts) != 1 || j.Hosts[0] != f.hostB.String() {
		t.Fatalf("job zone %s hosts %v, want zone %s hosts [%s]", j.ZoneID, j.Hosts, f.zoneB, f.hostB)
	}

	// The audit summary names the host the run was narrowed to.
	found := false
	for _, ev := range readActivity(t, f.s, f.operatorC, f.orgSlug).Items {
		if ev.Action == "POST runJobTemplate" && ev.ObjectID == tmplID.String() {
			found = strings.Contains(ev.Summary, f.hostB.String())
			break
		}
	}
	if !found {
		t.Fatalf("no runJobTemplate event naming host %s", f.hostB)
	}

	// An empty object is a run without narrowing: one job per zone.
	if all := runJobWith(t, f.operatorC, f.runURLFor(tmplID), map[string]any{}); len(all) != 2 {
		t.Fatalf("run with an empty body created %d jobs, want 2", len(all))
	}
	// So is no body at all (what the SPA sent before host_id existed).
	resp, err := f.operatorC.Post(f.runURLFor(tmplID), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run without a body: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, resp, &out)
	if len(out.Jobs) != 2 {
		t.Fatalf("run without a body created %d jobs, want 2", len(out.Jobs))
	}
	// A misspelled field is refused, never read as "no narrowing": it must
	// not silently run the template on every host.
	resp = postJSON(t, f.operatorC, f.runURLFor(tmplID), map[string]string{"hostId": f.hostB.String()})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("misspelled host_id: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	_ = resp.Body.Close()
}

func TestRunRouteRefusesAHostOutsideTheScope(t *testing.T) {
	f := newRunFixture(t, "run-host-scope-org")
	// The fixture template targets host A only.
	resp := postJSON(t, f.operatorC, f.runURL(), map[string]string{"host_id": f.hostB.String()})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("host outside the scope: status %d, body %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, jobs.ErrHostNotInScope.Error()) {
		t.Fatalf("body %s does not carry %q", body, jobs.ErrHostNotInScope)
	}
}

func TestRunRouteRejectsAnUnusableHost(t *testing.T) {
	f := newRunFixture(t, "run-host-bad-org")
	unmanaged, err := f.s.Deps.Hosts.CreateHost(db.WithOrgScope(context.Background(), f.orgID), f.orgID,
		hosts.HostInput{Name: "web-3", Address: "10.0.2.1", ZoneID: f.zoneA, Managed: false})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		hostID string
		want   int
	}{
		"unknown":   {uuid.Must(uuid.NewV7()).String(), http.StatusNotFound},
		"unmanaged": {unmanaged.ID.String(), http.StatusNotFound},
		"malformed": {"not-a-uuid", http.StatusBadRequest},
		// Empty is refused by the schema, never read as "no narrowing".
		"empty": {"", http.StatusUnprocessableEntity},
	} {
		resp := postJSON(t, f.operatorC, f.runURL(), map[string]string{"host_id": c.hostID})
		if resp.StatusCode != c.want {
			t.Errorf("%s host: status %d, want %d, body %s", name, resp.StatusCode, c.want, mustReadBody(t, resp))
		}
		_ = resp.Body.Close()
	}
}

// TestRunRouteRefusesAnotherOrganizationsHost: a host_id naming a managed
// host of a different organization answers 404, exactly like an unknown id,
// and starts nothing.
func TestRunRouteRefusesAnotherOrganizationsHost(t *testing.T) {
	f := newRunFixture(t, "run-host-tenant-org")
	otherOwner := f.s.SeedUser("run-host-tenant-other@example.test", "correct horse battery")
	otherOrg := f.s.SeedOrg("run-host-tenant-other", otherOwner)
	scoped := db.WithOrgScope(context.Background(), otherOrg)
	zones, err := f.s.Deps.Hosts.ListZones(scoped, otherOrg)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones of the other org: %v", err)
	}
	foreign, err := f.s.Deps.Hosts.CreateHost(scoped, otherOrg,
		hosts.HostInput{Name: "web-9", Address: "10.9.0.1", ZoneID: zones[0].ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, f.operatorC, f.runURL(), map[string]string{"host_id": foreign.ID.String()})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another organization's host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	_ = resp.Body.Close()
}

// runJobWith posts body to a run URL and returns the created jobs.
func runJobWith(t *testing.T, c *http.Client, url string, body any) []jobDTO {
	t.Helper()
	resp := postJSON(t, c, url, body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, resp, &out)
	return out.Jobs
}
