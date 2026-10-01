// The rule routes' tests (v0.3b-2): the CRUD surface and its role matrix,
// the metric catalogue, the position a malformed expression carries, the
// validate route's own no-op-on-storage contract, cross-organization
// invisibility, the audit trail (name and metric, never the source text)
// and the effective view's precedence across org, group and host scopes —
// all driven through the full API against the test database, mirroring
// check_handlers_test.go's own shape for the sibling surface it exercises.
package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/rules"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type ruleFormBody struct {
	Metric     string   `json:"metric"`
	Op         string   `json:"op"`
	Value      float64  `json:"value,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	ClearOp    string   `json:"clear_op,omitempty"`
	ClearValue float64  `json:"clear_value,omitempty"`
}

type ruleDTO struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Enabled    bool          `json:"enabled"`
	Origin     string        `json:"origin"`
	Scope      string        `json:"scope"`
	GroupID    string        `json:"group_id"`
	GroupName  string        `json:"group_name"`
	HostID     string        `json:"host_id"`
	HostName   string        `json:"host_name"`
	Item       string        `json:"item"`
	CheckKey   string        `json:"check_key"`
	Metric     string        `json:"metric"`
	Severity   string        `json:"severity"`
	Mode       string        `json:"mode"`
	Form       *ruleFormBody `json:"form"`
	Expression string        `json:"expression"`
	Source     string        `json:"source"`
	ForSeconds int           `json:"for_seconds"`
}

type ruleCheckedBody struct {
	OK        bool   `json:"ok"`
	Pos       int    `json:"pos"`
	Message   string `json:"message"`
	FormReady bool   `json:"form_ready"`
	Source    string `json:"source"`
}

type ruleMetricBody struct {
	Name     string `json:"name"`
	CheckKey string `json:"check_key"`
	Kind     string `json:"kind"`
}

type effectiveRuleBody struct {
	CheckKey string    `json:"check_key"`
	Item     string    `json:"item"`
	Metric   string    `json:"metric"`
	Severity string    `json:"severity"`
	Winner   ruleDTO   `json:"winner"`
	Shadowed []ruleDTO `json:"shadowed"`
	Observed bool      `json:"observed"`
}

// rulesFixture is the seeded organization the rule routes' tests share: an
// owner, an operator, a viewer and a non-member; one monitored host tagged
// role=web so a monitoring group's selector has something to match; and the
// five-row starter rule set every organization is created with.
type rulesFixture struct {
	s         *apitest.Server
	orgID     uuid.UUID
	orgSlug   string
	ownerC    *http.Client
	operatorC *http.Client
	viewerC   *http.Client
	stranger  *http.Client
	host      uuid.UUID
}

func newRulesFixture(t *testing.T, slug string) rulesFixture {
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
	h, err := s.Deps.Hosts.CreateHost(scoped, org, hosts.HostInput{
		Name: "web-1", Address: "10.0.9.1", ZoneID: zones[0].ID, Managed: true, Monitored: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Deps.Hosts.SetTags(scoped, org, h.ID, []hosts.Tag{{Key: "role", Value: "web", Source: hosts.TagSourceManual}}); err != nil {
		t.Fatal(err)
	}

	return rulesFixture{
		s: s, orgID: org, orgSlug: slug,
		ownerC: s.LoginAs(owner), operatorC: s.LoginAs(operator), viewerC: s.LoginAs(viewer),
		stranger: s.LoginAs(stranger), host: h.ID,
	}
}

func (f rulesFixture) rulesURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/rules"
}

func (f rulesFixture) hostURL(id uuid.UUID) string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/hosts/" + id.String()
}

func (f rulesFixture) groupsURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/monitoring-groups"
}

func (f rulesFixture) activityURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/activity?limit=200"
}

func (f rulesFixture) listRules(t *testing.T, c *http.Client) []ruleDTO {
	t.Helper()
	return f.listRulesQuery(t, c, "")
}

// listRulesQuery is listRules with a raw query string (e.g. "scope=host",
// "check=memory", "host="+id.String()) appended to GET /rules, for the
// filter tests.
func (f rulesFixture) listRulesQuery(t *testing.T, c *http.Client, query string) []ruleDTO {
	t.Helper()
	url := f.rulesURL()
	if query != "" {
		url += "?" + query
	}
	resp := requestJSON(t, c, http.MethodGet, url, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list rules (%s) = %d %s", query, resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Rules []ruleDTO `json:"rules"`
	}
	decodeInto(t, resp, &out)
	return out.Rules
}

// hasRuleID reports whether list contains a rule with the given id.
func hasRuleID(list []ruleDTO, id string) bool {
	for _, r := range list {
		if r.ID == id {
			return true
		}
	}
	return false
}

// createRule posts body and expects 201, returning the created row.
func (f rulesFixture) createRule(t *testing.T, c *http.Client, body map[string]any) ruleDTO {
	t.Helper()
	resp := postJSON(t, c, f.rulesURL(), body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create rule = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Rule ruleDTO `json:"rule"`
	}
	decodeInto(t, resp, &out)
	return out.Rule
}

// orgFormRuleBody is a valid, org-scoped, form-mode rule body over cpu.load1
// — a metric none of the five seed rules touch, so a test built on it never
// collides with the seed set.
func orgFormRuleBody(name string, op string, value float64) map[string]any {
	return map[string]any{
		"name": name, "scope": "org", "metric": "cpu.load1", "severity": "warn", "mode": "form",
		"form": map[string]any{"metric": "cpu.load1", "op": op, "value": value},
	}
}

func (f rulesFixture) auditActionsFor(t *testing.T, objectType, objectID string) map[string]int {
	t.Helper()
	resp := requestJSON(t, f.ownerC, http.MethodGet, f.activityURL(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activity = %d", resp.StatusCode)
	}
	actions := map[string]int{}
	for _, ev := range decodeActivity(t, resp).Items {
		if ev.ObjectType != objectType || ev.ObjectID != objectID {
			continue
		}
		if ev.Summary == "" {
			t.Fatalf("event %+v has no summary", ev)
		}
		actions[ev.Action]++
	}
	return actions
}

// TestRuleRoutes drives the whole rule surface. Its subtests share one
// fixture and run in order; each uses its own rule names and metrics not
// touched by the seed set, so none depends on another having (or not
// having) run.
func TestRuleRoutes(t *testing.T) {
	f := newRulesFixture(t, "rules")

	// Every organization is created with the five-row starter set
	// (rules.SeedRules), origin "seed", scope org, enabled.
	t.Run("lists the seed set", func(t *testing.T) {
		byName := map[string]ruleDTO{}
		for _, r := range f.listRules(t, f.viewerC) {
			byName[r.Name] = r
		}
		for _, name := range []string{"Disk usage warning", "Disk usage critical", "Memory usage warning", "Memory usage critical", "CPU usage warning"} {
			r, ok := byName[name]
			if !ok {
				t.Fatalf("seed rule %q missing from %+v", name, byName)
			}
			if r.Origin != "seed" || r.Scope != "org" || !r.Enabled || r.Mode != "form" || r.Form == nil {
				t.Fatalf("seed rule %q = %+v, want an enabled, org-scoped, form-mode seed row", name, r)
			}
		}
	})

	// The metric catalogue GET /rules/metrics is the editor's only honest
	// source for its picker — it must answer exactly rules.Catalogue(),
	// readable at the same permission as the rules themselves.
	t.Run("metric catalogue matches rules.Catalogue", func(t *testing.T) {
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.rulesURL()+"/metrics", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("metrics = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var out struct {
			Metrics []ruleMetricBody `json:"metrics"`
		}
		decodeInto(t, resp, &out)
		cat := rules.Catalogue()
		if len(out.Metrics) != len(cat) {
			t.Fatalf("metrics = %d entries, want %d", len(out.Metrics), len(cat))
		}
		byName := map[string]ruleMetricBody{}
		for _, m := range out.Metrics {
			byName[m.Name] = m
		}
		cpu, ok := byName["cpu.used_pct"]
		if !ok || cpu.CheckKey != "cpu" || cpu.Kind != "numeric" {
			t.Fatalf("cpu.used_pct = %+v, want check_key cpu, kind numeric", cpu)
		}
		state, ok := byName["service.state"]
		if !ok || state.CheckKey != "service" || state.Kind != "discrete" {
			t.Fatalf("service.state = %+v, want check_key service, kind discrete", state)
		}
		if resp := requestJSON(t, f.stranger, http.MethodGet, f.rulesURL()+"/metrics", nil); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("non-member metrics = %d, want 403", resp.StatusCode)
		}
	})

	// Who may do what: a viewer reads, an operator writes, a non-member
	// sees nothing — checks.read / checks.write, the same bar the check
	// definitions of v0.3b-1 clear.
	t.Run("role matrix", func(t *testing.T) {
		existing := f.listRules(t, f.viewerC)[0]
		body := orgFormRuleBody("matrix rule", ">", 5)
		for _, c := range []struct {
			name           string
			method, url    string
			body           map[string]any
			viewer, member int
		}{
			{"list", http.MethodGet, f.rulesURL(), nil, http.StatusOK, http.StatusForbidden},
			{"get", http.MethodGet, f.rulesURL() + "/" + existing.ID, nil, http.StatusOK, http.StatusForbidden},
			{"create", http.MethodPost, f.rulesURL(), body, http.StatusForbidden, http.StatusForbidden},
			{"update", http.MethodPut, f.rulesURL() + "/" + existing.ID, body, http.StatusForbidden, http.StatusForbidden},
			{"delete", http.MethodDelete, f.rulesURL() + "/" + existing.ID, nil, http.StatusForbidden, http.StatusForbidden},
			{"validate", http.MethodPost, f.rulesURL() + "/validate", body, http.StatusOK, http.StatusForbidden},
			{"effective", http.MethodGet, f.hostURL(f.host) + "/effective-rules", nil, http.StatusOK, http.StatusForbidden},
		} {
			if resp := requestJSON(t, f.viewerC, c.method, c.url, c.body); resp.StatusCode != c.viewer {
				t.Errorf("viewer %s = %d, want %d", c.name, resp.StatusCode, c.viewer)
			}
			if resp := requestJSON(t, f.stranger, c.method, c.url, c.body); resp.StatusCode != c.member {
				t.Errorf("non-member %s = %d, want %d", c.name, resp.StatusCode, c.member)
			}
		}
		// The same calls an operator makes must succeed, or the matrix
		// above would pass for a route that is simply broken.
		created := f.createRule(t, f.operatorC, body)
		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.rulesURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("operator delete = %d", resp.StatusCode)
		}
	})

	// Create, read back, replace, delete, and the 404 that follows.
	t.Run("lifecycle", func(t *testing.T) {
		created := f.createRule(t, f.operatorC, orgFormRuleBody("lifecycle rule", ">", 5))
		if created.Origin != "user" || created.Scope != "org" || !created.Enabled || created.CheckKey != "cpu" {
			t.Fatalf("created = %+v, want a user, org-scoped, enabled rule with check_key cpu", created)
		}
		if created.Form == nil || created.Form.Op != ">" || created.Form.Value != 5 {
			t.Fatalf("created.form = %+v, want the submitted comparison echoed back", created.Form)
		}
		if created.Source == "" {
			t.Fatalf("created.source is empty, want a rendered summary")
		}

		resp := requestJSON(t, f.viewerC, http.MethodGet, f.rulesURL()+"/"+created.ID, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("viewer get = %d", resp.StatusCode)
		}
		var single struct {
			Rule ruleDTO `json:"rule"`
		}
		decodeInto(t, resp, &single)
		if single.Rule.Name != "lifecycle rule" {
			t.Fatalf("get = %+v", single.Rule)
		}

		resp = requestJSON(t, f.operatorC, http.MethodPut, f.rulesURL()+"/"+created.ID, orgFormRuleBody("lifecycle rule", ">", 8))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		decodeInto(t, resp, &single)
		if single.Rule.Form == nil || single.Rule.Form.Value != 8 {
			t.Fatalf("updated = %+v, want form value 8", single.Rule)
		}

		var listed *ruleDTO
		for _, r := range f.listRules(t, f.viewerC) {
			if r.ID == created.ID {
				listed = &r
			}
		}
		if listed == nil || listed.Form == nil || listed.Form.Value != 8 {
			t.Fatalf("listed = %+v, want form value 8", listed)
		}

		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.rulesURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("delete = %d", resp.StatusCode)
		}
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.rulesURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("get after delete = %d, want 404", resp.StatusCode)
		}
		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.rulesURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("delete twice = %d, want 404", resp.StatusCode)
		}
	})

	// GET /rules?scope=&check=&host= — each of the three filters is
	// exercised against a fixture with one rule that must be included and
	// (at least) one that must be excluded, so a wrong parameter name or a
	// mis-wired field in toRuleFilter fails loudly rather than passing
	// because the excluded rule was simply never checked for.
	t.Run("list filters by scope, check and host", func(t *testing.T) {
		// org-scoped, check_key cpu (cpu.load1) — must be excluded by
		// scope=host and by check=memory and by host=<f.host>.
		orgRule := f.createRule(t, f.operatorC, orgFormRuleBody("filter org rule", ">", 5))
		t.Cleanup(func() { requestJSON(t, f.operatorC, http.MethodDelete, f.rulesURL()+"/"+orgRule.ID, nil) })

		// host-scoped, bound to f.host, check_key memory (mem.used_pct) —
		// must be included by scope=host, by check=memory and by
		// host=<f.host>.
		hostRule := f.createRule(t, f.operatorC, map[string]any{
			"name": "filter host rule", "scope": "host", "host_id": f.host.String(),
			"metric": "mem.used_pct", "severity": "warn", "mode": "form",
			"form": map[string]any{"metric": "mem.used_pct", "op": ">", "value": 90},
		})
		t.Cleanup(func() { requestJSON(t, f.operatorC, http.MethodDelete, f.rulesURL()+"/"+hostRule.ID, nil) })

		byScope := f.listRulesQuery(t, f.viewerC, "scope=host")
		if !hasRuleID(byScope, hostRule.ID) {
			t.Fatalf("scope=host = %+v, want it to include the host-scoped rule", byScope)
		}
		if hasRuleID(byScope, orgRule.ID) {
			t.Fatalf("scope=host = %+v, want it to exclude the org-scoped rule", byScope)
		}

		byCheck := f.listRulesQuery(t, f.viewerC, "check=memory")
		if !hasRuleID(byCheck, hostRule.ID) {
			t.Fatalf("check=memory = %+v, want it to include the memory rule", byCheck)
		}
		if hasRuleID(byCheck, orgRule.ID) {
			t.Fatalf("check=memory = %+v, want it to exclude the cpu rule", byCheck)
		}

		byHost := f.listRulesQuery(t, f.viewerC, "host="+f.host.String())
		if !hasRuleID(byHost, hostRule.ID) {
			t.Fatalf("host=%s = %+v, want it to include the rule bound to that host", f.host, byHost)
		}
		if hasRuleID(byHost, orgRule.ID) {
			t.Fatalf("host=%s = %+v, want it to exclude the org-scoped rule (no host_id)", f.host, byHost)
		}
	})

	// A malformed expression is refused with 422, its message naming the
	// byte position in words ("at byte <n>") — not in errors[].value
	// (redactErrorValues nils that on every response) and not in a
	// machine-parsed location either: the editor's caret comes from
	// POST /rules/validate's own Checked.Pos field (see the "validate never
	// persists" subtest below), not from parsing this message. Position
	// pinned against the parser's own answer, exactly like
	// TestCheckInvalidCarriesPosition (internal/rules/service_test.go) pins
	// it one layer down.
	// backlog 3.30: a host- or item-scoped rule names the host it binds to,
	// and a group-scoped rule names the group, the same enrichment
	// checkDefinitionDTO already carries for its own host-bound
	// definitions — resolved via the list route's own batched lookup
	// (ruleHostNames/ruleGroupNames) as well as the single-rule routes
	// (ruleHostName/ruleGroupName), so every shape that returns a ruleDTO
	// agrees.
	//
	// The "degrades rather than errors" half of the brief (a rule whose
	// host or group was deleted) has no reachable fixture here: both FKs
	// on check_rules (00045) are ON DELETE CASCADE, so deleting a group or
	// host takes every rule bound to it down with it — there is no
	// surviving rule left to observe a blank name on, exactly the shape
	// checkDefinitionDTO's own HostName is in (no such test exists for it
	// either). ruleHostName/ruleGroupName/ruleHostNames/ruleGroupNames
	// still log-and-return-empty on a lookup failure, the same defensive
	// shape checkHostName/checkHostNames use, for the only way that path
	// actually runs: a store error, not a dangling id.
	t.Run("host- and group-scoped rules carry their target's name", func(t *testing.T) {
		groupResp := postJSON(t, f.operatorC, f.groupsURL(), map[string]any{"name": "name lookup fleet", "selector": "role=web", "priority": 0})
		if groupResp.StatusCode != http.StatusCreated {
			t.Fatalf("create group = %d %s", groupResp.StatusCode, mustReadBody(t, groupResp))
		}
		var groupOut struct {
			Group struct {
				ID string `json:"id"`
			} `json:"group"`
		}
		decodeInto(t, groupResp, &groupOut)
		t.Cleanup(func() { requestJSON(t, f.operatorC, http.MethodDelete, f.groupsURL()+"/"+groupOut.Group.ID, nil) })

		hostRule := f.createRule(t, f.operatorC, map[string]any{
			"name": "names host rule", "scope": "host", "host_id": f.host.String(),
			"metric": "mem.used_pct", "severity": "warn", "mode": "form",
			"form": map[string]any{"metric": "mem.used_pct", "op": ">", "value": 90},
		})
		t.Cleanup(func() { requestJSON(t, f.operatorC, http.MethodDelete, f.rulesURL()+"/"+hostRule.ID, nil) })
		if hostRule.HostName != "web-1" {
			t.Fatalf("create host-scoped rule host_name = %q, want %q", hostRule.HostName, "web-1")
		}

		groupRule := f.createRule(t, f.operatorC, map[string]any{
			"name": "names group rule", "scope": "group", "group_id": groupOut.Group.ID,
			"metric": "disk.used_pct", "severity": "warn", "mode": "form",
			"form": map[string]any{"metric": "disk.used_pct", "op": ">", "value": 90},
		})
		t.Cleanup(func() { requestJSON(t, f.operatorC, http.MethodDelete, f.rulesURL()+"/"+groupRule.ID, nil) })
		if groupRule.GroupName != "name lookup fleet" {
			t.Fatalf("create group-scoped rule group_name = %q, want %q", groupRule.GroupName, "name lookup fleet")
		}

		// GET by id agrees with what Create answered.
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.rulesURL()+"/"+hostRule.ID, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get host rule = %d", resp.StatusCode)
		}
		var single struct {
			Rule ruleDTO `json:"rule"`
		}
		decodeInto(t, resp, &single)
		if single.Rule.HostName != "web-1" {
			t.Fatalf("get host rule host_name = %q, want %q", single.Rule.HostName, "web-1")
		}

		// The list route resolves both names through its own batched
		// lookup, not a loop that happens to agree with the single-rule
		// routes by coincidence.
		listed := f.listRules(t, f.viewerC)
		var listedHost, listedGroup *ruleDTO
		for i := range listed {
			switch listed[i].ID {
			case hostRule.ID:
				listedHost = &listed[i]
			case groupRule.ID:
				listedGroup = &listed[i]
			}
		}
		if listedHost == nil || listedHost.HostName != "web-1" {
			t.Fatalf("listed host rule = %+v, want host_name %q", listedHost, "web-1")
		}
		if listedGroup == nil || listedGroup.GroupName != "name lookup fleet" {
			t.Fatalf("listed group rule = %+v, want group_name %q", listedGroup, "name lookup fleet")
		}
	})

	t.Run("a malformed expression is refused with its position in the message", func(t *testing.T) {
		const expr = "cpu.used_pct >"
		_, perr := rules.Parse(expr)
		if perr == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded; fixture is wrong", expr)
		}
		var pe *rules.ParseError
		if !errors.As(perr, &pe) {
			t.Fatalf("Parse(%q) error is not a *ParseError: %v", expr, perr)
		}
		wantSuffix := fmt.Sprintf("at byte %d)", pe.Pos)

		before := f.listRules(t, f.viewerC)
		body := map[string]any{
			"name": "broken expr", "scope": "org", "metric": "cpu.used_pct", "severity": "warn",
			"mode": "expression", "expression": expr,
		}
		resp := postJSON(t, f.operatorC, f.rulesURL(), body)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("create with a bad expression = %d, want 422 %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var problem struct {
			Errors []struct {
				Message  string `json:"message"`
				Location string `json:"location"`
				Value    any    `json:"value"`
			} `json:"errors"`
		}
		decodeInto(t, resp, &problem)
		if len(problem.Errors) != 1 {
			t.Fatalf("errors = %+v, want exactly one detail", problem.Errors)
		}
		if problem.Errors[0].Location != "body.expression" {
			t.Fatalf("location = %q, want %q", problem.Errors[0].Location, "body.expression")
		}
		if !strings.HasSuffix(problem.Errors[0].Message, wantSuffix) {
			t.Fatalf("message = %q, want it to end with %q", problem.Errors[0].Message, wantSuffix)
		}
		if problem.Errors[0].Value != nil {
			t.Fatalf("value = %v, want nil: redactErrorValues nils it on every response", problem.Errors[0].Value)
		}
		if len(f.listRules(t, f.viewerC)) != len(before) {
			t.Fatalf("a failed create persisted something: before %d, after %d", len(before), len(f.listRules(t, f.viewerC)))
		}

		// The same refusal, same position, on update.
		created := f.createRule(t, f.operatorC, orgFormRuleBody("update-refusal rule", ">", 5))
		t.Cleanup(func() { requestJSON(t, f.operatorC, http.MethodDelete, f.rulesURL()+"/"+created.ID, nil) })
		resp = requestJSON(t, f.operatorC, http.MethodPut, f.rulesURL()+"/"+created.ID, body)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("update with a bad expression = %d, want 422", resp.StatusCode)
		}
		decodeInto(t, resp, &problem)
		if len(problem.Errors) != 1 || problem.Errors[0].Location != "body.expression" || !strings.HasSuffix(problem.Errors[0].Message, wantSuffix) {
			t.Fatalf("update errors = %+v, want location %q and a message ending %q", problem.Errors, "body.expression", wantSuffix)
		}

		// Existence before deep validation, exactly like check_handlers.go's
		// own PUT: a malformed body against an id that does not exist at all
		// answers the uniform 404, not 422 — what was sent is irrelevant
		// once where it was sent does not exist.
		resp = requestJSON(t, f.operatorC, http.MethodPut, f.rulesURL()+"/"+uuid.Must(uuid.NewV7()).String(), body)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("update of a nonexistent id with a bad body = %d, want 404 %s", resp.StatusCode, mustReadBody(t, resp))
		}
	})

	// POST /rules/validate calls Check and answers Checked; it must never
	// persist anything, valid or not — a create attempt right after still
	// sees the same rule count.
	t.Run("validate never persists", func(t *testing.T) {
		before := f.listRules(t, f.viewerC)

		valid := orgFormRuleBody("would-be rule", ">", 42)
		resp := postJSON(t, f.operatorC, f.rulesURL()+"/validate", valid)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("validate valid = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var checked ruleCheckedBody
		decodeInto(t, resp, &checked)
		if !checked.OK || !checked.FormReady || checked.Source == "" {
			t.Fatalf("checked = %+v, want OK, FormReady and a rendered source", checked)
		}
		if checked.Pos != 0 || checked.Message != "" {
			t.Fatalf("a valid input must carry no position or message: %+v", checked)
		}

		invalid := map[string]any{
			"name": "broken", "scope": "org", "metric": "cpu.used_pct", "severity": "warn",
			"mode": "expression", "expression": "cpu.used_pct >",
		}
		resp = postJSON(t, f.operatorC, f.rulesURL()+"/validate", invalid)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("validate invalid = %d, want 422 %s", resp.StatusCode, mustReadBody(t, resp))
		}

		after := f.listRules(t, f.viewerC)
		if len(after) != len(before) {
			t.Fatalf("validate persisted something: before %d, after %d", len(before), len(after))
		}
	})

	// A rule of another organization is invisible, not merely refused: its
	// id answers 404 inside an organization the caller belongs to, 403
	// inside the one it actually lives in, and it never appears in anyone
	// else's list.
	t.Run("cross-organization invisibility", func(t *testing.T) {
		otherOwner := f.s.SeedUser("rules-other-owner@example.test", "correct horse battery")
		f.s.SeedOrg("rules-other", otherOwner)
		otherC := f.s.LoginAs(otherOwner)
		otherURL := f.s.HTTP.URL + "/api/v1/orgs/rules-other/rules"

		resp := postJSON(t, otherC, otherURL, orgFormRuleBody("other org rule", ">", 5))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create in the other org = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var created struct {
			Rule ruleDTO `json:"rule"`
		}
		decodeInto(t, resp, &created)

		update := orgFormRuleBody("other org rule", ">", 6)
		for _, c := range []struct {
			method string
			body   map[string]any
		}{{http.MethodGet, nil}, {http.MethodPut, update}, {http.MethodDelete, nil}} {
			if resp := requestJSON(t, f.operatorC, c.method, f.rulesURL()+"/"+created.Rule.ID, c.body); resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s of a foreign id in my own org = %d, want 404", c.method, resp.StatusCode)
			}
			if resp := requestJSON(t, f.operatorC, c.method, otherURL+"/"+created.Rule.ID, c.body); resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s in an org I am not a member of = %d, want 403", c.method, resp.StatusCode)
			}
		}
		for _, r := range f.listRules(t, f.ownerC) {
			if r.Name == "other org rule" {
				t.Fatalf("another organization's rule is visible: %+v", r)
			}
		}
	})

	// Create, update and delete each leave one attributable entry in the
	// organization's activity trail, naming the rule and its metric — never
	// the expression itself. A successful validate call leaves none at all:
	// it is allowlisted (audit_coverage_test.go), not audited — the same
	// shape as the monitoring-group and schedule preview routes.
	t.Run("mutations are audited, never with the source text; validate leaves no trail", func(t *testing.T) {
		body := map[string]any{
			"name": "audit rule", "scope": "org", "metric": "cpu.load1", "severity": "warn",
			"mode": "expression", "expression": "cpu.load1 > 999999",
		}
		created := f.createRule(t, f.operatorC, body)
		if resp := requestJSON(t, f.operatorC, http.MethodPut, f.rulesURL()+"/"+created.ID, map[string]any{
			"name": "audit rule", "scope": "org", "metric": "cpu.load1", "severity": "warn",
			"mode": "expression", "expression": "cpu.load1 > 888888",
		}); resp.StatusCode != http.StatusOK {
			t.Fatalf("update = %d", resp.StatusCode)
		}
		if resp := postJSON(t, f.operatorC, f.rulesURL()+"/validate", body); resp.StatusCode != http.StatusOK {
			t.Fatalf("validate = %d", resp.StatusCode)
		}
		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.rulesURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("delete = %d", resp.StatusCode)
		}

		actions := f.auditActionsFor(t, "rule", created.ID)
		for _, action := range []string{"POST createRule", "PUT updateRule", "DELETE deleteRule"} {
			if actions[action] != 1 {
				t.Fatalf("action %q recorded %d times, want 1; trail for this rule = %+v", action, actions[action], actions)
			}
		}

		// validate is allowlisted, not audited: no "POST validateRule"
		// action anywhere in the organization's feed at all — scanned
		// unfiltered by object id, since an audited validate call (which
		// this now never produces) would carry no object id to filter by
		// in the first place.
		resp := requestJSON(t, f.ownerC, http.MethodGet, f.activityURL(), nil)
		for _, ev := range decodeActivity(t, resp).Items {
			if ev.Action == "POST validateRule" {
				t.Fatalf("validate recorded an audit event %+v, want none: it is allowlisted, not audited", ev)
			}
			if ev.ObjectType != "rule" {
				continue
			}
			if strings.Contains(ev.Summary, "999999") || strings.Contains(ev.Summary, "888888") {
				t.Fatalf("summary %q leaks the rule's expression source", ev.Summary)
			}
			if ev.ObjectID == created.ID {
				if !strings.Contains(ev.Summary, "audit rule") || !strings.Contains(ev.Summary, "cpu.load1") {
					t.Fatalf("summary %q does not name the rule and its metric", ev.Summary)
				}
			}
		}
	})

	// The effective view's precedence: an org rule, a group rule bound to a
	// group matching this host, and a host rule, all judging the same
	// (check_key, metric, severity) tuple — the host rule must win, and the
	// group and org rules must both appear shadowed. cpu.load1 is used
	// throughout this file precisely so this does not compete with the
	// seed set's own cpu.used_pct rule.
	t.Run("effective rules layers org, group and host scopes", func(t *testing.T) {
		scoped := db.WithOrgScope(context.Background(), f.orgID)
		res, err := f.s.Checks.RecordObservations(scoped, f.orgID, []checks.Observation{
			{HostID: f.host, CheckKey: "cpu", Metric: "cpu.load1", At: time.Now(), Value: floatPtr(8)},
		})
		if err != nil || res.Accepted != 1 {
			t.Fatalf("record observation: %+v, err %v", res, err)
		}

		orgRule := f.createRule(t, f.operatorC, orgFormRuleBody("layer org", ">", 5))
		t.Cleanup(func() { requestJSON(t, f.operatorC, http.MethodDelete, f.rulesURL()+"/"+orgRule.ID, nil) })

		groupResp := postJSON(t, f.operatorC, f.groupsURL(), map[string]any{"name": "web fleet", "selector": "role=web", "priority": 0})
		if groupResp.StatusCode != http.StatusCreated {
			t.Fatalf("create group = %d %s", groupResp.StatusCode, mustReadBody(t, groupResp))
		}
		var groupOut struct {
			Group struct {
				ID string `json:"id"`
			} `json:"group"`
		}
		decodeInto(t, groupResp, &groupOut)
		t.Cleanup(func() { requestJSON(t, f.operatorC, http.MethodDelete, f.groupsURL()+"/"+groupOut.Group.ID, nil) })

		groupRule := f.createRule(t, f.operatorC, map[string]any{
			"name": "layer group", "scope": "group", "group_id": groupOut.Group.ID, "metric": "cpu.load1", "severity": "warn", "mode": "form",
			"form": map[string]any{"metric": "cpu.load1", "op": ">", "value": 6},
		})
		t.Cleanup(func() { requestJSON(t, f.operatorC, http.MethodDelete, f.rulesURL()+"/"+groupRule.ID, nil) })

		hostRule := f.createRule(t, f.operatorC, map[string]any{
			"name": "layer host", "scope": "host", "host_id": f.host.String(), "metric": "cpu.load1", "severity": "warn", "mode": "form",
			"form": map[string]any{"metric": "cpu.load1", "op": ">", "value": 7},
		})
		t.Cleanup(func() { requestJSON(t, f.operatorC, http.MethodDelete, f.rulesURL()+"/"+hostRule.ID, nil) })

		resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.host)+"/effective-rules", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("effective-rules = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var out struct {
			Effective []effectiveRuleBody `json:"effective"`
		}
		decodeInto(t, resp, &out)

		var found *effectiveRuleBody
		for i := range out.Effective {
			if out.Effective[i].Metric == "cpu.load1" && out.Effective[i].Severity == "warn" {
				found = &out.Effective[i]
			}
		}
		if found == nil {
			t.Fatalf("no effective entry for cpu.load1/warn in %+v", out.Effective)
		}
		if found.Winner.ID != hostRule.ID {
			t.Fatalf("winner = %+v, want the host-scoped rule %s", found.Winner, hostRule.ID)
		}
		if len(found.Shadowed) != 2 {
			t.Fatalf("shadowed = %+v, want exactly 2 (the group and org rules)", found.Shadowed)
		}
		shadowedIDs := map[string]bool{found.Shadowed[0].ID: true, found.Shadowed[1].ID: true}
		if !shadowedIDs[groupRule.ID] || !shadowedIDs[orgRule.ID] {
			t.Fatalf("shadowed ids = %v, want %s and %s", shadowedIDs, groupRule.ID, orgRule.ID)
		}
		if !found.Observed {
			t.Fatalf("found = %+v, want Observed true: the host just reported this tuple", found)
		}

		// Cross-organization invisibility holds for the effective view too,
		// on both its shapes: an org the caller is not a member of at all
		// answers 403 (the org itself is invisible)...
		if resp := requestJSON(t, f.operatorC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/rules-other/hosts/"+f.host.String()+"/effective-rules", nil); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("effective-rules in a foreign org = %d, want 403", resp.StatusCode)
		}

		// ...while a host id belonging to a *different* organization,
		// requested from an org the caller *is* a member of, answers the
		// uniform 404 (mapChecksErr via deps.Hosts.GetHost) — the same
		// "invisible, not merely refused" contract every other rule route
		// proves for a foreign rule id.
		otherHostOwner := f.s.SeedUser("rules-otherhost-owner@example.test", "correct horse battery")
		otherHostOrg := f.s.SeedOrg("rules-otherhost", otherHostOwner)
		otherHostScoped := db.WithOrgScope(context.Background(), otherHostOrg)
		otherHostZones, err := f.s.Deps.Hosts.ListZones(otherHostScoped, otherHostOrg)
		if err != nil || len(otherHostZones) == 0 {
			t.Fatalf("zones for the foreign-host org: %v", err)
		}
		otherHost, err := f.s.Deps.Hosts.CreateHost(otherHostScoped, otherHostOrg, hosts.HostInput{
			Name: "foreign-host", Address: "10.0.9.99", ZoneID: otherHostZones[0].ID, Managed: true, Monitored: true,
		})
		if err != nil {
			t.Fatalf("create foreign host: %v", err)
		}
		if resp := requestJSON(t, f.operatorC, http.MethodGet, f.hostURL(otherHost.ID)+"/effective-rules", nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("effective-rules for a foreign host id in my own org = %d, want 404 %s", resp.StatusCode, mustReadBody(t, resp))
		}
	})
}

// TestEffectiveRulesHidesTuplesWhoseCheckNoLongerResolves pins the two
// reads of one host agreeing about which of its tuples exist.
//
// GET /hosts/{id}/checks deliberately drops a tuple whose check no longer
// resolves an interval (currentTupleStates, host_check_handlers.go): a
// check_current row outlives the definition that produced it (backlog
// 3.28), and a disabled check must not carry a live-looking verdict. The
// effective-rules view read CurrentForHost raw and so still listed a
// winner and its shadowed rules for exactly those tuples — a rule the
// operator is shown as governing a tuple the host page says is not there.
func TestEffectiveRulesHidesTuplesWhoseCheckNoLongerResolves(t *testing.T) {
	f := newRulesFixture(t, "rules-disabled-check")
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	if _, err := f.s.Checks.RecordObservations(scoped, f.orgID, []checks.Observation{
		{HostID: f.host, CheckKey: "cpu", Metric: "cpu.load1", At: time.Now(), Value: floatPtr(8)},
	}); err != nil {
		t.Fatal(err)
	}
	f.createRule(t, f.operatorC, orgFormRuleBody("load rule", ">", 5))

	hasLoad1 := func(t *testing.T) bool {
		t.Helper()
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.host)+"/effective-rules", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("effective-rules = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var out struct {
			Effective []effectiveRuleBody `json:"effective"`
		}
		decodeInto(t, resp, &out)
		for _, e := range out.Effective {
			if e.Metric == "cpu.load1" {
				return true
			}
		}
		return false
	}

	if !hasLoad1(t) {
		t.Fatal("cpu.load1 is absent from the effective view while its check still resolves; the fixture is wrong")
	}

	// Disable the seeded cpu definition at the host, exactly as
	// TestHostChecksReportsTupleState does for service.
	resp := requestJSON(t, f.ownerC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/checks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list checks = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var checkList struct {
		Checks []checkDTO `json:"checks"`
	}
	decodeInto(t, resp, &checkList)
	var cpuDef checkDTO
	for _, d := range checkList.Checks {
		if d.Key == "cpu" {
			cpuDef = d
		}
	}
	if cpuDef.ID == "" {
		t.Fatalf("no seeded \"cpu\" check in %+v", checkList.Checks)
	}
	disable := requestJSON(t, f.operatorC, http.MethodPut, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/checks/"+cpuDef.ID, map[string]any{
		"plugin": cpuDef.Plugin, "key": cpuDef.Key, "interval_seconds": cpuDef.IntervalSeconds,
		"binding_kind": "host", "host_id": f.host.String(), "params": cpuDef.Params, "enabled": false,
	})
	if disable.StatusCode != http.StatusOK {
		t.Fatalf("disable cpu at the host = %d %s", disable.StatusCode, mustReadBody(t, disable))
	}

	if hasLoad1(t) {
		t.Fatal("the effective view still lists cpu.load1 although its check no longer resolves an interval; " +
			"the host page drops that tuple, and the two reads must not disagree about which tuples exist")
	}
}
