package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/templates"
)

// TestJobListFiltersByPlaybook is spec 2026-11-19 §2: playbook_id narrows
// the runs list to the runs of templates that use that playbook, in every
// shape the list answers (cursor and page mode, sorted, live runs, one
// host's runs); runs without a template never match.
func TestJobListFiltersByPlaybook(t *testing.T) {
	f := newRunFixture(t, "run-by-playbook-org")
	scoped := db.WithOrgScope(context.Background(), f.orgID)

	other, err := f.s.Deps.Playbooks.CreateEditor(scoped, f.orgID, playbooks.CreateInput{
		Name: "other", Inline: "- name: other\n  hosts: all\n  tasks: []\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherTmpl, err := f.s.Deps.Templates.Create(scoped, f.orgID, templates.CreateInput{
		Name: "other", PlaybookID: other.ID, Scope: templates.Scope{HostIDs: []uuid.UUID{f.hostA}},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{}
	for range 2 {
		for _, j := range runJob(t, f.operatorC, f.runURL()) {
			want[j.ID] = true
		}
	}
	runJob(t, f.operatorC, f.runURLFor(otherTmpl.ID))
	ctURL := f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/hosts/" + f.hostA.String() + "/connection-test"
	if resp := postJSON(t, f.operatorC, ctURL, nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("connection test: status %d", resp.StatusCode)
	}

	pb := "playbook_id=" + f.playbook.String()
	for _, query := range []string{
		pb,
		pb + "&page=1",
		pb + "&page=1&sort=status",
		pb + "&status=active",
		pb + "&status=active&page=1",
		pb + "&host_id=" + f.hostA.String(),
		pb + "&host_id=" + f.hostA.String() + "&page=1",
		pb + "&host_id=" + f.hostA.String() + "&page=1&sort=kind",
	} {
		var out struct {
			Jobs  []jobDTO `json:"jobs"`
			Total *int     `json:"total"`
		}
		resp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?"+query, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", query, resp.StatusCode, mustReadBody(t, resp))
		}
		decodeInto(t, resp, &out)
		if len(out.Jobs) != len(want) {
			t.Fatalf("%s: %d runs, want %d: %+v", query, len(out.Jobs), len(want), out.Jobs)
		}
		for _, j := range out.Jobs {
			if !want[j.ID] {
				t.Fatalf("%s returned a run of another playbook or none: %+v", query, j)
			}
		}
		if out.Total != nil && *out.Total != len(want) {
			t.Fatalf("%s: total %d, want %d", query, *out.Total, len(want))
		}
	}

	// A malformed id is a 400 like template_id, never a silent empty page.
	if resp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?playbook_id=not-a-uuid", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("playbook_id=not-a-uuid: status %d, want 400", resp.StatusCode)
	}
}

// TestJobListPlaybookFilterStaysInItsOrg: another organization's playbook
// id matches none of this organization's runs, and this organization's
// playbook id none of the other's.
func TestJobListPlaybookFilterStaysInItsOrg(t *testing.T) {
	f := newRunFixture(t, "run-by-playbook-iso-org")
	runJob(t, f.operatorC, f.runURL())

	owner2 := f.s.SeedUser("run-by-playbook-iso-other@example.test", "correct horse battery")
	org2 := f.s.SeedOrg("run-by-playbook-iso-other", owner2)
	pb2, err := f.s.Deps.Playbooks.CreateEditor(db.WithOrgScope(context.Background(), org2), org2, playbooks.CreateInput{
		Name: "foreign", Inline: "- name: foreign\n  hosts: all\n  tasks: []\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name   string
		client *http.Client
		url    string
	}{
		{"foreign playbook in this org", f.adminC, f.baseURL() + "?playbook_id=" + pb2.ID.String()},
		{"this playbook in the other org", f.s.LoginAs(owner2), f.s.HTTP.URL + "/api/v1/orgs/run-by-playbook-iso-other/jobs?page=1&playbook_id=" + f.playbook.String()},
	} {
		var out struct {
			Jobs []jobDTO `json:"jobs"`
		}
		resp := requestJSON(t, c.client, http.MethodGet, c.url, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", c.name, resp.StatusCode, mustReadBody(t, resp))
		}
		decodeInto(t, resp, &out)
		if len(out.Jobs) != 0 {
			t.Fatalf("%s: %+v, want none", c.name, out.Jobs)
		}
	}
}
