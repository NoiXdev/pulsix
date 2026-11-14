// The organization's secret store (backlog 3.13d): GET, PUT and DELETE
// under /orgs/{org}/secrets. Everyone who may read checks lists the
// secrets (names, field names, timestamps, the checks using each — never
// a value); only an admin (credentials.write, the SSH keys' bar) writes
// them. Every write is audited with the name and field names, never a
// value, and no response — an error included — carries one.
package api_test

import (
	"context"
	"encoding/json"
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
	for _, tc := range []struct {
		name, path string
		body       any
	}{
		{"invalid name", "/secrets/bad%20name", map[string]any{"fields": map[string]string{"community": secretLeak}}},
		{"no fields", "/secrets/sw", map[string]any{"fields": map[string]string{}}},
		{"invalid field name", "/secrets/sw", map[string]any{"fields": map[string]string{"a:b": secretLeak}}},
		{"empty value", "/secrets/sw", map[string]any{"fields": map[string]string{"community": ""}}},
		{"over-long value", "/secrets/sw", map[string]any{"fields": map[string]string{"community": strings.Repeat(secretLeak, 300)}}},
		{"NUL in a value", "/secrets/sw", map[string]any{"fields": map[string]string{"community": secretLeak + "\x00"}}},
		{"a value that is not a string", "/secrets/sw", map[string]any{"fields": map[string]any{"community": []string{secretLeak}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, text := f.do(t, f.ownerC, http.MethodPut, tc.path, tc.body); code != http.StatusUnprocessableEntity {
				t.Fatalf("PUT: %d %s, want 422", code, strings.ReplaceAll(text, secretLeak, "<LEAK>"))
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
	if list := f.list(t, f.viewer); len(list) != 1 || strings.Join(list[0].UsedBy, ",") != "pg-a,pg-b" {
		t.Fatalf("list = %+v, want db-main used by pg-a, pg-b", list)
	}
	f.assertNoLeak(t)
}
