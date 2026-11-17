// Phase 4 of the assistant suite: a run request through its approval to the
// run and its follow-up, then the two job tools that act on a run of the
// token's own request — a retry request of a failed one and the cancel of
// a running one.
package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// requestedRun is request_run's and request_retry's result.
type requestedRun struct {
	RequestID   string `json:"request_id"`
	Status      string `json:"status"`
	ApprovalURL string `json:"approval_url"`
}

func (a *assistantSuite) runRequest() {
	s := a.s
	s.t.Helper()
	cs := a.mustConnect(a.allToken)

	// --- A: requested, approved by the owner, run; B follows --------------
	var req requestedRun
	a.recordOK(cs, "request_run", map[string]any{"template_id": a.seeded.templateA, "reason": "e2e run request"}, &req)
	if want := s.api.base + "/o/" + s.org + "/runs/requests/" + req.RequestID; req.Status != "pending" || req.ApprovalURL != want {
		s.t.Fatalf("request_run = %+v, want a pending request linking %s", req, want)
	}
	group := s.approveRunRequest(req.RequestID)
	aJob := s.firstJobOf(group)
	if done := s.awaitJob(aJob, "approved run", chainWait); done.Job.Status != "succeeded" {
		s.t.Fatalf("approved run = %q (error %q), want succeeded%s", done.Job.Status, done.Job.Error, s.runnerOutput())
	}
	child := s.awaitChainJob(chainWait)
	if child.TriggerRef != group {
		s.t.Fatalf("follow-up trigger_ref = %q, want the approved run's group %q", child.TriggerRef, group)
	}
	if done := s.awaitJob(child.ID, "follow-up", chainWait); done.Job.Status != "succeeded" || done.Job.Template == nil || done.Job.Template.Name != "asst-b" {
		s.t.Fatalf("follow-up = %+v, want a succeeded run of asst-b%s", done.Job, s.runnerOutput())
	}
	for _, id := range []string{aJob, child.ID} {
		var log struct {
			Log string `json:"log"`
		}
		a.recordOK(cs, "get_job_log", map[string]any{"job_id": id}, &log)
		if !strings.Contains(log.Log, "Pulsix greeting") || !strings.Contains(log.Log, s.greeting) {
			s.t.Fatalf("get_job_log of %s does not show the greeting task:\n%s", id, log.Log)
		}
	}

	// --- request_retry of a failed run of the token's own request ---------
	failTID := s.createTemplate("asst-fail", s.createEditorPlaybook("asst-fail", failTemplate(s.t)), map[string]any{"should_fail": "true"})
	failJob := a.approvedJob(cs, failTID, "e2e failing run")
	if done := s.awaitJob(failJob, "failing run", chainWait); done.Job.Status != "failed" {
		s.t.Fatalf("failing run = %q, want failed%s", done.Job.Status, s.runnerOutput())
	}
	var retry requestedRun
	a.recordOK(cs, "request_retry", map[string]any{"job_id": failJob, "reason": "e2e retry"}, &retry)
	var retryRead struct {
		Request struct {
			Kind               string `json:"kind"`
			Status             string `json:"status"`
			JobID              string `json:"job_id"`
			RequestedByTokenID string `json:"requested_by_token_id"`
		} `json:"request"`
	}
	s.get("/api/v1/"+s.apiPath()+"/run-requests/"+retry.RequestID, &retryRead)
	if r := retryRead.Request; r.Kind != "retry_failed" || r.Status != "pending" || r.JobID != failJob || r.RequestedByTokenID != a.allTokenID {
		s.t.Fatalf("retry request = %+v, want a pending retry_failed of %s by token %s", r, failJob, a.allTokenID)
	}

	// --- cancel_job on a running job of the token's own request -----------
	release := filepath.Join(s.t.TempDir(), "release")
	s.t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	blockTID := s.createTemplate("asst-block", a.seeded.playbookA, map[string]any{"greeting": s.greeting, "block_until": release})
	blockJob := a.approvedJob(cs, blockTID, "e2e run to cancel")
	s.awaitJobRunning(blockJob, chainWait)
	a.recordOK(cs, "cancel_job", map[string]any{"job_id": blockJob}, nil)
	if done := s.awaitJob(blockJob, "cancelled run", chainWait); done.Job.Status != "cancelled" {
		s.t.Fatalf("run after cancel_job = %q, want cancelled", done.Job.Status)
	}
}

// approvedJob requests a run of template through the session, has the
// owner approve it and returns the run's first job.
func (a *assistantSuite) approvedJob(cs *sdk.ClientSession, template, reason string) string {
	a.s.t.Helper()
	var req requestedRun
	a.recordOK(cs, "request_run", map[string]any{"template_id": template, "reason": reason}, &req)
	return a.s.firstJobOf(a.s.approveRunRequest(req.RequestID))
}

// approveRunRequest approves a run request in the owner's session with the
// fingerprint of the detail it reads, and returns the run group started.
func (s *scenario) approveRunRequest(id string) string {
	s.t.Helper()
	path := "/api/v1/" + s.apiPath() + "/run-requests/" + id
	var detail struct {
		Fingerprint string `json:"fingerprint"`
	}
	s.get(path, &detail)
	if detail.Fingerprint == "" {
		s.t.Fatalf("run request %s carries no fingerprint", id)
	}
	var out struct {
		Request struct {
			Status     string `json:"status"`
			RunGroupID string `json:"run_group_id"`
		} `json:"request"`
	}
	s.api.do(http.MethodPost, path+"/approve", map[string]any{"fingerprint": detail.Fingerprint}, &out)
	if out.Request.Status != "approved" || out.Request.RunGroupID == "" {
		s.t.Fatalf("approve %s = %+v, want approved with a run group", id, out.Request)
	}
	return out.Request.RunGroupID
}

// firstJobOf returns the one job of a run group.
func (s *scenario) firstJobOf(group string) string {
	s.t.Helper()
	var out struct {
		Jobs []idOut `json:"jobs"`
	}
	s.get("/api/v1/"+s.apiPath()+"/jobs?group="+group, &out)
	if len(out.Jobs) != 1 {
		s.t.Fatalf("run group %s holds %d jobs, want 1", group, len(out.Jobs))
	}
	return out.Jobs[0].ID
}
