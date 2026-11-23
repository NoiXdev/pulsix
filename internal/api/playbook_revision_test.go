package api_test

import (
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

type playbookRevisionBody struct {
	Playbook struct {
		ID       string `json:"id"`
		Inline   string `json:"inline"`
		Revision int64  `json:"revision"`
	} `json:"playbook"`
}

const (
	revisionTextOne = "- hosts: one\n  tasks: []\n"
	revisionTextTwo = "- hosts: two\n  tasks: []\n"
	revisionTextBad = "- hosts: stale\n  tasks: []\n"
)

// TestPlaybookSaveRevisionAndIfMatch pins backlog 3.127: the revision starts
// at 1 and moves with every saved text, GET and PUT answer it as the ETag, a
// PUT naming an older revision answers 412 and leaves text and revision
// alone, one without If-Match still saves.
func TestPlaybookSaveRevisionAndIfMatch(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("pbrev-owner@example.test", "correct horse battery")
	s.SeedOrg("pbrev-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/pbrev-org/playbooks"

	var created playbookRevisionBody
	decodeInto(t, postJSON(t, c, base, map[string]any{"inline": revisionTextOne}), &created)
	url := base + "/" + created.Playbook.ID
	if created.Playbook.Revision != 1 {
		t.Fatalf("created revision = %d, want 1", created.Playbook.Revision)
	}

	resp := requestJSON(t, c, http.MethodGet, url, nil)
	if got := resp.Header.Get("ETag"); got != `"1"` {
		t.Fatalf("GET ETag = %q", got)
	}

	// Current If-Match: saved, new ETag.
	resp = requestWithHeaders(t, c, http.MethodPut, url, map[string]any{"inline": revisionTextTwo}, map[string]string{"If-Match": `"1"`})
	var saved playbookRevisionBody
	decodeInto(t, resp, &saved)
	if resp.StatusCode != http.StatusOK || saved.Playbook.Revision != 2 || resp.Header.Get("ETag") != `"2"` {
		t.Fatalf("PUT with current If-Match: %d %+v ETag %q", resp.StatusCode, saved, resp.Header.Get("ETag"))
	}

	// Stale If-Match: 412, nothing changed.
	resp = requestWithHeaders(t, c, http.MethodPut, url, map[string]any{"inline": revisionTextBad}, map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("PUT with stale If-Match = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var after playbookRevisionBody
	decodeInto(t, requestJSON(t, c, http.MethodGet, url, nil), &after)
	if after.Playbook.Inline != revisionTextTwo || after.Playbook.Revision != 2 {
		t.Fatalf("after the refused save: %+v", after.Playbook)
	}

	// Malformed If-Match: as the templates answer it (412).
	resp = requestWithHeaders(t, c, http.MethodPut, url, map[string]any{"inline": revisionTextBad}, map[string]string{"If-Match": "banana"})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("PUT with malformed If-Match = %d", resp.StatusCode)
	}

	// No If-Match: saved, revision moves again.
	decodeInto(t, requestJSON(t, c, http.MethodPut, url, map[string]any{"inline": revisionTextOne}), &saved)
	if saved.Playbook.Revision != 3 || saved.Playbook.Inline != revisionTextOne {
		t.Fatalf("PUT without If-Match: %+v", saved.Playbook)
	}

	// The list carries the revision too.
	resp = requestJSON(t, c, http.MethodGet, base, nil)
	var list struct {
		Playbooks []struct {
			Revision int64 `json:"revision"`
		} `json:"playbooks"`
	}
	decodeInto(t, resp, &list)
	if len(list.Playbooks) != 1 || list.Playbooks[0].Revision != 3 {
		t.Fatalf("list = %+v", list)
	}

	// A missing playbook stays 404, even with an If-Match.
	resp = requestWithHeaders(t, c, http.MethodPut, base+"/00000000-0000-4000-8000-0000000000ff", map[string]any{"inline": revisionTextOne}, map[string]string{"If-Match": `"9"`})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("PUT on a missing playbook = %d", resp.StatusCode)
	}
}

// TestPlaybookRepoPutIgnoresRevision: a repo playbook is refused as it was,
// whatever If-Match says, never 412.
func TestPlaybookRepoPutIgnoresRevision(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("pbrev2-owner@example.test", "correct horse battery")
	org := s.SeedOrg("pbrev2-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/pbrev2-org/playbooks"
	repoID := seedRepoFixture(t, s, org, "infra")
	var created playbookRevisionBody
	decodeInto(t, postJSON(t, c, base, map[string]any{"repo_id": repoID, "path": "site.yml"}), &created)
	resp := requestWithHeaders(t, c, http.MethodPut, base+"/"+created.Playbook.ID, map[string]any{"inline": revisionTextOne}, map[string]string{"If-Match": `"5"`})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT on a repo playbook = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}
