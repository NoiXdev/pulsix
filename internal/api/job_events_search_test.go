package api_test

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/jobs"
)

type jobEventsPage struct {
	Events []struct {
		Seq   uint64 `json:"seq"`
		Host  string `json:"host"`
		Task  string `json:"task"`
		State string `json:"state"`
	} `json:"events"`
	NextAfter *int64 `json:"next_after"`
}

func (p jobEventsPage) seqs() []uint64 {
	var out []uint64
	for _, e := range p.Events {
		out = append(out, e.Seq)
	}
	return out
}

// TestJobEventsSearch: GET …/jobs/{id}/events narrows by host, task, role
// and state (repeated or comma-joined), pages by limit and next_after, and
// refuses a limit or state outside the contract.
func TestJobEventsSearch(t *testing.T) {
	f := newRunFixture(t, "run-events-search")
	job := runJob(t, f.operatorC, f.runURL())[0]
	zone := f.zoneA
	if job.ZoneID == f.zoneB.String() {
		zone = f.zoneB
	}
	jobID := mustUUID(t, job.ID)
	base := f.baseURL() + "/" + job.ID + "/events"

	get := func(t *testing.T, query string) jobEventsPage {
		t.Helper()
		resp := requestJSON(t, f.adminC, http.MethodGet, base+query, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET events%s = %d %s", query, resp.StatusCode, mustReadBody(t, resp))
		}
		var p jobEventsPage
		decodeInto(t, resp, &p)
		return p
	}

	// Nothing yet: next_after stays where the caller was.
	if p := get(t, "?after=7"); len(p.Events) != 0 || p.NextAfter == nil || *p.NextAfter != 7 {
		t.Fatalf("empty log = %+v", p)
	}

	now := time.Now()
	appendEvents(t, f.s, zone, jobID, []jobs.Event{
		{Host: "web1", Task: "common : Install packages", State: "task", At: now},    // 1
		{Host: "web1", Task: "common : Install packages", State: "changed", At: now}, // 2
		{Host: "web2", Task: "nginx : Write 50%_config", State: "changed", At: now},  // 3
		{Host: "web2", Task: "nginx : Restart", State: "failed", At: now},            // 4
		{Host: "db1", Task: "Gathering Facts", State: "unreachable", At: now},        // 5
		{Host: "web1", Task: "Ping", State: "ok", At: now},                           // 6
	})

	for _, tc := range []struct {
		query string
		want  []uint64
		next  int64
	}{
		{"", []uint64{1, 2, 3, 4, 5, 6}, 6},
		{"?limit=2", []uint64{1, 2}, 2},
		{"?limit=2&after=2", []uint64{3, 4}, 4},
		{"?host=web2", []uint64{3, 4}, 4},
		{"?task=install", []uint64{1, 2}, 2},
		{"?task=50%25_", []uint64{3}, 3},
		{"?role=NGINX", []uint64{3, 4}, 4},
		{"?state=changed", []uint64{2, 3}, 3},
		{"?state=failed,unreachable", []uint64{4, 5}, 5},
		{"?state=failed&state=unreachable", []uint64{4, 5}, 5},
		{"?state=failed,unreachable&state=ok", []uint64{4, 5, 6}, 6},
		{"?host=web1&state=changed&role=common", []uint64{2}, 2},
		{"?host=nobody&after=3", nil, 3},
	} {
		t.Run(tc.query, func(t *testing.T) {
			p := get(t, tc.query)
			if !slices.Equal(p.seqs(), tc.want) {
				t.Fatalf("seqs %v, want %v", p.seqs(), tc.want)
			}
			if p.NextAfter == nil || *p.NextAfter != tc.next {
				t.Fatalf("next_after %v, want %d", p.NextAfter, tc.next)
			}
		})
	}

	for query, want := range map[string]int{
		"?limit=0":            http.StatusUnprocessableEntity,
		"?limit=1001":         http.StatusUnprocessableEntity,
		"?state=bogus":        http.StatusBadRequest,
		"?state=changed,,ok":  http.StatusBadRequest,
		"?state=failed,bogus": http.StatusBadRequest,
	} {
		resp := requestJSON(t, f.adminC, http.MethodGet, base+query, nil)
		if resp.StatusCode != want {
			t.Errorf("GET events%s = %d, want %d: %s", query, resp.StatusCode, want, mustReadBody(t, resp))
		}
	}
}
