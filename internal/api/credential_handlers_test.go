package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type credentialBody struct {
	Credential struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Kind        string `json:"kind"`
		PublicKey   string `json:"public_key"`
		Fingerprint string `json:"fingerprint"`
		KeyType     string `json:"key_type"`
		UsedBy      struct {
			Bastions int `json:"bastions"`
			Hosts    int `json:"hosts"`
		} `json:"used_by"`
	} `json:"credential"`
	Snippets *struct {
		AuthorizedKeysCommand string `json:"authorized_keys_command"`
		CloudInit             string `json:"cloud_init"`
		Terraform             string `json:"terraform"`
	} `json:"snippets"`
}

func decodeInto(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialLifecycle(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("cred-owner@example.test", "correct horse battery")
	s.SeedOrg("cred-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/cred-org/credentials"

	resp := postJSON(t, c, base, map[string]any{"name": "acme-deploy", "kind": "ssh_key", "username": "ansible", "generate": true})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "PRIVATE KEY") {
		t.Fatal("a private key reached the response")
	}
	var created credentialBody
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	cred := created.Credential
	if !strings.HasPrefix(cred.PublicKey, "ssh-ed25519 ") || !strings.HasSuffix(cred.PublicKey, " pulsix:cred-org:acme-deploy") || cred.KeyType != "ed25519" {
		t.Fatalf("credential = %+v", cred)
	}
	if created.Snippets == nil || !strings.Contains(created.Snippets.CloudInit, cred.PublicKey) {
		t.Fatalf("snippets = %+v", created.Snippets)
	}

	resp = requestJSON(t, c, http.MethodPatch, base+"/"+cred.ID, map[string]any{"name": "acme-deploy-2", "username": "ansible"})
	var renamed credentialBody
	decodeInto(t, resp, &renamed)
	if resp.StatusCode != http.StatusOK || !strings.HasSuffix(renamed.Credential.PublicKey, " pulsix:cred-org:acme-deploy-2") {
		t.Fatalf("rename: %d %+v", resp.StatusCode, renamed)
	}
	if renamed.Credential.Fingerprint != cred.Fingerprint {
		t.Fatal("a rename must keep the key")
	}

	resp = postJSON(t, c, base, map[string]any{"name": "office-fs-admin", "kind": "ssh_password", "username": "Administrator", "password": "pw-hunter2"})
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusCreated || strings.Contains(body, "hunter2") {
		t.Fatalf("password credential: %d %s", resp.StatusCode, body)
	}

	resp = requestJSON(t, c, http.MethodGet, base, nil)
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusOK || strings.Contains(body, "hunter2") || !strings.Contains(body, "office-fs-admin") {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/cred-org/activity", nil)
	if body := mustReadBody(t, resp); strings.Contains(body, "hunter2") || !strings.Contains(body, "created credential office-fs-admin") {
		t.Fatalf("activity must name the credential and never its secret: %s", body)
	}

	resp = requestJSON(t, c, http.MethodDelete, base+"/"+cred.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, c, http.MethodGet, base+"/"+cred.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted: %d", resp.StatusCode)
	}
}

func TestCredentialValidationAndConflicts(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("cred-val@example.test", "correct horse battery")
	s.SeedOrg("cred-val", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/cred-val/credentials"

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"key without material", map[string]any{"name": "k", "kind": "ssh_key"}, http.StatusBadRequest},
		{"unknown kind", map[string]any{"name": "k", "kind": "ftp"}, http.StatusUnprocessableEntity},
		{"bad private key", map[string]any{"name": "k", "kind": "git_key", "private_key": "nope"}, http.StatusBadRequest},
		{"token without token", map[string]any{"name": "t", "kind": "git_token"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, c, base, tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, tc.want, mustReadBody(t, resp))
			}
		})
	}
	resp := postJSON(t, c, base, map[string]any{"name": "t", "kind": "git_token", "token": "x"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	if resp := postJSON(t, c, base, map[string]any{"name": "t", "kind": "git_token", "token": "y"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate name: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, c, http.MethodGet, base+"/not-a-uuid", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed id: %d", resp.StatusCode)
	}
}

// TestCredentialWriteRoutesRejectBadIDs is backlog 2.19: rotate, update and
// delete must each 404 a foreign-organization credential id and 400 (or
// 404, never 500) a malformed one. Create has no id path parameter and so
// is not part of this table.
func TestCredentialWriteRoutesRejectBadIDs(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("cred-badid-owner@example.test", "correct horse battery")
	s.SeedOrg("cred-badid", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/cred-badid/credentials"

	other := s.SeedUser("cred-badid-other@example.test", "correct horse battery")
	s.SeedOrg("cred-badid-other", other)
	otherBase := s.HTTP.URL + "/api/v1/orgs/cred-badid-other/credentials"
	var foreign credentialBody
	decodeInto(t, postJSON(t, s.LoginAs(other), otherBase, map[string]any{"name": "foreign", "kind": "git_token", "token": "x"}), &foreign)

	url := base + "/"
	cases := []badIDCase{
		{"rotate", http.MethodPost, func(id string) string { return url + id + "/rotate" }, nil, nil, foreign.Credential.ID},
		{"update", http.MethodPatch, func(id string) string { return url + id }, map[string]any{"name": "x", "username": "u"}, nil, foreign.Credential.ID},
		{"delete", http.MethodDelete, func(id string) string { return url + id }, nil, nil, foreign.Credential.ID},
	}
	for _, tc := range cases {
		assertRejectsBadIDs(t, c, tc)
	}
}

func TestCredentialPermissions(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("cred-perm-owner@example.test", "correct horse battery")
	org := s.SeedOrg("cred-perm", owner)
	operator := s.SeedUser("cred-perm-op@example.test", "correct horse battery")
	viewer := s.SeedUser("cred-perm-viewer@example.test", "correct horse battery")
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	base := s.HTTP.URL + "/api/v1/orgs/cred-perm/credentials"

	op := s.LoginAs(operator)
	if resp := requestJSON(t, op, http.MethodGet, base, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator list: %d", resp.StatusCode)
	}
	if resp := postJSON(t, op, base, map[string]any{"name": "k", "kind": "ssh_key", "generate": true}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator create: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, s.LoginAs(viewer), http.MethodGet, base, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer list: %d", resp.StatusCode)
	}
}
