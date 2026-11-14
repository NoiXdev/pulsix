// The organization's secret store (backlog 3.13d): GET, PUT and DELETE
// under /orgs/{org}/secrets. Everyone who may read checks lists the
// secrets (names, field names, timestamps, the checks using each — never
// a value); only an admin (credentials.write, the SSH keys' bar) writes
// them. Every write is audited with the name and field names, never a
// value, and no response — an error included — carries one.
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// secretLeak is the distinctive value these tests store and then look for
// in every response and audit row.
const secretLeak = "api-s3cr3t-LEAK-91c4"

type orgSecretBody struct {
	Name       string   `json:"name"`
	FieldNames []string `json:"field_names"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
	UsedBy     []string `json:"used_by"`
}

type orgSecretsFixture struct {
	s                         *apitest.Server
	ownerC, operatorC, viewer *http.Client
	base                      string
	bodies                    []string
}

func decodeText(t *testing.T, text string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(text), v); err != nil {
		t.Fatal(err)
	}
}

func newOrgSecretsFixture(t *testing.T, slug string, opts ...apitest.Option) *orgSecretsFixture {
	t.Helper()
	s := apitest.New(t, opts...)
	owner := s.SeedUser(slug+"-owner@example.test", "correct horse battery")
	operator := s.SeedUser(slug+"-operator@example.test", "correct horse battery")
	viewer := s.SeedUser(slug+"-viewer@example.test", "correct horse battery")
	org := s.SeedOrg(slug, owner)
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	return &orgSecretsFixture{
		s: s, ownerC: s.LoginAs(owner), operatorC: s.LoginAs(operator), viewer: s.LoginAs(viewer),
		base: s.HTTP.URL + "/api/v1/orgs/" + slug,
	}
}

// do sends a request and keeps the response body for assertNoLeak.
func (f *orgSecretsFixture) do(t *testing.T, c *http.Client, method, path string, body any) (int, string) {
	t.Helper()
	resp := requestJSON(t, c, method, f.base+path, body)
	text := mustReadBody(t, resp)
	f.bodies = append(f.bodies, text)
	return resp.StatusCode, text
}

func (f *orgSecretsFixture) list(t *testing.T, c *http.Client) []orgSecretBody {
	t.Helper()
	code, text := f.do(t, c, http.MethodGet, "/secrets", nil)
	if code != http.StatusOK {
		t.Fatalf("GET secrets: %d %s", code, text)
	}
	var body struct {
		Secrets []orgSecretBody `json:"secrets"`
	}
	decodeText(t, text, &body)
	return body.Secrets
}

// assertNoLeak checks every response this fixture saw and every audit row
// of the instance for the distinctive value.
func (f *orgSecretsFixture) assertNoLeak(t *testing.T) {
	t.Helper()
	for _, b := range f.bodies {
		if strings.Contains(b, secretLeak) {
			t.Fatalf("a response carries the secret value: %.200s…", strings.ReplaceAll(b, secretLeak, "<LEAK>"))
		}
	}
	var n int
	if err := f.s.DB.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE to_jsonb(audit_events)::text LIKE '%' || $1 || '%'`, secretLeak).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d audit row(s) carry the secret value", n)
	}
}

func TestOrgSecretsLifecycleAndPermissions(t *testing.T) {
	f := newOrgSecretsFixture(t, "orgsecrets-life")

	// Writes need credentials.write: an operator and a viewer are refused.
	for _, c := range []*http.Client{f.operatorC, f.viewer} {
		if code, text := f.do(t, c, http.MethodPut, "/secrets/core-switch", map[string]any{"fields": map[string]string{"community": secretLeak}}); code != http.StatusForbidden {
			t.Fatalf("PUT by a non-admin: %d %s", code, text)
		}
	}

	code, text := f.do(t, f.ownerC, http.MethodPut, "/secrets/core-switch", map[string]any{"fields": map[string]string{"community": secretLeak, "user": "monitor"}})
	if code != http.StatusOK {
		t.Fatalf("PUT: %d %s", code, text)
	}
	var put struct {
		Secret orgSecretBody `json:"secret"`
	}
	decodeText(t, text, &put)
	if put.Secret.Name != "core-switch" || strings.Join(put.Secret.FieldNames, ",") != "community,user" || put.Secret.CreatedAt == "" {
		t.Fatalf("PUT response = %+v", put.Secret)
	}

	// A replace keeps created_at.
	if code, text := f.do(t, f.ownerC, http.MethodPut, "/secrets/core-switch", map[string]any{"fields": map[string]string{"community": secretLeak + "-2"}}); code != http.StatusOK {
		t.Fatalf("replace: %d %s", code, text)
	}

	// The list needs only checks.read: the viewer sees names and field
	// names, never a value.
	list := f.list(t, f.viewer)
	if len(list) != 1 || list[0].Name != "core-switch" || strings.Join(list[0].FieldNames, ",") != "community" ||
		list[0].CreatedAt != put.Secret.CreatedAt || list[0].UsedBy == nil || len(list[0].UsedBy) != 0 {
		t.Fatalf("list = %+v", list)
	}

	// Deletes need credentials.write too.
	for _, c := range []*http.Client{f.operatorC, f.viewer} {
		if code, text := f.do(t, c, http.MethodDelete, "/secrets/core-switch", nil); code != http.StatusForbidden {
			t.Fatalf("DELETE by a non-admin: %d %s", code, text)
		}
	}
	if code, text := f.do(t, f.ownerC, http.MethodDelete, "/secrets/core-switch", nil); code != http.StatusOK {
		t.Fatalf("DELETE: %d %s", code, text)
	}
	if code, text := f.do(t, f.ownerC, http.MethodDelete, "/secrets/core-switch", nil); code != http.StatusNotFound {
		t.Fatalf("second DELETE: %d %s, want 404", code, text)
	}
	if list := f.list(t, f.viewer); len(list) != 0 {
		t.Fatalf("list after the delete = %+v", list)
	}

	// The audit trail names the secret and its field names, nothing more.
	rows, err := f.s.DB.Pool().Query(context.Background(),
		`SELECT action, object_id, summary FROM audit_events WHERE object_type = 'secret' ORDER BY at, id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var action, object, summary string
		if err := rows.Scan(&action, &object, &summary); err != nil {
			t.Fatal(err)
		}
		got = append(got, action+" | "+object+" | "+summary)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"PUT putOrgSecret | core-switch | stored secret core-switch (fields: community, user)",
		"PUT putOrgSecret | core-switch | stored secret core-switch (fields: community)",
		"DELETE deleteOrgSecret | core-switch | deleted secret core-switch",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("audit rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if code, _ := f.do(t, f.ownerC, http.MethodGet, "/activity", nil); code != http.StatusOK {
		t.Fatalf("activity: %d", code)
	}
	f.assertNoLeak(t)
}

// TestOrgSecretsInvalidInput: every refusal is a 422 that says what is
// wrong and never repeats the value.
func TestOrgSecretsInvalidInput(t *testing.T) {
	f := newOrgSecretsFixture(t, "orgsecrets-invalid")
	seventeen := map[string]string{}
	for i := range 17 {
		seventeen[fmt.Sprintf("f%02d", i)] = secretLeak
	}
	// want pins who refused: the schema (a location) or the service (its
	// text). The schema bounds the count and the value length.
	for _, tc := range []struct {
		name, path string
		body       any
		want       string
	}{
		{"invalid name", "/secrets/bad%20name", map[string]any{"fields": map[string]string{"community": secretLeak}}, `a secret name is`},
		{"no fields", "/secrets/sw", map[string]any{"fields": map[string]string{}}, `"location":"body.fields"`},
		{"invalid field name", "/secrets/sw", map[string]any{"fields": map[string]string{"a:b": secretLeak}}, `a field name is`},
		{"empty value", "/secrets/sw", map[string]any{"fields": map[string]string{"community": ""}}, `"location":"body.fields.community"`},
		{"over-long value", "/secrets/sw", map[string]any{"fields": map[string]string{"community": strings.Repeat(secretLeak, 300)}}, `"location":"body.fields.community"`},
		{"NUL in a value", "/secrets/sw", map[string]any{"fields": map[string]string{"community": secretLeak + "\x00"}}, `NUL character`},
		{"a value that is not a string", "/secrets/sw", map[string]any{"fields": map[string]any{"community": []string{secretLeak}}}, `"location":"body.fields.community"`},
		// huma echoes the whole request object for a missing required
		// property and for an unexpected one; redactErrorValues must clear it.
		{"missing fields key", "/secrets/sw", map[string]any{"field": map[string]string{"community": secretLeak}}, `"expected required property fields`},
		{"unexpected property", "/secrets/sw", map[string]any{"fields": map[string]string{"community": secretLeak}, "extra": secretLeak}, `"location":"body.extra"`},
		{"whole body a value", "/secrets/sw", secretLeak, `"location":"body"`},
		{"17 fields", "/secrets/sw", map[string]any{"fields": seventeen}, `"location":"body.fields"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, text := f.do(t, f.ownerC, http.MethodPut, tc.path, tc.body)
			if code != http.StatusUnprocessableEntity || !strings.Contains(text, tc.want) {
				t.Fatalf("PUT: %d %s, want 422 with %s", code, strings.ReplaceAll(text, secretLeak, "<LEAK>"), tc.want)
			}
		})
	}
	if list := f.list(t, f.viewer); len(list) != 0 {
		t.Fatalf("an invalid PUT stored %+v", list)
	}
	f.assertNoLeak(t)
}

// TestOrgSecretsDeleteConflictNamesTheChecks: a secret a server-run check
// names cannot be deleted, and the 409 names the checks. No real server
// plugin takes a secret yet, so the store runs under a descriptor lookup
// that puts postgres on the server.
func TestOrgSecretsDeleteConflictNamesTheChecks(t *testing.T) {
	postgresOnServer := func(name string) (checkplugins.Descriptor, bool) {
		desc, ok := checkplugins.Get(name)
		if name == "postgres" {
			desc.Runs = checkplugins.Server
		}
		return desc, ok
	}
	f := newOrgSecretsFixture(t, "orgsecrets-conflict", apitest.WithOrgSecretsPlugins(postgresOnServer))
	if code, text := f.do(t, f.ownerC, http.MethodPut, "/secrets/db-main", map[string]any{"fields": map[string]string{"password": secretLeak}}); code != http.StatusOK {
		t.Fatalf("PUT: %d %s", code, text)
	}
	for _, key := range []string{"pg-b", "pg-a"} {
		if code, text := f.do(t, f.operatorC, http.MethodPost, "/checks", map[string]any{
			"key": key, "plugin": "postgres", "interval_seconds": 60, "binding_kind": "org",
			"params": map[string]any{"secret": "db-main"},
		}); code != http.StatusCreated {
			t.Fatalf("create check %s: %d %s", key, code, text)
		}
	}
	code, text := f.do(t, f.ownerC, http.MethodDelete, "/secrets/db-main", nil)
	if code != http.StatusConflict || !strings.Contains(text, "pg-a, pg-b") {
		t.Fatalf("DELETE of a used secret: %d %s, want 409 naming pg-a, pg-b", code, text)
	}
	// The keys also come structured: one error detail per check, located
	// at used_by, its message the key.
	var problem struct {
		Errors []struct {
			Location string `json:"location"`
			Message  string `json:"message"`
		} `json:"errors"`
	}
	decodeText(t, text, &problem)
	var keys []string
	for _, e := range problem.Errors {
		if e.Location != "used_by" {
			t.Fatalf("409 detail at %q, want used_by: %s", e.Location, text)
		}
		keys = append(keys, e.Message)
	}
	if strings.Join(keys, ",") != "pg-a,pg-b" {
		t.Fatalf("409 details name %v, want pg-a, pg-b: %s", keys, text)
	}
	if list := f.list(t, f.viewer); len(list) != 1 || strings.Join(list[0].UsedBy, ",") != "pg-a,pg-b" {
		t.Fatalf("list = %+v, want db-main used by pg-a, pg-b", list)
	}
	f.assertNoLeak(t)
}

// TestOrgSecretsAreOrganizationScoped: names are per organization — org B
// can store a secret under the name org A uses — and an admin of org B
// cannot list, replace or delete org A's secrets through A's slug, nor
// reach them through B's.
func TestOrgSecretsAreOrganizationScoped(t *testing.T) {
	a := newOrgSecretsFixture(t, "orgsecrets-xorg-a")
	bOwner := a.s.SeedUser("orgsecrets-xorg-b-owner@example.test", "correct horse battery")
	a.s.SeedOrg("orgsecrets-xorg-b", bOwner)
	bC := a.s.LoginAs(bOwner)
	bBase := a.s.HTTP.URL + "/api/v1/orgs/orgsecrets-xorg-b"

	if code, text := a.do(t, a.ownerC, http.MethodPut, "/secrets/sw", map[string]any{"fields": map[string]string{"community": secretLeak}}); code != http.StatusOK {
		t.Fatalf("A's PUT: %d %s", code, text)
	}

	// Through A's slug, B's admin is refused everything.
	for _, tc := range []struct {
		method string
		body   any
	}{
		{http.MethodGet, nil},
		{http.MethodPut, map[string]any{"fields": map[string]string{"community": "hijack"}}},
		{http.MethodDelete, nil},
	} {
		path := "/secrets/sw"
		if tc.method == http.MethodGet {
			path = "/secrets"
		}
		if code, text := a.do(t, bC, tc.method, path, tc.body); code != http.StatusForbidden && code != http.StatusNotFound {
			t.Fatalf("%s %s by B's admin through A's slug: %d %s, want 403 or 404", tc.method, path, code, text)
		}
	}

	// Through B's own slug, A's secret does not exist.
	resp := requestJSON(t, bC, http.MethodDelete, bBase+"/secrets/sw", nil)
	if text := mustReadBody(t, resp); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE of sw through B's slug: %d %s, want 404", resp.StatusCode, text)
	}
	resp = requestJSON(t, bC, http.MethodGet, bBase+"/secrets", nil)
	text := mustReadBody(t, resp)
	a.bodies = append(a.bodies, text)
	if resp.StatusCode != http.StatusOK || strings.Contains(text, `"sw"`) {
		t.Fatalf("B's list: %d %s, want 200 without sw", resp.StatusCode, text)
	}

	// The same name in B is B's own secret; A's is untouched.
	resp = requestJSON(t, bC, http.MethodPut, bBase+"/secrets/sw", map[string]any{"fields": map[string]string{"user": "b"}})
	if text := mustReadBody(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("B's PUT of sw: %d %s", resp.StatusCode, text)
	}
	if list := a.list(t, a.viewer); len(list) != 1 || list[0].Name != "sw" || strings.Join(list[0].FieldNames, ",") != "community" {
		t.Fatalf("A's list after B's write = %+v, want sw with community", list)
	}
	a.assertNoLeak(t)
}

// putWith sends a PUT with an If-None-Match header and keeps the body for
// assertNoLeak.
func (f *orgSecretsFixture) putWith(t *testing.T, c *http.Client, path, ifNoneMatch string, body any) (int, string) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, f.base+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-None-Match", ifNoneMatch)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	text := mustReadBody(t, resp)
	f.bodies = append(f.bodies, text)
	return resp.StatusCode, text
}

// TestOrgSecretsCreateOnly: PUT with If-None-Match: * creates a secret
// only when the name is free and answers 412 otherwise, leaving the stored
// secret alone; without the header PUT stays create-or-replace. Any other
// If-None-Match is refused: the store has no ETags to compare.
func TestOrgSecretsCreateOnly(t *testing.T) {
	f := newOrgSecretsFixture(t, "orgsecrets-create")
	fields := func(v string) map[string]any { return map[string]any{"fields": map[string]string{"community": v}} }

	if code, text := f.putWith(t, f.ownerC, "/secrets/sw", "*", fields(secretLeak)); code != http.StatusOK {
		t.Fatalf("create-only PUT of a new name: %d %s", code, text)
	}
	code, text := f.putWith(t, f.ownerC, "/secrets/sw", "*", map[string]any{"fields": map[string]string{"username": secretLeak}})
	if code != http.StatusPreconditionFailed || !strings.Contains(text, "already exists") {
		t.Fatalf("create-only PUT of an existing name: %d %s, want 412", code, text)
	}
	if list := f.list(t, f.viewer); len(list) != 1 || strings.Join(list[0].FieldNames, ",") != "community" {
		t.Fatalf("the refused create changed the secret: %+v", list)
	}
	if code, text := f.putWith(t, f.ownerC, "/secrets/sw", `"abc"`, fields(secretLeak)); code != http.StatusBadRequest {
		t.Fatalf("PUT with an ETag in If-None-Match: %d %s, want 400", code, text)
	}
	if code, text := f.putWith(t, f.operatorC, "/secrets/other", "*", fields(secretLeak)); code != http.StatusForbidden {
		t.Fatalf("create-only PUT by an operator: %d %s, want 403", code, text)
	}
	if code, text := f.do(t, f.ownerC, http.MethodPut, "/secrets/sw", map[string]any{"fields": map[string]string{"username": "monitor"}}); code != http.StatusOK {
		t.Fatalf("PUT without the header (replace): %d %s", code, text)
	}
	if list := f.list(t, f.viewer); len(list) != 1 || strings.Join(list[0].FieldNames, ",") != "username" {
		t.Fatalf("after the replace: %+v", list)
	}
	f.assertNoLeak(t)
}
