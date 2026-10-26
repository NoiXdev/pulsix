// The item routes' tests (v0.3b-3): discovery reads, decision CRUD and its
// role matrix, the preview route's no-op-on-storage contract, cross-
// organization invisibility, the audit trail, the overrule review focus —
// an organization glob that ignores and a host exact decision that
// watches the same item, with GET /hosts/{id}/items naming which decision
// won — and the controller's own fix: a decision write marks its
// affected host(s) due again immediately, rather than leaving them to a
// natural staleness deadline. Mirrors rule_handlers_test.go's shape for
// the sibling surface it exercises.
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
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type itemDecisionDTO struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	HostID    string    `json:"host_id"`
	CheckKey  string    `json:"check_key"`
	MatchKind string    `json:"match_kind"`
	Pattern   string    `json:"pattern"`
	Verdict   string    `json:"decision"`
	Note      string    `json:"note"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type itemDTO struct {
	HostID    string           `json:"host_id"`
	HostName  string           `json:"host_name"`
	CheckKey  string           `json:"check_key"`
	Item      string           `json:"item"`
	FirstSeen time.Time        `json:"first_seen"`
	LastSeen  *time.Time       `json:"last_seen"`
	Reported  bool             `json:"reported"`
	Verdict   string           `json:"decision"`
	DecidedBy *itemDecisionDTO `json:"decided_by"`
}

type itemPreviewTargetDTO struct {
	HostID   string `json:"host_id"`
	CheckKey string `json:"check_key"`
	Item     string `json:"item"`
}

type itemPreviewDTO struct {
	Matches []itemPreviewTargetDTO `json:"matches"`
	Total   int                    `json:"total"`
	Capped  bool                   `json:"capped"`
}

// itemsFixture is the seeded organization the item routes' tests share: an
// owner, an operator, a viewer and a non-member; two monitored hosts; and
// the "Linux base" seed set (which carries the "disk" and "service" check
// keys the decision bodies below name) every organization is created with.
type itemsFixture struct {
	s                                    *apitest.Server
	orgID                                uuid.UUID
	orgSlug                              string
	ownerC, operatorC, viewerC, stranger *http.Client
	web, dbHost                          uuid.UUID
	// operatorID is who every decision this fixture's operatorC creates is
	// stamped CreatedBy with — exposed so a test can assert on it.
	operatorID uuid.UUID
}

func newItemsFixture(t *testing.T, slug string) itemsFixture {
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser(slug+"-owner@example.test", "correct horse battery")
	operator := s.SeedUser(slug+"-operator@example.test", "correct horse battery")
	viewer := s.SeedUser(slug+"-viewer@example.test", "correct horse battery")
	stranger := s.SeedUser(slug+"-stranger@example.test", "correct horse battery")
	org := s.SeedOrg(slug, owner)
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	scoped := db.WithOrgScope(context.Background(), org)

	zones, err := s.Deps.Hosts.ListZones(scoped, org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	web, err := s.Deps.Hosts.CreateHost(scoped, org, hosts.HostInput{
		Name: "web-1", Address: "10.0.11.1", ZoneID: zones[0].ID, Managed: true, Monitored: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	dbHost, err := s.Deps.Hosts.CreateHost(scoped, org, hosts.HostInput{
		Name: "db-1", Address: "10.0.11.2", ZoneID: zones[0].ID, Managed: true, Monitored: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	return itemsFixture{
		s: s, orgID: org, orgSlug: slug,
		ownerC: s.LoginAs(owner), operatorC: s.LoginAs(operator), viewerC: s.LoginAs(viewer),
		stranger: s.LoginAs(stranger), web: web.ID, dbHost: dbHost.ID, operatorID: operator,
	}
}

func (f itemsFixture) decisionsURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/item-decisions"
}

func (f itemsFixture) itemsURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/items"
}

func (f itemsFixture) hostURL(id uuid.UUID) string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/hosts/" + id.String()
}

func (f itemsFixture) activityURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/activity?limit=200"
}

// discover seeds hostID with items directly through the discovery store —
// the same shape checksFixture.record uses to seed observations through
// s.Checks instead of a second, parallel ingest path. items.Service
// carries no Discover (only the evaluation ticker calls it), so the
// fixture builds its own store on the shared test database.
func (f itemsFixture) discover(t *testing.T, hostID uuid.UUID, checkKey string, names ...string) {
	t.Helper()
	store := items.NewSQLStore(f.s.DB)
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	seen := make([]items.Seen, len(names))
	for i, n := range names {
		seen[i] = items.Seen{CheckKey: checkKey, Name: n}
	}
	if err := store.Discover(scoped, f.orgID, hostID, seen); err != nil {
		t.Fatalf("discover: %v", err)
	}
}

func (f itemsFixture) createDecision(t *testing.T, c *http.Client, body map[string]any) itemDecisionDTO {
	t.Helper()
	resp := postJSON(t, c, f.decisionsURL(), body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create decision = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Decision itemDecisionDTO `json:"decision"`
	}
	decodeInto(t, resp, &out)
	return out.Decision
}

func (f itemsFixture) listDecisions(t *testing.T, c *http.Client) []itemDecisionDTO {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, f.decisionsURL(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list decisions = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Decisions []itemDecisionDTO `json:"decisions"`
	}
	decodeInto(t, resp, &out)
	return out.Decisions
}

func (f itemsFixture) hostItems(t *testing.T, c *http.Client, hostID uuid.UUID) []itemDTO {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, f.hostURL(hostID)+"/items", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("host items = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Items []itemDTO `json:"items"`
	}
	decodeInto(t, resp, &out)
	return out.Items
}

func (f itemsFixture) orgItems(t *testing.T, c *http.Client, query string) []itemDTO {
	t.Helper()
	url := f.itemsURL()
	if query != "" {
		url += "?" + query
	}
	resp := requestJSON(t, c, http.MethodGet, url, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list items = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Items []itemDTO `json:"items"`
	}
	decodeInto(t, resp, &out)
	return out.Items
}

// cursorDirtyAt reads check_eval_cursor.dirty_at for hostID directly, the
// only way to observe the controller's own fix (markDirtyForDecision,
// item_handlers.go) from outside: a nil time.Time means no cursor row
// exists at all (never ingested into, never evaluated).
func (f itemsFixture) cursorDirtyAt(t *testing.T, hostID uuid.UUID) *time.Time {
	t.Helper()
	var dirtyAt time.Time
	err := f.s.DB.Pool().QueryRow(context.Background(),
		`SELECT dirty_at FROM check_eval_cursor WHERE host_id = $1`, hostID).Scan(&dirtyAt)
	if err != nil {
		return nil
	}
	return &dirtyAt
}

func byHostDecisionBody(hostID uuid.UUID, checkKey, pattern, verdict string) map[string]any {
	return map[string]any{
		"scope": "host", "host_id": hostID.String(), "check_key": checkKey,
		"match_kind": "exact", "pattern": pattern, "decision": verdict,
	}
}

func orgGlobDecisionBody(checkKey, pattern, verdict string) map[string]any {
	return map[string]any{
		"scope": "org", "check_key": checkKey,
		"match_kind": "glob", "pattern": pattern, "decision": verdict,
	}
}

func TestItemDecisionRoutes(t *testing.T) {
	f := newItemsFixture(t, "items")

	// Who may do what: a viewer reads, an operator writes, a non-member
	// sees nothing — checks.read / checks.write, the same bar rules and
	// check definitions clear.
	t.Run("role matrix", func(t *testing.T) {
		created := f.createDecision(t, f.operatorC, byHostDecisionBody(f.web, "disk", "/var", "ignore"))
		t.Cleanup(func() { requestJSON(t, f.operatorC, http.MethodDelete, f.decisionsURL()+"/"+created.ID, nil) })

		body := byHostDecisionBody(f.web, "disk", "/tmp", "ignore")
		for _, c := range []struct {
			name           string
			method, url    string
			body           map[string]any
			viewer, member int
		}{
			{"list", http.MethodGet, f.decisionsURL(), nil, http.StatusOK, http.StatusForbidden},
			{"get", http.MethodGet, f.decisionsURL() + "/" + created.ID, nil, http.StatusOK, http.StatusForbidden},
			{"create", http.MethodPost, f.decisionsURL(), body, http.StatusForbidden, http.StatusForbidden},
			{"update", http.MethodPut, f.decisionsURL() + "/" + created.ID, body, http.StatusForbidden, http.StatusForbidden},
			{"delete", http.MethodDelete, f.decisionsURL() + "/" + created.ID, nil, http.StatusForbidden, http.StatusForbidden},
			{"preview", http.MethodPost, f.decisionsURL() + "/preview", body, http.StatusOK, http.StatusForbidden},
			{"host items", http.MethodGet, f.hostURL(f.web) + "/items", nil, http.StatusOK, http.StatusForbidden},
			{"org items", http.MethodGet, f.itemsURL(), nil, http.StatusOK, http.StatusForbidden},
		} {
			if resp := requestJSON(t, f.viewerC, c.method, c.url, c.body); resp.StatusCode != c.viewer {
				t.Errorf("viewer %s = %d, want %d", c.name, resp.StatusCode, c.viewer)
			}
			if resp := requestJSON(t, f.stranger, c.method, c.url, c.body); resp.StatusCode != c.member {
				t.Errorf("non-member %s = %d, want %d", c.name, resp.StatusCode, c.member)
			}
		}
	})

	// Create, read back, replace, delete, and the 404 that follows.
	t.Run("lifecycle", func(t *testing.T) {
		created := f.createDecision(t, f.operatorC, byHostDecisionBody(f.web, "disk", "/var", "ignore"))
		if created.Scope != "host" || created.HostID != f.web.String() || created.CheckKey != "disk" ||
			created.MatchKind != "exact" || created.Pattern != "/var" || created.Verdict != "ignore" {
			t.Fatalf("created = %+v, want the submitted fields echoed back", created)
		}
		// created_by names the operator who made the call — the handler's
		// own fix: items.Service.CreateDecision now takes the acting
		// principal, the same shape schedules/triggers/maintenance already
		// use, rather than leaving the column permanently null.
		if created.CreatedBy != f.operatorID.String() {
			t.Fatalf("created.created_by = %q, want the operator %q", created.CreatedBy, f.operatorID)
		}

		resp := requestJSON(t, f.viewerC, http.MethodGet, f.decisionsURL()+"/"+created.ID, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("viewer get = %d", resp.StatusCode)
		}
		var single struct {
			Decision itemDecisionDTO `json:"decision"`
		}
		decodeInto(t, resp, &single)
		if single.Decision.Pattern != "/var" {
			t.Fatalf("get = %+v", single.Decision)
		}

		resp = requestJSON(t, f.operatorC, http.MethodPut, f.decisionsURL()+"/"+created.ID,
			byHostDecisionBody(f.web, "disk", "/var", "watch"))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		decodeInto(t, resp, &single)
		if single.Decision.CreatedBy != f.operatorID.String() {
			t.Fatalf("update changed created_by: got %q, want it preserved as %q", single.Decision.CreatedBy, f.operatorID)
		}
		if single.Decision.Verdict != "watch" {
			t.Fatalf("updated = %+v, want verdict watch", single.Decision)
		}

		var listed *itemDecisionDTO
		for _, d := range f.listDecisions(t, f.viewerC) {
			if d.ID == created.ID {
				listed = &d
			}
		}
		if listed == nil || listed.Verdict != "watch" {
			t.Fatalf("listed = %+v, want verdict watch", listed)
		}

		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.decisionsURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("delete = %d", resp.StatusCode)
		}
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.decisionsURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("get after delete = %d, want 404", resp.StatusCode)
		}
		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.decisionsURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("delete twice = %d, want 404", resp.StatusCode)
		}
	})

	// internal/items.Service.buildDecision's own validation failures,
	// each refused with 422 and a message naming the failure — exactly the
	// shape checkDefinitionBody and ruleBody document for their own
	// routes: one validator, one message, not a second 422 with huma's own
	// generic wording layered on top.
	t.Run("validation failures answer 422", func(t *testing.T) {
		cases := map[string]map[string]any{
			"org scope must not set a host": {
				"scope": "org", "host_id": f.web.String(), "check_key": "disk",
				"match_kind": "glob", "pattern": "/mnt/*", "decision": "ignore",
			},
			"host scope must set a host": {
				"scope": "host", "check_key": "disk",
				"match_kind": "exact", "pattern": "/var", "decision": "ignore",
			},
			"unknown check key": {
				"scope": "org", "check_key": "no-such-check",
				"match_kind": "glob", "pattern": "*", "decision": "ignore",
			},
			"exact pattern must not contain a star": {
				"scope": "org", "check_key": "disk",
				"match_kind": "exact", "pattern": "/mnt/*", "decision": "ignore",
			},
			"empty pattern": {
				"scope": "org", "check_key": "disk",
				"match_kind": "glob", "pattern": "", "decision": "ignore",
			},
			"bad verdict": {
				"scope": "org", "check_key": "disk",
				"match_kind": "glob", "pattern": "*", "decision": "maybe",
			},
			"organization-wide watch has no effect": {
				"scope": "org", "check_key": "disk",
				"match_kind": "glob", "pattern": "*", "decision": "watch",
			},
		}
		for name, body := range cases {
			t.Run(name, func(t *testing.T) {
				resp := postJSON(t, f.operatorC, f.decisionsURL(), body)
				if resp.StatusCode != http.StatusUnprocessableEntity {
					t.Fatalf("%s = %d, want 422: %s", name, resp.StatusCode, mustReadBody(t, resp))
				}
			})
		}

		// Existence before deep validation, exactly like rule_handlers.go's
		// own PUT: a malformed body against an id that does not exist at all
		// answers the uniform 404, not 422.
		resp := requestJSON(t, f.operatorC, http.MethodPut, f.decisionsURL()+"/"+uuid.Must(uuid.NewV7()).String(),
			map[string]any{"scope": "org", "check_key": "no-such-check", "match_kind": "glob", "pattern": "*", "decision": "ignore"})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("update of a nonexistent id with a bad body = %d, want 404 %s", resp.StatusCode, mustReadBody(t, resp))
		}
	})

	// POST /item-decisions/preview resolves a pattern against discovered
	// items without ever calling Create — a test proves it by listing
	// decisions right after, not by reading Preview's own source.
	t.Run("preview persists nothing and needs no verdict", func(t *testing.T) {
		f.discover(t, f.web, "disk", "/var", "/tmp")
		before := f.listDecisions(t, f.viewerC)

		resp := postJSON(t, f.operatorC, f.decisionsURL()+"/preview", map[string]any{
			"scope": "host", "host_id": f.web.String(), "check_key": "disk",
			"match_kind": "glob", "pattern": "/*",
			// No "decision" field at all: Preview validates only scope, host,
			// check key, match kind and pattern — the GUI must be able to show
			// a match count before an operator has chosen ignore or watch.
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("preview = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var preview itemPreviewDTO
		decodeInto(t, resp, &preview)
		if preview.Total != 2 || len(preview.Matches) != 2 || preview.Capped {
			t.Fatalf("preview = %+v, want 2 uncapped matches (/var and /tmp)", preview)
		}

		after := f.listDecisions(t, f.viewerC)
		if len(after) != len(before) {
			t.Fatalf("preview persisted something: before %d, after %d", len(before), len(after))
		}
	})

	// A decision of another organization is invisible, not merely refused:
	// its id answers 404 inside an organization the caller belongs to, 403
	// inside the one it actually lives in.
	t.Run("cross-organization invisibility", func(t *testing.T) {
		otherOwner := f.s.SeedUser("items-other-owner@example.test", "correct horse battery")
		f.s.SeedOrg("items-other", otherOwner)
		otherC := f.s.LoginAs(otherOwner)
		otherURL := f.s.HTTP.URL + "/api/v1/orgs/items-other/item-decisions"

		resp := postJSON(t, otherC, otherURL, orgGlobDecisionBody("disk", "/mnt/*", "ignore"))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create in the other org = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var created struct {
			Decision itemDecisionDTO `json:"decision"`
		}
		decodeInto(t, resp, &created)

		update := orgGlobDecisionBody("disk", "/mnt/*", "watch")
		for _, c := range []struct {
			method string
			body   map[string]any
		}{{http.MethodGet, nil}, {http.MethodPut, update}, {http.MethodDelete, nil}} {
			if resp := requestJSON(t, f.operatorC, c.method, f.decisionsURL()+"/"+created.Decision.ID, c.body); resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s of a foreign id in my own org = %d, want 404", c.method, resp.StatusCode)
			}
			if resp := requestJSON(t, f.operatorC, c.method, otherURL+"/"+created.Decision.ID, c.body); resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s in an org I am not a member of = %d, want 403", c.method, resp.StatusCode)
			}
		}

		// GET /hosts/{id}/items: a host id belonging to a different
		// organization, requested from an org the caller IS a member of,
		// answers the uniform 404 (mapChecksErr via deps.Hosts.GetHost) —
		// the same "invisible, not merely refused" contract every other
		// host-scoped route in this codebase proves for a foreign host id
		// (see e.g. TestRuleRoutes' own effective-rules case).
		otherHostOrg := f.s.SeedOrg("items-other-host", otherOwner)
		otherHostScoped := db.WithOrgScope(context.Background(), otherHostOrg)
		otherHostZones, err := f.s.Deps.Hosts.ListZones(otherHostScoped, otherHostOrg)
		if err != nil || len(otherHostZones) == 0 {
			t.Fatalf("zones for the foreign-host org: %v", err)
		}
		otherHost, err := f.s.Deps.Hosts.CreateHost(otherHostScoped, otherHostOrg, hosts.HostInput{
			Name: "foreign-host", Address: "10.0.9.98", ZoneID: otherHostZones[0].ID, Managed: true, Monitored: true,
		})
		if err != nil {
			t.Fatalf("create foreign host: %v", err)
		}
		if resp := requestJSON(t, f.operatorC, http.MethodGet, f.hostURL(otherHost.ID)+"/items", nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("host items for a foreign host id in my own org = %d, want 404 %s", resp.StatusCode, mustReadBody(t, resp))
		}

		// POST /item-decisions/preview is host-addressed too whenever its
		// body is host-scoped, and must answer the same 404 for the same
		// id. It used to answer 200 with an empty match list (nothing
		// leaked — the store filters on org_id) while the sibling create
		// path refused the very same body with 422: one question, three
		// answers.
		if resp := postJSON(t, f.operatorC, f.decisionsURL()+"/preview",
			byHostDecisionBody(otherHost.ID, "disk", "/var", "ignore")); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("preview against a foreign host id = %d, want 404 %s", resp.StatusCode, mustReadBody(t, resp))
		}

		// GET /items' own `item` glob reaches a LIKE scan over
		// check_items, so it clears the same ValidPattern bound every
		// write path's pattern clears — an unbounded alternating-'%x'
		// pattern is otherwise an authenticated way to make a read
		// expensive.
		overLong := strings.Repeat("%x", items.MaxPatternLen)
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.itemsURL()+"?item="+url.QueryEscape(overLong), nil); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("org items with an over-long glob = %d, want 422 %s", resp.StatusCode, mustReadBody(t, resp))
		}

		// GET /items: not merely refused to a non-member (role matrix
		// already proves that) but genuinely blind to another
		// organization's items — an item discovered in "items-other-host"
		// must never appear in my own organization's listing, even one
		// that shares an item name with something of mine.
		f.discover(t, f.web, "disk", "/cross-org-canary")
		foreignItems := itemsFixture{s: f.s, orgID: otherHostOrg, orgSlug: "items-other-host"}
		foreignItems.discover(t, otherHost.ID, "disk", "/cross-org-canary")
		for _, it := range f.orgItems(t, f.viewerC, "") {
			if it.HostID == otherHost.ID.String() {
				t.Fatalf("GET /items leaked a foreign organization's item: %+v", it)
			}
		}
	})

	// Create, update and delete each leave one attributable entry in the
	// organization's activity trail, naming the scope, pattern and verdict.
	// Preview leaves none: it is allowlisted, not audited.
	t.Run("mutations are audited with scope, pattern and decision; preview leaves no trail", func(t *testing.T) {
		created := f.createDecision(t, f.operatorC, byHostDecisionBody(f.dbHost, "service", "sshd", "ignore"))
		if resp := requestJSON(t, f.operatorC, http.MethodPut, f.decisionsURL()+"/"+created.ID,
			byHostDecisionBody(f.dbHost, "service", "sshd", "watch")); resp.StatusCode != http.StatusOK {
			t.Fatalf("update = %d", resp.StatusCode)
		}
		if resp := postJSON(t, f.operatorC, f.decisionsURL()+"/preview",
			byHostDecisionBody(f.dbHost, "service", "sshd", "watch")); resp.StatusCode != http.StatusOK {
			t.Fatalf("preview = %d", resp.StatusCode)
		}
		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.decisionsURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("delete = %d", resp.StatusCode)
		}

		resp := requestJSON(t, f.ownerC, http.MethodGet, f.activityURL(), nil)
		actions := map[string]int{}
		var summaries []string
		for _, ev := range decodeActivity(t, resp).Items {
			if ev.Action == "POST previewItemDecision" {
				t.Fatalf("preview recorded an audit event %+v, want none: it is allowlisted, not audited", ev)
			}
			if ev.ObjectID != created.ID {
				continue
			}
			actions[ev.Action]++
			summaries = append(summaries, ev.Summary)
		}
		for _, action := range []string{"POST createItemDecision", "PUT updateItemDecision", "DELETE deleteItemDecision"} {
			if actions[action] != 1 {
				t.Fatalf("action %q recorded %d times, want 1; trail = %+v", action, actions[action], actions)
			}
		}
		for _, s := range summaries {
			if !strings.Contains(s, "sshd") || !strings.Contains(s, "service") || !strings.Contains(s, "host") {
				t.Fatalf("summary %q does not name scope, pattern and check", s)
			}
		}
	})

}

// TestItemDecisionMarksHostsDirty is the controller's own fix, proved on a
// fixture of its own: earlier subtests in TestItemDecisionRoutes share one
// organization and already dirty f.web through their own decisions, which
// would make "no cursor row yet" a false assumption there. A decision
// write must make its affected host(s) due again immediately — a
// host-scoped decision dirties just that host; an organization-scoped one
// dirties every host in the organization — rather than leaving them to
// wait out their own staleness deadline (see markDirtyForDecision's own
// doc comment, item_handlers.go, and EvalDirtyMarker's for why this is the
// controller's fix, not the original spec's).
func TestItemDecisionMarksHostsDirty(t *testing.T) {
	f := newItemsFixture(t, "items-dirty")

	if before := f.cursorDirtyAt(t, f.web); before != nil {
		t.Fatalf("web already has a cursor row before any decision touches it: %v", *before)
	}
	if before := f.cursorDirtyAt(t, f.dbHost); before != nil {
		t.Fatalf("db-1 already has a cursor row before any decision touches it: %v", *before)
	}

	hostScoped := f.createDecision(t, f.operatorC, byHostDecisionBody(f.web, "disk", "/opt", "ignore"))
	if f.cursorDirtyAt(t, f.web) == nil {
		t.Fatal("host-scoped decision create did not mark web dirty")
	}
	if f.cursorDirtyAt(t, f.dbHost) != nil {
		t.Fatal("host-scoped decision for web marked db-1 dirty too")
	}

	// db-1's cursor did not already exist (confirmed above), so a fresh
	// org-scoped decision dirtying it is only explained by the
	// organization-wide branch of markDirtyForDecision.
	f.createDecision(t, f.operatorC, orgGlobDecisionBody("service", "ssh*", "ignore"))
	if f.cursorDirtyAt(t, f.dbHost) == nil {
		t.Fatal("org-scoped decision create did not mark db-1 dirty")
	}
	if f.cursorDirtyAt(t, f.web) == nil {
		t.Fatal("org-scoped decision create did not mark web dirty")
	}

	// An update that MOVES a decision from one host to another marks
	// BOTH: the host it stopped applying to is owed a re-judgement every
	// bit as much as the one it now applies to, which is why the PUT
	// handler calls markDirtyForDecision for the pre-update row as well
	// as the post-update one (item_handlers.go, and the spec's own "As
	// built" calls it out by name). Only create was covered before.
	moved := f.createDecision(t, f.operatorC, byHostDecisionBody(f.web, "disk", "/moved", "ignore"))
	webBefore, dbBefore := f.cursorDirtyAt(t, f.web), f.cursorDirtyAt(t, f.dbHost)
	if webBefore == nil || dbBefore == nil {
		t.Fatalf("both cursor rows should exist by now: web=%v db-1=%v", webBefore, dbBefore)
	}
	// A real gap, so "advanced" is not two now() calls landing in the
	// same microsecond.
	time.Sleep(10 * time.Millisecond)
	if resp := requestJSON(t, f.operatorC, http.MethodPut, f.decisionsURL()+"/"+moved.ID,
		byHostDecisionBody(f.dbHost, "disk", "/moved", "ignore")); resp.StatusCode != http.StatusOK {
		t.Fatalf("move update = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	webAfter, dbAfter := f.cursorDirtyAt(t, f.web), f.cursorDirtyAt(t, f.dbHost)
	if webAfter == nil || !webAfter.After(*webBefore) {
		t.Fatalf("moving a decision AWAY from web did not mark web dirty again: %v -> %v", webBefore, webAfter)
	}
	if dbAfter == nil || !dbAfter.After(*dbBefore) {
		t.Fatalf("moving a decision ONTO db-1 did not mark db-1 dirty: %v -> %v", dbBefore, dbAfter)
	}
	if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.decisionsURL()+"/"+moved.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete the moved decision = %d", resp.StatusCode)
	}

	if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.decisionsURL()+"/"+hostScoped.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
}

// TestHostAndOrgItemsReportDiscoveredItems covers the two read routes:
// first_seen, last_seen (derived from check_current, absent when nothing
// has reported), reported, and the organization-wide listing's check/item
// filters and host naming.
func TestHostAndOrgItemsReportDiscoveredItems(t *testing.T) {
	f := newItemsFixture(t, "items-reads")
	f.discover(t, f.web, "disk", "/var", "/tmp")
	f.discover(t, f.dbHost, "disk", "/var")

	t.Run("host items names check, item, first_seen and reported", func(t *testing.T) {
		list := f.hostItems(t, f.viewerC, f.web)
		if len(list) != 2 {
			t.Fatalf("web items = %+v, want 2", list)
		}
		names := map[string]bool{}
		for _, it := range list {
			names[it.Item] = true
			if it.CheckKey != "disk" || it.FirstSeen.IsZero() {
				t.Fatalf("item = %+v, want check_key disk and a first_seen", it)
			}
			if it.Reported {
				t.Fatalf("item = %+v, want reported=false: nothing has recorded a current value for it", it)
			}
			if it.LastSeen != nil {
				t.Fatalf("item = %+v, want no last_seen: reported is false", it)
			}
			if it.Verdict != "watch" || it.DecidedBy != nil {
				t.Fatalf("item = %+v, want the default verdict watch with no deciding decision", it)
			}
			if it.HostID != "" {
				t.Fatalf("item = %+v, want no host_id on the per-host route", it)
			}
		}
		// Cross-host isolation: db-1 discovered its own "/var" too (on
		// purpose — the next subtest's glob filter relies on two hosts
		// sharing a name), so the isolation proof is not "db-1's item is
		// absent by name" but "web's own two names, exactly, and nothing
		// else" — a leak would either add a third name or change this set,
		// and len(list) == 2 above already rules out a leaked extra row.
		if !names["/var"] || !names["/tmp"] || len(names) != 2 {
			t.Fatalf("web item names = %v, want exactly {/var, /tmp}", names)
		}
	})

	t.Run("organization items names the host and filters by check and item glob", func(t *testing.T) {
		all := f.orgItems(t, f.viewerC, "")
		if len(all) != 3 {
			t.Fatalf("org items = %+v, want 3 (2 on web, 1 on db-1)", all)
		}
		for _, it := range all {
			if it.HostID == "" {
				t.Fatalf("item = %+v, want host_id set on the organization-wide listing", it)
			}
		}

		byCheck := f.orgItems(t, f.viewerC, "check=disk")
		if len(byCheck) != 3 {
			t.Fatalf("check=disk = %+v, want all 3", byCheck)
		}

		byGlob := f.orgItems(t, f.viewerC, "item="+"%2Fvar")
		if len(byGlob) != 2 {
			t.Fatalf("item=/var = %+v, want the 2 hosts reporting /var", byGlob)
		}

		byGlobTmp := f.orgItems(t, f.viewerC, "item="+"%2Ftmp")
		if len(byGlobTmp) != 1 {
			t.Fatalf("item=/tmp = %+v, want only web's /tmp", byGlobTmp)
		}
	})
}

// TestHostDecisionOverrulesOrgPattern is the milestone's own named review
// focus at the API level: an organization glob that ignores a pattern and
// a host-scoped exact decision that watches the very item it would have
// caught. The host decision must win — host beats org regardless of
// match kind — and GET /hosts/{id}/items must name it as the one that
// decided, not merely report the right verdict.
func TestHostDecisionOverrulesOrgPattern(t *testing.T) {
	f := newItemsFixture(t, "items-overrule")
	f.discover(t, f.web, "disk", "/mnt/scratch-db")

	f.createDecision(t, f.operatorC, orgGlobDecisionBody("disk", "/mnt/scratch-*", "ignore"))
	hostWatch := f.createDecision(t, f.operatorC, byHostDecisionBody(f.web, "disk", "/mnt/scratch-db", "watch"))

	list := f.hostItems(t, f.viewerC, f.web)
	var found *itemDTO
	for i := range list {
		if list[i].Item == "/mnt/scratch-db" {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("items = %+v, want /mnt/scratch-db", list)
	}
	if found.Verdict != "watch" {
		t.Fatalf("verdict = %q, want watch: the host-scoped decision must overrule the organization glob", found.Verdict)
	}
	if found.DecidedBy == nil {
		t.Fatal("decided_by is absent, want the host decision named")
	}
	if found.DecidedBy.ID != hostWatch.ID || found.DecidedBy.Scope != "host" {
		t.Fatalf("decided_by = %+v, want the host-scoped watch decision %q named, not the organization glob", found.DecidedBy, hostWatch.ID)
	}
}

// TestHostChecksReportsIgnoredAdditively covers host_check_handlers.go's
// own v0.3b-3 addition: GET /hosts/{id}/checks gains `ignored` per tuple
// without disturbing any existing field. It also pins the distinction the
// design spec calls out by name: an ignored item's state row is deleted
// (Apply's own contract), so a null `state` alone cannot tell "ignored"
// apart from "never evaluated" — only `ignored` can.
func TestHostChecksReportsIgnoredAdditively(t *testing.T) {
	f := newItemsFixture(t, "items-ignored-additive")
	ignoreVar := f.createDecision(t, f.operatorC, byHostDecisionBody(f.web, "disk", "/var", "ignore"))

	scoped := db.WithOrgScope(context.Background(), f.orgID)
	res, err := f.s.Checks.RecordObservations(scoped, f.orgID, []checks.Observation{
		{HostID: f.web, CheckKey: "disk", Item: "/var", Metric: "used_percent", At: time.Now(), Value: floatPtr(61)},
		{HostID: f.web, CheckKey: "disk", Item: "/opt", Metric: "used_percent", At: time.Now(), Value: floatPtr(20)},
	})
	if err != nil || res.Rejected() != 0 {
		t.Fatalf("record observations: %+v, err %v", res, err)
	}

	resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/checks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("host checks = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var body hostChecksBody
	decodeInto(t, resp, &body)
	disk := body.byKey(t, "disk")
	var varValue, optValue *hostCheckValue
	for i := range disk.Values {
		switch disk.Values[i].Item {
		case "/var":
			varValue = &disk.Values[i]
		case "/opt":
			optValue = &disk.Values[i]
		}
	}
	if varValue == nil || !varValue.Ignored {
		t.Fatalf("/var = %+v, want ignored=true", varValue)
	}
	if optValue == nil || optValue.Ignored {
		t.Fatalf("/opt = %+v, want ignored=false: no decision names it", optValue)
	}

	// decided_by rides with `ignored`, out of the same items.Decide
	// resolution — and note what this fixture does NOT do: nothing has
	// discovered /var, so check_items has no row for it and
	// GET /hosts/{id}/items would report nothing about it at all. That is
	// precisely the window in which the host row's own Ignore/Watch
	// button used to know it was ignored but not by what, and issued a
	// second POST that lost the older-created_at tie-break to the
	// decision already there — a dead row, no error, the verdict
	// unchanged. The button reads this field now, so the two can never
	// disagree.
	if varValue.DecidedBy == nil {
		t.Fatal("/var decided_by is absent, want the decision that ignored it named")
	}
	if varValue.DecidedBy.ID != ignoreVar.ID || varValue.DecidedBy.Scope != "host" ||
		varValue.DecidedBy.MatchKind != "exact" || varValue.DecidedBy.Pattern != "/var" {
		t.Fatalf("/var decided_by = %+v, want the host-scoped exact decision %q", varValue.DecidedBy, ignoreVar.ID)
	}
	if optValue.DecidedBy != nil {
		t.Fatalf("/opt decided_by = %+v, want absent: no decision names it", optValue.DecidedBy)
	}
}

// TestHostChecksReportsIgnoredForAnItemlessMetric is backlog 3.45. Two
// paths answer the same question — does an item decision currently keep
// this tuple from being judged — and they disagreed for a metric with no
// item. items.Match("*", "") is true, so a host-scoped glob over
// check_key cpu genuinely drops cpu.used_pct from judgement, and the
// evaluation path (internal/rules.resolveIgnored) has always run Decide
// over the empty-item tuples for that reason. The read path skipped them
// outright, on a doc comment that asserted they had "nothing to ignore".
//
// The effect was the quiet kind: an operator who silenced cpu on a host
// really did silence it, and the host page's badge could not say so —
// the same invisible-until-you-look-elsewhere gap 3.44 closed one level
// up, still open at the tuple level.
func TestHostChecksReportsIgnoredForAnItemlessMetric(t *testing.T) {
	f := newItemsFixture(t, "items-ignored-itemless")
	// A glob over an itemless check. "*" is the pattern an operator
	// reaches for to mean "this whole check on this host", and it is
	// exactly what the evaluation path already honours.
	ignoreCPU := f.createDecision(t, f.operatorC, map[string]any{
		"scope": "host", "host_id": f.web.String(), "check_key": "cpu",
		"match_kind": "glob", "pattern": "*", "decision": "ignore",
	})

	scoped := db.WithOrgScope(context.Background(), f.orgID)
	res, err := f.s.Checks.RecordObservations(scoped, f.orgID, []checks.Observation{
		{HostID: f.web, CheckKey: "cpu", Metric: "used_percent", At: time.Now(), Value: floatPtr(42)},
		{HostID: f.web, CheckKey: "disk", Item: "/opt", Metric: "used_percent", At: time.Now(), Value: floatPtr(20)},
	})
	if err != nil || res.Rejected() != 0 {
		t.Fatalf("record observations: %+v, err %v", res, err)
	}

	var body hostChecksBody
	decodeInto(t, requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/checks", nil), &body)

	cpu := body.byKey(t, "cpu")
	var used *hostCheckValue
	for i := range cpu.Values {
		if cpu.Values[i].Item == "" && cpu.Values[i].Metric == "used_percent" {
			used = &cpu.Values[i]
		}
	}
	if used == nil {
		t.Fatalf("cpu.used_percent is missing from %+v", cpu.Values)
	}
	if !used.Ignored {
		t.Fatalf("cpu.used_percent = %+v, want ignored=true: the glob silences it, and the evaluator already agrees", used)
	}
	// decided_by rides with it, out of the same resolution, so the row's
	// own Ignore/Watch button knows which decision it would be flipping.
	if used.DecidedBy == nil || used.DecidedBy.ID != ignoreCPU.ID || used.DecidedBy.MatchKind != "glob" {
		t.Fatalf("cpu.used_percent decided_by = %+v, want the host-scoped glob %q", used.DecidedBy, ignoreCPU.ID)
	}

	// The glob names cpu, so nothing else moves — a change that reported
	// every itemless tuple as ignored would pass the assertions above.
	disk := body.byKey(t, "disk")
	for _, v := range disk.Values {
		if v.Ignored || v.DecidedBy != nil {
			t.Fatalf("disk %s/%s = %+v, want untouched by a cpu decision", v.Item, v.Metric, v)
		}
	}
}
