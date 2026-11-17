package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
)

type hostPageFixture struct {
	s       *apitest.Server
	c       *http.Client
	orgID   uuid.UUID
	base    string
	zones   []uuid.UUID
	hostIDs []uuid.UUID // in creation (= name) order
}

type hostPageItem struct {
	hostDTO
	ActiveRuns int `json:"active_runs"`
}

type hostPageBody struct {
	Hosts      []hostPageItem `json:"hosts"`
	NextCursor string         `json:"next_cursor"`
	Total      *int           `json:"total"`
}

// newHostPageFixture seeds n hosts named h-01.. over three zones with
// alternating flags, so every sortable column has ties. Every third host has
// been seen (two of those at the very same instant).
func newHostPageFixture(t *testing.T, slug string, n int) hostPageFixture {
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser(slug+"@example.test", "correct horse battery")
	orgID := s.SeedOrg(slug, owner)
	scoped := db.WithOrgScope(context.Background(), orgID)
	f := hostPageFixture{s: s, c: s.LoginAs(owner), orgID: orgID, base: s.HTTP.URL + "/api/v1/orgs/" + slug + "/hosts"}
	zs, err := s.Deps.Hosts.ListZones(scoped, orgID)
	if err != nil || len(zs) == 0 {
		t.Fatalf("zones: %v", err)
	}
	f.zones = []uuid.UUID{zs[0].ID}
	for _, slugZ := range []string{"zone-b", "zone-c"} {
		z, err := s.Deps.Hosts.CreateZone(scoped, orgID, slugZ, slugZ)
		if err != nil {
			t.Fatal(err)
		}
		f.zones = append(f.zones, z.ID)
	}
	seen := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		h, err := s.Deps.Hosts.CreateHost(scoped, orgID, hosts.HostInput{
			Name: fmt.Sprintf("h-%02d", i), Address: fmt.Sprintf("10.0.0.%d", i), ZoneID: f.zones[i%3],
			Managed: i%2 == 0, Monitored: i%4 < 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		f.hostIDs = append(f.hostIDs, h.ID)
		if i%3 == 0 {
			at := seen.Add(time.Duration(i%2) * time.Hour) // ties between neighbours of one parity
			if _, err := s.DB.Pool().Exec(context.Background(), "UPDATE hosts SET last_seen_at = $2 WHERE id = $1", h.ID, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f
}

func (f hostPageFixture) get(t *testing.T, query string) hostPageBody {
	t.Helper()
	var out hostPageBody
	resp := requestJSON(t, f.c, http.MethodGet, f.base+"?"+query, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET ?%s: status %d, body %s", query, resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &out)
	return out
}

func (f hostPageFixture) status(t *testing.T, query string) int {
	t.Helper()
	resp := requestJSON(t, f.c, http.MethodGet, f.base+"?"+query, nil)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func hostNames(items []hostPageItem) []string {
	names := make([]string, len(items))
	for i, h := range items {
		names[i] = h.Name
	}
	return names
}

// addRun inserts one run of the given status targeting host.
func (f hostPageFixture) addRun(t *testing.T, host uuid.UUID, status jobs.Status) {
	t.Helper()
	j := jobs.Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.orgID, ZoneID: f.zones[0], Kind: jobs.KindAdhoc,
		HostIDs: []uuid.UUID{host}, Status: status, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: jobs.TriggerManual,
	}
	if err := jobs.NewSQLStore(f.s.DB).CreateJob(db.WithOrgScope(context.Background(), f.orgID), j, nil); err != nil {
		t.Fatal(err)
	}
}

func TestHostsPageModeReturnsTotalAndPage(t *testing.T) {
	f := newHostPageFixture(t, "hostpage-basic", 30)
	got := f.get(t, "page=2&per_page=10")
	if len(got.Hosts) != 10 || got.Hosts[0].Name != "h-11" || got.Hosts[9].Name != "h-20" {
		t.Fatalf("page 2 = %v, want h-11..h-20", hostNames(got.Hosts))
	}
	if got.Total == nil || *got.Total != 30 {
		t.Fatalf("total = %v, want 30", got.Total)
	}
	if got.NextCursor != "" {
		t.Fatalf("next_cursor = %q, want empty in page mode", got.NextCursor)
	}
}

func TestHostsWithoutPageKeepsCursorMode(t *testing.T) {
	f := newHostPageFixture(t, "hostpage-cursor", 30)
	resp := requestJSON(t, f.c, http.MethodGet, f.base+"?limit=10", nil)
	var raw map[string]json.RawMessage
	decodeInto(t, resp, &raw)
	if _, has := raw["total"]; has {
		t.Fatalf("cursor mode answered a total: %s", raw["total"])
	}
	var next string
	if err := json.Unmarshal(raw["next_cursor"], &next); err != nil || next == "" {
		t.Fatalf("next_cursor = %s, want a cursor", raw["next_cursor"])
	}
	var hs []hostPageItem
	if err := json.Unmarshal(raw["hosts"], &hs); err != nil || len(hs) != 10 || hs[0].Name != "h-01" {
		t.Fatalf("hosts = %v (%v)", hostNames(hs), err)
	}
}

func TestHostsSortEveryKeyStable(t *testing.T) {
	f := newHostPageFixture(t, "hostpage-stable", 25)
	// A few hosts with live runs, several sharing a count.
	for _, i := range []int{0, 1, 2, 3, 4} {
		f.addRun(t, f.hostIDs[i], jobs.StatusRunning)
	}
	f.addRun(t, f.hostIDs[0], jobs.StatusQueued)
	for _, key := range []string{"name", "zone", "managed", "monitored", "last_seen", "active_runs"} {
		for _, order := range []string{"asc", "desc"} {
			t.Run(key+"/"+order, func(t *testing.T) {
				q := "sort=" + key + "&order=" + order
				whole := f.get(t, q+"&page=1&per_page=100")
				if len(whole.Hosts) != 25 {
					t.Fatalf("whole page holds %d hosts, want 25", len(whole.Hosts))
				}
				var walked []string
				for p := 1; p <= 3; p++ {
					pg := f.get(t, fmt.Sprintf("%s&page=%d&per_page=10", q, p))
					walked = append(walked, hostNames(pg.Hosts)...)
				}
				if !slices.Equal(walked, hostNames(whole.Hosts)) {
					t.Fatalf("paged walk differs from the whole list:\n%v\n%v", walked, hostNames(whole.Hosts))
				}
				seen := map[string]bool{}
				for _, n := range walked {
					if seen[n] {
						t.Fatalf("host %s appears twice", n)
					}
					seen[n] = true
				}
			})
		}
	}
}

func TestHostsSortLastSeenNullsLast(t *testing.T) {
	f := newHostPageFixture(t, "hostpage-nulls", 12) // h-03, h-06, h-09, h-12 are seen
	for _, order := range []string{"asc", "desc"} {
		got := f.get(t, "page=1&per_page=100&sort=last_seen&order="+order)
		for i, h := range got.Hosts {
			wantSeen := i < 4
			if (h.LastSeenAt != nil) != wantSeen {
				t.Fatalf("%s: row %d (%s) last_seen_at = %v, want seen=%v; order %v", order, i, h.Name, h.LastSeenAt, wantSeen, hostNames(got.Hosts))
			}
		}
	}
	// The seen hosts themselves are reversed between the directions.
	asc := f.get(t, "page=1&per_page=100&sort=last_seen&order=asc")
	desc := f.get(t, "page=1&per_page=100&sort=last_seen&order=desc")
	if asc.Hosts[0].Name == desc.Hosts[0].Name {
		t.Fatalf("desc did not reorder the seen hosts: %v vs %v", hostNames(asc.Hosts[:4]), hostNames(desc.Hosts[:4]))
	}
}

func TestHostsPageBeyondEnd(t *testing.T) {
	f := newHostPageFixture(t, "hostpage-beyond", 30)
	resp := requestJSON(t, f.c, http.MethodGet, f.base+"?page=99&per_page=10", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var raw map[string]json.RawMessage
	decodeInto(t, resp, &raw)
	if string(raw["hosts"]) != "[]" || string(raw["total"]) != "30" {
		t.Fatalf("hosts = %s, total = %s, want [] and 30", raw["hosts"], raw["total"])
	}
}

func TestHostsHugePageIsEmptyNotWrapped(t *testing.T) {
	f := newHostPageFixture(t, "hostpage-huge", 30)
	// The highest accepted page times the largest size must not wrap the SQL
	// offset (a wrapped value answered 500 or the wrong rows).
	for _, q := range []string{"page=1000000&per_page=100", "page=429496&per_page=10&sort=zone&order=desc"} {
		resp := requestJSON(t, f.c, http.MethodGet, f.base+"?"+q, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("?%s: status %d: %s", q, resp.StatusCode, mustReadBody(t, resp))
		}
		var raw map[string]json.RawMessage
		decodeInto(t, resp, &raw)
		if string(raw["hosts"]) != "[]" || string(raw["total"]) != "30" {
			t.Fatalf("?%s: hosts = %s, total = %s, want [] and 30", q, raw["hosts"], raw["total"])
		}
	}
}

func TestHostsPageRespectsFilters(t *testing.T) {
	f := newHostPageFixture(t, "hostpage-filter", 30)
	if got := f.get(t, "page=1&q=h-1"); got.Total == nil || *got.Total != 10 || len(got.Hosts) != 10 {
		t.Fatalf("q=h-1: total %v, %d rows, want 10 and 10", got.Total, len(got.Hosts))
	}
	if got := f.get(t, "page=1&per_page=10&zone="+f.zones[1].String()); got.Total == nil || *got.Total != 10 {
		t.Fatalf("zone: total %v, want 10", got.Total)
	}
	got := f.get(t, "page=1&per_page=10&managed=true")
	if got.Total == nil || *got.Total != 15 || len(got.Hosts) != 10 {
		t.Fatalf("managed: total %v, %d rows, want 15 and 10", got.Total, len(got.Hosts))
	}
	for _, h := range got.Hosts {
		if !h.Managed {
			t.Fatalf("unmanaged host %s in managed=true", h.Name)
		}
	}
	both := f.get(t, "page=2&per_page=10&managed=true&q=h-2")
	if both.Total == nil || *both.Total != 5 || len(both.Hosts) != 0 {
		t.Fatalf("managed + q: total %v, %d rows on page 2, want 5 and 0", both.Total, len(both.Hosts))
	}
}

func TestHostsPageRejectsBadParams(t *testing.T) {
	f := newHostPageFixture(t, "hostpage-bad", 3)
	for _, q := range []string{"page=1&per_page=20", "page=1&sort=address", "page=1&order=up", "page=0", "page=1000001", "page=30000000&per_page=100", "page=1&cursor=abc"} {
		if got := f.status(t, q); got != http.StatusUnprocessableEntity {
			t.Errorf("?%s: status %d, want 422", q, got)
		}
	}
}

func TestHostsActiveRuns(t *testing.T) {
	f := newHostPageFixture(t, "hostpage-runs", 3)
	a, b := f.hostIDs[0], f.hostIDs[1]
	f.addRun(t, a, jobs.StatusQueued)
	f.addRun(t, a, jobs.StatusRunning)
	f.addRun(t, a, jobs.StatusSucceeded)
	f.addRun(t, b, jobs.StatusFailed)

	byName := func(items []hostPageItem) map[string]int {
		m := map[string]int{}
		for _, h := range items {
			m[h.Name] = h.ActiveRuns
		}
		return m
	}
	got := byName(f.get(t, "page=1").Hosts)
	if got["h-01"] != 2 || got["h-02"] != 0 || got["h-03"] != 0 {
		t.Fatalf("active_runs = %v, want h-01 2, others 0", got)
	}
	// Cursor mode carries the count too.
	if got := byName(f.get(t, "limit=10").Hosts); got["h-01"] != 2 {
		t.Fatalf("cursor mode active_runs = %v", got)
	}

	only := f.get(t, "page=1&active_runs=true")
	if only.Total == nil || *only.Total != 1 || len(only.Hosts) != 1 || only.Hosts[0].Name != "h-01" {
		t.Fatalf("active_runs=true: %v total %v", hostNames(only.Hosts), only.Total)
	}
	if cur := f.get(t, "active_runs=true"); len(cur.Hosts) != 1 || cur.Hosts[0].Name != "h-01" {
		t.Fatalf("cursor mode active_runs=true: %v", hostNames(cur.Hosts))
	}
	if idle := f.get(t, "page=1&active_runs=false"); idle.Total == nil || *idle.Total != 2 {
		t.Fatalf("active_runs=false total = %v, want 2", idle.Total)
	}
	if first := f.get(t, "page=1&sort=active_runs&order=desc").Hosts[0]; first.Name != "h-01" {
		t.Fatalf("sort=active_runs desc starts with %s", first.Name)
	}

	// POST /hosts/query carries it.
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	for _, id := range f.hostIDs {
		if _, err := f.s.Deps.Hosts.SetTags(scoped, f.orgID, id, []hosts.Tag{{Key: "role", Value: "web", Source: hosts.TagSourceManual}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	resp := requestJSON(t, f.c, http.MethodPost, f.base+"/query", map[string]string{"selector": "role=web"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("query: status %d: %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var q hostPageBody
	decodeInto(t, resp, &q)
	if m := byName(q.Hosts); len(m) != 3 || m["h-01"] != 2 {
		t.Fatalf("query active_runs = %v", m)
	}
}
