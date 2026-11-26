package api_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/jobs"
)

// sleepRecorder is the fake pause of a long poll: it counts the pauses and
// runs onSleep(n) at the n-th one, so a test changes the row "while the
// handler waits" without any real time.
type sleepRecorder struct {
	calls   int
	onSleep func(n int)
	err     error
}

func (r *sleepRecorder) sleep(_ context.Context, d time.Duration) error {
	r.calls++
	if d != 500*time.Millisecond {
		panic("poll interval must be 500ms")
	}
	if r.err != nil {
		return r.err
	}
	if r.onSleep != nil {
		r.onSleep(r.calls)
	}
	return nil
}

func jobStatusAt(t *testing.T, f runFixture, query string, id string) string {
	t.Helper()
	resp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+id+query, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get job %s = %d %s", query, resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Job jobDTO `json:"job"`
	}
	decodeInto(t, resp, &out)
	return out.Job.Status
}

func TestGetJobWaitsForStatusChange(t *testing.T) {
	rec := &sleepRecorder{}
	f := newRunFixtureWith(t, "wait-job", apitest.WithWaitSleep(rec.sleep))
	job := runJob(t, f.operatorC, f.runURL())[0]
	zone := f.zoneA
	if job.ZoneID == f.zoneB.String() {
		zone = f.zoneB
	}

	// Without wait: no pause at all.
	if got := jobStatusAt(t, f, "", job.ID); got != "queued" || rec.calls != 0 {
		t.Fatalf("no wait: status %s, pauses %d", got, rec.calls)
	}

	// The job finishes during the third pause; the answer is the new status.
	rec.onSleep = func(n int) {
		if n == 3 {
			finishJob(t, f.s, zone, mustUUID(t, job.ID), jobs.StatusSucceeded, nil, "")
		}
	}
	if got := jobStatusAt(t, f, "?wait=25", job.ID); got != "succeeded" || rec.calls != 3 {
		t.Fatalf("wait: status %s after %d pauses, want succeeded after 3", got, rec.calls)
	}

	// unless: the status the caller knows differs from the current one, so
	// the answer is immediate.
	rec.calls = 0
	if got := jobStatusAt(t, f, "?wait=25&unless=queued", job.ID); got != "succeeded" || rec.calls != 0 {
		t.Fatalf("unless=queued: status %s, pauses %d", got, rec.calls)
	}
}

func TestGetJobWaitEndsWithTheTimeBudget(t *testing.T) {
	rec := &sleepRecorder{}
	f := newRunFixtureWith(t, "wait-job-time", apitest.WithWaitSleep(rec.sleep))
	job := runJob(t, f.operatorC, f.runURL())[0]
	if got := jobStatusAt(t, f, "?wait=2&unless=queued", job.ID); got != "queued" || rec.calls != 4 {
		t.Fatalf("status %s after %d pauses, want queued after 4 (2 s at 500 ms)", got, rec.calls)
	}
}

func TestGetJobWaitEndsWithTheRequestContext(t *testing.T) {
	rec := &sleepRecorder{err: context.Canceled}
	f := newRunFixtureWith(t, "wait-job-ctx", apitest.WithWaitSleep(rec.sleep))
	job := runJob(t, f.operatorC, f.runURL())[0]
	if got := jobStatusAt(t, f, "?wait=25", job.ID); got != "queued" || rec.calls != 1 {
		t.Fatalf("status %s after %d pauses, want queued after 1", got, rec.calls)
	}
}

func TestGetJobWaitIsBounded(t *testing.T) {
	f := newRunFixture(t, "wait-job-cap")
	job := runJob(t, f.operatorC, f.runURL())[0]
	for _, q := range []string{"?wait=26", "?wait=-1"} {
		resp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+job.ID+q, nil)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d, want 422", q, resp.StatusCode)
		}
	}
}

func TestGetJobWaitStillAuthorizes(t *testing.T) {
	rec := &sleepRecorder{}
	f := newRunFixtureWith(t, "wait-job-authz", apitest.WithWaitSleep(rec.sleep))
	job := runJob(t, f.operatorC, f.runURL())[0]
	anon := &http.Client{}
	if resp := requestJSON(t, anon, http.MethodGet, f.baseURL()+"/"+job.ID+"?wait=5", nil); resp.StatusCode != http.StatusUnauthorized || rec.calls != 0 {
		t.Fatalf("anonymous = %d, pauses %d", resp.StatusCode, rec.calls)
	}
}

func TestGetRunRequestWaitsForStatusChange(t *testing.T) {
	rec := &sleepRecorder{}
	f := newRunRequestFixture(t, "wait-rr", apitest.WithWaitSleep(rec.sleep))
	created := f.request(t, map[string]any{"reason": "wait for it"})
	d := f.detail(t, created.Request.ID)

	rec.onSleep = func(n int) {
		if n == 2 {
			resp := postJSON(t, f.adminC, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+created.Request.ID+"/reject", nil)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("reject = %d %s", resp.StatusCode, mustReadBody(t, resp))
			}
		}
	}
	rec.calls = 0
	resp := requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+created.Request.ID+"?wait=10&unless="+d.Request.Status, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out runRequestDetail
	decodeInto(t, resp, &out)
	if out.Request.Status != "rejected" || rec.calls != 2 {
		t.Fatalf("status %s after %d pauses, want rejected after 2", out.Request.Status, rec.calls)
	}

	// Already different from unless: immediate.
	rec.calls = 0
	resp = requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+created.Request.ID+"?wait=10&unless=pending", nil)
	decodeInto(t, resp, &out)
	if out.Request.Status != "rejected" || rec.calls != 0 {
		t.Fatalf("status %s, pauses %d", out.Request.Status, rec.calls)
	}

	// No change: the budget ends the wait with the current picture.
	rec.onSleep = nil
	resp = requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+created.Request.ID+"?wait=1", nil)
	decodeInto(t, resp, &out)
	if out.Request.Status != "rejected" || rec.calls != 2 {
		t.Fatalf("status %s after %d pauses, want rejected after 2", out.Request.Status, rec.calls)
	}
}

func TestGetRunRequestWaitResolvesThePictureOnce(t *testing.T) {
	var pictures atomic.Int64
	rec := &sleepRecorder{}
	f := newRunRequestFixture(t, "wait-rr-pic", apitest.WithWaitSleep(rec.sleep), apitest.WithPictureCounter(&pictures))
	created := f.request(t, map[string]any{"reason": "count the pictures"})
	pictures.Store(0)
	resp := requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+created.Request.ID+"?wait=25", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if rec.calls != 50 || pictures.Load() != 1 {
		t.Fatalf("%d pauses, %d picture resolutions; want 50 and 1", rec.calls, pictures.Load())
	}
}
