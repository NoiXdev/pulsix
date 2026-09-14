package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type repoBody struct {
	Repo struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		URL          string `json:"url"`
		Branch       string `json:"branch"`
		Subpath      string `json:"subpath"`
		CredentialID string `json:"credential_id"`
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
