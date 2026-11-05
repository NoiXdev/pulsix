package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/varsource"
)

type discoveredBody struct {
	Sources *struct {
		Commit      string           `json:"commit"`
		SyncedAt    time.Time        `json:"synced_at"`
		WorkerReads bool             `json:"worker_reads"`
		Incomplete  bool             `json:"incomplete"`
		LeftOut     []string         `json:"left_out"`
		Notes       []varsource.Note `json:"notes"`
	} `json:"sources"`
	Playbooks []struct {
		Path       string  `json:"path"`
		Fields     int     `json:"fields"`
		Warnings   int     `json:"warnings"`
		Failed     bool    `json:"failed"`
		LeftOut    bool    `json:"left_out"`
		PlaybookID *string `json:"playbook_id"`
	} `json:"playbooks"`
}

// seedVarSources stores a repository's variable sources record directly,
// standing in for a sync's finish.
func seedVarSources(t *testing.T, s *apitest.Server, orgID uuid.UUID, repoID string, rec repos.SourcesRecord, pbs []varsource.Playbook) {
	t.Helper()
	if err := repos.NewSQLStore(s.DB).ReplaceVarSources(db.WithOrgScope(context.Background(), orgID), orgID, uuid.MustParse(repoID), rec, pbs); err != nil {
		t.Fatal(err)
	}
}

func TestRepoPlaybooksDiscoveredList(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("rpd-owner@example.test", "correct horse battery")
	org := s.SeedOrg("rpd-org", owner)
	viewer := s.SeedUser("rpd-viewer@example.test", "correct horse battery")
	s.AddMember(org, viewer, tenancy.RoleViewer)
	repoID := seedRepoFixture(t, s, org, "infra")
	base := s.HTTP.URL + "/api/v1/orgs/rpd-org/repos/" + repoID + "/playbooks"
	vc := s.LoginAs(viewer)

	// Before any sync recorded sources.
	var body discoveredBody
	resp := requestJSON(t, vc, http.MethodGet, base, nil)
	decodeInto(t, resp, &body)
	if resp.StatusCode != http.StatusOK || body.Sources != nil || body.Playbooks == nil || len(body.Playbooks) != 0 {
		t.Fatalf("before a sync: %d %+v", resp.StatusCode, body)
	}

	registered, err := s.Deps.Playbooks.CreateRepo(db.WithOrgScope(context.Background(), org), org, uuid.MustParse(repoID), "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	synced := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	warn := varsource.Note{Level: varsource.LevelWarning, Code: "unsupported_type", File: "site.yml", Line: 3, Message: "x"}
	seedVarSources(t, s, org, repoID, repos.SourcesRecord{
		Commit: "abc123", SyncedAt: synced, WorkerReads: true, Incomplete: true,
		LeftOut: []string{"kept.yml", "never.yml"},
		Notes:   []varsource.Note{{Level: varsource.LevelWarning, Code: "time_limit", Message: "y"}},
	}, []varsource.Playbook{
		{Path: "site.yml", Fields: []schema.Field{{Name: "a", Type: schema.TypeString}, {Name: "b", Type: schema.TypeString}}, Notes: []varsource.Note{warn}},
		{Path: "broken.yml", Failed: true, Notes: []varsource.Note{warn}},
		{Path: "kept.yml", Fields: []schema.Field{{Name: "a", Type: schema.TypeString}}},
	})

	body = discoveredBody{}
	resp = requestJSON(t, vc, http.MethodGet, base, nil)
	decodeInto(t, resp, &body)
	if resp.StatusCode != http.StatusOK || body.Sources == nil {
		t.Fatalf("viewer list: %d %+v", resp.StatusCode, body)
	}
	src := body.Sources
	if src.Commit != "abc123" || !src.SyncedAt.Equal(synced) || !src.WorkerReads || !src.Incomplete ||
		strings.Join(src.LeftOut, ",") != "kept.yml,never.yml" || len(src.Notes) != 1 || src.Notes[0].Code != "time_limit" {
		t.Fatalf("sources = %+v", src)
	}
	if len(body.Playbooks) != 3 {
		t.Fatalf("playbooks = %+v, want broken.yml, kept.yml, site.yml", body.Playbooks)
	}
	broken, kept, site := body.Playbooks[0], body.Playbooks[1], body.Playbooks[2]
	if broken.Path != "broken.yml" || !broken.Failed || broken.Warnings != 1 || broken.PlaybookID != nil {
		t.Fatalf("broken.yml = %+v", broken)
	}
	if kept.Path != "kept.yml" || !kept.LeftOut || kept.Failed || kept.Fields != 1 || kept.PlaybookID != nil {
		t.Fatalf("kept.yml = %+v", kept)
	}
	if site.Path != "site.yml" || site.Fields != 2 || site.Warnings != 1 || site.Failed || site.LeftOut ||
		site.PlaybookID == nil || *site.PlaybookID != registered.ID.String() {
		t.Fatalf("site.yml = %+v, want playbook_id %v", site, registered.ID)
	}

	// Another organization's member sees no such repository.
	outsider := s.SeedUser("rpd-outsider@example.test", "correct horse battery")
	s.SeedOrg("rpd-other", outsider)
	oc := s.LoginAs(outsider)
	if resp := requestJSON(t, oc, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/rpd-other/repos/"+repoID+"/playbooks", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another org: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, vc, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/rpd-org/repos/"+uuid.Must(uuid.NewV7()).String()+"/playbooks", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown repo: %d", resp.StatusCode)
	}
}

func TestPickRepoPlaybook(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("rpp-owner@example.test", "correct horse battery")
	org := s.SeedOrg("rpp-org", owner)
	operator := s.SeedUser("rpp-op@example.test", "correct horse battery")
	viewer := s.SeedUser("rpp-viewer@example.test", "correct horse battery")
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	repoID := seedRepoFixture(t, s, org, "infra")
	base := s.HTTP.URL + "/api/v1/orgs/rpp-org/repos/" + repoID + "/playbooks"
	oc := s.LoginAs(operator)

	type pickBody struct {
		Playbook struct {
			ID     string `json:"id"`
			Source string `json:"source"`
			RepoID string `json:"repo_id"`
			Path   string `json:"path"`
		} `json:"playbook"`
		Created bool `json:"created"`
	}
	var first pickBody
	resp := postJSON(t, oc, base, map[string]any{"path": "deploy.yml"})
	decodeInto(t, resp, &first)
	if resp.StatusCode != http.StatusOK || !first.Created || first.Playbook.Source != "repo" ||
		first.Playbook.RepoID != repoID || first.Playbook.Path != "deploy.yml" {
		t.Fatalf("first pick: %d %+v", resp.StatusCode, first)
	}
	var again pickBody
	resp = postJSON(t, oc, base, map[string]any{"path": "deploy.yml"})
	decodeInto(t, resp, &again)
	if resp.StatusCode != http.StatusOK || again.Created || again.Playbook.ID != first.Playbook.ID {
		t.Fatalf("second pick: %d %+v, want %s reused", resp.StatusCode, again, first.Playbook.ID)
	}

	vc := s.LoginAs(viewer)
	if resp := postJSON(t, vc, base, map[string]any{"path": "deploy.yml"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer pick: %d", resp.StatusCode)
	}
	if resp := postJSON(t, oc, base, map[string]any{"path": "../x.yml"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("../x.yml: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	unknown := s.HTTP.URL + "/api/v1/orgs/rpp-org/repos/" + uuid.Must(uuid.NewV7()).String() + "/playbooks"
	if resp := postJSON(t, oc, unknown, map[string]any{"path": "deploy.yml"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown repo: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, s.LoginAs(owner), http.MethodGet, s.HTTP.URL+"/api/v1/orgs/rpp-org/activity", nil)
	if activity := mustReadBody(t, resp); !strings.Contains(activity, "POST pickRepoPlaybook") {
		t.Fatalf("activity missing the pick: %s", activity)
	}
}
