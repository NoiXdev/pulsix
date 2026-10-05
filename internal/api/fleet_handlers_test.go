// The fleet overview's tests (v0.3b-2 Task 11): ordering worst-state-first
// across all four states plus "no data", each of the three filters, an
// empty organization, and cross-organization isolation — all driven
// through the full API on checksFixture (check_handlers_test.go), the same
// fixture host_check_handlers_test.go shares.
package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/items"
)

type fleetHostBody struct {
	HostID      string     `json:"host_id"`
	Name        string     `json:"name"`
	ZoneID      string     `json:"zone_id"`
	State       *string    `json:"state"`
	Crit        int        `json:"crit"`
	Warn        int        `json:"warn"`
	Unknown     int        `json:"unknown"`
	Since       *time.Time `json:"since"`
	EvaluatedAt *time.Time `json:"evaluated_at"`
	AllIgnored  bool       `json:"all_ignored"`
}

type fleetBody struct {
	Hosts  []fleetHostBody `json:"hosts"`
	Totals struct {
		Crit       int `json:"crit"`
		Warn       int `json:"warn"`
		Unknown    int `json:"unknown"`
		OK         int `json:"ok"`
		NoData     int `json:"no_data"`
		AllIgnored int `json:"all_ignored"`
	} `json:"totals"`
}

func (f checksFixture) fleetURL(query string) string {
	url := f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/fleet"
	if query != "" {
		url += "?" + query
	}
	return url
}

// fleet reads GET /fleet?query and expects 200.
func (f checksFixture) fleet(t *testing.T, c *http.Client, query string) fleetBody {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, f.fleetURL(query), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fleet?%s = %d %s", query, resp.StatusCode, mustReadBody(t, resp))
	}
	var out fleetBody
	decodeInto(t, resp, &out)
	return out
}

// discoverItem seeds hostID with one discovered item directly through the
// discovery store — the same shape itemsFixture.discover
// (item_handlers_test.go) uses. Duplicated here rather than shared because
// checksFixture and itemsFixture are two separate fixtures this package
// otherwise keeps apart; items.Service carries no Discover of its own
// (only the evaluation ticker calls it), so the fixture builds its own
// store on the shared test database, exactly like itemsFixture does.
func (f checksFixture) discoverItem(t *testing.T, hostID uuid.UUID, checkKey, name string) {
	t.Helper()
	store := items.NewSQLStore(f.s.DB)
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	if err := store.Discover(scoped, f.orgID, hostID, []items.Seen{{CheckKey: checkKey, Name: name}}); err != nil {
		t.Fatalf("discover item: %v", err)
	}
}

// createItemDecision creates an item decision through the real API — the
// same POST the Items tab's "ignore this item" action sends — and fails
// the test unless the server accepts it. hostID is nil for scope "org".
func (f checksFixture) createItemDecision(t *testing.T, scope string, hostID *uuid.UUID, checkKey, matchKind, pattern, verdict string) {
	t.Helper()
	body := map[string]any{
		"scope": scope, "check_key": checkKey, "match_kind": matchKind, "pattern": pattern, "decision": verdict,
	}
	if hostID != nil {
		body["host_id"] = hostID.String()
	}
	resp := postJSON(t, f.operatorC, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/item-decisions", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create item decision = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// hostIDs extracts fleetBody.Hosts' HostID column, in the order the
// response carried it — the shape the ordering assertions below compare.
func (b fleetBody) hostIDs() []string {
	out := make([]string, len(b.Hosts))
	for i, h := range b.Hosts {
		out[i] = h.HostID
	}
	return out
}

func (b fleetBody) byHost(t *testing.T, id uuid.UUID) fleetHostBody {
	t.Helper()
	for _, h := range b.Hosts {
		if h.HostID == id.String() {
			return h
		}
	}
	t.Fatalf("no host %s in %+v", id, b.Hosts)
	return fleetHostBody{}
}

// seedHostState writes one check_host_states row directly through the
// generated query the evaluator itself upserts through
// (gen.UpsertCheckHostState) — this file's job is the fleet route's READ
// side, not re-proving the roll-up Task 9's own tests already cover.
func seedHostState(t *testing.T, s *apitest.Server, orgID, hostID uuid.UUID, state string, crit, warn, unknown int, since, evaluatedAt time.Time) {
	t.Helper()
	if err := s.Q.UpsertCheckHostState(context.Background(), gen.UpsertCheckHostStateParams{
		OrgID: orgID, HostID: hostID, State: state,
		CritCount: int32(crit), WarnCount: int32(warn), UnknownCount: int32(unknown), //nolint:gosec // test fixture, small counts
		Since: since, EvaluatedAt: evaluatedAt,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestFleetRoutes drives GET /fleet on one fixture, augmented with two
// extra hosts (checksFixture's own three cover only two states plus "no
// data") and a fourth zone-only host for the zone filter.
func TestFleetRoutes(t *testing.T) {
	f := newChecksFixture(t, "fleet")
	now := time.Now().UTC().Truncate(time.Microsecond)
	since := now.Add(-time.Hour)

	scoped := db.WithOrgScope(context.Background(), f.orgID)
	zones, err := f.s.Deps.Hosts.ListZones(scoped, f.orgID)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	zone := zones[0].ID
	okHost := seedCheckHost(t, f.s, scoped, f.orgID, zone, "fleet-ok-1", "10.1.0.1", true, "")
	unknownHost := seedCheckHost(t, f.s, scoped, f.orgID, zone, "fleet-unknown-1", "10.1.0.2", true, "")

	// f.web = warn, f.dbHost = crit, f.cold gets no roll-up row at all
	// (never evaluated — it is not monitored, and no test below ever
	// writes one for it).
	seedHostState(t, f.s, f.orgID, f.web, "warn", 0, 1, 0, since, now)
	seedHostState(t, f.s, f.orgID, f.dbHost, "crit", 1, 0, 0, since, now)
	seedHostState(t, f.s, f.orgID, okHost, "ok", 0, 0, 0, since, now)
	seedHostState(t, f.s, f.orgID, unknownHost, "unknown", 0, 0, 1, since, now)

	t.Run("ordering across all four states, and a host with no roll-up row is not dropped", func(t *testing.T) {
		body := f.fleet(t, f.viewerC, "")
		want := []string{f.dbHost.String(), f.web.String(), unknownHost.String(), okHost.String(), f.cold.String()}
		if got := body.hostIDs(); !equalStrings(got, want) {
			t.Fatalf("order = %v, want crit, warn, unknown, ok, no-data = %v", got, want)
		}

		cold := body.byHost(t, f.cold)
		if cold.State != nil {
			t.Fatalf("cold-1 state = %v, want null: it has no roll-up row at all", *cold.State)
		}
		if cold.Crit != 0 || cold.Warn != 0 || cold.Unknown != 0 {
			t.Fatalf("cold-1 counts = %+v, want all zero, not omitted", cold)
		}
		if cold.Since != nil || cold.EvaluatedAt != nil {
			t.Fatalf("cold-1 = %+v, want since and evaluated_at both absent", cold)
		}
		// cold-1 discovered nothing at all — genuinely "no data", not "all
		// ignored" (backlog 3.44's own distinction).
		if cold.AllIgnored {
			t.Fatalf("cold-1 all_ignored = true, want false: it never discovered anything to ignore")
		}

		crit := body.byHost(t, f.dbHost)
		if crit.State == nil || *crit.State != "crit" || crit.Crit != 1 {
			t.Fatalf("db-1 = %+v, want crit with one crit tuple", crit)
		}
		if crit.Since == nil || !crit.Since.Equal(since) || crit.EvaluatedAt == nil || !crit.EvaluatedAt.Equal(now) {
			t.Fatalf("db-1 since/evaluated_at = %+v, want %v / %v", crit, since, now)
		}

		if body.Totals.Crit != 1 || body.Totals.Warn != 1 || body.Totals.Unknown != 1 || body.Totals.OK != 1 || body.Totals.NoData != 1 || body.Totals.AllIgnored != 0 {
			t.Fatalf("totals = %+v, want exactly one host per bucket and none in all_ignored", body.Totals)
		}
	})

	t.Run("the secondary sort key is host name, not creation order", func(t *testing.T) {
		// Two hosts sharing one state (rank alone cannot separate them),
		// created in the OPPOSITE of name order. uuid.NewV7 embeds a
		// timestamp, so the host created first also sorts first by id —
		// a discriminating case: if a future change dropped the name
		// tie-break (say, by swapping the stable sort for a plain one, or
		// by building the roster from an id-ordered query), "fleet-tie-zzz"
		// would come out ahead of "fleet-tie-aaa" because it was created,
		// and given its state, first. A single scoped zone isolates these
		// two from every other host this test seeds.
		tieZone, err := f.s.Deps.Hosts.CreateZone(scoped, f.orgID, "fleet-tie-zone", "Tie Zone")
		if err != nil {
			t.Fatal(err)
		}
		zHost := seedCheckHost(t, f.s, scoped, f.orgID, tieZone.ID, "fleet-tie-zzz", "10.1.0.4", true, "")
		aHost := seedCheckHost(t, f.s, scoped, f.orgID, tieZone.ID, "fleet-tie-aaa", "10.1.0.5", true, "")
		seedHostState(t, f.s, f.orgID, zHost, "crit", 1, 0, 0, since, now)
		seedHostState(t, f.s, f.orgID, aHost, "crit", 1, 0, 0, since, now)

		body := f.fleet(t, f.viewerC, "zone="+tieZone.ID.String())
		want := []string{aHost.String(), zHost.String()}
		if got := body.hostIDs(); !equalStrings(got, want) {
			t.Fatalf("tied hosts = %v, want name order (aaa before zzz) = %v", got, want)
		}
	})

	t.Run("the zone filter", func(t *testing.T) {
		other, err := f.s.Deps.Hosts.CreateZone(scoped, f.orgID, "fleet-zone-b", "Zone B")
		if err != nil {
			t.Fatal(err)
		}
		zoned := seedCheckHost(t, f.s, scoped, f.orgID, other.ID, "fleet-zoned-1", "10.1.0.3", true, "")
		seedHostState(t, f.s, f.orgID, zoned, "crit", 1, 0, 0, since, now)

		body := f.fleet(t, f.viewerC, "zone="+other.ID.String())
		if got := body.hostIDs(); !equalStrings(got, []string{zoned.String()}) {
			t.Fatalf("zone filter hosts = %v, want only %s", got, zoned)
		}
	})

	t.Run("the group filter matches only what the group's selector reaches", func(t *testing.T) {
		// seedCheckHost tags web-1 "role=web" and db-1 "role=db"
		// (check_handlers_test.go's own doc comment on checksFixture).
		g := f.createGroup(t, map[string]any{"name": "fleet-web-tier", "selector": "role=web", "priority": 10})

		body := f.fleet(t, f.viewerC, "group="+g.ID)
		if got := body.hostIDs(); !equalStrings(got, []string{f.web.String()}) {
			t.Fatalf("group filter hosts = %v, want only web-1", got)
		}

		// A group naming id that does not exist in this organization
		// matches no host — the same reading rules.HostStates itself gives
		// a deleted or foreign group — not a 404: the id is a filter value,
		// not the resource this request is about.
		body = f.fleet(t, f.viewerC, "group="+uuid.Must(uuid.NewV7()).String())
		if len(body.Hosts) != 0 {
			t.Fatalf("nonexistent group hosts = %+v, want none", body.Hosts)
		}
	})

	t.Run("the tag filter: equality and existence, never a full selector", func(t *testing.T) {
		body := f.fleet(t, f.viewerC, "tag=role=web")
		if got := body.hostIDs(); !equalStrings(got, []string{f.web.String()}) {
			t.Fatalf("tag=role=web hosts = %v, want only web-1", got)
		}

		body = f.fleet(t, f.viewerC, "tag=role")
		want := []string{f.dbHost.String(), f.web.String()}
		got := body.hostIDs()
		if len(got) != len(want) {
			t.Fatalf("tag=role hosts = %v, want both tagged hosts %v", got, want)
		}
		for _, id := range want {
			found := false
			for _, g := range got {
				found = found || g == id
			}
			if !found {
				t.Fatalf("tag=role hosts = %v, missing %s", got, id)
			}
		}

		for _, bad := range []string{"role!=web", "role in (web,db)", "role=web and other=x"} {
			resp := requestJSON(t, f.viewerC, http.MethodGet, f.fleetURL("tag="+url.QueryEscape(bad)), nil)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("tag=%q = %d, want 422", bad, resp.StatusCode)
			}
			if body := mustReadBody(t, resp); !strings.Contains(body, "key") || !strings.Contains(body, "value") {
				t.Fatalf("refusal for tag=%q = %q, want it to name the accepted shape", bad, body)
			}
		}
	})

	t.Run("a host whose every discovered item is ignored reads all_ignored, not no data (backlog 3.44)", func(t *testing.T) {
		host := seedCheckHost(t, f.s, scoped, f.orgID, zone, "fleet-ignored-1", "10.1.0.10", true, "")
		f.discoverItem(t, host, "disk", "/data")
		f.createItemDecision(t, "host", &host, "disk", "exact", "/data", "ignore")
		// No seedHostState call: this reproduces exactly what Apply leaves
		// behind once the only judged tuple is dropped as ignored — no
		// check_host_states row at all (rules' own "a host with no tuples
		// gets no row" contract), the same absence a never-evaluated host
		// leaves.

		body := f.fleet(t, f.viewerC, "")
		row := body.byHost(t, host)
		if row.State != nil {
			t.Fatalf("fleet-ignored-1 state = %v, want null", *row.State)
		}
		if !row.AllIgnored {
			t.Fatalf("fleet-ignored-1 all_ignored = false, want true: its only discovered item is ignored")
		}
		if body.Totals.AllIgnored != 1 {
			t.Fatalf("totals.all_ignored = %d, want 1 (only fleet-ignored-1)", body.Totals.AllIgnored)
		}
		// cold-1 is still the only genuine "no data" host; this host must
		// not have been counted there instead.
		if body.Totals.NoData != 1 {
			t.Fatalf("totals.no_data = %d, want 1 (only cold-1, unaffected by fleet-ignored-1)", body.Totals.NoData)
		}
	})

	t.Run("a host with no roll-up row is not all_ignored unless every discovered item is", func(t *testing.T) {
		host := seedCheckHost(t, f.s, scoped, f.orgID, zone, "fleet-partly-ignored-1", "10.1.0.11", true, "")
		f.discoverItem(t, host, "disk", "/data")
		f.discoverItem(t, host, "service", "sshd")
		f.createItemDecision(t, "host", &host, "disk", "exact", "/data", "ignore")
		// "sshd" carries no decision at all — the default verdict is watch
		// (items.Decide's own doc comment) — so this host has a real,
		// unignored item and must read as plain "no data", not "all
		// ignored": claiming the honest middle case for a host that still
		// has something unjudged would be exactly as dishonest as the
		// "no data" collapse backlog 3.44 is about.
		body := f.fleet(t, f.viewerC, "")
		row := body.byHost(t, host)
		if row.State != nil {
			t.Fatalf("fleet-partly-ignored-1 state = %v, want null", *row.State)
		}
		if row.AllIgnored {
			t.Fatalf("fleet-partly-ignored-1 all_ignored = true, want false: sshd is still watched")
		}
	})

	t.Run("a host with a real roll-up state keeps it even if every discovered item is ignored", func(t *testing.T) {
		host := seedCheckHost(t, f.s, scoped, f.orgID, zone, "fleet-ignored-but-ok-1", "10.1.0.12", true, "")
		f.discoverItem(t, host, "disk", "/data")
		f.createItemDecision(t, "host", &host, "disk", "exact", "/data", "ignore")
		// Unlike the first case above, this host DOES still have a
		// roll-up row — standing in for a host whose cpu/memory tuples
		// (which discover no item at all, per the v0.3b-3 design's own
		// "items with an empty name" ruling) are still judged and keep it
		// ok. A host with even one judged tuple keeps its real state
		// regardless of how many other items are ignored.
		seedHostState(t, f.s, f.orgID, host, "ok", 0, 0, 0, since, now)

		body := f.fleet(t, f.viewerC, "")
		row := body.byHost(t, host)
		if row.State == nil || *row.State != "ok" {
			t.Fatalf("fleet-ignored-but-ok-1 state = %v, want ok", row.State)
		}
		if row.AllIgnored {
			t.Fatalf("fleet-ignored-but-ok-1 all_ignored = true, want false: it has a real state")
		}
	})

	t.Run("an organization-wide ignore decision marks a host all_ignored too", func(t *testing.T) {
		host := seedCheckHost(t, f.s, scoped, f.orgID, zone, "fleet-org-ignored-1", "10.1.0.13", true, "")
		f.discoverItem(t, host, "service", "sshd")
		f.createItemDecision(t, "org", nil, "service", "glob", "*", "ignore")

		body := f.fleet(t, f.viewerC, "")
		row := body.byHost(t, host)
		if row.State != nil || !row.AllIgnored {
			t.Fatalf("fleet-org-ignored-1 = %+v, want state null and all_ignored true", row)
		}
	})

	t.Run("an empty organization renders no hosts and zero totals", func(t *testing.T) {
		owner := f.s.SeedUser("fleet-empty-owner@example.test", "correct horse battery")
		f.s.SeedOrg("fleet-empty", owner)
		c := f.s.LoginAs(owner)
		resp := requestJSON(t, c, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/fleet-empty/fleet", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("empty org fleet = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var body fleetBody
		decodeInto(t, resp, &body)
		if len(body.Hosts) != 0 {
			t.Fatalf("empty org hosts = %+v, want none", body.Hosts)
		}
		if body.Totals != (struct {
			Crit       int `json:"crit"`
			Warn       int `json:"warn"`
			Unknown    int `json:"unknown"`
			OK         int `json:"ok"`
			NoData     int `json:"no_data"`
			AllIgnored int `json:"all_ignored"`
		}{}) {
			t.Fatalf("empty org totals = %+v, want all zero", body.Totals)
		}
	})

	t.Run("cross-organization isolation", func(t *testing.T) {
		otherOwner := f.s.SeedUser("fleet-other-owner@example.test", "correct horse battery")
		otherOrg := f.s.SeedOrg("fleet-other", otherOwner)
		otherScoped := db.WithOrgScope(context.Background(), otherOrg)
		otherZones, err := f.s.Deps.Hosts.ListZones(otherScoped, otherOrg)
		if err != nil || len(otherZones) == 0 {
			t.Fatalf("other org zones: %v", err)
		}
		otherHost := seedCheckHost(t, f.s, otherScoped, otherOrg, otherZones[0].ID, "other-1", "10.2.0.1", true, "")
		seedHostState(t, f.s, otherOrg, otherHost, "crit", 1, 0, 0, since, now)

		otherC := f.s.LoginAs(otherOwner)
		resp := requestJSON(t, otherC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/fleet-other/fleet", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("other org fleet = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var body fleetBody
		decodeInto(t, resp, &body)
		if got := body.hostIDs(); !equalStrings(got, []string{otherHost.String()}) {
			t.Fatalf("other org hosts = %v, want only its own host", got)
		}

		// And the original organization's own listing is unaffected by the
		// other organization's write.
		mine := f.fleet(t, f.viewerC, "")
		for _, h := range mine.Hosts {
			if h.HostID == otherHost.String() {
				t.Fatalf("fleet-org's listing leaked another organization's host: %+v", mine.Hosts)
			}
		}
	})

	t.Run("access", func(t *testing.T) {
		if resp := requestJSON(t, f.stranger, http.MethodGet, f.fleetURL(""), nil); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("non-member = %d, want 403", resp.StatusCode)
		}
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.fleetURL("zone=not-a-uuid"), nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("malformed zone id = %d, want 400", resp.StatusCode)
		}
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.fleetURL("group=not-a-uuid"), nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("malformed group id = %d, want 400", resp.StatusCode)
		}
	})
}
