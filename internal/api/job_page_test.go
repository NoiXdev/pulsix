package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
)

type jobPageFixture struct {
	s     *apitest.Server
	c     *http.Client
	orgID uuid.UUID
	zone  uuid.UUID
	base  string
	ids   []uuid.UUID // in creation order, oldest first
}

type jobPageBody struct {
	Jobs []struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Status  string `json:"status"`
		Trigger string `json:"trigger"`
	} `json:"jobs"`
	NextCursor string `json:"next_cursor"`
	Total      *int   `json:"total"`
}

func newJobPageFixture(t *testing.T, slug string) jobPageFixture {
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser(slug+"@example.test", "correct horse battery")
	orgID := s.SeedOrg(slug, owner)
	zs, err := s.Deps.Hosts.ListZones(db.WithOrgScope(context.Background(), orgID), orgID)
	if err != nil || len(zs) == 0 {
		t.Fatalf("zones: %v", err)
	}
	return jobPageFixture{s: s, c: s.LoginAs(owner), orgID: orgID, zone: zs[0].ID, base: s.HTTP.URL + "/api/v1/orgs/" + slug + "/jobs"}
}

func (f *jobPageFixture) host(t *testing.T, name string) uuid.UUID {
	t.Helper()
	h, err := f.s.Deps.Hosts.CreateHost(db.WithOrgScope(context.Background(), f.orgID), f.orgID, hosts.HostInput{Name: name, Address: "10.0.0.1", ZoneID: f.zone})
	if err != nil {
		t.Fatal(err)
	}
	return h.ID
}

func (f *jobPageFixture) add(t *testing.T, kind jobs.Kind, status jobs.Status, trigger string, hs ...uuid.UUID) uuid.UUID {
	t.Helper()
	j := jobs.Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.orgID, ZoneID: f.zone, Kind: kind, HostIDs: hs, Status: status,
		RunGroupID: uuid.Must(uuid.NewV7()), Trigger: trigger,
	}
	if err := jobs.NewSQLStore(f.s.DB).CreateJob(db.WithOrgScope(context.Background(), f.orgID), j, nil); err != nil {
		t.Fatal(err)
	}
	f.ids = append(f.ids, j.ID)
	return j.ID
}

func (f *jobPageFixture) get(t *testing.T, query string) jobPageBody {
	t.Helper()
	var out jobPageBody
	resp := requestJSON(t, f.c, http.MethodGet, f.base+"?"+query, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET ?%s: status %d, body %s", query, resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &out)
	return out
}

func (b jobPageBody) idList() []string {
	ids := make([]string, len(b.Jobs))
	for i, j := range b.Jobs {
		ids[i] = j.ID
	}
	return ids
}

func strs(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

var allStatuses = []jobs.Status{
	jobs.StatusQueued, jobs.StatusClaimed, jobs.StatusRunning, jobs.StatusCancelling,
	jobs.StatusSucceeded, jobs.StatusFailed, jobs.StatusCancelled, jobs.StatusError,
}

func isLive(s string) bool {
	return slices.Contains([]string{"queued", "claimed", "running", "cancelling"}, s)
}

func TestJobsStatusActive(t *testing.T) {
	f := newJobPageFixture(t, "jobpage-active")
	h := f.host(t, "h1")
	for _, st := range allStatuses {
		f.add(t, jobs.KindAdhoc, st, jobs.TriggerManual, h)
		f.add(t, jobs.KindAdhoc, st, jobs.TriggerManual) // the same status, no host
	}
	check := func(query string, wantLive int) {
		t.Helper()
		got := f.get(t, query)
		for _, j := range got.Jobs {
			if !isLive(j.Status) {
				t.Errorf("?%s returned a %s run", query, j.Status)
			}
		}
		if len(got.Jobs) != wantLive {
			t.Errorf("?%s: %d runs, want %d", query, len(got.Jobs), wantLive)
		}
		if got.Total != nil && *got.Total != wantLive {
			t.Errorf("?%s: total %d, want %d", query, *got.Total, wantLive)
		}
	}
	check("status=active", 8)
	check("status=active&page=1", 8)
	check("status=active&page=1&per_page=10&sort=status", 8)
	check(fmt.Sprintf("status=active&host_id=%s", h), 4)
	check(fmt.Sprintf("status=active&host_id=%s&page=1", h), 4)
	if got := f.get(t, "status=active&page=1"); got.Total == nil {
		t.Error("page mode answered no total")
	}
	// A page past the end answers 200, no rows, the right total.
	if got := f.get(t, "status=active&page=3&per_page=10"); len(got.Jobs) != 0 || got.Total == nil || *got.Total != 8 {
		t.Errorf("past the end = %d runs, total %v", len(got.Jobs), got.Total)
	}
	// Cursor mode pages the live runs too.
	first := f.get(t, "status=active&limit=3")
	if len(first.Jobs) != 3 || first.NextCursor == "" || first.Total != nil {
		t.Fatalf("cursor page 1: %d runs, cursor %q, total %v", len(first.Jobs), first.NextCursor, first.Total)
	}
	rest := f.get(t, "status=active&limit=10&cursor="+first.NextCursor)
	if len(rest.Jobs) != 5 || rest.NextCursor != "" {
		t.Fatalf("cursor rest: %d runs, cursor %q", len(rest.Jobs), rest.NextCursor)
	}
	resp := requestJSON(t, f.c, http.MethodGet, f.base+"?status=bogus", nil)
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || !json.Valid([]byte(body)) || !strings.Contains(body, "active") {
		t.Errorf("bad status: %d %s, want 400 naming active", resp.StatusCode, body)
	}
}

func TestJobsPageMode(t *testing.T) {
	f := newJobPageFixture(t, "jobpage-basic")
	for range 30 {
		f.add(t, jobs.KindAdhoc, jobs.StatusSucceeded, jobs.TriggerManual)
	}
	got := f.get(t, "page=3&per_page=10")
	if want := strs(f.ids[:10]); !slices.Equal(slices.Sorted(slices.Values(got.idList())), slices.Sorted(slices.Values(want))) {
		t.Fatalf("page 3 = %v, want the oldest 10 %v", got.idList(), want)
	}
	if got.idList()[0] != f.ids[9].String() || got.Total == nil || *got.Total != 30 || got.NextCursor != "" {
		t.Fatalf("page 3 order/total/cursor = %v / %v / %q", got.idList(), got.Total, got.NextCursor)
	}
	if past := f.get(t, "page=4&per_page=10"); len(past.Jobs) != 0 || past.Total == nil || *past.Total != 30 {
		t.Fatalf("page 4 = %d runs, total %v", len(past.Jobs), past.Total)
	}
	var raw map[string]json.RawMessage
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, f.base+"?limit=10&sort=kind", nil), &raw)
	if _, has := raw["total"]; has {
		t.Fatalf("cursor mode answered a total: %s", raw["total"])
	}
	for _, q := range []string{"page=1&cursor=abc", "page=0", "page=1&per_page=7", "page=1&sort=template"} {
		resp := requestJSON(t, f.c, http.MethodGet, f.base+"?"+q, nil)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("?%s: status %d, want 422", q, resp.StatusCode)
		}
	}
}

func TestJobsPageWithHostFilter(t *testing.T) {
	f := newJobPageFixture(t, "jobpage-host")
	a, b := f.host(t, "a"), f.host(t, "b")
	for i := range 12 {
		if i%3 == 0 {
			f.add(t, jobs.KindAdhoc, jobs.StatusSucceeded, jobs.TriggerManual, a)
		} else {
			f.add(t, jobs.KindAdhoc, jobs.StatusSucceeded, jobs.TriggerManual, b)
		}
	}
	for _, q := range []string{"", "&sort=kind&order=asc"} {
		got := f.get(t, fmt.Sprintf("host_id=%s&page=1&per_page=10%s", a, q))
		if got.Total == nil || *got.Total != 4 || len(got.Jobs) != 4 {
			t.Fatalf("host a%s: %d runs, total %v, want 4 and 4", q, len(got.Jobs), got.Total)
		}
	}
	got := f.get(t, fmt.Sprintf("host_id=%s&page=2&per_page=10", b))
	if got.Total == nil || *got.Total != 8 || len(got.Jobs) != 0 {
		t.Fatalf("host b page 2: %d runs, total %v", len(got.Jobs), got.Total)
	}
}

func TestJobsSortStatusRank(t *testing.T) {
	f := newJobPageFixture(t, "jobpage-rank")
	for _, st := range slices.Backward(allStatuses) {
		f.add(t, jobs.KindAdhoc, st, jobs.TriggerManual)
	}
	got := f.get(t, "page=1&sort=status&order=asc")
	for i, st := range allStatuses {
		if got.Jobs[i].Status != string(st) {
			t.Fatalf("asc[%d] = %s, want %s", i, got.Jobs[i].Status, st)
		}
	}
	got = f.get(t, "page=1&sort=status&order=desc")
	if got.Jobs[0].Status != "error" || got.Jobs[7].Status != "queued" {
		t.Fatalf("desc = %v", got.Jobs)
	}
}

func TestJobsSortStable(t *testing.T) {
	f := newJobPageFixture(t, "jobpage-stable")
	kinds := []jobs.Kind{jobs.KindAdhoc, jobs.KindConnectionTest, jobs.KindSyncRepo}
	triggers := []string{jobs.TriggerManual, jobs.TriggerSchedule, jobs.TriggerSystem, jobs.TriggerChain}
	for i := range 40 {
		f.add(t, kinds[i%3], allStatuses[i%8], triggers[i%4])
	}
	for _, key := range []string{"created", "status", "kind", "trigger"} {
		for _, order := range []string{"asc", "desc"} {
			var seen []string
			for page := 1; page <= 4; page++ {
				got := f.get(t, fmt.Sprintf("page=%d&per_page=10&sort=%s&order=%s", page, key, order))
				if got.Total == nil || *got.Total != 40 {
					t.Fatalf("%s %s: total %v", key, order, got.Total)
				}
				seen = append(seen, got.idList()...)
			}
			if len(seen) != 40 || len(slices.Compact(slices.Sorted(slices.Values(seen)))) != 40 {
				t.Errorf("%s %s: pages hold %d ids, %d distinct, want 40 each", key, order, len(seen), len(slices.Compact(slices.Sorted(slices.Values(seen)))))
			}
		}
	}
}
