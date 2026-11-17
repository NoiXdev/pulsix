// An organization's view of personal tokens and its switch (backlog 3.100,
// spec rules 6 and 12, "an admin's revocation sticks", the per-organization
// switch "Allow personal tokens").
package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/tenancy"
)

type ptEntry struct {
	TokenID      string  `json:"token_id"`
	Name         string  `json:"name"`
	Kind         string  `json:"kind"`
	OwnerID      string  `json:"owner_id"`
	OwnerName    string  `json:"owner_name"`
	OwnerEmail   string  `json:"owner_email"`
	Role         string  `json:"role"`
	Blocked      bool    `json:"blocked"`
	TokenRevoked bool    `json:"token_revoked"`
	OwnerBlocked bool    `json:"owner_blocked"`
	LastUsedAt   *string `json:"last_used_at"`
	CreatedAt    string  `json:"created_at"`
}

// entries lists slug's personal token entries as client c.
func (f ptFixture) entries(t *testing.T, c *http.Client, slug string) []ptEntry {
	t.Helper()
	code, raw := f.call(t, c, http.MethodGet, "/api/v1/orgs/"+slug+"/personal-tokens", nil)
	if code != http.StatusOK {
		t.Fatalf("entries of %s: %d (%s)", slug, code, raw)
	}
	var out struct {
		Entries []ptEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out.Entries
}

func grantState(tok ptToken, slug string) string {
	for _, g := range tok.Grants {
		if g.OrgSlug == slug {
			return g.State
		}
	}
	return ""
}

func TestAdminRevocationSticks(t *testing.T) {
	f := newPTFixture(t, "pt-sticky")
	b := f.org(t, "pt-sticky-b", tenancy.RoleOperator)
	f.org(t, "pt-sticky-a", tenancy.RoleOperator)
	admin := f.s.LoginAs(f.owner)
	tok := f.create(t, map[string]any{"name": "deploy", "grants": grantsOf("pt-sticky-a", "operator", "pt-sticky-b", "viewer")})
	wantStatus(t, "B before", f.s.Get(t, tok.Token, "/api/v1/orgs/pt-sticky-b/hosts"), http.StatusOK)

	if code, raw := f.call(t, admin, http.MethodDelete, "/api/v1/orgs/pt-sticky-b/personal-tokens/"+tok.ID, nil); code != http.StatusOK {
		t.Fatalf("admin revoke: %d (%s)", code, raw)
	}
	wantStatus(t, "B after the admin's revocation", f.s.Get(t, tok.Token, "/api/v1/orgs/pt-sticky-b/hosts"), http.StatusForbidden)
	wantStatus(t, "A still works", f.s.Get(t, tok.Token, "/api/v1/orgs/pt-sticky-a/hosts"), http.StatusOK)
	if code, raw := f.call(t, admin, http.MethodDelete, "/api/v1/orgs/pt-sticky-b/personal-tokens/"+tok.ID, nil); code != http.StatusConflict || rawDetail(t, raw) != "grant_blocked" {
		t.Fatalf("second admin revoke: %d (%s)", code, raw)
	}

	// The owner cannot list B again; keeping A only leaves B blocked.
	code, raw := f.patch(t, tok.ID, map[string]any{"grants": grantsOf("pt-sticky-a", "operator", "pt-sticky-b", "viewer")})
	if code != http.StatusConflict || rawDetail(t, raw) != "grant_blocked" {
		t.Fatalf("owner re-adding B: %d (%s)", code, raw)
	}
	if code, raw := f.patch(t, tok.ID, map[string]any{"grants": grantsOf("pt-sticky-a", "viewer")}); code != http.StatusOK {
		t.Fatalf("owner editing A: %d (%s)", code, raw)
	}
	if got := grantState(f.list(t)[0], "pt-sticky-b"); got != "blocked" {
		t.Fatalf("B state = %q, want blocked", got)
	}
	if e := f.entries(t, admin, "pt-sticky-b"); len(e) != 1 || !e[0].Blocked {
		t.Fatalf("B entries = %+v", e)
	}

	// The admin lifts the block; the owner adds B again.
	if code, raw := f.call(t, admin, http.MethodPost, "/api/v1/orgs/pt-sticky-b/personal-tokens/"+tok.ID+"/unblock", nil); code != http.StatusOK {
		t.Fatalf("unblock: %d (%s)", code, raw)
	}
	if code, _ := f.call(t, admin, http.MethodPost, "/api/v1/orgs/pt-sticky-b/personal-tokens/"+tok.ID+"/unblock", nil); code != http.StatusNotFound {
		t.Fatalf("second unblock: %d, want 404", code)
	}
	if code, raw := f.patch(t, tok.ID, map[string]any{"grants": grantsOf("pt-sticky-a", "viewer", "pt-sticky-b", "viewer")}); code != http.StatusOK {
		t.Fatalf("owner re-adding B after the unblock: %d (%s)", code, raw)
	}
	wantStatus(t, "B again", f.s.Get(t, tok.Token, "/api/v1/orgs/pt-sticky-b/hosts"), http.StatusOK)

	feed := auditRows(t, f.s, "org_id = $1", b)
	if got := strings.Join(actionsOf(feed), ","); got != "addPersonalTokenGrant,DELETE revokePersonalTokenGrant,POST unblockPersonalTokenGrant,addPersonalTokenGrant" {
		t.Fatalf("B feed = %s", got)
	}
	if feed[1].Summary != "revoked personal token deploy (viewer) of pt-sticky-user@example.test" {
		t.Fatalf("revoke summary = %q", feed[1].Summary)
	}
	if feed[2].Summary != "lifted the block on personal tokens of pt-sticky-user@example.test (blocked with deploy)" {
		t.Fatalf("unblock summary = %q", feed[2].Summary)
	}
}

// TestAdminCannotWidenGrant: no organization route takes a cap; an admin
// can only revoke a grant or lift a block.
func TestAdminCannotWidenGrant(t *testing.T) {
	f := newPTFixture(t, "pt-widen")
	a := f.org(t, "pt-widen-a", tenancy.RoleOperator)
	admin := f.s.LoginAs(f.owner)
	tok := f.create(t, map[string]any{"name": "deploy", "grants": grantsOf("pt-widen-a", "viewer")})
	base := "/api/v1/orgs/pt-widen-a/personal-tokens"
	for _, try := range []struct{ method, path string }{
		{http.MethodPost, base},
		{http.MethodPut, base + "/" + tok.ID},
		{http.MethodPatch, base + "/" + tok.ID},
	} {
		code, raw := f.call(t, admin, try.method, try.path, map[string]any{"role": "owner", "token_id": tok.ID})
		if code/100 == 2 {
			t.Fatalf("%s %s accepted a cap: %d (%s)", try.method, try.path, code, raw)
		}
	}
	// Unblocking a grant that is not blocked restores nothing.
	if code, _ := f.call(t, admin, http.MethodPost, base+"/"+tok.ID+"/unblock", map[string]any{"role": "owner"}); code/100 == 2 {
		t.Fatalf("unblock of an active grant succeeded")
	}
	if e := f.entries(t, admin, "pt-widen-a"); len(e) != 1 || e[0].Role != "viewer" {
		t.Fatalf("entries = %+v", e)
	}
	wantStatus(t, "still a viewer", f.s.Post(t, tok.Token, "/api/v1/orgs/pt-widen-a/hosts", map[string]any{"name": "x", "zone_id": f.zone(t, a)}), http.StatusForbidden)
}

func TestSwitchOffThenOn(t *testing.T) {
	f := newPTFixture(t, "pt-switch")
	a := f.org(t, "pt-switch-a", tenancy.RoleOperator)
	f.org(t, "pt-switch-b", tenancy.RoleOperator)
	admin := f.s.LoginAs(f.owner)
	settings := "/api/v1/orgs/pt-switch-a/settings/personal-tokens"
	plain := f.create(t, map[string]any{"name": "plain", "grants": grantsOf("pt-switch-a", "viewer")})
	// The blocked grant belongs to another member: a block is per person,
	// so one on the user's own token would stop "plain" as well.
	other := f.s.SeedUser("pt-switch-other@example.test", "correct horse battery")
	f.s.AddMember(a, other, tenancy.RoleOperator)
	blockedID, blockedToken := f.s.PersonalToken(t, other, map[uuid.UUID]tenancy.Role{a: tenancy.RoleViewer})
	if code, raw := f.call(t, f.c, http.MethodGet, settings, nil); code != http.StatusOK || !strings.Contains(raw, `"allowed":true`) {
		t.Fatalf("GET settings as an operator: %d (%s)", code, raw)
	}
	if code, _ := f.call(t, f.c, http.MethodPut, settings, map[string]any{"allowed": false}); code != http.StatusForbidden {
		t.Fatalf("PUT settings as an operator: %d, want 403", code)
	}
	if code, raw := f.call(t, admin, http.MethodPut, settings, map[string]any{"allowed": false}); code != http.StatusOK || !strings.Contains(raw, `"allowed":false`) {
		t.Fatalf("switch off: %d (%s)", code, raw)
	}

	r := f.s.Get(t, plain.Token, "/api/v1/orgs/pt-switch-a/hosts")
	wantStatus(t, "existing grant while off", r, http.StatusForbidden)
	if d := detailOf(t, r); d != "personal tokens are turned off for this organization" {
		t.Fatalf("detail = %q", d)
	}
	if got := grantState(f.list(t)[0], "pt-switch-a"); got != "off" {
		t.Fatalf("state while off = %q", got)
	}
	code, raw := f.call(t, f.c, http.MethodPost, myTokensPath, map[string]any{"name": "new", "grants": grantsOf("pt-switch-a", "viewer")})
	if code != http.StatusConflict || rawDetail(t, raw) != "personal_tokens_off" {
		t.Fatalf("new grant while off: %d (%s)", code, raw)
	}
	if code, raw := f.patch(t, plain.ID, map[string]any{"grants": grantsOf("pt-switch-a", "operator")}); code != http.StatusConflict || rawDetail(t, raw) != "personal_tokens_off" {
		t.Fatalf("raising a cap while off: %d (%s)", code, raw)
	}

	// An admin revokes one grant while the switch is off (the block is per
	// person, so it comes after the owner's checks above).
	if code, raw := f.call(t, admin, http.MethodDelete, "/api/v1/orgs/pt-switch-a/personal-tokens/"+blockedID.String(), nil); code != http.StatusOK {
		t.Fatalf("admin revoke: %d (%s)", code, raw)
	}

	if code, raw := f.call(t, admin, http.MethodPut, settings, map[string]any{"allowed": true}); code != http.StatusOK {
		t.Fatalf("switch on: %d (%s)", code, raw)
	}
	wantStatus(t, "existing grant on again", f.s.Get(t, plain.Token, "/api/v1/orgs/pt-switch-a/hosts"), http.StatusOK)
	wantStatus(t, "blocked grant stays blocked", f.s.Get(t, blockedToken, "/api/v1/orgs/pt-switch-a/hosts"), http.StatusForbidden)

	feed := auditRows(t, f.s, "org_id = $1 AND action = 'PUT updatePersonalTokenSettings'", a)
	if len(feed) != 2 || feed[0].Summary != "turned personal tokens off" || feed[1].Summary != "turned personal tokens on" || feed[0].ObjectType != "org_settings" {
		t.Fatalf("switch events = %+v", feed)
	}
}

// TestOrgPersonalTokenRoutesRefuseTokens: unblocking and the switch need
// the person at the browser; listing and revoking accept a token with the
// right to manage tokens, like the organization token routes.
func TestOrgPersonalTokenRoutesRefuseTokens(t *testing.T) {
	f := newPTFixture(t, "pt-orgbearer")
	a := f.org(t, "pt-orgbearer-a", tenancy.RoleOperator)
	tok := f.create(t, map[string]any{"name": "deploy", "grants": grantsOf("pt-orgbearer-a", "viewer")})
	_, adminTok := f.s.PersonalToken(t, f.owner, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOwner})

	wantStatus(t, "list by an admin's token", f.s.Get(t, adminTok, "/api/v1/orgs/pt-orgbearer-a/personal-tokens"), http.StatusOK)
	wantStatus(t, "switch by a token", f.s.Put(t, adminTok, "/api/v1/orgs/pt-orgbearer-a/settings/personal-tokens", "", map[string]any{"allowed": false}), http.StatusForbidden)
	wantStatus(t, "revoke by an admin's token", f.s.Delete(t, adminTok, "/api/v1/orgs/pt-orgbearer-a/personal-tokens/"+tok.ID, ""), http.StatusOK)
	wantStatus(t, "unblock by a token", f.s.Post(t, adminTok, "/api/v1/orgs/pt-orgbearer-a/personal-tokens/"+tok.ID+"/unblock", nil), http.StatusForbidden)
	// The owner's own token cannot reach the list through a grant below admin.
	wantStatus(t, "list by a viewer's token", f.s.Get(t, tok.Token, "/api/v1/orgs/pt-orgbearer-a/personal-tokens"), http.StatusForbidden)
}

func TestOrgViewListsOnlyOwnGrants(t *testing.T) {
	f := newPTFixture(t, "pt-view")
	f.org(t, "pt-view-a", tenancy.RoleOperator)
	f.org(t, "pt-view-b", tenancy.RoleAdmin)
	admin := f.s.LoginAs(f.owner)
	both := f.create(t, map[string]any{"name": "both", "grants": grantsOf("pt-view-a", "operator", "pt-view-b", "admin")})
	f.create(t, map[string]any{"name": "b-only", "grants": grantsOf("pt-view-b", "viewer")})
	wantStatus(t, "use on A", f.s.Get(t, both.Token, "/api/v1/orgs/pt-view-a/hosts"), http.StatusOK)

	entries := f.entries(t, admin, "pt-view-a")
	if len(entries) != 1 {
		t.Fatalf("A entries = %+v", entries)
	}
	e := entries[0]
	if e.TokenID != both.ID || e.Name != "both" || e.Kind != "api" || e.Role != "operator" || e.Blocked ||
		e.OwnerID != f.user.String() || e.OwnerEmail != "pt-view-user@example.test" || e.LastUsedAt == nil || e.CreatedAt == "" {
		t.Fatalf("A entry = %+v", e)
	}
	if got := f.entries(t, admin, "pt-view-b"); len(got) != 2 || got[0].Role != "admin" || got[0].LastUsedAt != nil {
		t.Fatalf("B entries = %+v", got)
	}
	// An operator is no admin of the token page.
	if code, _ := f.call(t, f.c, http.MethodGet, "/api/v1/orgs/pt-view-a/personal-tokens", nil); code != http.StatusForbidden {
		t.Fatalf("operator's list: %d, want 403", code)
	}
	// A's admin cannot reach B's grant through A.
	if code, _ := f.call(t, admin, http.MethodDelete, "/api/v1/orgs/pt-view-a/personal-tokens/"+f.list(t)[1].ID, nil); code != http.StatusNotFound {
		t.Fatalf("revoking B's grant through A: %d, want 404", code)
	}
}

// TestAdminBlockIsPerPerson (fix round 1): an admin's revocation keeps the
// person out of the organization with every token — a new one, and one made
// after revoking the blocked token — until an admin lifts the block, which
// the admin can still do from the revoked token's entry.
func TestAdminBlockIsPerPerson(t *testing.T) {
	f := newPTFixture(t, "pt-person")
	f.org(t, "pt-person-a", tenancy.RoleOperator)
	f.org(t, "pt-person-b", tenancy.RoleOperator)
	admin := f.s.LoginAs(f.owner)
	tok := f.create(t, map[string]any{"name": "deploy", "grants": grantsOf("pt-person-a", "viewer", "pt-person-b", "viewer")})
	if code, raw := f.call(t, admin, http.MethodDelete, "/api/v1/orgs/pt-person-b/personal-tokens/"+tok.ID, nil); code != http.StatusOK {
		t.Fatalf("admin revoke: %d (%s)", code, raw)
	}
	newToken := map[string]any{"name": "fresh", "grants": grantsOf("pt-person-b", "viewer")}
	if code, raw := f.call(t, f.c, http.MethodPost, myTokensPath, newToken); code != http.StatusConflict || rawDetail(t, raw) != "grant_blocked" {
		t.Fatalf("a new token for B: %d (%s)", code, raw)
	}
	if code, raw := f.call(t, f.c, http.MethodDelete, myTokensPath+"/"+tok.ID, nil); code != http.StatusOK {
		t.Fatalf("owner revoke: %d (%s)", code, raw)
	}
	if code, raw := f.call(t, f.c, http.MethodPost, myTokensPath, newToken); code != http.StatusConflict || rawDetail(t, raw) != "grant_blocked" {
		t.Fatalf("a new token for B after revoking the blocked one: %d (%s)", code, raw)
	}
	entries := f.entries(t, admin, "pt-person-b")
	if len(entries) != 1 || entries[0].TokenID != tok.ID || !entries[0].Blocked || !entries[0].TokenRevoked {
		t.Fatalf("B entries = %+v", entries)
	}
	if code, raw := f.call(t, admin, http.MethodPost, "/api/v1/orgs/pt-person-b/personal-tokens/"+tok.ID+"/unblock", nil); code != http.StatusOK {
		t.Fatalf("unblock: %d (%s)", code, raw)
	}
	fresh := f.create(t, newToken)
	wantStatus(t, "B with the new token", f.s.Get(t, fresh.Token, "/api/v1/orgs/pt-person-b/hosts"), http.StatusOK)
}

// TestPersonalBlockStopsOtherTokens (fix round 2): the block is per person
// and organization, so it also stops the person's other tokens there at
// use, and nowhere else. Lifting it lets those work again; the blocked
// token's own grant was deleted with the block, so the owner adds it back.
func TestPersonalBlockStopsOtherTokens(t *testing.T) {
	f := newPTFixture(t, "pt-others")
	f.org(t, "pt-others-a", tenancy.RoleOperator)
	f.org(t, "pt-others-b", tenancy.RoleOperator)
	admin := f.s.LoginAs(f.owner)
	t1 := f.create(t, map[string]any{"name": "one", "grants": grantsOf("pt-others-b", "viewer")})
	t2 := f.create(t, map[string]any{"name": "two", "grants": grantsOf("pt-others-a", "viewer", "pt-others-b", "viewer")})
	if code, raw := f.call(t, admin, http.MethodDelete, "/api/v1/orgs/pt-others-b/personal-tokens/"+t1.ID, nil); code != http.StatusOK {
		t.Fatalf("admin revoke: %d (%s)", code, raw)
	}
	r := f.s.Get(t, t2.Token, "/api/v1/orgs/pt-others-b/hosts")
	wantStatus(t, "T2 in B while T1 is blocked", r, http.StatusForbidden)
	if d := detailOf(t, r); d != "token is scoped to another organization" {
		t.Fatalf("detail = %q", d)
	}
	wantStatus(t, "T2 in A", f.s.Get(t, t2.Token, "/api/v1/orgs/pt-others-a/hosts"), http.StatusOK)

	if code, raw := f.call(t, admin, http.MethodPost, "/api/v1/orgs/pt-others-b/personal-tokens/"+t1.ID+"/unblock", nil); code != http.StatusOK {
		t.Fatalf("unblock: %d (%s)", code, raw)
	}
	wantStatus(t, "T2 in B after the unblock", f.s.Get(t, t2.Token, "/api/v1/orgs/pt-others-b/hosts"), http.StatusOK)
	wantStatus(t, "T1 in B after the unblock, before re-adding", f.s.Get(t, t1.Token, "/api/v1/orgs/pt-others-b/hosts"), http.StatusForbidden)
	if code, raw := f.patch(t, t1.ID, map[string]any{"grants": grantsOf("pt-others-b", "viewer")}); code != http.StatusOK {
		t.Fatalf("owner re-adding B to T1: %d (%s)", code, raw)
	}
	wantStatus(t, "T1 in B after re-adding", f.s.Get(t, t1.Token, "/api/v1/orgs/pt-others-b/hosts"), http.StatusOK)
}

// TestOrgViewMarksBlockedOwner (Task 19): every entry of a person blocked in
// the organization carries owner_blocked, since the block stops all their
// tokens there, and the block can be lifted through any of those entries.
func TestOrgViewMarksBlockedOwner(t *testing.T) {
	f := newPTFixture(t, "pt-ownerblk")
	f.org(t, "pt-ownerblk-a", tenancy.RoleOperator)
	b := f.org(t, "pt-ownerblk-b", tenancy.RoleOperator)
	admin := f.s.LoginAs(f.owner)
	t1 := f.create(t, map[string]any{"name": "one", "grants": grantsOf("pt-ownerblk-b", "viewer")})
	t2 := f.create(t, map[string]any{"name": "two", "grants": grantsOf("pt-ownerblk-a", "viewer", "pt-ownerblk-b", "viewer")})
	for _, e := range f.entries(t, admin, "pt-ownerblk-b") {
		if e.Blocked || e.OwnerBlocked {
			t.Fatalf("entry before any block = %+v", e)
		}
	}
	if code, raw := f.call(t, admin, http.MethodDelete, "/api/v1/orgs/pt-ownerblk-b/personal-tokens/"+t1.ID, nil); code != http.StatusOK {
		t.Fatalf("admin revoke: %d (%s)", code, raw)
	}
	byID := map[string]ptEntry{}
	for _, e := range f.entries(t, admin, "pt-ownerblk-b") {
		byID[e.TokenID] = e
	}
	if e := byID[t1.ID]; !e.Blocked || !e.OwnerBlocked {
		t.Fatalf("blocked entry = %+v; want blocked and owner_blocked", e)
	}
	if e := byID[t2.ID]; e.Blocked || !e.OwnerBlocked {
		t.Fatalf("other token's entry = %+v; want owner_blocked only", e)
	}
	if e := f.entries(t, admin, "pt-ownerblk-a"); len(e) != 1 || e[0].OwnerBlocked {
		t.Fatalf("A entries = %+v; want not owner_blocked", e)
	}
	// The block is lifted through the entry that was never blocked itself.
	if code, raw := f.call(t, admin, http.MethodPost, "/api/v1/orgs/pt-ownerblk-b/personal-tokens/"+t2.ID+"/unblock", nil); code != http.StatusOK {
		t.Fatalf("unblock through the other entry: %d (%s)", code, raw)
	}
	// The audit names the entry it went through, which was not blocked itself.
	feed := auditRows(t, f.s, "org_id = $1 AND action = $2", b, "POST unblockPersonalTokenGrant")
	if len(feed) != 1 || feed[0].Summary != "lifted the block on personal tokens of pt-ownerblk-user@example.test (through two)" {
		t.Fatalf("unblock feed = %+v", feed)
	}
	entries := f.entries(t, admin, "pt-ownerblk-b")
	if len(entries) != 1 || entries[0].TokenID != t2.ID || entries[0].OwnerBlocked || entries[0].Blocked {
		t.Fatalf("B entries after the unblock = %+v", entries)
	}
	wantStatus(t, "T2 in B after the unblock", f.s.Get(t, t2.Token, "/api/v1/orgs/pt-ownerblk-b/hosts"), http.StatusOK)
	if code, _ := f.call(t, admin, http.MethodPost, "/api/v1/orgs/pt-ownerblk-b/personal-tokens/"+t2.ID+"/unblock", nil); code != http.StatusNotFound {
		t.Fatalf("unblock without a block: %d, want 404", code)
	}
}
