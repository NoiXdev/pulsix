package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/varsource"
)

// linkedVariablesBody decodes the variables page with its source and
// review.
type linkedVariablesBody struct {
	templateVariablesBody
	Source *struct {
		Kind        string `json:"kind"`
		Commit      string `json:"commit"`
		AppliedRev  string `json:"applied_rev"`
		Newer       bool   `json:"newer"`
		WorkerReads bool   `json:"worker_reads"`
		Found       bool   `json:"found"`
		Available   bool   `json:"available"`
		Path        string `json:"path"`
		RepoID      string `json:"repo_id"`
	} `json:"source"`
	Review *struct {
		Gone            int `json:"gone"`
		NeedsReview     int `json:"needs_review"`
		MissingRequired int `json:"missing_required"`
		LeftOut         int `json:"left_out"`
	} `json:"review"`
}

type previewBody struct {
	Preview struct {
		SourceRev string `json:"source_rev"`
		Added     []struct {
			Field struct {
				Name   string `json:"name"`
				Active *bool  `json:"active"`
			} `json:"field"`
		} `json:"added"`
		Notes []varsource.Note `json:"notes"`
	} `json:"preview"`
	Source struct {
		Commit   string     `json:"commit"`
		SyncedAt *time.Time `json:"synced_at"`
	} `json:"source"`
}

// siteRecord is a repository record whose site.yml declares release
// (required) and nginx_port (default 80), read at commit.
func siteRecord(commit string) (repos.SourcesRecord, []varsource.Playbook) {
	src := func(line int) *schema.FieldSource {
		return &schema.FieldSource{Kind: schema.SourceMarker, File: "site.yml", Line: line}
	}
	return repos.SourcesRecord{Commit: commit, SyncedAt: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC), WorkerReads: true},
		[]varsource.Playbook{{
			Path: "site.yml",
			Fields: []schema.Field{
				{Name: "release", Type: schema.TypeString, Required: true, Origin: schema.OriginSource, Source: src(2)},
				{Name: "nginx_port", Type: schema.TypeInt, Default: float64(80), Origin: schema.OriginSource, Source: src(3)},
			},
			Files: []varsource.FileUse{{Path: "site.yml", State: varsource.FileRead}},
			Notes: []varsource.Note{},
		}}
}

// TestTemplateVariablesLinkFlow: a template of a discovered playbook is
// linked through a preview and an apply, shows where its fields come from
// and whether a newer read exists, switches between manual and automatic,
// and refuses an apply of a stale preview.
func TestTemplateVariablesLinkFlow(t *testing.T) {
	f := newTemplateFixture(t, "tmpl-link-org")
	repoID := seedRepoFixture(t, f.s, f.orgID, "infra")
	rec, pbs := siteRecord("c1")
	seedVarSources(t, f.s, f.orgID, repoID, rec, pbs)
	var picked struct {
		Playbook struct {
			ID string `json:"id"`
		} `json:"playbook"`
	}
	resp := postJSON(t, f.c, f.s.HTTP.URL+"/api/v1/orgs/tmpl-link-org/repos/"+repoID+"/playbooks", map[string]any{"path": "site.yml"})
	decodeInto(t, resp, &picked)
	body := f.body()
	body["playbook_id"] = picked.Playbook.ID
	created := f.create(t, body)
	url := f.base + "/" + created.Template.ID + "/variables"

	var preview previewBody
	resp = postJSON(t, f.c, url+"/preview", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &preview)
	if len(preview.Preview.Added) != 2 || preview.Preview.Added[0].Field.Name != "release" || preview.Preview.Added[1].Field.Name != "nginx_port" ||
		preview.Preview.SourceRev != "c1" || preview.Source.Commit != "c1" {
		t.Fatalf("preview = %+v", preview)
	}

	if preview.Source.SyncedAt == nil {
		t.Fatalf("preview source without synced_at: %+v", preview.Source)
	}
	apply := map[string]any{"mode": "manual", "source_rev": "c1", "source_synced_at": preview.Source.SyncedAt,
		"remove_gone": false, "values": map[string]any{"release": "1.0"}}
	resp = requestWithHeaders(t, f.c, http.MethodPost, url+"/apply", apply, map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"2"` {
		t.Fatalf("apply: %d ETag %q %s", resp.StatusCode, resp.Header.Get("ETag"), mustReadBody(t, resp))
	}

	var got linkedVariablesBody
	resp = requestJSON(t, f.c, http.MethodGet, url, nil)
	decodeInto(t, resp, &got)
	if got.Mode != "manual" || got.Source == nil || got.Source.Commit != "c1" || got.Source.AppliedRev != "c1" || got.Source.Newer ||
		got.Source.Kind != "repo" || got.Source.Path != "site.yml" || got.Source.RepoID != repoID || !got.Source.Available ||
		got.Review == nil || got.ExtraVars["release"] != "1.0" || got.ExtraVarsSchema == nil || len(got.ExtraVarsSchema.Fields) != 2 {
		t.Fatalf("get after apply = %+v source %+v review %+v", got, got.Source, got.Review)
	}

	rec, pbs = siteRecord("c2")
	seedVarSources(t, f.s, f.orgID, repoID, rec, pbs)
	got = linkedVariablesBody{}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, url, nil), &got)
	if got.Source == nil || !got.Source.Newer || got.Source.Commit != "c2" || got.Source.AppliedRev != "c1" {
		t.Fatalf("source after a newer sync = %+v", got.Source)
	}

	resp = requestWithHeaders(t, f.c, http.MethodPut, url+"/mode", map[string]any{"mode": "automatic"}, map[string]string{"If-Match": `"2"`})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"3"` {
		t.Fatalf("mode automatic: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &got)
	if got.Mode != "automatic" {
		t.Fatalf("mode = %q", got.Mode)
	}

	resp = postJSON(t, f.c, url+"/apply", apply)
	if msg := mustReadBody(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(msg, "preview again") {
		t.Fatalf("stale source_rev: %d %s, want 409", resp.StatusCode, msg)
	}
	current := map[string]any{"mode": "automatic", "source_rev": "c2", "source_synced_at": rec.SyncedAt}
	resp = postJSON(t, f.c, url+"/apply", map[string]any{"mode": "automatic", "source_rev": "c2"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("apply without the preview's synced_at: %d %s, want 409", resp.StatusCode, mustReadBody(t, resp))
	}

	// A value entered for an inactive field is stored; null removes it.
	for _, tc := range []struct {
		value any
		want  bool
	}{{float64(8080), true}, {nil, false}} {
		current["values"] = map[string]any{"nginx_port": tc.value}
		resp = postJSON(t, f.c, url+"/apply", current)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("apply nginx_port %v: %d %s", tc.value, resp.StatusCode, mustReadBody(t, resp))
		}
		got = linkedVariablesBody{}
		decodeInto(t, resp, &got)
		if v, ok := got.ExtraVars["nginx_port"]; ok != tc.want || got.ExtraVars["release"] != "1.0" {
			t.Fatalf("after nginx_port %v: vars %v (nginx_port %v)", tc.value, got.ExtraVars, v)
		}
	}

	// An unlinked template links only through a preview; its apply checks
	// the values like a save, located under body.values.
	other := f.body()
	other["name"], other["playbook_id"] = "other-site", picked.Playbook.ID
	otherURL := f.base + "/" + f.create(t, other).Template.ID + "/variables"
	resp = requestJSON(t, f.c, http.MethodPut, otherURL+"/mode", map[string]any{"mode": "manual"})
	if msg := mustReadBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, "preview") {
		t.Fatalf("mode on an unlinked template: %d %s, want 400", resp.StatusCode, msg)
	}
	resp = postJSON(t, f.c, otherURL+"/apply", map[string]any{"mode": "manual", "source_rev": "c2", "source_synced_at": rec.SyncedAt})
	if msg := mustReadBody(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(msg, "body.values.release") {
		t.Fatalf("apply without the required value: %d %s, want 422 at body.values.release", resp.StatusCode, msg)
	}

	// The list carries the review of the linked template only.
	var list struct {
		Templates []struct {
			ID     string          `json:"id"`
			Review *map[string]any `json:"variables_review"`
		} `json:"templates"`
	}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, f.base, nil), &list)
	for _, tm := range list.Templates {
		if (tm.ID == created.Template.ID) != (tm.Review != nil) {
			t.Errorf("template %s review %v", tm.ID, tm.Review)
		}
	}

	viewer := f.s.SeedUser("tmpl-link-viewer@example.test", "correct horse battery")
	f.s.AddMember(f.orgID, viewer, tenancy.RoleViewer)
	vc := f.s.LoginAs(viewer)
	if resp := postJSON(t, vc, url+"/preview", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer preview: %d, want 403", resp.StatusCode)
	}
	if resp := requestJSON(t, vc, http.MethodPut, url+"/mode", map[string]any{"mode": "off"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer mode: %d, want 403", resp.StatusCode)
	}

	activity := mustReadBody(t, requestJSON(t, f.c, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/tmpl-link-org/activity", nil))
	if !strings.Contains(activity, "applyTemplateVariables") || !strings.Contains(activity, "setTemplateVariablesMode") {
		t.Fatalf("activity = %s, want the apply and the mode change audited", activity)
	}
}

// TestInlinePlaybookSaveAppliesAutomaticTemplates: saving an editor
// playbook brings its new variable sources to its templates in automatic
// mode, as wired in production.
func TestInlinePlaybookSaveAppliesAutomaticTemplates(t *testing.T) {
	f := newTemplateFixture(t, "tmpl-inline-org")
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	pb, err := f.s.Deps.Playbooks.CreateEditor(scoped, f.orgID, playbooks.CreateInput{
		Inline: "- hosts: all\n  vars:\n    region: eu  # pulsix: Region\n  tasks: []\n"})
	if err != nil {
		t.Fatal(err)
	}
	body := f.body()
	body["playbook_id"] = pb.ID.String()
	url := f.base + "/" + f.create(t, body).Template.ID + "/variables"
	var preview previewBody
	decodeInto(t, postJSON(t, f.c, url+"/preview", nil), &preview)
	resp := postJSON(t, f.c, url+"/apply", map[string]any{"mode": "automatic", "source_rev": preview.Preview.SourceRev})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	if _, err := f.s.Deps.Playbooks.UpdateEditor(scoped, f.orgID, pb.ID, playbooks.CreateInput{
		Inline: "- hosts: all\n  vars:\n    region: eu  # pulsix: Region\n    size: 3  # pulsix: Size\n  tasks: []\n"}); err != nil {
		t.Fatal(err)
	}
	var got linkedVariablesBody
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, url, nil), &got)
	if got.ExtraVarsSchema == nil || len(got.ExtraVarsSchema.Fields) != 2 || got.ExtraVarsSchema.Fields[1]["name"] != "size" ||
		got.Revision != 3 || got.Source == nil || got.Source.Newer || got.Source.Kind != "inline" {
		t.Fatalf("after the save = %+v source %+v", got, got.Source)
	}
}

// TestSourceRevisionOnlySyncKeepsTheETag: a sync whose read changes none
// of an automatic template's fields records its commit without a new
// revision, so the template dialog's save based on the earlier ETag
// still goes through; a read that changes a field moves the ETag.
func TestSourceRevisionOnlySyncKeepsTheETag(t *testing.T) {
	f := newTemplateFixture(t, "tmpl-revonly-org")
	repoID := seedRepoFixture(t, f.s, f.orgID, "infra")
	rec, pbs := siteRecord("c1")
	seedVarSources(t, f.s, f.orgID, repoID, rec, pbs)
	pb, _, err := f.s.Deps.Playbooks.EnsureRepo(db.WithOrgScope(context.Background(), f.orgID), f.orgID, uuid.MustParse(repoID), "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	body := f.body()
	body["playbook_id"] = pb.ID.String()
	created := f.create(t, body)
	url := f.base + "/" + created.Template.ID
	resp := postJSON(t, f.c, url+"/variables/apply", map[string]any{"mode": "automatic", "source_rev": "c1",
		"source_synced_at": rec.SyncedAt, "values": map[string]any{"release": "1.0"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	type stamped struct {
		Template struct {
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"template"`
	}
	read := func() (string, time.Time) {
		t.Helper()
		resp := requestJSON(t, f.c, http.MethodGet, url, nil)
		var s stamped
		decodeInto(t, resp, &s)
		return resp.Header.Get("ETag"), s.Template.UpdatedAt
	}
	etag, updated := read()
	if etag != `"2"` {
		t.Fatalf("ETag after linking = %s", etag)
	}

	sync := func(commit string, mod func([]varsource.Playbook)) {
		t.Helper()
		rec, pbs := siteRecord(commit)
		mod(pbs)
		seedVarSources(t, f.s, f.orgID, repoID, rec, pbs)
		if err := f.s.Deps.Templates.ApplyRepoSources(context.Background(), f.orgID, uuid.MustParse(repoID)); err != nil {
			t.Fatal(err)
		}
	}
	sync("c2", func([]varsource.Playbook) {})
	if e, u := read(); e != etag || !u.Equal(updated) {
		t.Fatalf("after a sync without changes: ETag %s updated %v, want %s %v", e, u, etag, updated)
	}
	var vars linkedVariablesBody
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, url+"/variables", nil), &vars)
	if vars.Source == nil || vars.Source.AppliedRev != "c2" || vars.Source.Newer {
		t.Fatalf("source = %+v, want c2 recorded", vars.Source)
	}
	save := f.body()
	save["playbook_id"] = pb.ID.String()
	save["extra_vars_schema"] = map[string]any{"fields": vars.ExtraVarsSchema.Fields}
	save["extra_vars"] = vars.ExtraVars
	resp = requestWithHeaders(t, f.c, http.MethodPut, url, save, map[string]string{"If-Match": etag})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"3"` {
		t.Fatalf("save with the ETag read before the sync: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	sync("c3", func(pbs []varsource.Playbook) { pbs[0].Fields[1].Default = float64(81) })
	if e, _ := read(); e != `"4"` {
		t.Fatalf("after a sync that changes a field: ETag %s, want \"4\"", e)
	}
	vars = linkedVariablesBody{}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, url+"/variables", nil), &vars)
	if fields := vars.ExtraVarsSchema.Fields; len(fields) != 2 || fields[1]["default"] != float64(81) || vars.ExtraVars["release"] != "1.0" {
		t.Fatalf("after c3: %+v", vars.templateVariablesBody)
	}
}

// TestPlaybookChangeLeavesTheVariablesToAPreview: the template dialog's
// save with another playbook turns an automatic template manual and
// forgets the applied source, in the save's own revision.
func TestPlaybookChangeLeavesTheVariablesToAPreview(t *testing.T) {
	f := newTemplateFixture(t, "tmpl-repoint-org")
	repoID := seedRepoFixture(t, f.s, f.orgID, "infra")
	rec, pbs := siteRecord("c1")
	seedVarSources(t, f.s, f.orgID, repoID, rec, pbs)
	pb, _, err := f.s.Deps.Playbooks.EnsureRepo(db.WithOrgScope(context.Background(), f.orgID), f.orgID, uuid.MustParse(repoID), "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	body := f.body()
	body["playbook_id"] = pb.ID.String()
	created := f.create(t, body)
	url := f.base + "/" + created.Template.ID
	resp := postJSON(t, f.c, url+"/variables/apply", map[string]any{"mode": "automatic", "source_rev": "c1",
		"source_synced_at": rec.SyncedAt, "values": map[string]any{"release": "1.0"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var vars linkedVariablesBody
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, url+"/variables", nil), &vars)

	save := f.body()
	save["extra_vars_schema"] = map[string]any{"fields": vars.ExtraVarsSchema.Fields}
	save["extra_vars"] = vars.ExtraVars
	resp = requestWithHeaders(t, f.c, http.MethodPut, url, save, map[string]string{"If-Match": `"2"`})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"3"` {
		t.Fatalf("save with another playbook: %d ETag %q %s", resp.StatusCode, resp.Header.Get("ETag"), mustReadBody(t, resp))
	}
	vars = linkedVariablesBody{}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, url+"/variables", nil), &vars)
	if vars.Mode != "manual" || vars.Source == nil || vars.Source.AppliedRev != "" || vars.Revision != 3 {
		t.Fatalf("after the playbook change: mode %q revision %d source %+v, want manual with nothing applied", vars.Mode, vars.Revision, vars.Source)
	}
}
