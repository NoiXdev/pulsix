package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/api/apitest"
)

// passwordChangeRequired is the refusal detail the SPA matches on.
const passwordChangeRequired = "password_change_required"

// mustChangeAllowed is every operation a complete session whose account
// must change its password may still reach: who am I, sign out, the
// password change itself, and verifying a factor, which the change's
// step-up needs. Enrolling a factor is not on it: only a pending session
// of an account without one may enrol, before the change
// (TestMustChangePasswordPendingSessionCanEnrol).
var mustChangeAllowed = map[string]bool{
	"GET /api/v1/auth/me":           true,
	"POST /api/v1/auth/logout":      true,
	"POST /api/v1/auth/password":    true,
	"POST /api/v1/auth/totp/verify": true,
}

// mustChangeSkippedExact and mustChangeSkippedPrefixes are the routes that
// never take a session at all: everything else, including any route added
// later, must refuse a must-change session. A single route is listed
// exactly, so a later route that merely shares its beginning (say
// /auth/login-history) is walked rather than skipped; a prefix ends in a
// slash and covers a family of routes.
var mustChangeSkippedExact = map[string]bool{
	"/api/v1/health":         true,
	"/api/v1/auth/login":     true, // password sign-in
	"/api/v1/invites/accept": true, // the invite token is the credential
}

var mustChangeSkippedPrefixes = []string{
	"/api/v1/setup/",               // first-run wizard and its status
	"/api/v1/auth/passkeys/login/", // passkey sign-in
	"/api/v1/auth/password/",       // public forgot and reset routes (Task 4)
	"/api/v1/hooks/",               // webhook receivers, the trigger secret is the credential
}

// markMustChange raises the account's forced-change flag the way an
// administrator's temporary password does.
func markMustChange(t *testing.T, s *apitest.Server, uid uuid.UUID) {
	t.Helper()
	if _, err := s.DB.Pool().Exec(t.Context(),
		`UPDATE users SET must_change_password = true, temp_password_expires_at = now() + interval '24 hours' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
}

// problemDetail reads a huma error body's detail.
func problemDetail(t *testing.T, resp *http.Response) string {
	t.Helper()
	var problem struct {
		Detail string `json:"detail"`
	}
	body := mustReadBody(t, resp)
	_ = json.Unmarshal([]byte(body), &problem)
	return problem.Detail
}

// walkOp is one registered operation of the OpenAPI document.
type walkOp struct {
	method, path string
	op           map[string]any
}

// TestMustChangePasswordRouteWalk calls every registered operation with a
// cookie session that is MFA-complete but whose account must change its
// password. Every operation must refuse it with 403
// password_change_required, except the allow-list above and the routes
// that take no session at all. The request is well-formed for each
// operation (a body generated from its schema, a path naming a real
// organization the caller owns), so a refusal comes from the gate and not
// from input validation. The account is an instance admin and has a fresh
// factor, so nothing but the gate stands in the way.
func TestMustChangePasswordRouteWalk(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("walk@example.test", "correct horse battery")
	if _, err := s.DB.Pool().Exec(t.Context(), `UPDATE users SET instance_admin = true WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	s.SeedPasskey(uid, "walk key")
	s.SeedOrg("walk", uid)
	markMustChange(t, s, uid)
	c := s.LoginAs(uid)

	raw, err := api.Document()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	gen := schemaSampler{t: t, components: componentSchemas(doc)}

	var ops []walkOp
	for path, item := range doc["paths"].(map[string]any) {
		for method, op := range item.(map[string]any) {
			switch method {
			case "get", "post", "put", "patch", "delete":
				ops = append(ops, walkOp{strings.ToUpper(method), path, op.(map[string]any)})
			}
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].path+ops[i].method < ops[j].path+ops[j].method })

	seenAllowed := map[string]bool{}
	for _, o := range ops {
		key := o.method + " " + o.path
		if skippedForMustChange(o.path) {
			continue
		}
		target, body := gen.request(o, "walk")
		client := c
		if mustChangeAllowed[key] {
			// A fresh session each, so logging out (or anything else an
			// allowed route does to its session) cannot spoil the walk.
			client = s.LoginAs(uid)
			seenAllowed[key] = true
		}
		resp := requestJSON(t, client, o.method, s.HTTP.URL+target, body)
		detail := problemDetail(t, resp)
		if mustChangeAllowed[key] {
			if resp.StatusCode == http.StatusForbidden && detail == passwordChangeRequired {
				t.Errorf("%s: an allowed route refused the must-change session", key)
			}
			continue
		}
		if resp.StatusCode != http.StatusForbidden || detail != passwordChangeRequired {
			t.Errorf("%s (%s): status %d detail %q, want 403 %q", key, target, resp.StatusCode, detail, passwordChangeRequired)
		}
	}
	for key := range mustChangeAllowed {
		if !seenAllowed[key] {
			t.Errorf("allow-list entry %s is not a registered operation", key)
		}
	}

	// The routes outside the OpenAPI document that take a session: the
	// backup download and the API explorer with its spec and schemas.
	for _, path := range []string{
		"/api/v1/admin/backups/pulsix-20261104-120000.dump/download",
		"/api/explorer",
		"/api/openapi.json",
	} {
		resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+path, nil)
		if detail := problemDetail(t, resp); resp.StatusCode != http.StatusForbidden || detail != passwordChangeRequired {
			t.Errorf("GET %s: status %d detail %q, want 403 %q", path, resp.StatusCode, detail, passwordChangeRequired)
		}
	}
}

func skippedForMustChange(path string) bool {
	if mustChangeSkippedExact[path] {
		return true
	}
	for _, p := range mustChangeSkippedPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// TestMustChangeSkipListIsExact: a single skipped route does not hide the
// routes that merely begin like it.
func TestMustChangeSkipListIsExact(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/v1/auth/login":                   true,
		"/api/v1/auth/login-history":           false,
		"/api/v1/health":                       true,
		"/api/v1/healthcheck":                  false,
		"/api/v1/invites/accept":               true,
		"/api/v1/invites/accepted":             false,
		"/api/v1/auth/passkeys/login/begin":    true,
		"/api/v1/auth/password":                false,
		"/api/v1/auth/password/forgot":         true,
		"/api/v1/admin/users/{userId}/disable": false,
	} {
		if got := skippedForMustChange(path); got != want {
			t.Errorf("skippedForMustChange(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestMustChangePasswordPasskeySession: a session created already complete
// — the passkey sign-in path, which never sees the password — is gated all
// the same, because the flag is on the account.
func TestMustChangePasswordPasskeySession(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("passkey-must-change@example.test", "correct horse battery")
	s.SeedPasskey(uid, "key")
	markMustChange(t, s, uid)
	c := s.LoginAs(uid) // Sessions.Create(uid, true), as Passkeys.FinishLogin does

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /orgs: status %d, want 403", resp.StatusCode)
	}
	if d := problemDetail(t, resp); d != passwordChangeRequired {
		t.Fatalf("GET /orgs: detail %q, want %q", d, passwordChangeRequired)
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /auth/me: status %d, want 200", resp.StatusCode)
	}
	var me struct {
		MustChangePassword bool `json:"must_change_password"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !me.MustChangePassword {
		t.Fatal("/auth/me must_change_password = false, want true")
	}
}

// TestMustChangePasswordPendingSessionCanEnrol: a must-change account
// whose second factor was reset signs in with the temporary password and
// must enrol first; the gate never stands in the way of the enrolment
// routes, the passkey list the enrolment page reads included. Confirming
// completes the session, and from then on the change comes first: the
// enrolment routes refuse the complete session like everything else.
func TestMustChangePasswordPendingSessionCanEnrol(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("enrol-must-change@example.test", "correct horse battery")
	markMustChange(t, s, uid)
	c := s.LoginAsPending(uid)

	if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/passkeys", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /auth/passkeys: status %d (%s), want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	enrolAndConfirmTOTP(t, s, c)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil)
	var me struct {
		MFAComplete        bool `json:"mfa_complete"`
		MustChangePassword bool `json:"must_change_password"`
	}
	decodeInto(t, resp, &me)
	if !me.MFAComplete || !me.MustChangePassword {
		t.Fatalf("/auth/me after confirming = %+v, want a complete session that must change its password", me)
	}

	for _, r := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/auth/totp/enrol", map[string]any{}},
		{http.MethodPost, "/api/v1/auth/totp/confirm", map[string]any{"code": "123456"}},
		{http.MethodPost, "/api/v1/auth/passkeys/register/begin", nil},
		{http.MethodGet, "/api/v1/auth/passkeys", nil},
	} {
		resp := requestJSON(t, c, r.method, s.HTTP.URL+r.path, r.body)
		if d := problemDetail(t, resp); resp.StatusCode != http.StatusForbidden || d != passwordChangeRequired {
			t.Errorf("%s %s after enrolling: status %d detail %q, want 403 %s", r.method, r.path, resp.StatusCode, d, passwordChangeRequired)
		}
	}
}

// TestMustChangePasswordTokenUnaffected: an API token does not sign in
// with a password, so the flag does not gate it (controller's ruling).
func TestMustChangePasswordTokenUnaffected(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	var owner uuid.UUID
	if err := s.DB.Pool().QueryRow(t.Context(),
		`SELECT user_id FROM org_memberships WHERE org_id = $1`, org).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	markMustChange(t, s, owner)

	if resp := s.Get(t, tok, "/api/v1/orgs/"+org.String()); resp.Code != http.StatusOK {
		t.Fatalf("token GET /orgs/{org}: status %d (%s), want 200", resp.Code, resp.Body)
	}
	// The same account's cookie session is gated.
	resp := requestJSON(t, s.LoginAs(owner), http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+org.String(), nil)
	if resp.StatusCode != http.StatusForbidden || problemDetail(t, resp) != passwordChangeRequired {
		t.Fatalf("cookie GET /orgs/{org}: status %d, want 403 %s", resp.StatusCode, passwordChangeRequired)
	}
}

// TestMustChangePasswordInviteAcceptNeedsTheChange: accepting an invitation
// joins the signed-in account to an organization, so a must-change session
// does not count as signed in there either. It is told to change its
// password (403 password_change_required, which sends the SPA to the change
// page) rather than to sign in (409), which it already is (backlog 7.82).
func TestMustChangePasswordInviteAcceptNeedsTheChange(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("invite-owner@example.test", "correct horse battery")
	s.SeedOrg("invite-org", owner)
	invitee := s.SeedUser("invitee-must-change@example.test", "correct horse battery")

	resp := postJSON(t, s.LoginAs(owner), s.HTTP.URL+"/api/v1/orgs/invite-org/invites",
		map[string]any{"email": "invitee-must-change@example.test", "role": "viewer"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("invite: status %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	_ = resp.Body.Close()
	token := extractInviteToken(t, s)

	markMustChange(t, s, invitee)
	resp = postJSON(t, s.LoginAs(invitee), s.HTTP.URL+"/api/v1/invites/accept", map[string]any{"token": token})
	if resp.StatusCode != http.StatusForbidden || problemDetail(t, resp) != passwordChangeRequired {
		t.Fatalf("accept with a must-change session: status %d, want 403 %s", resp.StatusCode, passwordChangeRequired)
	}
	var n int
	if err := s.DB.Pool().QueryRow(t.Context(),
		`SELECT count(*) FROM org_memberships WHERE user_id = $1`, invitee).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the must-change account joined %d organizations", n)
	}
}

// componentSchemas returns the document's named schemas, for $ref lookups.
func componentSchemas(doc map[string]any) map[string]any {
	comps, _ := doc["components"].(map[string]any)
	schemas, _ := comps["schemas"].(map[string]any)
	return schemas
}

// schemaSampler builds the smallest request an operation's schema accepts:
// every required property, the first enum value, a value of the right
// format, the minimum of a range.
type schemaSampler struct {
	t          *testing.T
	components map[string]any
}

// patternSamples are values for the string patterns the API declares. A
// pattern missing here fails the walk, naming it, rather than sending a
// value that is refused before the gate.
var patternSamples = map[string]string{
	`^[a-z0-9]+(-[a-z0-9]+)*$`:               "walk",
	`^[0-9]{6}$`:                             "123456",
	`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`: "walk",
	`^SHA256:[A-Za-z0-9+/]{43}$`:             "SHA256:" + strings.Repeat("A", 43),
	`^[0-2][0-9]:[0-5][0-9]$`:                "12:00",
}

// request returns the operation's path with its parameters filled in (and
// its required query parameters appended) and a JSON body, or nil when the
// operation takes none.
func (g schemaSampler) request(o walkOp, orgSlug string) (string, any) {
	path := o.path
	query := url.Values{}
	params, _ := o.op["parameters"].([]any)
	for _, p := range params {
		param := p.(map[string]any)
		name, in := param["name"].(string), param["in"].(string)
		schema, _ := param["schema"].(map[string]any)
		switch {
		case in == "path" && name == "org":
			path = strings.ReplaceAll(path, "{org}", orgSlug)
		case in == "path":
			// Path ids are plain strings in the schema and uuids in
			// practice; a real-shaped one keeps a handler that parses its
			// id early from answering 400 before the gate.
			value := uuid.NewString()
			if _, ok := schema["enum"]; ok {
				value = fmt.Sprint(g.sample(schema))
			}
			path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
		case in == "query" && param["required"] == true:
			query.Set(name, fmt.Sprint(g.sample(schema)))
		}
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	rb, _ := o.op["requestBody"].(map[string]any)
	if rb == nil {
		return path, nil
	}
	content, _ := rb["content"].(map[string]any)
	media, ok := content["application/json"].(map[string]any)
	if !ok {
		g.t.Fatalf("%s %s: request body is not JSON; skip the route or teach the walk its media type", o.method, o.path)
	}
	return path, g.sample(media["schema"].(map[string]any))
}

func (g schemaSampler) sample(schema map[string]any) any {
	if ref, ok := schema["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		return g.sample(g.components[name].(map[string]any))
	}
	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	switch schemaType(schema) {
	case "object":
		out := map[string]any{}
		props, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		for _, r := range required {
			name := r.(string)
			if prop, ok := props[name].(map[string]any); ok {
				out[name] = g.sample(prop)
			}
		}
		return out
	case "array":
		n := 0
		if v, ok := schema["minItems"].(float64); ok {
			n = int(v)
		}
		items := make([]any, n)
		for i := range items {
			items[i] = g.sample(schema["items"].(map[string]any))
		}
		return items
	case "integer", "number":
		if v, ok := schema["minimum"].(float64); ok {
			return v
		}
		if v, ok := schema["maximum"].(float64); ok && v < 1 {
			return v
		}
		return 1
	case "boolean":
		return false
	default:
		return g.sampleString(schema)
	}
}

func (g schemaSampler) sampleString(schema map[string]any) string {
	if pattern, ok := schema["pattern"].(string); ok {
		v, known := patternSamples[pattern]
		if !known {
			g.t.Fatalf("no sample value for the pattern %q: add one to patternSamples", pattern)
		}
		return v
	}
	switch schema["format"] {
	case "uuid":
		return uuid.NewString()
	case "email":
		return "walk-target@example.test"
	case "date-time":
		return time.Now().UTC().Format(time.RFC3339)
	case "uri":
		return "https://walk.example.test/"
	}
	n := 1
	if v, ok := schema["minLength"].(float64); ok && int(v) > n {
		n = int(v)
	}
	return strings.Repeat("w", n)
}

// schemaType is the schema's type, the non-null one of a nullable pair.
func schemaType(schema map[string]any) string {
	switch t := schema["type"].(type) {
	case string:
		return t
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok && s != "null" {
				return s
			}
		}
	}
	if _, ok := schema["properties"]; ok {
		return "object"
	}
	return "string"
}
