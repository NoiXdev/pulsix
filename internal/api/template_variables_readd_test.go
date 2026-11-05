package api_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/varsource"
)

// TestRemovedSecretDeclaredAgainAsPlainIsNeverSentInTheClear (spec 7.75
// owner decision 4): a person removes the secret field token, whose sealed
// value stays stored; a later sync declares token again as a required
// plain string. The automatic apply holds that addition for a person, so
// a run does not receive the secret. And a template already holding a
// plain token field over the sealed value (applied before the hold
// existed) sends the run everything but token: the claim never opens a
// sealed value into a plain var.
func TestRemovedSecretDeclaredAgainAsPlainIsNeverSentInTheClear(t *testing.T) {
	const secret = "readd-s3cret-0f42"
	f := newRunFixture(t, "tmpl-readd-org")
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	repoID := seedRepoFixture(t, f.s, f.orgID, "infra")
	sync := func(commit string, fields ...schema.Field) {
		t.Helper()
		for i := range fields {
			fields[i].Origin = schema.OriginSource
			fields[i].Source = &schema.FieldSource{Kind: schema.SourceMarker, File: "site.yml", Line: i + 2}
		}
		seedVarSources(t, f.s, f.orgID, repoID, repos.SourcesRecord{Commit: commit, SyncedAt: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC), WorkerReads: true},
			[]varsource.Playbook{{Path: "site.yml", Fields: fields, Files: []varsource.FileUse{{Path: "site.yml", State: varsource.FileRead}}, Notes: []varsource.Note{}}})
	}
	sync("c1", schema.Field{Name: "token", Type: schema.TypeSecret, Required: true}, schema.Field{Name: "region", Type: schema.TypeString, Required: true})
	pb, _, err := f.s.Deps.Playbooks.EnsureRepo(scoped, f.orgID, uuid.MustParse(repoID), "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := f.s.Deps.Templates.Create(scoped, f.orgID, templates.CreateInput{
		Name: "deploy-readd", PlaybookID: pb.ID, Scope: templates.Scope{HostIDs: []uuid.UUID{f.hostA}},
	})
	if err != nil {
		t.Fatal(err)
	}
	syncedAt := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	linked, err := f.s.Deps.Templates.ApplyVariables(scoped, f.orgID, tmpl.ID, templates.ApplyInput{
		Mode: templates.ModeAutomatic, SourceRev: "c1", SourceSyncedAt: &syncedAt,
		Values: map[string]any{"token": secret, "region": "eu"},
	})
	if err != nil {
		t.Fatal(err)
	}
	region := linked.ExtraVarsSchema.Fields[1]
	if _, err := f.s.Deps.Templates.UpdateVariables(scoped, f.orgID, tmpl.ID, templates.VariablesInput{
		Schema: &schema.Schema{Fields: []schema.Field{region}}, ExtraVars: map[string]any{"region": "eu"},
	}); err != nil {
		t.Fatal(err)
	}

	sync("c2", schema.Field{Name: "token", Type: schema.TypeString, Required: true}, schema.Field{Name: "region", Type: schema.TypeString, Required: true})
	if err := f.s.Deps.Templates.ApplyRepoSources(context.Background(), f.orgID, uuid.MustParse(repoID)); err != nil {
		t.Fatal(err)
	}
	view, err := f.s.Deps.Templates.Variables(scoped, f.orgID, tmpl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(view.Template.ExtraVarsSchema.Fields); n != 1 || view.Review.NeedsReview != 1 || view.Source.AppliedRev != "c2" {
		t.Fatalf("after the sync: %d fields, review %+v, applied %q; want token held for review", n, view.Review, view.Source.AppliedRev)
	}
	claimVars := func() (map[string]string, []string) {
		t.Helper()
		jobID := runJob(t, f.operatorC, f.runURLFor(tmpl.ID))[0].ID
		claim, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA}, "v1")
		if err != nil || claim == nil || claim.Job.GetId() != jobID {
			t.Fatalf("Claim = %v, %v; want job %s", claim, err, jobID)
		}
		return claim.Job.GetExtraVars(), claim.Job.GetRedact()
	}
	vars, _ := claimVars()
	if _, sent := vars["token"]; sent || vars["region"] != `"eu"` {
		t.Fatalf("claim extra vars = %v, want region only", vars)
	}

	// The state an automatic apply left before the hold: token declared a
	// plain string, active, over the kept sealed value.
	store := templates.NewSQLStore(f.s.DB)
	cur, err := store.Get(scoped, f.orgID, tmpl.ID)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	cur.ExtraVarsSchema.Fields = append(cur.ExtraVarsSchema.Fields, schema.Field{Name: "token", Type: schema.TypeString, Required: true,
		Active: &on, Origin: schema.OriginSource, Source: &schema.FieldSource{Kind: schema.SourceMarker, File: "site.yml", Line: 2}})
	if _, err := store.UpdateVarsSource(scoped, cur, nil, nil); err != nil {
		t.Fatal(err)
	}
	vars, redact := claimVars()
	if _, sent := vars["token"]; sent || vars["region"] != `"eu"` {
		t.Fatalf("claim extra vars over a stray sealed token = %v, want region only", vars)
	}
	for _, v := range vars {
		if strings.Contains(v, secret) {
			t.Fatalf("claim extra vars = %v carry the secret", vars)
		}
	}
	if strings.Contains(strings.Join(redact, "\n"), secret) {
		t.Fatalf("redact = %v names a value that is not sent", redact)
	}
}

// TestTemplateVariablesShowNotSentValues (backlog 7.87): the variables
// read names the variables a run leaves out — a stored secret under a
// field that is no longer a secret — in review.not_sent, always an array.
func TestTemplateVariablesShowNotSentValues(t *testing.T) {
	f := newTemplateFixture(t, "tmpl-not-sent-org")
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	create := func(name string) string {
		t.Helper()
		body := f.body()
		body["name"] = name
		body["extra_vars_schema"] = map[string]any{"fields": []any{
			map[string]any{"name": "region", "type": "string"},
			map[string]any{"name": "token", "type": "secret"},
		}}
		body["extra_vars"] = map[string]any{"region": "eu", "token": "not-sent-s3cret"}
		return f.create(t, body).Template.ID
	}
	notSent := func(id string) any {
		t.Helper()
		resp := requestJSON(t, f.c, http.MethodGet, f.base+"/"+id+"/variables", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get variables: %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var got struct {
			Review map[string]any `json:"review"`
		}
		decodeInto(t, resp, &got)
		return got.Review["not_sent"]
	}

	clean := create("deploy-clean")
	if got := notSent(clean); !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("not_sent without strays = %#v, want []", got)
	}

	stray := create("deploy-stray")
	store := templates.NewSQLStore(f.s.DB)
	cur, err := store.Get(scoped, f.orgID, uuid.MustParse(stray))
	if err != nil {
		t.Fatal(err)
	}
	cur.ExtraVarsSchema.Fields[1].Type = schema.TypeString
	if _, err := store.UpdateVarsSource(scoped, cur, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := notSent(stray); !reflect.DeepEqual(got, []any{"token"}) {
		t.Fatalf("not_sent over a stray sealed token = %#v, want [token]", got)
	}
}
