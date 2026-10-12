// The alert-route API's tests (v0.4b Task 10): the six routes
// (registerAlertRouteRoutes, alert_route_handlers.go) driven through the
// full API against the test database, authenticated with a bearer token
// (apitest.Server.OrgWithToken/OrgWithTokenPerms) rather than a cookie —
// this surface is exactly the kind of thing a CI pipeline or an external
// integration configures, so exercising it the same way a real automation
// token would is more informative here than a browser session.
package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// decodeID reads {"route": {"id": "..."}} — the shape every create/get/
// update alert-route response carries its id in (alert_route_dto.go).
func decodeID(t *testing.T, resp *apitest.Response) string {
	t.Helper()
	var body struct {
		Route struct {
			ID string `json:"id"`
		} `json:"route"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode id: %v: %s", err, resp.Body)
	}
	if body.Route.ID == "" {
		t.Fatalf("no id in response: %s", resp.Body)
	}
	return body.Route.ID
}

func TestAlertRoutesCRUD(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	ch := s.SeedChannel(t, org)

	body := map[string]any{
		"name": "ops", "enabled": true, "scope": "org",
		"severities": []string{"crit", "unknown"},
		"events":     []string{"opened", "closed"},
		"channels":   []string{ch.String()},
	}
	created := s.Post(t, tok, "/api/v1/orgs/"+org.String()+"/alert-routes", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201: %s", created.Code, created.Body)
	}

	list := s.Get(t, tok, "/api/v1/orgs/"+org.String()+"/alert-routes")
	if list.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", list.Code)
	}
}

func TestAlertRouteWriteNeedsTheChannelPermission(t *testing.T) {
	// Spec, "API and permissions": a route decides where data leaves the
	// system, which weighs more than acknowledging an alert. Being
	// allowed to acknowledge does not carry the right to route.
	s := apitest.New(t)
	org, tok := s.OrgWithTokenPerms(t, tenancy.PermChecksWrite, tenancy.PermChecksRead)
	ch := s.SeedChannel(t, org)

	resp := s.Post(t, tok, "/api/v1/orgs/"+org.String()+"/alert-routes", map[string]any{
		"name": "ops", "enabled": true, "scope": "org",
		"severities": []string{"crit"}, "events": []string{"opened"},
		"channels": []string{ch.String()},
	})
	if resp.Code != http.StatusForbidden {
		t.Errorf("POST with checks.write but not channels.write = %d, want 403", resp.Code)
	}
}

func TestAlertRouteUpdateRequiresIfMatch(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	ch := s.SeedChannel(t, org)
	created := s.Post(t, tok, "/api/v1/orgs/"+org.String()+"/alert-routes", map[string]any{
		"name": "ops", "enabled": true, "scope": "org",
		"severities": []string{"crit"}, "events": []string{"opened"},
		"channels": []string{ch.String()},
	})
	id := decodeID(t, created)

	resp := s.Patch(t, tok, "/api/v1/orgs/"+org.String()+"/alert-routes/"+id, "", map[string]any{"enabled": false})
	if resp.Code != http.StatusPreconditionRequired && resp.Code != http.StatusBadRequest {
		t.Errorf("PATCH without If-Match = %d, want it refused", resp.Code)
	}
}

func TestHostRouteUnionView(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	ch := s.SeedChannel(t, org)
	host := s.SeedHost(t, org)
	s.Post(t, tok, "/api/v1/orgs/"+org.String()+"/alert-routes", map[string]any{
		"name": "all", "enabled": true, "scope": "org",
		"severities": []string{"crit"}, "events": []string{"opened"},
		"channels": []string{ch.String()},
	})

	resp := s.Get(t, tok, "/api/v1/orgs/"+org.String()+"/hosts/"+host.String()+"/alert-routes")
	if resp.Code != http.StatusOK {
		t.Fatalf("GET host routes = %d, want 200: %s", resp.Code, resp.Body)
	}
	if !strings.Contains(resp.Body.String(), "all") {
		t.Errorf("the organization-wide route must appear in a host's union view: %s", resp.Body)
	}
}

// TestAlertRouteReadNeedsTheChannelPermission is TestAlertRouteWriteNeeds-
// TheChannelPermission's read-side counterpart: hosts.read is not
// channels.read either, so a token minted with only hosts permissions must
// not see the organization's routes.
func TestAlertRouteReadNeedsTheChannelPermission(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithTokenPerms(t, tenancy.PermHostsRead)

	resp := s.Get(t, tok, "/api/v1/orgs/"+org.String()+"/alert-routes")
	if resp.Code != http.StatusForbidden {
		t.Errorf("GET with hosts.read but not channels.read = %d, want 403", resp.Code)
	}
}

// TestAlertRouteUpdateAndDelete exercises the rest of the CRUD surface
// TestAlertRoutesCRUD leaves untouched: a PATCH with a correct If-Match
// changes the stored route and its ETag, and a DELETE then makes the id
// answer 404.
func TestAlertRouteUpdateAndDelete(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	ch := s.SeedChannel(t, org)
	url := "/api/v1/orgs/" + org.String() + "/alert-routes"
	created := s.Post(t, tok, url, map[string]any{
		"name": "ops", "enabled": true, "scope": "org",
		"severities": []string{"crit"}, "events": []string{"opened"},
		"channels": []string{ch.String()},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", created.Code, created.Body)
	}
	id := decodeID(t, created)

	updated := s.Patch(t, tok, url+"/"+id, `"1"`, map[string]any{
		"name": "ops", "enabled": false, "scope": "org",
		"severities": []string{"crit", "warn"}, "events": []string{"opened"},
		"channels": []string{ch.String()},
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, want 200: %s", updated.Code, updated.Body)
	}
	if strings.Contains(updated.Body.String(), `"enabled":true`) {
		t.Errorf("update did not take effect: %s", updated.Body)
	}

	if resp := s.Delete(t, tok, url+"/"+id, `"2"`); resp.Code != http.StatusOK {
		t.Fatalf("DELETE = %d, want 200: %s", resp.Code, resp.Body)
	}
	if resp := s.Get(t, tok, url+"/"+id); resp.Code != http.StatusNotFound {
		t.Errorf("GET after delete = %d, want 404", resp.Code)
	}
}

// TestAlertRouteRejectsAChannellessRoute pins alertroutes.Validate's own
// rule through the HTTP boundary: a route with no channel tells nobody, so
// the service refuses to create one, and the API must surface that as a
// client error rather than a silently accepted, silent route.
func TestAlertRouteRejectsAChannellessRoute(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)

	resp := s.Post(t, tok, "/api/v1/orgs/"+org.String()+"/alert-routes", map[string]any{
		"name": "no-channel", "enabled": true, "scope": "org",
		"severities": []string{"crit"}, "events": []string{"opened"},
		"channels": []string{},
	})
	if resp.Code != http.StatusBadRequest {
		t.Errorf("POST with no channel = %d, want 400: %s", resp.Code, resp.Body)
	}
}
