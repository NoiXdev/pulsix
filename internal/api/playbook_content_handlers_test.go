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
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/varsource"
)

type playbookContentBody struct {
	Path     string    `json:"path"`
	Commit   string    `json:"commit"`
	SyncedAt time.Time `json:"synced_at"`
	Content  *string   `json:"content"`
	LeftOut  *string   `json:"left_out"`
	Earlier  bool      `json:"from_earlier_sync"`
}

func TestPlaybookContent(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("pbc-owner@example.test", "correct horse battery")
	org := s.SeedOrg("pbc-org", owner)
	viewer := s.SeedUser("pbc-viewer@example.test", "correct horse battery")
	s.AddMember(org, viewer, tenancy.RoleViewer)
	repoID := seedRepoFixture(t, s, org, "infra")
	scoped := db.WithOrgScope(context.Background(), org)
	register := func(path string) string {
		t.Helper()
		p, err := s.Deps.Playbooks.CreateRepo(scoped, org, uuid.MustParse(repoID), path)
		if err != nil {
			t.Fatal(err)
		}
		return p.ID.String()
	}
	site, big, old, capped := register("site.yml"), register("big.yml"), register("old.yml"), register("capped.yml")
	unsynced := register("unsynced.yml")
	base := s.HTTP.URL + "/api/v1/orgs/pbc-org/playbooks/"
	vc := s.LoginAs(viewer)

	// No sync recorded the repository yet.
	resp := requestJSON(t, vc, http.MethodGet, base+site+"/content", nil)
	if b := mustReadBody(t, resp); resp.StatusCode != http.StatusNotFound || !strings.Contains(b, "not been synced") {
		t.Fatalf("before a sync: %d %s, want 404 saying the repository has not been synced", resp.StatusCode, b)
	}

	synced := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	text := "- hosts: all\n  tasks: []\n"
	if err := repos.NewSQLStore(s.DB).ReplaceVarSources(scoped, org, uuid.MustParse(repoID),
		repos.SourcesRecord{Commit: "abc123", SyncedAt: synced, WorkerReads: true, Incomplete: true, LeftOut: []string{"capped.yml"}},
		[]varsource.Playbook{{Path: "site.yml"}, {Path: "big.yml"}, {Path: "old.yml"}, {Path: "capped.yml"}},
		map[string]repos.PlaybookText{"site.yml": {Content: &text}, "big.yml": {LeftOut: varsource.TextTooLarge}, "capped.yml": {Content: &text}},
	); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		id, path string
		content  *string
		leftOut  string
		commit   string
		earlier  bool
	}{
		{site, "site.yml", &text, "", "abc123", false},
		{big, "big.yml", nil, "too_large", "abc123", false},
		{old, "old.yml", nil, "not_sent", "abc123", false},
		// Left unread by a cap: the text is from an earlier sync, said by the flag.
		{capped, "capped.yml", &text, "", "", true},
	} {
		var body playbookContentBody
		resp := requestJSON(t, vc, http.MethodGet, base+tc.id+"/content", nil)
		decodeInto(t, resp, &body)
		if resp.StatusCode != http.StatusOK || body.Path != tc.path || body.Commit != tc.commit || body.Earlier != tc.earlier || !body.SyncedAt.Equal(synced) {
			t.Fatalf("%s: %d %+v", tc.path, resp.StatusCode, body)
		}
		if (body.Content == nil) != (tc.content == nil) || (body.Content != nil && *body.Content != *tc.content) {
			t.Fatalf("%s: content %v, want %v", tc.path, body.Content, tc.content)
		}
		if (tc.leftOut == "") != (body.LeftOut == nil) || (body.LeftOut != nil && *body.LeftOut != tc.leftOut) {
			t.Fatalf("%s: left_out %v, want %q", tc.path, body.LeftOut, tc.leftOut)
		}
	}

	// A path the last sync did not report (e.g. the file was removed) has
	// no text as of that sync.
	var unreported playbookContentBody
	resp = requestJSON(t, vc, http.MethodGet, base+unsynced+"/content", nil)
	decodeInto(t, resp, &unreported)
	if resp.StatusCode != http.StatusOK || unreported.Path != "unsynced.yml" || unreported.Commit != "abc123" ||
		!unreported.SyncedAt.Equal(synced) || unreported.Content != nil || unreported.LeftOut == nil || *unreported.LeftOut != "not_sent" {
		t.Fatalf("unreported path: %d %+v, want 200 not_sent", resp.StatusCode, unreported)
	}
	// An editor playbook's text is in its own DTO.
	editor := seedEditorPlaybook(t, s, org, namedPlaybook)
	if resp := requestJSON(t, vc, http.MethodGet, base+editor+"/content", nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("editor playbook: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, vc, http.MethodGet, base+uuid.Must(uuid.NewV7()).String()+"/content", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown playbook: %d", resp.StatusCode)
	}

	// Another organization's member reads nothing of it, in their own org
	// or in this one.
	outsider := s.SeedUser("pbc-out@example.test", "correct horse battery")
	s.SeedOrg("pbc-other", outsider)
	oc := s.LoginAs(outsider)
	if resp := requestJSON(t, oc, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/pbc-other/playbooks/"+site+"/content", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org read: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, oc, http.MethodGet, base+site+"/content", nil); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("non-member: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, s.Client(), http.MethodGet, base+site+"/content", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", resp.StatusCode)
	}
}
