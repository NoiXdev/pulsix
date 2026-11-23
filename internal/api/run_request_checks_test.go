package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/templates"
)

// TestRunRequestCreationChecks: a request is checked like the run it asks
// for (backlog 2.120): the template's limit applies to the one host it
// names, and a template whose scope matches no host under its limit is
// refused at once instead of waiting for an approval that cannot start it.
func TestRunRequestCreationChecks(t *testing.T) {
	f := newRunRequestFixture(t, "rr-checks")
	updateTemplate(t, f, func(in *templates.UpdateInput) {
		in.Scope = templates.Scope{HostIDs: []uuid.UUID{f.hostA, f.hostB}}
		in.Limit = "web-1"
	})
	path := f.orgPath() + "/job-templates/" + f.templateID.String() + "/run-requests"
	resp := f.s.AssistantDo(t, http.MethodPost, f.tok, path, "", map[string]any{"reason": "x", "host_id": f.hostB.String()})
	if resp.Code != http.StatusUnprocessableEntity || !strings.Contains(resp.Body.String(), jobs.ErrHostNotInScope.Error()) {
		t.Fatalf("a host the limit excludes = %d %s, want 422", resp.Code, resp.Body)
	}
	if resp := f.s.AssistantDo(t, http.MethodPost, f.tok, path, "", map[string]any{"reason": "x", "host_id": f.hostA.String()}); resp.Code != http.StatusCreated {
		t.Fatalf("a host the limit keeps = %d %s, want 201", resp.Code, resp.Body)
	}

	updateTemplate(t, f, func(in *templates.UpdateInput) { in.Limit = "db-*" })
	resp = f.s.AssistantDo(t, http.MethodPost, f.tok, path, "", map[string]any{"reason": "x"})
	if resp.Code != http.StatusUnprocessableEntity || !strings.Contains(resp.Body.String(), "the template's scope matches no host") {
		t.Fatalf("a scope the limit empties = %d %s, want 422", resp.Code, resp.Body)
	}
}

// TestRunRequestPendingCapPerToken: the 21st pending request of one token
// is refused with 429; a decision or an expiry frees a slot.
func TestRunRequestPendingCapPerToken(t *testing.T) {
	clock := newTestClock()
	f := newRunRequestFixture(t, "rr-cap", apitest.WithRunRequestClock(clock.Now))
	var first runRequestDetail
	for i := range runrequests.MaxPendingPerToken {
		d := f.request(t, map[string]any{"reason": "x"})
		if i == 0 {
			first = d
		}
	}
	path := f.orgPath() + "/job-templates/" + f.templateID.String() + "/run-requests"
	resp := f.s.AssistantDo(t, http.MethodPost, f.tok, path, "", map[string]any{"reason": "x"})
	if resp.Code != http.StatusTooManyRequests || !strings.Contains(resp.Body.String(), "wait until a person decides") {
		t.Fatalf("request 21 = %d %s, want 429", resp.Code, resp.Body)
	}
	if resp := postJSON(t, f.adminC, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+first.Request.ID+"/reject", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("reject = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	f.request(t, map[string]any{"reason": "a slot freed by a decision"})
	if resp := f.s.AssistantDo(t, http.MethodPost, f.tok, path, "", map[string]any{"reason": "x"}); resp.Code != http.StatusTooManyRequests {
		t.Fatalf("full again = %d %s, want 429", resp.Code, resp.Body)
	}
	clock.advance(time.Hour)
	f.request(t, map[string]any{"reason": "slots freed by expiry"})
}

func (f runRequestFixture) pendingCount(t *testing.T, c *http.Client, slug string) int {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+slug+"/run-requests/pending-count", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pending count = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Count *int `json:"count"`
	}
	decodeInto(t, resp, &out)
	if out.Count == nil {
		t.Fatal("pending count without a count")
	}
	return *out.Count
}

// TestRunRequestPendingCount: the approvers' badge counts the
// organization's pending, unexpired requests, with the list's permission.
func TestRunRequestPendingCount(t *testing.T) {
	clock := newTestClock()
	f := newRunRequestFixture(t, "rr-count", apitest.WithRunRequestClock(clock.Now))
	f.s.SeedOrg("rr-count-other", f.memberID(t, "rr-count-admin@example.test"))
	if n := f.pendingCount(t, f.viewerC, f.orgSlug); n != 0 {
		t.Fatalf("count of an empty queue = %d", n)
	}
	early := f.request(t, map[string]any{"reason": "x"})
	clock.advance(30 * time.Minute)
	f.request(t, map[string]any{"reason": "x"})
	rejected := f.request(t, map[string]any{"reason": "x"})
	if resp := postJSON(t, f.adminC, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+rejected.Request.ID+"/reject", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("reject = %d", resp.StatusCode)
	}
	if n := f.pendingCount(t, f.viewerC, f.orgSlug); n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}
	if n := f.pendingCount(t, f.adminC, "rr-count-other"); n != 0 {
		t.Fatalf("another organization's count = %d, want 0", n)
	}
	clock.advance(30 * time.Minute) // early reaches its hour
	if n := f.pendingCount(t, f.viewerC, f.orgSlug); n != 1 {
		t.Fatalf("count after %s expired = %d, want 1", early.Request.ID, n)
	}
}

// TestRunRequestPendingFilterBeyondTheCap: 501 newer decided requests do
// not hide a pending one from ?status=pending.
func TestRunRequestPendingFilterBeyondTheCap(t *testing.T) {
	f := newRunRequestFixture(t, "rr-filter")
	pending := f.request(t, map[string]any{"reason": "the one waiting"})
	if _, err := f.s.DB.Pool().Exec(context.Background(), `
		INSERT INTO run_requests (id, org_id, kind, template_id, template_revision, reason, requested_by_user, status, decided_by, decided_at, expires_at, created_at)
		SELECT gen_random_uuid(), r.org_id, 'run', r.template_id, 1, 'x', r.requested_by_user, 'approved', r.requested_by_user,
		       now(), now() + interval '1 hour', now() + n * interval '1 millisecond'
		FROM run_requests r, generate_series(1, 501) AS n WHERE r.id = $1`, pending.Request.ID); err != nil {
		t.Fatal(err)
	}
	resp := f.s.AssistantDo(t, http.MethodGet, f.tok, f.orgPath()+"/run-requests?status=pending", "", nil)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), pending.Request.ID) {
		t.Fatalf("pending list = %d, want it to hold %s", resp.Code, pending.Request.ID)
	}
}

// TestRunRequestRetryApprovedTwiceEnqueuesOnce: a retry request approved
// again after it went back to pending (a failed enqueue reverts it)
// replays the first approval's retry instead of enqueueing a second one.
func TestRunRequestRetryApprovedTwiceEnqueuesOnce(t *testing.T) {
	f := newRunRequestFixture(t, "rr-retry-once")
	failed := runJob(t, f.operatorC, f.runURLFor(f.templateID))[0]
	finishJob(t, f.s, f.zoneA, mustUUID(t, failed.ID), jobs.StatusFailed, map[string]any{
		jobs.StatsFailedHosts: []any{f.hostA.String()}, jobs.StatsUnreachableHosts: []any{},
	}, "playbook failed")
	created := f.retryRequest(t, failed.ID)
	if resp := f.approve(t, f.adminC, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	countJobs := func() int {
		t.Helper()
		var n int
		if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE run_group_id = $1`, created.Request.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countJobs(); n != 1 {
		t.Fatalf("jobs of the approved retry = %d, want 1", n)
	}
	if _, err := f.s.DB.Pool().Exec(context.Background(),
		`UPDATE run_requests SET status = 'pending', decided_by = NULL, decided_at = NULL, approved_fingerprint = NULL WHERE id = $1`, created.Request.ID); err != nil {
		t.Fatal(err)
	}
	if resp := f.approve(t, f.adminC, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve again = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if n := countJobs(); n != 1 {
		t.Fatalf("jobs after the second approval = %d, want still 1", n)
	}
}

// TestRunRequestNamesItsToken: the request names the token that made it
// and whether that token can still ask (backlog 2.120); a revoked,
// expired or deleted token is a hint for the approver, never a block.
func TestRunRequestNamesItsToken(t *testing.T) {
	f := newRunRequestFixture(t, "rr-token")
	created := f.request(t, map[string]any{"reason": "x"})
	var tokenID uuid.UUID
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT requested_by_token FROM run_requests WHERE id = $1`, created.Request.ID).Scan(&tokenID); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := f.s.DB.Pool().Exec(context.Background(), sql, tokenID); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		state, name, change string
	}{
		{"active", "assistant", ""},
		{"expired", "assistant", `UPDATE api_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1`},
		{"revoked", "assistant", `UPDATE api_tokens SET revoked_at = now() WHERE id = $1`},
		{"deleted", "", `DELETE FROM api_tokens WHERE id = $1`},
	} {
		if tc.change != "" {
			exec(tc.change)
		}
		d := f.detail(t, created.Request.ID)
		if d.Request.TokenState != tc.state || d.Request.RequestedByToken != tc.name {
			t.Fatalf("%s: detail names %q %q", tc.state, d.Request.RequestedByToken, d.Request.TokenState)
		}
		resp := requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/run-requests", nil)
		var list struct {
			Requests []runRequestDTO `json:"requests"`
		}
		decodeInto(t, resp, &list)
		if len(list.Requests) != 1 || list.Requests[0].TokenState != tc.state || list.Requests[0].RequestedByToken != tc.name {
			t.Fatalf("%s: list = %+v", tc.state, list.Requests)
		}
	}
	// A hint, not a block: the request of a deleted token still approves.
	if resp := f.approve(t, f.adminC, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}
