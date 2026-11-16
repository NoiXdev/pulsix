// The run flow's registry steps: editor playbooks, job templates, the run
// and retry actions. All over the real HTTP API.
package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// createEditorPlaybook stores an inline playbook.
func (s *scenario) createEditorPlaybook(name, inline string) string {
	s.t.Helper()
	var out playbookOut
	s.post("/api/v1/"+s.apiPath()+"/playbooks", map[string]any{
		"name": name, "inline": inline,
	}, &out)
	if out.Playbook.Source != "editor" {
		s.t.Fatalf("playbook source = %q, want editor", out.Playbook.Source)
	}
	return out.Playbook.ID
}

// createTemplate stores a job template scoped to the one host.
func (s *scenario) createTemplate(name, playbookID string, extraVars map[string]any) string {
	s.t.Helper()
	var out templateOut
	s.post("/api/v1/"+s.apiPath()+"/job-templates", map[string]any{
		"name": name, "playbook_id": playbookID,
		"scope":      map[string]any{"host_ids": []string{s.hostID}},
		"extra_vars": extraVars,
	}, &out)
	if out.Template.ID == "" {
		s.t.Fatalf("template %s carries no id", name)
	}
	return out.Template.ID
}

// runTemplate starts a run of the template and returns the first job's id.
func (s *scenario) runTemplate(templateID string) string {
	s.t.Helper()
	var out jobStartOut
	s.post("/api/v1/"+s.apiPath()+"/job-templates/"+templateID+"/run", struct{}{}, &out)
	if len(out.Jobs) != 1 {
		s.t.Fatalf("run of template %s created %d job(s), want 1", templateID, len(out.Jobs))
	}
	return out.Jobs[0].ID
}

// retryFailed triggers the retry action on a finished failed run.
func (s *scenario) retryFailed(jobID string) jobStartOut {
	s.t.Helper()
	var out jobStartOut
	s.post("/api/v1/"+s.apiPath()+"/jobs/"+jobID+"/retry-failed", struct{}{}, &out)
	if len(out.Jobs) != 1 {
		s.t.Fatalf("retry of job %s created %d job(s), want 1", jobID, len(out.Jobs))
	}
	return out
}

// createTemplateScoped stores a job template scoped to the given hosts with
// follow-up templates (hello playbook vars are the scenario's greeting).
func (s *scenario) createTemplateScoped(name, pbID string, hostIDs, followUps []string) string {
	s.t.Helper()
	ups := make([]map[string]any, len(followUps))
	for i, id := range followUps {
		ups[i] = map[string]any{"template_id": id}
	}
	var out templateOut
	s.post("/api/v1/"+s.apiPath()+"/job-templates", map[string]any{
		"name": name, "playbook_id": pbID,
		"scope":      map[string]any{"host_ids": hostIDs},
		"extra_vars": map[string]any{"greeting": s.greeting},
		"follow_ups": ups,
	}, &out)
	if out.Template.ID == "" {
		s.t.Fatalf("template %s carries no id", name)
	}
	return out.Template.ID
}

// updateFollowUps replaces a template's follow-ups and returns the raw
// status and body, so a refusal can be asserted.
func (s *scenario) updateFollowUps(id, name, pbID string, ids []string) (int, string) {
	s.t.Helper()
	ups := make([]map[string]any, len(ids))
	for i, f := range ids {
		ups[i] = map[string]any{"template_id": f}
	}
	raw, err := json.Marshal(map[string]any{"name": name, "playbook_id": pbID, "follow_ups": ups})
	if err != nil {
		s.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, s.api.base+"/api/v1/"+s.apiPath()+"/job-templates/"+id, bytes.NewReader(raw))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.api.http.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}
