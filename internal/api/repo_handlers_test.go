package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type vaultIdentityBody struct {
	CredentialID string `json:"credential_id"`
	Label        string `json:"label"`
}

type repoBody struct {
	Repo struct {
		ID              string              `json:"id"`
		Name            string              `json:"name"`
		URL             string              `json:"url"`
		Branch          string              `json:"branch"`
		Subpath         string              `json:"subpath"`
		CredentialID    string              `json:"credential_id"`
		VaultIdentities []vaultIdentityBody `json:"vault_identities"`
		SSHHostKey      *string             `json:"ssh_host_key"`
		PinState        *string             `json:"pin_state"`
	} `json:"repo"`
}

type jobBody struct {
	Job struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
		ZoneID string `json:"zone_id"`
	} `json:"job"`
}

// defaultZoneID lists the org's zones and returns the "default" zone's id —
// the zone a sync_repo job is enqueued into.
func defaultZoneID(t *testing.T, c *http.Client, s *apitest.Server, org string) string {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+org+"/zones", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list zones: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var body struct {
		Zones []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"zones"`
	}
	decodeInto(t, resp, &body)
	for _, z := range body.Zones {
		if z.Slug == "default" {
			return z.ID
		}
	}
	t.Fatal("no default zone")
	return ""
}

func TestRepoLifecycle(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("repo-owner@example.test", "correct horse battery")
	s.SeedOrg("repo-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/repo-org/repos"
	zone := defaultZoneID(t, c, s, "repo-org")

	resp := postJSON(t, c, base, map[string]any{"name": "infra", "url": "https://git.example.com/acme/infra.git"})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var created repoBody
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	repo := created.Repo
	if repo.Branch != "main" || repo.URL != "https://git.example.com/acme/infra.git" || repo.ID == "" {
		t.Fatalf("created = %+v", repo)
	}

	resp = requestJSON(t, c, http.MethodGet, base, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(mustReadBody(t, resp), "infra") {
		t.Fatalf("list: %d", resp.StatusCode)
	}

	// Sync: one queued sync_repo job, in the org's default zone.
	resp = postJSON(t, c, base+"/"+repo.ID+"/sync", nil)
	syncBody := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("sync: %d %s", resp.StatusCode, syncBody)
	}
	var job jobBody
	if err := json.Unmarshal([]byte(syncBody), &job); err != nil {
		t.Fatal(err)
	}
	if job.Job.Kind != "sync_repo" || job.Job.Status != "queued" || job.Job.ZoneID != zone || job.Job.ID == "" {
		t.Fatalf("sync job = %+v (want zone %s)", job.Job, zone)
	}

	resp = requestJSON(t, c, http.MethodPut, base+"/"+repo.ID, map[string]any{"name": "infra-2", "url": repo.URL, "branch": "release/1"})
	var renamed repoBody
	decodeInto(t, resp, &renamed)
	if resp.StatusCode != http.StatusOK || renamed.Repo.Name != "infra-2" || renamed.Repo.Branch != "release/1" {
		t.Fatalf("update: %d %+v", resp.StatusCode, renamed)
	}

	resp = requestJSON(t, c, http.MethodDelete, base+"/"+repo.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// Every mutating call is on the activity feed.
	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/repo-org/activity", nil)
	activity := mustReadBody(t, resp)
	for _, want := range []string{"POST createRepo", "POST syncRepo", "PUT updateRepo", "DELETE deleteRepo"} {
		if !strings.Contains(activity, want) {
			t.Fatalf("activity missing %q: %s", want, activity)
		}
	}

	resp = requestJSON(t, c, http.MethodGet, base+"/"+repo.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted: %d", resp.StatusCode)
	}
}

func TestRepoValidationAndConflicts(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("repo-val@example.test", "correct horse battery")
	s.SeedOrg("repo-val", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/repo-val/repos"

	// An SSH credential from the same org: the repo must refuse it.
	otherOrg := s.SeedOrg("repo-val-other", owner)
	scoped := db.WithOrgScope(context.Background(), otherOrg)
	sshCred, err := s.Deps.Credentials.Create(scoped, otherOrg, credentials.CreateInput{
		Name: "host-login", Kind: credentials.KindSSHKey, Username: "ansible",
		Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	sshCredID := sshCred.ID.String()

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"plain http url", map[string]any{"name": "r", "url": "http://git.example.com/x"}, http.StatusBadRequest},
		{"url with space", map[string]any{"name": "r", "url": "https://git example.com/x"}, http.StatusBadRequest},
		{"bad name", map[string]any{"name": "Bad Name", "url": "https://git.example.com/x"}, http.StatusBadRequest},
		{"branch with dotdot", map[string]any{"name": "r", "url": "https://git.example.com/x", "branch": "a..b"}, http.StatusBadRequest},
		{"subpath with dotdot", map[string]any{"name": "r", "url": "https://git.example.com/x", "subpath": "a/../b"}, http.StatusBadRequest},
		{"ssh credential", map[string]any{"name": "r", "url": "https://git.example.com/x", "credential_id": sshCredID}, http.StatusBadRequest},
		{"unknown credential", map[string]any{"name": "r", "url": "https://git.example.com/x", "credential_id": "11111111-1111-1111-1111-111111111111"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, c, base, tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, tc.want, mustReadBody(t, resp))
			}
		})
	}

	if resp := postJSON(t, c, base, map[string]any{"name": "good", "url": "https://git.example.com/x"}); resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	if resp := postJSON(t, c, base, map[string]any{"name": "good", "url": "https://git.example.com/y"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate name: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, c, http.MethodGet, base+"/not-a-uuid", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed id: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, c, http.MethodGet, base+"/11111111-1111-1111-1111-111111111111", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown repo: %d", resp.StatusCode)
	}
	if resp := postJSON(t, c, base+"/11111111-1111-1111-1111-111111111111/sync", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("sync unknown repo: %d", resp.StatusCode)
	}
	// A schema violation is 422 and its offending value is redacted.
	overlong := strings.Repeat("a", 70)
	resp := postJSON(t, c, base, map[string]any{"name": overlong, "url": "https://git.example.com/x"})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || strings.Contains(body, overlong) {
		t.Fatalf("overlong name: %d %s", resp.StatusCode, body)
	}
}

func TestRepoPermissionsAndCrossOrgIsolation(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("repo-perm-owner@example.test", "correct horse battery")
	org := s.SeedOrg("repo-perm", owner)
	operator := s.SeedUser("repo-perm-op@example.test", "correct horse battery")
	viewer := s.SeedUser("repo-perm-viewer@example.test", "correct horse battery")
	outsider := s.SeedUser("repo-perm-out@example.test", "correct horse battery")
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	base := s.HTTP.URL + "/api/v1/orgs/repo-perm/repos"

	// No session: 401.
	if resp := postJSON(t, s.Client(), base, map[string]any{"name": "r", "url": "https://git.example.com/x"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous create: %d", resp.StatusCode)
	}

	c := s.LoginAs(owner)
	if resp := postJSON(t, c, base, map[string]any{"name": "infra", "url": "https://git.example.com/x"}); resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	resp := requestJSON(t, c, http.MethodGet, base, nil)
	var list struct {
		Repos []struct {
			ID string `json:"id"`
		} `json:"repos"`
	}
	decodeInto(t, resp, &list)
	repoID := list.Repos[0].ID

	// Viewer reads everything, writes nothing.
	vc := s.LoginAs(viewer)
	if resp := requestJSON(t, vc, http.MethodGet, base, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer list: %d", resp.StatusCode)
	}
	if resp := postJSON(t, vc, base, map[string]any{"name": "r", "url": "https://git.example.com/x"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create: %d", resp.StatusCode)
	}
	if resp := postJSON(t, vc, base+"/"+repoID+"/sync", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer sync: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, vc, http.MethodDelete, base+"/"+repoID, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer delete: %d", resp.StatusCode)
	}

	// Operator writes.
	oc := s.LoginAs(operator)
	if resp := postJSON(t, oc, base, map[string]any{"name": "other", "url": "https://git.example.com/y"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("operator create: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, oc, http.MethodDelete, base+"/"+repoID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator delete: %d", resp.StatusCode)
	}

	// A repo id from another org is invisible — exactly like one that does
	// not exist. The reader is a member of the other org (a non-member gets
	// 403 before any lookup), asking for the first org's repo id inside
	// their own org.
	s.SeedOrg("repo-perm-other", outsider)
	otherC := s.LoginAs(outsider)
	if resp := requestJSON(t, otherC, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/repo-perm-other/repos/"+repoID, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org repo read: %d", resp.StatusCode)
	}
}

// TestRepoHostKeyPin covers the git server key pin surface (backlog 2.41):
// the read carries pin_state/ssh_host_key only while pinned, the unpin
// route (repos.write) clears it and lands on the activity feed, a viewer is
// refused, and a foreign organization's repository is as invisible as a
// missing one.
func TestRepoHostKeyPin(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("repo-pin-owner@example.test", "correct horse battery")
	org := s.SeedOrg("repo-pin-org", owner)
	operator := s.SeedUser("repo-pin-op@example.test", "correct horse battery")
	viewer := s.SeedUser("repo-pin-viewer@example.test", "correct horse battery")
	outsider := s.SeedUser("repo-pin-out@example.test", "correct horse battery")
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	base := s.HTTP.URL + "/api/v1/orgs/repo-pin-org/repos"

	c := s.LoginAs(owner)
	resp := postJSON(t, c, base, map[string]any{"name": "infra", "url": "ssh://git@git.example.com/acme/infra.git"})
	var created repoBody
	decodeInto(t, resp, &created)
	repoID := created.Repo.ID

	// Unpinned: neither field is present, exactly like the hosts read.
	if resp := requestJSON(t, c, http.MethodGet, base+"/"+repoID, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	} else {
		var got repoBody
		decodeInto(t, resp, &got)
		if got.Repo.PinState != nil || got.Repo.SSHHostKey != nil {
			t.Fatalf("unpinned DTO = %+v, want no pin fields", got.Repo)
		}
	}

	// Seed the pin the way the sync sink does.
	scoped := db.WithOrgScope(context.Background(), org)
	if _, err := s.DB.Exec(scoped,
		`UPDATE git_repos SET ssh_host_key = $3, ssh_host_public_key = $4 WHERE org_id = $1 AND id = $2`,
		org, repoID, "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "ssh-ed25519 AAAA pinned"); err != nil {
		t.Fatal(err)
	}
	resp = requestJSON(t, c, http.MethodGet, base, nil)
	var list struct {
		Repos []struct {
			ID         string  `json:"id"`
			SSHHostKey *string `json:"ssh_host_key"`
			PinState   *string `json:"pin_state"`
		} `json:"repos"`
	}
	decodeInto(t, resp, &list)
	if len(list.Repos) != 1 || list.Repos[0].PinState == nil || *list.Repos[0].PinState != "full" ||
		list.Repos[0].SSHHostKey == nil || *list.Repos[0].SSHHostKey != "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("pinned list = %+v", list.Repos)
	}

	// A viewer can see the pin state, cannot clear it.
	vc := s.LoginAs(viewer)
	if resp := requestJSON(t, vc, http.MethodDelete, base+"/"+repoID+"/host-key", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer unpin: %d", resp.StatusCode)
	}
	// An anonymous call is 401.
	if resp := requestJSON(t, s.Client(), http.MethodDelete, base+"/"+repoID+"/host-key", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous unpin: %d", resp.StatusCode)
	}

	// The operator's unpin clears the pin and is on the activity feed.
	oc := s.LoginAs(operator)
	if resp := requestJSON(t, oc, http.MethodDelete, base+"/"+repoID+"/host-key", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unpin: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, c, http.MethodGet, base+"/"+repoID, nil)
	var cleared repoBody
	decodeInto(t, resp, &cleared)
	if cleared.Repo.PinState != nil || cleared.Repo.SSHHostKey != nil {
		t.Fatalf("unpinned DTO = %+v, want no pin fields", cleared.Repo)
	}
	// A second unpin is the idempotent no-op (the repository still exists).
	if resp := requestJSON(t, oc, http.MethodDelete, base+"/"+repoID+"/host-key", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("second unpin: %d, want 200", resp.StatusCode)
	}

	// A foreign org's repo id is invisible to the unpin.
	s.SeedOrg("repo-pin-other", outsider)
	otherC := s.LoginAs(outsider)
	if resp := requestJSON(t, otherC, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/repo-pin-other/repos/"+repoID+"/host-key", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org unpin: %d, want 404", resp.StatusCode)
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/repo-pin-org/activity", nil)
	activity := mustReadBody(t, resp)
	if !strings.Contains(activity, "DELETE clearRepoHostKey") {
		t.Fatalf("activity missing the unpin: %s", activity)
	}
}

// seedVaultCredentialForRepo creates a vault_password credential through the
// real credentials service, outside any HTTP session — what a repository's
// vault_identities entry points at.
func seedVaultCredentialForRepo(t *testing.T, s *apitest.Server, orgID uuid.UUID, name string) string {
	t.Helper()
	scoped := db.WithOrgScope(context.Background(), orgID)
	c, err := s.Deps.Credentials.Create(scoped, orgID, credentials.CreateInput{
		Name: name, Kind: credentials.KindVaultPassword,
		Material: credentials.Material{Password: "the-vault-passphrase"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID.String()
}

// TestRepoVaultIdentitiesRoundTrip is the brief's Step 3: create a repo with
// two labelled vault identities, read the values back (not only the count —
// an earlier sub-project here shipped a round-trip test that counted and
// never compared, and the mapping bug it would have caught was found only by
// a later review), then update down to one and assert the dropped label is
// gone, on every read (GET and the PUT's own response).
func TestRepoVaultIdentitiesRoundTrip(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("repo-vault-owner@example.test", "correct horse battery")
	org := s.SeedOrg("repo-vault-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/repo-vault-org/repos"

	credProd := seedVaultCredentialForRepo(t, s, org, "prod-vault")
	credStaging := seedVaultCredentialForRepo(t, s, org, "staging-vault")

	resp := postJSON(t, c, base, map[string]any{
		"name": "infra", "url": "https://git.example.com/acme/infra.git",
		"vault_identities": []map[string]any{
			{"credential_id": credProd, "label": "prod"},
			{"credential_id": credStaging, "label": "staging"},
		},
	})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var created repoBody
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	wantCreated := []vaultIdentityBody{
		{CredentialID: credProd, Label: "prod"},
		{CredentialID: credStaging, Label: "staging"},
	}
	if !reflect.DeepEqual(created.Repo.VaultIdentities, wantCreated) {
		t.Fatalf("created vault identities = %+v, want %+v", created.Repo.VaultIdentities, wantCreated)
	}

	resp = requestJSON(t, c, http.MethodGet, base+"/"+created.Repo.ID, nil)
	var got repoBody
	decodeInto(t, resp, &got)
	if resp.StatusCode != http.StatusOK || !reflect.DeepEqual(got.Repo.VaultIdentities, wantCreated) {
		t.Fatalf("get: %d, vault identities = %+v, want %+v", resp.StatusCode, got.Repo.VaultIdentities, wantCreated)
	}

	resp = requestJSON(t, c, http.MethodPut, base+"/"+created.Repo.ID, map[string]any{
		"name": "infra", "url": created.Repo.URL,
		"vault_identities": []map[string]any{{"credential_id": credProd, "label": "prod"}},
	})
	var updated repoBody
	decodeInto(t, resp, &updated)
	wantUpdated := []vaultIdentityBody{{CredentialID: credProd, Label: "prod"}}
	if resp.StatusCode != http.StatusOK || !reflect.DeepEqual(updated.Repo.VaultIdentities, wantUpdated) {
		t.Fatalf("update: %d, vault identities = %+v, want %+v (staging must be dropped)", resp.StatusCode, updated.Repo.VaultIdentities, wantUpdated)
	}

	resp = requestJSON(t, c, http.MethodGet, base+"/"+created.Repo.ID, nil)
	decodeInto(t, resp, &got)
	if !reflect.DeepEqual(got.Repo.VaultIdentities, wantUpdated) {
		t.Fatalf("get after update = %+v, want %+v", got.Repo.VaultIdentities, wantUpdated)
	}
	for _, id := range got.Repo.VaultIdentities {
		if id.Label == "staging" {
			t.Fatalf("staging must be gone, got %+v", got.Repo.VaultIdentities)
		}
	}
}

// TestRepoCreateAndUpdateRefuseVaultCredentialID (2.92): the single
// vault_credential_id field stays on the request body (so a client sending
// it gets this 400, rather than huma silently dropping an undeclared
// field), but the service refuses it on every write now that the
// VaultIdentities editor is the only way to configure a repository's vault
// password.
func TestRepoCreateAndUpdateRefuseVaultCredentialID(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("repo-vault-legacy-owner@example.test", "correct horse battery")
	org := s.SeedOrg("repo-vault-legacy", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/repo-vault-legacy/repos"
	credLegacy := seedVaultCredentialForRepo(t, s, org, "legacy-vault")

	resp := postJSON(t, c, base, map[string]any{
		"name": "infra", "url": "https://git.example.com/acme/infra.git", "vault_credential_id": credLegacy,
	})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "vault_identities") {
		t.Fatalf("create with vault_credential_id: %d %s, want 400 naming vault_identities", resp.StatusCode, body)
	}

	resp = postJSON(t, c, base, map[string]any{"name": "infra", "url": "https://git.example.com/acme/infra.git"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var created repoBody
	decodeInto(t, resp, &created)

	resp = requestJSON(t, c, http.MethodPut, base+"/"+created.Repo.ID, map[string]any{
		"name": "infra", "url": created.Repo.URL, "vault_credential_id": credLegacy,
	})
	body = mustReadBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "vault_identities") {
		t.Fatalf("update with vault_credential_id: %d %s, want 400 naming vault_identities", resp.StatusCode, body)
	}
}

// TestRepoVaultIdentityLabelValidation is the brief's Step 2/3: one case per
// character class rejected (status and message), plus a duplicate label, a
// wrong-kind credential and an unknown credential.
func TestRepoVaultIdentityLabelValidation(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("repo-vault-val-owner@example.test", "correct horse battery")
	org := s.SeedOrg("repo-vault-val", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/repo-vault-val/repos"
	credVault := seedVaultCredentialForRepo(t, s, org, "vault")
	scoped := db.WithOrgScope(context.Background(), org)
	sshCred, err := s.Deps.Credentials.Create(scoped, org, credentials.CreateInput{
		Name: "host-login", Kind: credentials.KindSSHKey, Username: "ansible",
		Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		items []map[string]any
		want  int
		msg   string
	}{
		{"empty label", []map[string]any{{"credential_id": credVault, "label": ""}}, http.StatusBadRequest, "label is empty"},
		{"leading whitespace", []map[string]any{{"credential_id": credVault, "label": " prod"}}, http.StatusBadRequest, "leading or trailing whitespace"},
		{"trailing whitespace", []map[string]any{{"credential_id": credVault, "label": "prod "}}, http.StatusBadRequest, "leading or trailing whitespace"},
		{"newline", []map[string]any{{"credential_id": credVault, "label": "prod\nhost_key_checking = False"}}, http.StatusBadRequest, "newline"},
		{"carriage return", []map[string]any{{"credential_id": credVault, "label": "prod\rx"}}, http.StatusBadRequest, "carriage return"},
		{"comma", []map[string]any{{"credential_id": credVault, "label": "prod,staging"}}, http.StatusBadRequest, "comma"},
		{"at sign", []map[string]any{{"credential_id": credVault, "label": "prod@file"}}, http.StatusBadRequest, "'@'"},
		{"duplicate label", []map[string]any{
			{"credential_id": credVault, "label": "prod"},
			{"credential_id": credVault, "label": "prod"},
		}, http.StatusBadRequest, "duplicate vault identity label"},
		{"wrong credential kind", []map[string]any{{"credential_id": sshCred.ID.String(), "label": "prod"}}, http.StatusBadRequest, "vault_password credential"},
		{"unknown credential", []map[string]any{{"credential_id": "11111111-1111-1111-1111-111111111111", "label": "prod"}}, http.StatusBadRequest, "vault credential not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, c, base, map[string]any{
				"name": "r", "url": "https://git.example.com/x", "vault_identities": tc.items,
			})
			body := mustReadBody(t, resp)
			if resp.StatusCode != tc.want || !strings.Contains(body, tc.msg) {
				t.Fatalf("status %d, want %d naming %q: %s", resp.StatusCode, tc.want, tc.msg, body)
			}
		})
	}
	// An overlong label is a schema-level 422 (maxLength), not the service's
	// 400 — huma rejects it before the handler ever runs.
	overlong := strings.Repeat("a", repos.MaxVaultLabelLength+1)
	resp := postJSON(t, c, base, map[string]any{
		"name": "r", "url": "https://git.example.com/x",
		"vault_identities": []map[string]any{{"credential_id": credVault, "label": overlong}},
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("overlong label: %d, want 422", resp.StatusCode)
	}
}

// TestRepoVaultIdentityLabelValidationOnUpdate is the brief's Finding 3:
// TestRepoVaultIdentityLabelValidation above only ever exercises Create. The
// validate call both handlers share makes Update's behaviour provable by
// inspection, but a rule that guards create and not update is a classic
// back door, so it gets proven directly too — PUT an existing repo with a
// label that forges an ansible.cfg key and confirm it is refused exactly
// like Create refuses it.
func TestRepoVaultIdentityLabelValidationOnUpdate(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("repo-vault-val-upd-owner@example.test", "correct horse battery")
	org := s.SeedOrg("repo-vault-val-upd", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/repo-vault-val-upd/repos"
	credVault := seedVaultCredentialForRepo(t, s, org, "vault")

	resp := postJSON(t, c, base, map[string]any{
		"name": "r", "url": "https://git.example.com/x",
	})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var created repoBody
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}

	resp = requestJSON(t, c, http.MethodPut, base+"/"+created.Repo.ID, map[string]any{
		"name": "r", "url": "https://git.example.com/x",
		"vault_identities": []map[string]any{
			{"credential_id": credVault, "label": "prod\nhost_key_checking = False"},
		},
	})
	respBody := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(respBody, "newline") {
		t.Fatalf("update: %d, want 400 naming newline: %s", resp.StatusCode, respBody)
	}
}

// seedRepoFixture creates a repo through the service (outside any HTTP
// session) for tests that need a row seeded directly.
func seedRepoFixture(t *testing.T, s *apitest.Server, orgID uuid.UUID, name string) string {
	t.Helper()
	scoped := db.WithOrgScope(context.Background(), orgID)
	cred, err := s.Deps.Credentials.Create(scoped, orgID, credentials.CreateInput{
		Name: "deploy", Kind: credentials.KindGitToken, Username: "git",
		Material: credentials.Material{Token: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	credID := cred.ID
	r, err := s.Deps.Repos.Create(scoped, orgID, repos.CreateInput{
		Name: name, URL: "https://git.example.com/acme/" + name + ".git", CredentialID: &credID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID.String()
}
