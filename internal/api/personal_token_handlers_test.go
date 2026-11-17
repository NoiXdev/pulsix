// The owner's personal token routes (backlog 3.100, spec docs/superpowers/
// specs/2026-11-17-personal-tokens-design.md rules 1, 5, 9, 10, 12):
// /api/v1/auth/me/tokens, cookie sessions only.
package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

const myTokensPath = "/api/v1/auth/me/tokens"

type ptGrant struct {
	OrgID      string  `json:"org_id"`
	OrgSlug    string  `json:"org_slug"`
	OrgName    string  `json:"org_name"`
	Role       string  `json:"role"`
	State      string  `json:"state"`
	LastUsedAt *string `json:"last_used_at"`
}

type ptToken struct {
	Token      string    `json:"token"`
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Areas      []string  `json:"areas"`
	ExpiresAt  *string   `json:"expires_at"`
	LastUsedAt *string   `json:"last_used_at"`
	CreatedAt  string    `json:"created_at"`
	Grants     []ptGrant `json:"grants"`
}

// ptFixture is newPersonalFixture with the user's session.
type ptFixture struct {
	personalFixture
	c *http.Client
}

func newPTFixture(t *testing.T, name string, opts ...apitest.Option) ptFixture {
	t.Helper()
	s := apitest.New(t, opts...)
	f := personalFixture{
		s:     s,
		owner: s.SeedUser(name+"-owner@example.test", "correct horse battery"),
		user:  s.SeedUser(name+"-user@example.test", "correct horse battery"),
	}
	return ptFixture{personalFixture: f, c: s.LoginAs(f.user)}
}

func grantsOf(pairs ...string) []map[string]any {
	out := make([]map[string]any, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, map[string]any{"org": pairs[i], "role": pairs[i+1]})
	}
	return out
}

// call sends method path body as client c and returns status and body.
func (f ptFixture) call(t *testing.T, c *http.Client, method, path string, body any) (int, string) {
	t.Helper()
	resp := requestJSON(t, c, method, f.s.HTTP.URL+path, body)
	return resp.StatusCode, mustReadBody(t, resp)
}

func (f ptFixture) create(t *testing.T, body map[string]any) ptToken {
	t.Helper()
	code, raw := f.call(t, f.c, http.MethodPost, myTokensPath, body)
	if code != http.StatusCreated {
		t.Fatalf("create: status %d (%s)", code, raw)
	}
	var tok ptToken
	if err := json.Unmarshal([]byte(raw), &tok); err != nil {
		t.Fatal(err)
	}
	return tok
}

func (f ptFixture) list(t *testing.T) []ptToken {
	t.Helper()
	code, raw := f.call(t, f.c, http.MethodGet, myTokensPath, nil)
	if code != http.StatusOK {
		t.Fatalf("list: status %d (%s)", code, raw)
	}
	var out struct {
		Tokens []ptToken `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out.Tokens
}

func (f ptFixture) patch(t *testing.T, id string, body map[string]any) (int, string) {
	t.Helper()
	return f.call(t, f.c, http.MethodPatch, myTokensPath+"/"+id, body)
}

func rawDetail(t *testing.T, raw string) string {
	t.Helper()
	var p struct {
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal([]byte(raw), &p)
	return p.Detail
}

// auditRow is one stored event, every text column of it.
type auditRow struct {
	OrgID      *uuid.UUID
	ActorID    *uuid.UUID
	Action     string
	ObjectType string
	ObjectID   string
	Summary    string
	Meta       string
}

func auditRows(t *testing.T, s *apitest.Server, where string, args ...any) []auditRow {
	t.Helper()
	rows, err := s.DB.Pool().Query(context.Background(),
		`SELECT org_id, actor_id, action, object_type, object_id, summary, meta::text FROM audit_events WHERE `+where+` ORDER BY at, id`, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.OrgID, &r.ActorID, &r.Action, &r.ObjectType, &r.ObjectID, &r.Summary, &r.Meta); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func actionsOf(rows []auditRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Action
	}
	return out
}

func TestViewerCanCreatePersonalToken(t *testing.T) {
	f := newPTFixture(t, "pt-viewer")
	f.org(t, "pt-viewer-a", tenancy.RoleViewer)
	tok := f.create(t, map[string]any{"name": "laptop", "grants": grantsOf("pt-viewer-a", "viewer")})
	if !strings.HasPrefix(tok.Token, "pxp_") || tok.Kind != "api" || tok.Name != "laptop" {
		t.Fatalf("created = %+v", tok)
	}
	if len(tok.Grants) != 1 || tok.Grants[0].OrgSlug != "pt-viewer-a" || tok.Grants[0].Role != "viewer" || tok.Grants[0].State != "active" {
		t.Fatalf("grants = %+v", tok.Grants)
	}
	wantStatus(t, "GET hosts with the new token", f.s.Get(t, tok.Token, "/api/v1/orgs/pt-viewer-a/hosts"), http.StatusOK)
	// The list never carries the secret.
	listed := f.list(t)
	if len(listed) != 1 || listed[0].Token != "" || listed[0].ID != tok.ID {
		t.Fatalf("list = %+v", listed)
	}
}

func TestCreatePersonalTokenRefusals(t *testing.T) {
	f := newPTFixture(t, "pt-refuse")
	f.org(t, "pt-refuse-a", tenancy.RoleOperator)
	f.org(t, "pt-refuse-n", "") // not a member
	off := f.org(t, "pt-refuse-off", tenancy.RoleOwner)
	if err := f.s.PersonalTokens.SetAllowed(context.Background(), off, false); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		body   map[string]any
		code   int
		detail string
	}{
		{"cap above role", map[string]any{"name": "x", "grants": grantsOf("pt-refuse-a", "admin")}, http.StatusUnprocessableEntity,
			"the role cap is above your role in organization pt-refuse-a"},
		{"not a member", map[string]any{"name": "x", "grants": grantsOf("pt-refuse-n", "viewer")}, http.StatusUnprocessableEntity,
			"you are not a member of organization pt-refuse-n"},
		{"unknown organization reads the same", map[string]any{"name": "x", "grants": grantsOf("pt-refuse-nowhere", "viewer")}, http.StatusUnprocessableEntity,
			"you are not a member of organization pt-refuse-nowhere"},
		{"switched off", map[string]any{"name": "x", "grants": grantsOf("pt-refuse-off", "viewer")}, http.StatusConflict, "personal_tokens_off"},
		{"no grant", map[string]any{"name": "x", "grants": []any{}}, http.StatusUnprocessableEntity, ""},
		{"assistant area beyond the cap", map[string]any{"name": "x", "kind": "assistant", "areas": []string{"inventory.write"},
			"grants": grantsOf("pt-refuse-a", "viewer")}, http.StatusUnprocessableEntity, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := f.call(t, f.c, http.MethodPost, myTokensPath, tc.body)
			if code != tc.code {
				t.Fatalf("status %d (%s), want %d", code, raw, tc.code)
			}
			if tc.detail != "" && rawDetail(t, raw) != tc.detail {
				t.Fatalf("detail = %q, want %q", rawDetail(t, raw), tc.detail)
			}
		})
	}
	if got := f.list(t); len(got) != 0 {
		t.Fatalf("refused creates stored %+v", got)
	}
}

func TestPersonalTokenLimit(t *testing.T) {
	f := newPTFixture(t, "pt-limit")
	a := f.org(t, "pt-limit-a", tenancy.RoleViewer)
	for range 50 {
		f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleViewer})
	}
	code, raw := f.call(t, f.c, http.MethodPost, myTokensPath, map[string]any{"name": "x", "grants": grantsOf("pt-limit-a", "viewer")})
	if code != http.StatusConflict || rawDetail(t, raw) != "personal_token_limit" {
		t.Fatalf("51st token: status %d (%s)", code, raw)
	}
}

// TestCreatePersonalTokenAnswersStoredPrecision (Review Focus 5): the
// create answer's created_at and expires_at equal what the list answers
// later, although the clock and the requested expiry carry nanoseconds.
func TestCreatePersonalTokenAnswersStoredPrecision(t *testing.T) {
	clock := func() time.Time { return time.Now().Truncate(time.Microsecond).Add(321 * time.Nanosecond) }
	f := newPTFixture(t, "pt-precision", apitest.WithPersonalTokenClock(clock))
	f.org(t, "pt-precision-a", tenancy.RoleAdmin)
	exp := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second).Add(123456789 * time.Nanosecond)
	// Sent in UTC: the answer must still read exactly as the list does,
	// whatever zone the database connection reports times in.
	created := f.create(t, map[string]any{"name": "ci", "expires_at": exp.UTC().Format(time.RFC3339Nano),
		"grants": grantsOf("pt-precision-a", "operator")})
	listed := f.list(t)
	if len(listed) != 1 || listed[0].ExpiresAt == nil || created.ExpiresAt == nil {
		t.Fatalf("listed %+v, created %+v", listed, created)
	}
	if listed[0].CreatedAt != created.CreatedAt || *listed[0].ExpiresAt != *created.ExpiresAt {
		t.Fatalf("create answered %s / %s, list %s / %s", created.CreatedAt, *created.ExpiresAt, listed[0].CreatedAt, *listed[0].ExpiresAt)
	}
	// And after an edit of the expiry, the edit's answer equals the list's.
	code, raw := f.patch(t, created.ID, map[string]any{"expires_at": exp.Add(time.Hour).UTC().Format(time.RFC3339Nano)})
	if code != http.StatusOK {
		t.Fatalf("patch: %d (%s)", code, raw)
	}
	var patched struct {
		Token ptToken `json:"token"`
	}
	if err := json.Unmarshal([]byte(raw), &patched); err != nil {
		t.Fatal(err)
	}
	if listed = f.list(t); *listed[0].ExpiresAt != *patched.Token.ExpiresAt || listed[0].CreatedAt != patched.Token.CreatedAt {
		t.Fatalf("patch answered %s, list %s", *patched.Token.ExpiresAt, *listed[0].ExpiresAt)
	}
}

// TestCreatePersonalTokenSecretNotInAudit (spec rule 9): no stored audit
// field carries the plaintext, its secret part or its hash.
func TestCreatePersonalTokenSecretNotInAudit(t *testing.T) {
	f := newPTFixture(t, "pt-secret")
	f.org(t, "pt-secret-a", tenancy.RoleAdmin)
	f.org(t, "pt-secret-b", tenancy.RoleOperator)
	tok := f.create(t, map[string]any{"name": "ci", "grants": grantsOf("pt-secret-a", "admin")})
	if code, raw := f.patch(t, tok.ID, map[string]any{"grants": grantsOf("pt-secret-a", "viewer", "pt-secret-b", "viewer")}); code != http.StatusOK {
		t.Fatalf("patch: %d (%s)", code, raw)
	}
	if code, raw := f.call(t, f.c, http.MethodDelete, myTokensPath+"/"+tok.ID, nil); code != http.StatusOK {
		t.Fatalf("revoke: %d (%s)", code, raw)
	}
	secret := strings.TrimPrefix(tok.Token, "pxp_")
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	needles := []string{tok.Token, secret, hex.EncodeToString(sum[:]), base64.StdEncoding.EncodeToString(sum[:])}
	rows := auditRows(t, f.s, "true")
	if len(rows) < 4 {
		t.Fatalf("only %d audit events recorded", len(rows))
	}
	for _, r := range rows {
		for _, field := range []string{r.Action, r.ObjectType, r.ObjectID, r.Summary, r.Meta} {
			for _, n := range needles {
				if strings.Contains(field, n) {
					t.Fatalf("audit event %q carries the secret in %q", r.Action, field)
				}
			}
		}
	}
}

// TestGrantChangesInOrgFeed (spec rule 12): the token's own events go to
// the owner's feed (no organization); each grant change also goes to the
// affected organization's feed.
func TestGrantChangesInOrgFeed(t *testing.T) {
	f := newPTFixture(t, "pt-feed")
	a := f.org(t, "pt-feed-a", tenancy.RoleAdmin)
	b := f.org(t, "pt-feed-b", tenancy.RoleOperator)
	tok := f.create(t, map[string]any{"name": "deploy", "grants": grantsOf("pt-feed-a", "operator")})
	for i, grants := range [][]map[string]any{
		grantsOf("pt-feed-a", "operator", "pt-feed-b", "viewer"), // add B
		grantsOf("pt-feed-a", "viewer", "pt-feed-b", "viewer"),   // change A
		grantsOf("pt-feed-a", "viewer"),                          // remove B
	} {
		if code, raw := f.patch(t, tok.ID, map[string]any{"grants": grants}); code != http.StatusOK {
			t.Fatalf("patch %d: %d (%s)", i, code, raw)
		}
	}

	own := auditRows(t, f.s, "org_id IS NULL AND actor_id = $1", f.user)
	if got := strings.Join(actionsOf(own), ","); got != "POST createPersonalToken,PATCH updatePersonalToken,PATCH updatePersonalToken,PATCH updatePersonalToken" {
		t.Fatalf("owner feed = %s", got)
	}
	for _, r := range own {
		if r.ObjectType != "personal_token" || r.ObjectID != tok.ID {
			t.Fatalf("owner event %+v", r)
		}
	}
	if !strings.Contains(own[0].Summary, "deploy") || !strings.Contains(own[0].Summary, "pt-feed-a") {
		t.Fatalf("create summary = %q", own[0].Summary)
	}

	feedA := auditRows(t, f.s, "org_id = $1", a)
	if got := strings.Join(actionsOf(feedA), ","); got != "addPersonalTokenGrant,changePersonalTokenGrant" {
		t.Fatalf("A feed = %s", got)
	}
	if feedA[0].Summary != "added personal token deploy (operator)" || feedA[1].Summary != "changed personal token deploy (operator → viewer)" {
		t.Fatalf("A summaries = %q / %q", feedA[0].Summary, feedA[1].Summary)
	}
	feedB := auditRows(t, f.s, "org_id = $1", b)
	if got := strings.Join(actionsOf(feedB), ","); got != "addPersonalTokenGrant,removePersonalTokenGrant" {
		t.Fatalf("B feed = %s", got)
	}
	if feedB[1].Summary != "removed personal token deploy (viewer)" {
		t.Fatalf("B summary = %q", feedB[1].Summary)
	}
	for _, r := range append(feedA, feedB...) {
		if r.ObjectType != "personal_token" || r.ObjectID != tok.ID || r.ActorID == nil || *r.ActorID != f.user {
			t.Fatalf("org event %+v", r)
		}
	}
}

// TestRevokeWholeTokenEndsEveryGrant (controller ruling P11): revoking the
// token ends it everywhere at once, and each organization's feed says so.
func TestRevokeWholeTokenEndsEveryGrant(t *testing.T) {
	f := newPTFixture(t, "pt-revoke")
	a := f.org(t, "pt-revoke-a", tenancy.RoleAdmin)
	b := f.org(t, "pt-revoke-b", tenancy.RoleViewer)
	tok := f.create(t, map[string]any{"name": "deploy", "grants": grantsOf("pt-revoke-a", "operator", "pt-revoke-b", "viewer")})
	wantStatus(t, "before, A", f.s.Get(t, tok.Token, "/api/v1/orgs/pt-revoke-a/hosts"), http.StatusOK)

	if code, raw := f.call(t, f.c, http.MethodDelete, myTokensPath+"/"+tok.ID, nil); code != http.StatusOK {
		t.Fatalf("revoke: %d (%s)", code, raw)
	}
	for _, slug := range []string{"pt-revoke-a", "pt-revoke-b"} {
		wantStatus(t, "after, "+slug, f.s.Get(t, tok.Token, "/api/v1/orgs/"+slug+"/hosts"), http.StatusUnauthorized)
	}
	if got := f.list(t); len(got) != 0 {
		t.Fatalf("list after revoke = %+v", got)
	}
	for org, role := range map[uuid.UUID]string{a: "operator", b: "viewer"} {
		feed := auditRows(t, f.s, "org_id = $1 AND action = 'removePersonalTokenGrant'", org)
		if len(feed) != 1 || feed[0].Summary != "removed personal token deploy ("+role+"): the token was revoked" {
			t.Fatalf("feed of %s = %+v", org, feed)
		}
	}
	own := auditRows(t, f.s, "org_id IS NULL AND action = 'DELETE revokePersonalToken'")
	if len(own) != 1 || own[0].Summary != "revoked personal API token deploy" {
		t.Fatalf("owner feed = %+v", own)
	}
	if code, _ := f.call(t, f.c, http.MethodDelete, myTokensPath+"/"+tok.ID, nil); code != http.StatusNotFound {
		t.Fatalf("second revoke: %d, want 404", code)
	}
}

func TestPatchPersonalTokenAnswers(t *testing.T) {
	f := newPTFixture(t, "pt-patch")
	a := f.org(t, "pt-patch-a", tenancy.RoleAdmin)
	tok := f.create(t, map[string]any{"name": "deploy", "grants": grantsOf("pt-patch-a", "operator")})

	code, raw := f.patch(t, tok.ID, map[string]any{"name": "renamed", "never_expires": true})
	if code != http.StatusOK || !strings.Contains(raw, `"name":"renamed"`) {
		t.Fatalf("rename: %d (%s)", code, raw)
	}
	if code, _ := f.patch(t, uuid.Must(uuid.NewV7()).String(), map[string]any{"name": "x"}); code != http.StatusNotFound {
		t.Fatalf("unknown token: %d, want 404", code)
	}
	if code, _ := f.patch(t, "not-a-uuid", map[string]any{"name": "x"}); code != http.StatusBadRequest {
		t.Fatalf("bad id: %d, want 400", code)
	}
	if code, _ := f.patch(t, tok.ID, map[string]any{"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339), "never_expires": true}); code != http.StatusUnprocessableEntity {
		t.Fatalf("both expiry fields: %d, want 422", code)
	}
	// Another user cannot see or change the token.
	other := f.s.SeedUser("pt-patch-other@example.test", "correct horse battery")
	f.s.AddMember(a, other, tenancy.RoleAdmin)
	oc := f.s.LoginAs(other)
	if code, _ := f.call(t, oc, http.MethodPatch, myTokensPath+"/"+tok.ID, map[string]any{"name": "mine"}); code != http.StatusNotFound {
		t.Fatalf("another user's PATCH: %d, want 404", code)
	}
	if code, _ := f.call(t, oc, http.MethodDelete, myTokensPath+"/"+tok.ID, nil); code != http.StatusNotFound {
		t.Fatalf("another user's DELETE: %d, want 404", code)
	}

	// An expired token cannot be edited.
	past := time.Now().Add(-time.Hour)
	_, hash, err := auth.MintPersonalSecret()
	if err != nil {
		t.Fatal(err)
	}
	expired, err := f.s.PersonalTokens.InsertToken(context.Background(), auth.PersonalToken{
		ID: uuid.Must(uuid.NewV7()), UserID: f.user, Name: "old", Kind: auth.TokenKindAPI, CreatedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: &past}, hash)
	if err != nil {
		t.Fatal(err)
	}
	if code, raw := f.patch(t, expired.ID.String(), map[string]any{"name": "new"}); code != http.StatusConflict {
		t.Fatalf("expired token: %d (%s), want 409", code, raw)
	}
}

// TestPersonalTokenRoutesRefuseBearerEndToEnd: the routes sit outside the
// bearer group, so a token — personal or of an organization — is no
// principal there at all.
func TestPersonalTokenRoutesRefuseBearerEndToEnd(t *testing.T) {
	f := newPTFixture(t, "pt-bearer")
	a := f.org(t, "pt-bearer-a", tenancy.RoleOwner)
	_, personal := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOwner})
	for _, tok := range []string{personal} {
		for _, r := range []*apitest.Response{
			f.s.Get(t, tok, myTokensPath),
			f.s.Post(t, tok, myTokensPath, map[string]any{"name": "x", "grants": grantsOf("pt-bearer-a", "viewer")}),
		} {
			if r.Code/100 == 2 {
				t.Fatalf("bearer reached the owner's routes: %d (%s)", r.Code, r.Body)
			}
		}
	}
	if got := f.list(t); len(got) != 1 {
		t.Fatalf("tokens = %d, want only the fixture's", len(got))
	}
}
