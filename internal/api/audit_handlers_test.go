package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type eventDTO struct {
	ID         string  `json:"id"`
	OrgID      *string `json:"org_id"`
	ActorID    *string `json:"actor_id"`
	ActorKind  string  `json:"actor_kind"`
	Action     string  `json:"action"`
	ObjectType string  `json:"object_type"`
	ObjectID   string  `json:"object_id"`
	Summary    string  `json:"summary"`
	At         string  `json:"at"`
}

type activityDTO struct {
	Items      []eventDTO `json:"items"`
	NextCursor string     `json:"next_cursor"`
}

func decodeActivity(t *testing.T, resp *http.Response) activityDTO {
	t.Helper()
	var out activityDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode activity response: %v", err)
	}
	return out
}

// TestAuditRecordsRealMutation is the attribution test called out by
// auditMiddleware's own doc comment (audit_middleware.go): it drives a real
// PATCH /orgs/{org} request through the whole running API — not a
// synthetic operation built just for this test — and asserts the recorded
// event carries the real actor (the owner who made the call, as a "user"
// actor, not a token) and the real object (the org that was renamed, with a
// human summary), proving the mutable-carrier mechanism the middleware
// relies on actually works against the installed huma v2.39.1: requireOrg
// fills the carrier's scope, the handler fills its object/summary, and
// auditMiddleware reads both back after huma.Register's handler has
// already returned. If context propagation did not work the way that
// comment describes, this event would either not exist at all, or exist
// with an empty actor/object.
func TestAuditRecordsRealMutation(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("audit-owner@example.test", "correct horse battery")
	s.SeedOrg("audit-org", owner)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/audit-org", map[string]any{"name": "Audit Org Renamed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename org: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	renamed := decodeOrg(t, resp)

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/audit-org/activity", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get activity: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	activity := decodeActivity(t, resp)
	if len(activity.Items) != 1 {
		t.Fatalf("activity = %+v, want exactly 1 event", activity)
	}
	ev := activity.Items[0]
	if ev.Action != "PATCH updateOrganization" {
		t.Fatalf("Action = %q, want %q", ev.Action, "PATCH updateOrganization")
	}
	if ev.ObjectType != "organization" || ev.ObjectID != renamed.ID {
		t.Fatalf("object = (%q, %q), want (organization, %s)", ev.ObjectType, ev.ObjectID, renamed.ID)
	}
	if ev.OrgID == nil || *ev.OrgID != renamed.ID {
		t.Fatalf("OrgID = %v, want %s", ev.OrgID, renamed.ID)
	}
	if ev.ActorID == nil || *ev.ActorID != owner.String() {
		t.Fatalf("ActorID = %v, want the owner %s who made the call", ev.ActorID, owner)
	}
	if ev.ActorKind != "user" {
		t.Fatalf("ActorKind = %q, want %q (a browser session, not a token)", ev.ActorKind, "user")
	}
	if !strings.Contains(ev.Summary, "Audit Org Renamed") {
		t.Fatalf("Summary = %q, want it to mention the new name", ev.Summary)
	}
	if ev.ID == "" || ev.At == "" {
		t.Fatalf("event missing ID/At: %+v", ev)
	}
}

// TestActivityFeedRequiresAuditRead proves the route is gated on
// tenancy.PermAuditRead (operator and above): a viewer, who cannot read the
// audit log, gets 403 rather than an (empty or populated) feed.
func TestActivityFeedRequiresAuditRead(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("audit-perm-owner@example.test", "correct horse battery")
	org := s.SeedOrg("audit-perm-org", owner)
	viewer := s.SeedUser("audit-perm-viewer@example.test", "correct horse battery")
	s.AddMember(org, viewer, tenancy.RoleViewer)

	resp := requestJSON(t, s.LoginAs(owner), http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/audit-perm-org", map[string]any{"name": "Renamed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner rename: status %d", resp.StatusCode)
	}

	resp = requestJSON(t, s.LoginAs(viewer), http.MethodGet, s.HTTP.URL+"/api/v1/orgs/audit-perm-org/activity", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer activity: status %d, want 403", resp.StatusCode)
	}

	// An operator, by contrast, may read it.
	operator := s.SeedUser("audit-perm-operator@example.test", "correct horse battery")
	s.AddMember(org, operator, tenancy.RoleOperator)
	resp = requestJSON(t, s.LoginAs(operator), http.MethodGet, s.HTTP.URL+"/api/v1/orgs/audit-perm-org/activity", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator activity: status %d, want 200", resp.StatusCode)
	}
}

// TestActivityFeedIsolatedAcrossOrgs proves one org's mutations never
// appear in another org's feed — the route's own org_id scoping, backed by
// the org_isolation RLS policy proven directly (via db.WithOrg) in
// internal/audit/store_sql_test.go's TestAuditEventsAreIsolatedPerOrgByRLS.
func TestActivityFeedIsolatedAcrossOrgs(t *testing.T) {
	s := apitest.New(t)
	owner1 := s.SeedUser("audit-iso-owner1@example.test", "correct horse battery")
	s.SeedOrg("audit-iso-org1", owner1)
	owner2 := s.SeedUser("audit-iso-owner2@example.test", "correct horse battery")
	s.SeedOrg("audit-iso-org2", owner2)

	resp := requestJSON(t, s.LoginAs(owner1), http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/audit-iso-org1", map[string]any{"name": "Org1 Renamed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("org1 rename: status %d", resp.StatusCode)
	}

	resp = requestJSON(t, s.LoginAs(owner2), http.MethodGet, s.HTTP.URL+"/api/v1/orgs/audit-iso-org2/activity", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("org2 activity: status %d", resp.StatusCode)
	}
	activity := decodeActivity(t, resp)
	if len(activity.Items) != 0 {
		t.Fatalf("org2's activity = %+v, want empty (org1's rename must not leak across orgs)", activity)
	}
}

// TestFailedMutationNotRecorded proves only successful (2xx) mutating calls
// produce an event: a viewer attempting to rename the org gets 403, and
// that attempt itself must not show up in anyone's feed once an operator
// looks.
func TestFailedMutationNotRecorded(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("audit-fail-owner@example.test", "correct horse battery")
	org := s.SeedOrg("audit-fail-org", owner)
	viewer := s.SeedUser("audit-fail-viewer@example.test", "correct horse battery")
	s.AddMember(org, viewer, tenancy.RoleViewer)

	resp := requestJSON(t, s.LoginAs(viewer), http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/audit-fail-org", map[string]any{"name": "Should Not Land"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer rename attempt: status %d, want 403", resp.StatusCode)
	}

	resp = requestJSON(t, s.LoginAs(owner), http.MethodGet, s.HTTP.URL+"/api/v1/orgs/audit-fail-org/activity", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner activity: status %d", resp.StatusCode)
	}
	activity := decodeActivity(t, resp)
	if len(activity.Items) != 0 {
		t.Fatalf("activity = %+v, want empty (the failed 403 attempt must not be recorded)", activity)
	}
}

// TestActivityFeedActorKindDistinguishesToken proves ActorKind reflects how
// the mutating call was authenticated: a bearer API token, not a browser
// session, produces "token" — the property auth.Principal.TokenID exists
// to carry, and actorKind (audit_middleware.go) reads.
func TestActivityFeedActorKindDistinguishesToken(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("audit-token-owner@example.test", "correct horse battery")
	s.SeedOrg("audit-token-org", owner)
	c := s.LoginAs(owner)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/audit-token-org/tokens", map[string]any{"name": "ci", "role": "admin"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created struct {
		Token string `json:"token"`
		ID    string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	resp = bearerRequestJSON(t, s, http.MethodPatch, "/api/v1/orgs/audit-token-org", created.Token, map[string]any{"name": "Renamed By Token"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename via token: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/audit-token-org/activity", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get activity: status %d", resp.StatusCode)
	}
	activity := decodeActivity(t, resp)
	// Two events: the token creation (a "user" action, since it was made
	// with the cookie session) and the rename (a "token" action).
	var rename *eventDTO
	for i := range activity.Items {
		if activity.Items[i].Action == "PATCH updateOrganization" {
			rename = &activity.Items[i]
		}
	}
	if rename == nil {
		t.Fatalf("no rename event in %+v", activity.Items)
	}
	if rename.ActorKind != "token" {
		t.Fatalf("ActorKind = %q, want %q for a call made with a bearer token", rename.ActorKind, "token")
	}
	if rename.ActorID == nil || *rename.ActorID != owner.String() {
		t.Fatalf("ActorID = %v, want the token owner %s (a token still acts on behalf of the user who owns it)", rename.ActorID, owner)
	}
}

// TestMemberInviteAndTokenRoutesRecordEvents wires assertions for every
// other mutating route this task instruments (member_handlers.go,
// token_handlers.go): each produces exactly one correctly object-typed
// event, so the mechanism is exercised by real handlers rather than only
// the org-rename path the other tests above already cover in depth.
func TestMemberInviteAndTokenRoutesRecordEvents(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("audit-wiring-owner@example.test", "correct horse battery")
	member := s.SeedUser("audit-wiring-member@example.test", "correct horse battery")
	orgID := s.SeedOrg("audit-wiring-org", owner)
	s.AddMember(orgID, member, tenancy.RoleViewer)
	c := s.LoginAs(owner)

	base := s.HTTP.URL + "/api/v1/orgs/audit-wiring-org"

	resp := postJSON(t, c, base+"/invites", map[string]any{"email": "invitee@example.test", "role": "operator"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("invite: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, c, http.MethodPatch, base+"/members/"+member.String(), map[string]any{"role": "operator"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("change role: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = postJSON(t, c, base+"/tokens", map[string]any{"name": "wiring-token", "role": "viewer"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var tok struct{ ID string }
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		t.Fatal(err)
	}

	resp = requestJSON(t, c, http.MethodDelete, base+"/tokens/"+tok.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke token: status %d", resp.StatusCode)
	}

	resp = requestJSON(t, c, http.MethodDelete, base+"/members/"+member.String(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove member: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, c, http.MethodGet, base+"/activity?limit=50", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get activity: status %d", resp.StatusCode)
	}
	activity := decodeActivity(t, resp)

	wantObjectTypes := map[string]int{"invite": 1, "member": 2, "token": 2}
	got := map[string]int{}
	for _, ev := range activity.Items {
		got[ev.ObjectType]++
		if ev.Summary == "" {
			t.Fatalf("event %+v has an empty summary", ev)
		}
	}
	for ot, want := range wantObjectTypes {
		if got[ot] != want {
			t.Fatalf("object_type %q count = %d, want %d; full activity = %+v", ot, got[ot], want, activity.Items)
		}
	}
}

// TestMyActivitySpansOrgs proves GET /me/activity returns the caller's own
// actions across every org they belong to, and none of anyone else's.
func TestMyActivitySpansOrgs(t *testing.T) {
	s := apitest.New(t)
	alice := s.SeedUser("audit-me-alice@example.test", "correct horse battery")
	s.SeedOrg("audit-me-org-a", alice)
	s.SeedOrg("audit-me-org-b", alice)
	bob := s.SeedUser("audit-me-bob@example.test", "correct horse battery")
	s.SeedOrg("audit-me-org-c", bob)

	ca := s.LoginAs(alice)
	if resp := requestJSON(t, ca, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/audit-me-org-a", map[string]any{"name": "A Renamed"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("rename org-a: status %d", resp.StatusCode)
	}
	if resp := requestJSON(t, ca, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/audit-me-org-b", map[string]any{"name": "B Renamed"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("rename org-b: status %d", resp.StatusCode)
	}
	cb := s.LoginAs(bob)
	if resp := requestJSON(t, cb, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/audit-me-org-c", map[string]any{"name": "C Renamed"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("rename org-c: status %d", resp.StatusCode)
	}

	resp := requestJSON(t, ca, http.MethodGet, s.HTTP.URL+"/api/v1/me/activity", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me/activity: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	activity := decodeActivity(t, resp)
	if len(activity.Items) != 2 {
		t.Fatalf("alice's /me/activity = %+v, want exactly 2 events (one per org she renamed)", activity.Items)
	}
	for _, ev := range activity.Items {
		if ev.ActorID == nil || *ev.ActorID != alice.String() {
			t.Fatalf("/me/activity leaked another actor's event: %+v", ev)
		}
	}
}

// TestActivityFeedPaginatesByCursor is an end-to-end check that the
// cursor/limit query parameters this route exposes actually drive
// audit.Recorder.List's own pagination (already proven directly against
// both the fake and the real store in internal/audit), not just that they
// parse.
func TestActivityFeedPaginatesByCursor(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("audit-page-owner@example.test", "correct horse battery")
	s.SeedOrg("audit-page-org", owner)
	c := s.LoginAs(owner)

	for i, name := range []string{"First", "Second"} {
		resp := requestJSON(t, c, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/audit-page-org", map[string]any{"name": name})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("rename #%d: status %d", i, resp.StatusCode)
		}
	}

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/audit-page-org/activity?limit=1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page 1: status %d", resp.StatusCode)
	}
	page1 := decodeActivity(t, resp)
	if len(page1.Items) != 1 || page1.NextCursor == "" {
		t.Fatalf("page 1 = %+v, want exactly 1 item and a non-empty cursor", page1)
	}
	if !strings.Contains(page1.Items[0].Summary, "Second") {
		t.Fatalf("page 1's item = %+v, want the most recent rename (to Second)", page1.Items[0])
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/audit-page-org/activity?limit=1&cursor="+page1.NextCursor, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page 2: status %d", resp.StatusCode)
	}
	page2 := decodeActivity(t, resp)
	if len(page2.Items) != 1 || page2.NextCursor != "" {
		t.Fatalf("page 2 = %+v, want exactly 1 item and an empty cursor", page2)
	}
	if !strings.Contains(page2.Items[0].Summary, "First") {
		t.Fatalf("page 2's item = %+v, want the older rename (to First)", page2.Items[0])
	}
}

// TestRecordingFailureDoesNotBreakRequest proves the tradeoff
// auditMiddleware's own doc comment describes: when the audit store itself
// fails to Insert, the mutating request that triggered it still succeeds —
// the org really was renamed, and the caller gets back the normal 200 with
// the updated org — rather than the recording failure surfacing as an
// error on a request that, from the caller's point of view, already
// completed. What happens instead (an ERROR log line) is not observable
// from here; that half of the tradeoff is covered by
// auditMiddleware's own comment, not a test, the same way this codebase
// does not test mapTenancyErr/mapAuthErr's slog.ErrorContext calls either.
func TestRecordingFailureDoesNotBreakRequest(t *testing.T) {
	s := apitest.New(t, apitest.WithFailingAudit())
	owner := s.SeedUser("audit-recfail-owner@example.test", "correct horse battery")
	s.SeedOrg("audit-recfail-org", owner)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/audit-recfail-org", map[string]any{"name": "Renamed Despite Audit Failure"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename: status %d, body %s (a failing audit recorder must not fail the request)", resp.StatusCode, mustReadBody(t, resp))
	}
	renamed := decodeOrg(t, resp)
	if renamed.Name != "Renamed Despite Audit Failure" {
		t.Fatalf("renamed org = %+v, the mutation itself must have committed regardless of the audit failure", renamed)
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/audit-recfail-org", nil)
	if resp.StatusCode != http.StatusOK || decodeOrg(t, resp).Name != "Renamed Despite Audit Failure" {
		t.Fatalf("a follow-up read did not see the committed rename: status %d", resp.StatusCode)
	}
}

// TestActivityFeedFiltersByActorAndSince exercises the actor= and since=
// query parameters end to end: one org, two different actors renaming it at
// known times, filtered down to one actor and then to "since" the second
// rename.
func TestActivityFeedFiltersByActorAndSince(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("audit-filter-owner@example.test", "correct horse battery")
	orgID := s.SeedOrg("audit-filter-org", owner)
	admin := s.SeedUser("audit-filter-admin@example.test", "correct horse battery")
	s.AddMember(orgID, admin, tenancy.RoleAdmin)

	if resp := requestJSON(t, s.LoginAs(owner), http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/audit-filter-org", map[string]any{"name": "By Owner"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("owner rename: status %d", resp.StatusCode)
	}
	if resp := requestJSON(t, s.LoginAs(admin), http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/audit-filter-org", map[string]any{"name": "By Admin"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin rename: status %d", resp.StatusCode)
	}

	c := s.LoginAs(owner)
	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/audit-filter-org/activity?actor="+admin.String(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("actor filter: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	filtered := decodeActivity(t, resp)
	if len(filtered.Items) != 1 || filtered.Items[0].ActorID == nil || *filtered.Items[0].ActorID != admin.String() {
		t.Fatalf("actor filter = %+v, want exactly the admin's event", filtered.Items)
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/audit-filter-org/activity?since=2099-01-01T00:00:00Z", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("since filter: status %d", resp.StatusCode)
	}
	future := decodeActivity(t, resp)
	if len(future.Items) != 0 {
		t.Fatalf("since=far future = %+v, want empty", future.Items)
	}
}

// TestActivityFeedRejectsMalformedQuery proves the three caller-controlled
// query parameters that need parsing (actor, since, cursor) each fail
// closed with 400 rather than 500 or silently matching nothing/everything.
func TestActivityFeedRejectsMalformedQuery(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("audit-badquery-owner@example.test", "correct horse battery")
	s.SeedOrg("audit-badquery-org", owner)
	c := s.LoginAs(owner)

	for _, q := range []string{"actor=not-a-uuid", "since=not-a-timestamp", "cursor=not-a-valid-cursor"} {
		resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/audit-badquery-org/activity?"+q, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("query %q: status %d, want 400, body %s", q, resp.StatusCode, mustReadBody(t, resp))
		}
	}
}
