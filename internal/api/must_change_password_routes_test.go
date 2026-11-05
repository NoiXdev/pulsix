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
