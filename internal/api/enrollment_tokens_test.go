package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/jobs"
)

// claimedEnrollToken claims the fixture's queued job in zone and returns
// the claim and the token it hands host.
func claimedEnrollToken(t *testing.T, f runRequestFixture, zone uuid.UUID, jobID, host string) (*jobs.Claim, string) {
	t.Helper()
	c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{zone}, "v1")
	if err != nil || c == nil || c.Job.GetId() != jobID {
		t.Fatalf("Claim = %+v, %v; want job %s", c, err, jobID)
	}
	var doc struct {
		Meta struct {
			Hostvars map[string]map[string]any `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal([]byte(c.Job.GetInventoryDoc()), &doc); err != nil {
		t.Fatal(err)
	}
	tok, _ := doc.Meta.Hostvars[host]["pulsix_agent_enroll_token"].(string)
	if _, err := enrolltoken.ParseAgent(tok); err != nil {
		t.Fatalf("%s's hostvars = %v, want an agent enrollment token", host, doc.Meta.Hostvars[host])
	}
	return c, tok
}

// get reads one route as the fixture's admin and returns the body.
func (f runRequestFixture) get(t *testing.T, path string) string {
	t.Helper()
	resp := requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+path, nil)
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, resp.StatusCode, body)
	}
	return body
}

// TestEnrollingRunNeverShowsItsToken (spec 2026-11-25 B, the acceptance):
// an approved run of a template with agent enrollment hands its host a
// token at the claim, and no API answer — the job, its log, the request
// and its picture, the activity log — ever carries it. The job shows
// counts only: minted, and used once the agent enrolled.
func TestEnrollingRunNeverShowsItsToken(t *testing.T) {
	f := newRunRequestFixture(t, "enrol-tokens")
	f.enrolOn(t)
	created := f.request(t, map[string]any{"reason": "install the agent"})
	if resp := f.approve(t, f.adminC, created.Request.ID, created.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	group := f.groupJobs(t, created.Request.ID)
	if len(group) != 1 {
		t.Fatalf("approved jobs = %+v", group)
	}
	jobID := group[0].ID.String()
	_, tok := claimedEnrollToken(t, f, group[0].ZoneID, jobID, "web-1")
	parsed, _ := enrolltoken.ParseAgent(tok)
	secret := base64.RawURLEncoding.EncodeToString(parsedSecret(t, tok))

	assertClean := func(where, body string) {
		t.Helper()
		if strings.Contains(body, tok) || strings.Contains(body, secret) {
			t.Fatalf("%s carries the token: %s", where, body)
		}
	}
	counts := func() (int, int) {
		t.Helper()
		body := f.get(t, f.orgPath()+"/jobs/"+jobID)
		assertClean("the job", body)
		var d struct {
			Job struct {
				EnrollTokens *struct {
					Minted int `json:"minted"`
					Used   int `json:"used"`
				} `json:"enroll_tokens"`
			} `json:"job"`
		}
		if err := json.Unmarshal([]byte(body), &d); err != nil || d.Job.EnrollTokens == nil {
			t.Fatalf("job = %s, want enroll_tokens", body)
		}
		return d.Job.EnrollTokens.Minted, d.Job.EnrollTokens.Used
	}
	if minted, used := counts(); minted != 1 || used != 0 {
		t.Fatalf("counts = %d minted, %d used; want 1, 0", minted, used)
	}
	assertClean("the job list", f.get(t, f.orgPath()+"/jobs"))
	// The pending token list marks the run's token as the job's (review
	// finding 7) and never carries its secret.
	pending := f.get(t, f.orgPath()+"/agents/tokens")
	assertClean("the pending tokens", pending)
	var list struct {
		Tokens []struct {
			ID    string `json:"id"`
			JobID string `json:"job_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(pending), &list); err != nil || len(list.Tokens) != 1 ||
		list.Tokens[0].ID != parsed.ID.String() || list.Tokens[0].JobID != jobID {
		t.Fatalf("pending tokens = %s, want the run's token marked with job %s", pending, jobID)
	}
	assertClean("the job log", f.get(t, f.orgPath()+"/jobs/"+jobID+"/events"))
	assertClean("the run request and its picture", f.get(t, f.orgPath()+"/run-requests/"+created.Request.ID))
	activity := f.get(t, f.orgPath()+"/activity?limit=100")
	assertClean("the activity log", activity)
	if !strings.Contains(activity, "enrollment tokens minted for 1 hosts by job "+jobID) {
		t.Fatalf("activity = %s, want the mint", activity)
	}

	if _, err := f.s.Deps.Agents.ClaimToken(context.Background(), f.orgID, parsed, ""); err != nil {
		t.Fatalf("enrol web-1: %v", err)
	}
	if err := f.s.Queue.Finish(context.Background(), group[0].ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	if minted, used := counts(); minted != 1 || used != 1 {
		t.Fatalf("counts after the enrollment = %d minted, %d used; want 1, 1", minted, used)
	}
	assertClean("the activity log", f.get(t, f.orgPath()+"/activity?limit=100"))
}

// A run that minted no token shows no token counts: one that enrols
// nothing, and one whose confirmed list had nothing to mint (review
// finding 6).
func TestJobWithoutEnrollmentShowsNoTokenCounts(t *testing.T) {
	f := newRunRequestFixture(t, "enrol-tokens-none")
	started := runJob(t, f.operatorC, f.runURLFor(f.templateID))
	empty, err := f.s.Deps.Jobs.EnqueueRunWith(f.scoped, f.orgID, f.templateID, jobs.RunRequest{Trigger: jobs.TriggerManual, EnrollHostIDs: []uuid.UUID{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{started[0].ID, empty[0].ID.String()} {
		body := f.get(t, f.orgPath()+"/jobs/"+id)
		if strings.Contains(body, "enroll_tokens") {
			t.Fatalf("job = %s, want no enroll_tokens", body)
		}
	}
}

// parsedSecret is an agent token's secret part.
func parsedSecret(t *testing.T, tok string) []byte {
	t.Helper()
	parsed, err := enrolltoken.ParseAgent(tok)
	if err != nil || len(parsed.Secret) == 0 {
		t.Fatalf("not an agent token: %v", err)
	}
	return parsed.Secret
}
