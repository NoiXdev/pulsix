package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/templates"
)

func (f runFixture) dryRunURL(playbook uuid.UUID) string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/playbooks/" + playbook.String() + "/dry-run"
}

// startDryRun posts a dry run and returns the jobs it started.
func startDryRun(t *testing.T, c *http.Client, url string, body any) []jobDTO {
	t.Helper()
	resp := postJSON(t, c, url, body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("dry run: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, resp, &out)
	return out.Jobs
}

// countJobs counts the organization's jobs straight from the table.
func (f runFixture) countJobs(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE org_id = $1`, f.orgID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// dryRunAudit reads the dry runs' audit rows, oldest first.
func (f runFixture) dryRunAudit(t *testing.T) []struct {
	objectID, summary string
	meta              map[string]any
} {
	t.Helper()
	rows, err := f.s.DB.Pool().Query(context.Background(),
		`SELECT object_id, summary, meta FROM audit_events WHERE org_id = $1 AND action = 'POST dryRunPlaybook' ORDER BY at, id`, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []struct {
		objectID, summary string
		meta              map[string]any
	}
	for rows.Next() {
		var r struct {
			objectID, summary string
			meta              map[string]any
		}
		var raw []byte
		if err := rows.Scan(&r.objectID, &r.summary, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &r.meta); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// TestPlaybookDryRunStartsACheckModeRun is spec 2026-11-22 §1: a dry run
// is a manual run of the template marked check_mode, in the start answer,
// the run detail and the audit log, which records the tags, the host count
// and a draft's SHA-256, never its text.
func TestPlaybookDryRunStartsACheckModeRun(t *testing.T) {
	f := newRunFixture(t, "dry-run-start-org")
	started := startDryRun(t, f.operatorC, f.dryRunURL(f.playbook), map[string]any{
		"template_id": f.templateID.String(), "tags": []string{"web", "db"},
	})
	if len(started) != 1 || started[0].CheckMode == nil || !*started[0].CheckMode || started[0].Trigger != "manual" || started[0].TemplateID != f.templateID.String() {
		t.Fatalf("started = %+v", started)
	}
	resp := requestJSON(t, f.viewerC, http.MethodGet, f.baseURL()+"/"+started[0].ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail: status %d", resp.StatusCode)
	}
	var detail struct {
		Job jobDTO `json:"job"`
	}
	decodeInto(t, resp, &detail)
	if detail.Job.CheckMode == nil || !*detail.Job.CheckMode {
		t.Fatalf("detail = %+v, want check_mode true", detail.Job)
	}
	if dr := detail.Job.DryRun; dr == nil || !slices.Equal(dr.Tags, []string{"web", "db"}) || !dr.Diff || dr.Draft {
		t.Fatalf("detail dry_run = %+v, want tags web,db, diff, no draft", dr)
	}
	if dr := started[0].DryRun; dr == nil || !slices.Equal(dr.Tags, []string{"web", "db"}) {
		t.Fatalf("started dry_run = %+v", dr)
	}

	draft := "- name: draft-text-7c1e\n  hosts: all\n  tasks: []\n"
	drafted := startDryRun(t, f.operatorC, f.dryRunURL(f.playbook), map[string]any{
		"template_id": f.templateID.String(), "draft": draft, "diff": false,
	})
	if dr := jobDetail(t, f, drafted[0].ID).DryRun; dr == nil || dr.Tags == nil || len(dr.Tags) != 0 || dr.Diff || !dr.Draft {
		t.Fatalf("draft detail dry_run = %+v, want no tags, no diff, draft", dr)
	}
	if ordinary := runJob(t, f.operatorC, f.runURL())[0]; ordinary.DryRun != nil || jobDetail(t, f, ordinary.ID).DryRun != nil {
		t.Fatalf("an ordinary run carries a dry_run: %+v", ordinary)
	}
	resp = requestJSON(t, f.viewerC, http.MethodGet, f.baseURL()+"/"+started[0].ID, nil)
	if body := mustReadBody(t, resp); strings.Contains(body, "draft-text-7c1e") {
		t.Fatalf("the detail carries the draft text: %s", body)
	}

	audit := f.dryRunAudit(t)
	if len(audit) != 2 {
		t.Fatalf("audit rows = %d, want 2", len(audit))
	}
	first, second := audit[0], audit[1]
	if first.objectID != f.playbook.String() || !strings.Contains(first.summary, f.templateID.String()) {
		t.Fatalf("first audit row = %+v", first)
	}
	if first.meta["check_mode"] != true || first.meta["host_count"] != float64(1) || first.meta["diff"] != true {
		t.Fatalf("first audit meta = %v", first.meta)
	}
	if tags, _ := first.meta["tags"].([]any); len(tags) != 2 || tags[0] != "web" || tags[1] != "db" {
		t.Fatalf("first audit tags = %v", first.meta["tags"])
	}
	if _, ok := first.meta["draft_sha256"]; ok {
		t.Fatalf("a run of the saved playbook has no draft hash: %v", first.meta)
	}
	sum := sha256.Sum256([]byte(draft))
	if second.meta["draft_sha256"] != hex.EncodeToString(sum[:]) || second.meta["diff"] != false {
		t.Fatalf("second audit meta = %v, want draft_sha256 %x", second.meta, sum)
	}
	raw, _ := json.Marshal(second.meta)
	if strings.Contains(string(raw)+second.summary, "draft-text-7c1e") {
		t.Fatalf("the audit row carries the draft text: %s %s", second.summary, raw)
	}
}

// jobDetail reads one job's detail record.
func jobDetail(t *testing.T, f runFixture, id string) jobDTO {
	t.Helper()
	resp := requestJSON(t, f.viewerC, http.MethodGet, f.baseURL()+"/"+id, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail %s: status %d", id, resp.StatusCode)
	}
	var detail struct {
		Job jobDTO `json:"job"`
	}
	decodeInto(t, resp, &detail)
	return detail.Job
}

// TestPlaybookDryRunNarrowsToChosenHosts is Review Focus 3 at the API: the
// chosen hosts narrow the template's targets; none of them among the
// targets is a 422 and starts nothing.
func TestPlaybookDryRunNarrowsToChosenHosts(t *testing.T) {
	f := newRunFixture(t, "dry-run-hosts-org")
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	both, err := f.s.Deps.Templates.Create(scoped, f.orgID, templates.CreateInput{
		Name: "deploy-both", PlaybookID: f.playbook, Scope: templates.Scope{HostIDs: []uuid.UUID{f.hostA, f.hostB}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started := startDryRun(t, f.operatorC, f.dryRunURL(f.playbook), map[string]any{
		"template_id": both.ID.String(), "host_ids": []string{f.hostB.String()},
	})
	if len(started) != 1 || started[0].ZoneID != f.zoneB.String() {
		t.Fatalf("started = %+v, want one job in hostB's zone", started)
	}
	before := f.countJobs(t)
	resp := postJSON(t, f.operatorC, f.dryRunURL(f.playbook), map[string]any{
		"template_id": f.templateID.String(), "host_ids": []string{f.hostB.String()},
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("host outside the template: status %d, want 422, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if n := f.countJobs(t); n != before {
		t.Fatalf("jobs = %d, want %d", n, before)
	}
}

func TestPlaybookDryRunRefusals(t *testing.T) {
	f := newRunFixture(t, "dry-run-refuse-org")
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	other, err := f.s.Deps.Playbooks.CreateEditor(scoped, f.orgID, playbooks.CreateInput{Name: "other", Inline: "- hosts: all\n  tasks: []\n"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := f.s.Deps.Repos.Create(scoped, f.orgID, repos.CreateInput{Name: "infra", URL: "https://git.example.com/acme/infra.git"})
	if err != nil {
		t.Fatal(err)
	}
	repoPB, err := f.s.Deps.Playbooks.CreateRepo(scoped, f.orgID, repo.ID, "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	repoTmpl, err := f.s.Deps.Templates.Create(scoped, f.orgID, templates.CreateInput{
		Name: "deploy-infra", PlaybookID: repoPB.ID, Scope: templates.Scope{HostIDs: []uuid.UUID{f.hostA}},
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := "- hosts: all\n  tasks: []\n"
	tmpl := f.templateID.String()
	tags33 := make([]string, 33)
	for i := range tags33 {
		tags33[i] = "t"
	}

	for _, c := range []struct {
		name     string
		client   *http.Client
		playbook uuid.UUID
		body     map[string]any
		want     int
	}{
		{"template of another playbook", f.operatorC, other.ID, map[string]any{"template_id": tmpl}, http.StatusUnprocessableEntity},
		{"unknown playbook", f.operatorC, uuid.Must(uuid.NewV7()), map[string]any{"template_id": tmpl}, http.StatusUnprocessableEntity},
		{"unknown template", f.operatorC, f.playbook, map[string]any{"template_id": uuid.Must(uuid.NewV7()).String()}, http.StatusNotFound},
		{"malformed template id", f.operatorC, f.playbook, map[string]any{"template_id": "nope"}, http.StatusBadRequest},
		{"malformed host id", f.operatorC, f.playbook, map[string]any{"template_id": tmpl, "host_ids": []string{"nope"}}, http.StatusBadRequest},
		{"draft of a repository playbook", f.operatorC, repoPB.ID, map[string]any{"template_id": repoTmpl.ID.String(), "draft": valid}, http.StatusUnprocessableEntity},
		{"invalid draft", f.operatorC, f.playbook, map[string]any{"template_id": tmpl, "draft": "hosts: [unclosed"}, http.StatusUnprocessableEntity},
		{"draft without plays", f.operatorC, f.playbook, map[string]any{"template_id": tmpl, "draft": "{}"}, http.StatusUnprocessableEntity},
		{"empty draft", f.operatorC, f.playbook, map[string]any{"template_id": tmpl, "draft": ""}, http.StatusUnprocessableEntity},
		{"oversize draft", f.operatorC, f.playbook, map[string]any{"template_id": tmpl, "draft": valid + "# " + strings.Repeat("x", playbooks.MaxInlineBytes) + "\n"}, http.StatusUnprocessableEntity},
		{"33 tags", f.operatorC, f.playbook, map[string]any{"template_id": tmpl, "tags": tags33}, http.StatusUnprocessableEntity},
		{"tag with a comma", f.operatorC, f.playbook, map[string]any{"template_id": tmpl, "tags": []string{"a,b"}}, http.StatusUnprocessableEntity},
		{"tag with a space", f.operatorC, f.playbook, map[string]any{"template_id": tmpl, "tags": []string{"a b"}}, http.StatusUnprocessableEntity},
		{"tag of 65 characters", f.operatorC, f.playbook, map[string]any{"template_id": tmpl, "tags": []string{strings.Repeat("a", 65)}}, http.StatusUnprocessableEntity},
		{"viewer", f.viewerC, f.playbook, map[string]any{"template_id": tmpl}, http.StatusForbidden},
		{"viewer with a draft", f.viewerC, f.playbook, map[string]any{"template_id": tmpl, "draft": valid}, http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := postJSON(t, c.client, f.dryRunURL(c.playbook), c.body)
			if resp.StatusCode != c.want {
				t.Fatalf("status %d, want %d, body %s", resp.StatusCode, c.want, mustReadBody(t, resp))
			}
		})
	}
	if n := f.countJobs(t); n != 0 {
		t.Fatalf("jobs = %d, want none", n)
	}
	if rows := f.dryRunAudit(t); len(rows) != 0 {
		t.Fatalf("audit rows = %d, want none for refused dry runs", len(rows))
	}
}

// TestJobListFiltersByCheckMode: check_mode=true lists only dry runs,
// false only real runs, and omitted both, in cursor and page mode.
func TestJobListFiltersByCheckMode(t *testing.T) {
	f := newRunFixture(t, "dry-run-list-org")
	realRun := runJob(t, f.operatorC, f.runURL())[0].ID
	dryRun := startDryRun(t, f.operatorC, f.dryRunURL(f.playbook), map[string]any{"template_id": f.templateID.String()})[0].ID

	for _, c := range []struct {
		query string
		want  []string
	}{
		{"", []string{dryRun, realRun}},
		{"check_mode=true", []string{dryRun}},
		{"check_mode=false", []string{realRun}},
		{"page=1", []string{dryRun, realRun}},
		{"page=1&check_mode=true", []string{dryRun}},
		{"page=1&check_mode=false&sort=status", []string{realRun}},
		{"check_mode=true&playbook_id=" + f.playbook.String(), []string{dryRun}},
		{"check_mode=true&host_id=" + f.hostA.String(), []string{dryRun}},
		{"check_mode=false&status=active", []string{realRun}},
	} {
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.baseURL()+"?"+c.query, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%q: status %d, body %s", c.query, resp.StatusCode, mustReadBody(t, resp))
		}
		var out struct {
			Jobs []jobDTO `json:"jobs"`
		}
		decodeInto(t, resp, &out)
		var got []string
		for _, j := range out.Jobs {
			got = append(got, j.ID)
			if j.CheckMode == nil || *j.CheckMode != (j.ID == dryRun) {
				t.Fatalf("%q: job %s check_mode = %v", c.query, j.ID, j.CheckMode)
			}
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Fatalf("%q: jobs = %v, want %v", c.query, got, c.want)
		}
	}
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.baseURL()+"?check_mode=maybe", nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("check_mode=maybe: status %d, want 422", resp.StatusCode)
	}
}

// TestRetryFailedRefusesADryRun: a retry would run a dry run's failed
// hosts for real, so it is refused with a reason.
func TestRetryFailedRefusesADryRun(t *testing.T) {
	f := newRunFixture(t, "dry-run-retry-org")
	dry := startDryRun(t, f.operatorC, f.dryRunURL(f.playbook), map[string]any{"template_id": f.templateID.String()})[0].ID
	if _, err := f.s.DB.Pool().Exec(context.Background(),
		`UPDATE jobs SET status = 'failed', finished_at = now(), stats = jsonb_build_object('failed_hosts', jsonb_build_array($2::text)) WHERE id = $1`,
		dry, f.hostA.String()); err != nil {
		t.Fatal(err)
	}
	before := f.countJobs(t)
	resp := postJSON(t, f.operatorC, f.baseURL()+"/"+dry+"/retry-failed", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if body := mustReadBody(t, resp); !strings.Contains(body, "dry run") {
		t.Fatalf("body %s does not give the reason", body)
	}
	if n := f.countJobs(t); n != before {
		t.Fatalf("jobs = %d, want %d", n, before)
	}
}
