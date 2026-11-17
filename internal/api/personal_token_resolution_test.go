// Personal tokens at the org-scoped REST API (backlog 3.100, spec
// docs/superpowers/specs/2026-11-17-personal-tokens-design.md rules 2, 3, 8,
// 11, 13): the bearer middleware only authenticates the token, requireOrg
// looks up the grant for the route's organization, and the effective role is
// the lower of the grant's cap and the user's current membership role.
package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// personalFixture: owner owns organizations; user is a member of them at
// the roles a test chooses, and holds the personal tokens.
type personalFixture struct {
	s     *apitest.Server
	owner uuid.UUID
	user  uuid.UUID
}

func newPersonalFixture(t *testing.T, name string) personalFixture {
	t.Helper()
	s := apitest.New(t)
	return personalFixture{
		s:     s,
		owner: s.SeedUser(name+"-owner@example.test", "correct horse battery"),
		user:  s.SeedUser(name+"-user@example.test", "correct horse battery"),
	}
}

// org seeds an organization the fixture's owner owns, with the user as a
// member at role (none when role is empty).
func (f personalFixture) org(t *testing.T, slug string, role tenancy.Role) uuid.UUID {
	t.Helper()
	id := f.s.SeedOrg(slug, f.owner)
	if role != "" {
		f.s.AddMember(id, f.user, role)
	}
	return id
}

func (f personalFixture) zone(t *testing.T, org uuid.UUID) string {
	t.Helper()
	zones, err := f.s.Deps.Hosts.ListZones(db.WithOrgScope(context.Background(), org), org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	return zones[0].ID.String()
}

func detailOf(t *testing.T, r *apitest.Response) string {
	t.Helper()
	var problem struct {
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &problem)
	return problem.Detail
}

func wantStatus(t *testing.T, what string, r *apitest.Response, code int) {
	t.Helper()
	if r.Code != code {
		t.Fatalf("%s: status %d (%s), want %d", what, r.Code, r.Body, code)
	}
}

func TestPersonalTokenGrantPresent(t *testing.T) {
	f := newPersonalFixture(t, "pt-present")
	a := f.org(t, "pt-present-a", tenancy.RoleOperator)
	_, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOperator})
	wantStatus(t, "GET hosts of A", f.s.Get(t, tok, "/api/v1/orgs/pt-present-a/hosts"), http.StatusOK)
}

func TestPersonalTokenGrantAbsent(t *testing.T) {
	f := newPersonalFixture(t, "pt-absent")
	a := f.org(t, "pt-absent-a", tenancy.RoleOperator)
	f.org(t, "pt-absent-c", tenancy.RoleAdmin) // a member, but no grant on the token
	_, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOperator})
	r := f.s.Get(t, tok, "/api/v1/orgs/pt-absent-c/hosts")
	wantStatus(t, "GET hosts of C", r, http.StatusForbidden)
	if d := detailOf(t, r); d != "token is scoped to another organization" {
		t.Fatalf("detail = %q", d)
	}
	// A blocked grant is no grant.
	if _, err := f.s.PersonalTokens.BlockGrant(db.WithOrgScope(context.Background(), a), mustTokenID(t, f, a), a, f.owner); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, "GET hosts of A after the grant was revoked", f.s.Get(t, tok, "/api/v1/orgs/pt-absent-a/hosts"), http.StatusForbidden)
}

// mustTokenID reads the id of the one personal token holding a grant for org.
func mustTokenID(t *testing.T, f personalFixture, org uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT token_id FROM personal_token_grants WHERE org_id = $1`, org).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPersonalTokenCapVersusMembership(t *testing.T) {
	f := newPersonalFixture(t, "pt-cap")
	// Cap admin, member operator: the membership decides.
	a := f.org(t, "pt-cap-a", tenancy.RoleOperator)
	// Cap viewer, member admin: the cap decides.
	b := f.org(t, "pt-cap-b", tenancy.RoleAdmin)
	_, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleAdmin, b: tenancy.RoleViewer})

	wantStatus(t, "PUT settings/jobs of A", f.s.Put(t, tok, "/api/v1/orgs/pt-cap-a/settings/jobs", "",
		map[string]any{"jobs_require_pinned_hosts": true}), http.StatusForbidden)
	wantStatus(t, "POST hosts of A", f.s.Post(t, tok, "/api/v1/orgs/pt-cap-a/hosts",
		map[string]any{"name": "web-1", "zone_id": f.zone(t, a)}), http.StatusCreated)
	wantStatus(t, "POST hosts of B", f.s.Post(t, tok, "/api/v1/orgs/pt-cap-b/hosts",
		map[string]any{"name": "web-1", "zone_id": f.zone(t, b)}), http.StatusForbidden)
	wantStatus(t, "GET hosts of B", f.s.Get(t, tok, "/api/v1/orgs/pt-cap-b/hosts"), http.StatusOK)

	// Cap owner, member operator (a cap no create would accept, held since
	// before a demotion): the membership decides, never the cap.
	c := f.org(t, "pt-cap-c", tenancy.RoleOperator)
	_, ownerCap := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{c: tenancy.RoleOwner})
	wantStatus(t, "POST hosts of C, owner cap", f.s.Post(t, ownerCap, "/api/v1/orgs/pt-cap-c/hosts",
		map[string]any{"name": "web-1", "zone_id": f.zone(t, c)}), http.StatusCreated)
	wantStatus(t, "PUT settings/jobs of C, owner cap", f.s.Put(t, ownerCap, "/api/v1/orgs/pt-cap-c/settings/jobs", "",
		map[string]any{"jobs_require_pinned_hosts": true}), http.StatusForbidden)

	// Cap admin, member admin, then demoted to viewer after the grant was
	// created: the demotion takes effect at the next request.
	d := f.org(t, "pt-cap-d", tenancy.RoleAdmin)
	_, demoted := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{d: tenancy.RoleAdmin})
	wantStatus(t, "PUT settings/jobs of D before the demotion", f.s.Put(t, demoted, "/api/v1/orgs/pt-cap-d/settings/jobs", "",
		map[string]any{"jobs_require_pinned_hosts": true}), http.StatusOK)
	resp := requestJSON(t, f.s.LoginAs(f.owner), http.MethodPatch, f.s.HTTP.URL+"/api/v1/orgs/pt-cap-d/members/"+f.user.String(),
		map[string]any{"role": "viewer"})
	if resp.StatusCode/100 != 2 {
		t.Fatalf("demote: status %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	wantStatus(t, "PUT settings/jobs of D after the demotion", f.s.Put(t, demoted, "/api/v1/orgs/pt-cap-d/settings/jobs", "",
		map[string]any{"jobs_require_pinned_hosts": false}), http.StatusForbidden)
	wantStatus(t, "POST hosts of D after the demotion", f.s.Post(t, demoted, "/api/v1/orgs/pt-cap-d/hosts",
		map[string]any{"name": "web-1", "zone_id": f.zone(t, d)}), http.StatusForbidden)
	wantStatus(t, "GET hosts of D after the demotion", f.s.Get(t, demoted, "/api/v1/orgs/pt-cap-d/hosts"), http.StatusOK)
}

func TestPersonalTokenMembershipLost(t *testing.T) {
	f := newPersonalFixture(t, "pt-lost")
	a := f.org(t, "pt-lost-a", tenancy.RoleOperator)
	_, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOperator})
	wantStatus(t, "before", f.s.Get(t, tok, "/api/v1/orgs/pt-lost-a/hosts"), http.StatusOK)

	resp := requestJSON(t, f.s.LoginAs(f.owner), http.MethodDelete, f.s.HTTP.URL+"/api/v1/orgs/pt-lost-a/members/"+f.user.String(), nil)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("remove member: status %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	wantStatus(t, "after removal", f.s.Get(t, tok, "/api/v1/orgs/pt-lost-a/hosts"), http.StatusForbidden)
	// Rejoining does not revive the grant: it was deleted with the membership.
	f.s.AddMember(a, f.user, tenancy.RoleOperator)
	r := f.s.Get(t, tok, "/api/v1/orgs/pt-lost-a/hosts")
	wantStatus(t, "after rejoining", r, http.StatusForbidden)
	if d := detailOf(t, r); d != "token is scoped to another organization" {
		t.Fatalf("detail = %q", d)
	}
}

func TestPersonalTokenUserDeactivated(t *testing.T) {
	f := newPersonalFixture(t, "pt-deact")
	a := f.org(t, "pt-deact-a", tenancy.RoleOperator)
	id, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOperator})
	_, adminClient := adminWithFactor(t, f.s, "pt-deact-admin@example.test")

	resp := postJSON(t, adminClient, f.s.HTTP.URL+adminUserPath(f.user, "deactivate"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deactivate: status %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	wantStatus(t, "after deactivation", f.s.Get(t, tok, "/api/v1/orgs/pt-deact-a/hosts"), http.StatusUnauthorized)
	var revoked bool
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT revoked_at IS NOT NULL FROM personal_tokens WHERE id = $1`, id).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("deactivation left the personal token unrevoked")
	}
}

func TestPersonalTokenSwitchOff(t *testing.T) {
	f := newPersonalFixture(t, "pt-switch")
	a := f.org(t, "pt-switch-a", tenancy.RoleOperator)
	_, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOperator})
	scoped := db.WithOrgScope(context.Background(), a)

	if err := f.s.PersonalTokens.SetAllowed(scoped, a, false); err != nil {
		t.Fatal(err)
	}
	r := f.s.Get(t, tok, "/api/v1/orgs/pt-switch-a/hosts")
	wantStatus(t, "switched off", r, http.StatusForbidden)
	if d := detailOf(t, r); d != "personal tokens are turned off for this organization" {
		t.Fatalf("detail = %q", d)
	}
	if err := f.s.PersonalTokens.SetAllowed(scoped, a, true); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, "switched on again", f.s.Get(t, tok, "/api/v1/orgs/pt-switch-a/hosts"), http.StatusOK)
}

func TestPersonalAssistantTokenRefusedOnREST(t *testing.T) {
	f := newPersonalFixture(t, "pt-assist")
	a := f.org(t, "pt-assist-a", tenancy.RoleOperator)
	_, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOperator}, tenancy.Area("jobs.read"))
	r := f.s.Get(t, tok, "/api/v1/orgs/pt-assist-a/hosts")
	wantStatus(t, "assistant token on REST", r, http.StatusForbidden)
	if d := detailOf(t, r); d != "assistant tokens work only at the assistant endpoint" {
		t.Fatalf("detail = %q", d)
	}
}

// TestPersonalTokenNeverInstanceAdmin (spec rule 3, controller ruling P5):
// the bearer credential is resolved only on the org-scoped routes, so on an
// instance-admin route a personal token is no principal at all (401, as for
// an organization token — see TestAdminUserRoutesGate's bearer case); were
// it ever resolved there, requireInstanceAdmin would refuse a token with
// 403. On the org routes it acts with the grant's cap, not as an instance
// administrator.
func TestPersonalTokenNeverInstanceAdmin(t *testing.T) {
	f := newPersonalFixture(t, "pt-iadmin")
	makeInstanceAdmin(t, f.s, f.user)
	a := f.org(t, "pt-iadmin-a", tenancy.RoleViewer)
	_, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleViewer})
	for _, p := range []string{"/api/v1/admin/users", "/api/v1/admin/orgs"} {
		r := f.s.Get(t, tok, p)
		if r.Code == http.StatusOK || r.Code/100 == 2 {
			t.Fatalf("GET %s with a personal token: status %d (%s)", p, r.Code, r.Body)
		}
	}
	// An organization the instance administrator is no member of stays shut.
	f.s.SeedOrg("pt-iadmin-x", f.owner)
	wantStatus(t, "GET hosts of a foreign org", f.s.Get(t, tok, "/api/v1/orgs/pt-iadmin-x/hosts"), http.StatusForbidden)
}

// TestJobStreamEndsWhenGrantRevoked (spec rule 13): a live job stream opened
// with a personal token re-checks the grant, so revoking it or turning the
// organization's switch off ends the stream within one re-check.
func TestJobStreamEndsWhenGrantRevoked(t *testing.T) {
	cases := map[string]func(f runFixture, tokenID, owner uuid.UUID){
		"grant revoked": func(f runFixture, tokenID, _ uuid.UUID) {
			if _, err := f.s.PersonalTokens.BlockGrant(db.WithOrgScope(context.Background(), f.orgID), tokenID, f.orgID, uuid.Nil); err != nil {
				t.Fatal(err)
			}
		},
		// Fix round 2: the block is per person and organization, so an
		// admin's revocation of another token's grant ends this one too.
		"other token's grant revoked": func(f runFixture, _, owner uuid.UUID) {
			other, _ := f.s.PersonalToken(t, owner, map[uuid.UUID]tenancy.Role{f.orgID: tenancy.RoleViewer})
			if _, err := f.s.PersonalTokens.BlockGrant(db.WithOrgScope(context.Background(), f.orgID), other, f.orgID, uuid.Nil); err != nil {
				t.Fatal(err)
			}
		},
		"switched off": func(f runFixture, _, _ uuid.UUID) {
			if err := f.s.PersonalTokens.SetAllowed(db.WithOrgScope(context.Background(), f.orgID), f.orgID, false); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, revoke := range cases {
		t.Run(name, func(t *testing.T) {
			slug := "pt-sse-" + map[string]string{"grant revoked": "grant", "other token's grant revoked": "other", "switched off": "switch"}[name]
			f := newRunFixture(t, slug)
			jobID := mustUUID(t, runJob(t, f.operatorC, f.runURL())[0].ID)
			watcher := f.s.SeedUser(slug+"-watcher@example.test", "correct horse battery")
			f.s.AddMember(f.orgID, watcher, tenancy.RoleViewer)
			tokenID, tok := f.s.PersonalToken(t, watcher, map[uuid.UUID]tenancy.Role{f.orgID: tenancy.RoleViewer})

			sctx, scancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer scancel()
			req, err := http.NewRequestWithContext(sctx, http.MethodGet, f.baseURL()+"/"+jobID.String()+"/stream", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("open stream: status %d (%s)", resp.StatusCode, mustReadBody(t, resp))
			}
			br := bufio.NewReader(resp.Body)
			if first := readSSEFrame(t, br); first.event != "status" {
				t.Fatalf("first frame = %q, want status", first.event)
			}
			events := streamEvents(br)

			// While the grant holds, the stream outlives a re-check of
			// access: the personal token is not refused for having no
			// organization of its own.
			deadline := time.After(jobStreamRecheck + time.Second)
		alive:
			for {
				select {
				case ev, ok := <-events:
					if !ok || ev != "log" {
						t.Fatalf("stream ended (%q) while the grant held", ev)
					}
				case <-deadline:
					break alive
				}
			}

			revoke(f, tokenID, watcher)

			for ev := range events {
				if ev == "error" {
					break
				}
				if ev != "log" {
					t.Fatalf("frame %q before the stream ended, want error", ev)
				}
			}
			if _, ok := <-events; ok {
				t.Fatal("stream must end after the grant ended")
			}
		})
	}
}

// TestActivityShowsPersonalTokenName (spec rule 12): an entry made with a
// personal token shows the token's name in the organization's feed, in
// cursor and in page mode.
func TestActivityShowsPersonalTokenName(t *testing.T) {
	f := newPersonalFixture(t, "pt-activity")
	a := f.org(t, "pt-activity-a", tenancy.RoleOperator)
	_, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOperator})
	wantStatus(t, "POST hosts", f.s.Post(t, tok, "/api/v1/orgs/pt-activity-a/hosts",
		map[string]any{"name": "web-1", "zone_id": f.zone(t, a)}), http.StatusCreated)

	owner := f.s.LoginAs(f.owner)
	for _, query := range []string{"", "?page=1"} {
		resp := requestJSON(t, owner, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/pt-activity-a/activity"+query, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("activity%s: status %d (%s)", query, resp.StatusCode, mustReadBody(t, resp))
		}
		var body struct {
			Items []struct {
				TokenName string `json:"token_name"`
			} `json:"items"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		found := false
		for _, it := range body.Items {
			if it.TokenName == "laptop" {
				found = true
			}
		}
		if !found {
			t.Fatalf("activity%s: no entry names the personal token: %+v", query, body.Items)
		}
	}
}

// jobStreamRecheck is the stream's access re-check interval
// (jobStreamAccessInterval in job_sse.go).
const jobStreamRecheck = 2 * time.Second

// streamEvents reads the stream's frames in the background and sends each
// frame's event name; the channel closes when the stream ends.
func streamEvents(br *bufio.Reader) <-chan string {
	out := make(chan string, 64)
	go func() {
		defer close(out)
		event := ""
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSuffix(line, "\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case line == "":
				out <- event
				event = ""
			}
		}
	}()
	return out
}
