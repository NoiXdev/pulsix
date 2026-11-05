package playbooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

const markedPlaybook = "- hosts: all\n  vars:\n    http_port: 8080  # pulsix: required HTTP port\n  tasks: []\n"

// recordingApplier is playbooks.InlineApplier's fake: it records every
// call and returns err.
type recordingApplier struct {
	mu    sync.Mutex
	calls []uuid.UUID
	err   error
}

func (a *recordingApplier) ApplyPlaybookSources(_ context.Context, _, playbookID uuid.UUID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, playbookID)
	return a.err
}

// logBuffer is a logger whose lines a test can count.
func logBuffer() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func fieldNames(pb varsource.Playbook) []string {
	names := make([]string, len(pb.Fields))
	for i, f := range pb.Fields {
		names[i] = f.Name
	}
	return names
}

func TestCreateEditorStoresItsVarSource(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, newFakeRepos(nil))

	p, err := svc.CreateEditor(ctx, org, playbooks.CreateInput{Inline: markedPlaybook})
	if err != nil {
		t.Fatalf("CreateEditor: %v", err)
	}
	if len(store.byID[p.ID].InlineSource) == 0 {
		t.Fatal("the parse result was not stored with the playbook")
	}
	src, err := svc.VarSource(ctx, org, p.ID)
	if err != nil {
		t.Fatalf("VarSource: %v", err)
	}
	if src.Kind != playbooks.VarSourceInline || !src.Found || !src.WorkerReads || src.SyncedAt != nil || src.RepoID != nil {
		t.Fatalf("VarSource = %+v", src)
	}
	if got := fieldNames(src.Playbook); len(got) != 1 || got[0] != "http_port" {
		t.Fatalf("fields = %v, want [http_port]", got)
	}
	if f := src.Playbook.Fields[0]; !f.Required || f.Label != "HTTP port" || f.Type != schema.TypeInt {
		t.Fatalf("field = %+v", f)
	}
	if !strings.HasPrefix(src.Rev, "text:") || len(src.Rev) != len("text:")+12 {
		t.Fatalf("Rev = %q, want text: and 12 hex characters", src.Rev)
	}

	// The same text has the same revision; an edit a different one.
	again, err := svc.CreateEditor(ctx, org, playbooks.CreateInput{Inline: markedPlaybook})
	if err != nil {
		t.Fatal(err)
	}
	againSrc, err := svc.VarSource(ctx, org, again.ID)
	if err != nil || againSrc.Rev != src.Rev {
		t.Fatalf("same text Rev = %q, %v; want %q", againSrc.Rev, err, src.Rev)
	}
	if _, err := svc.UpdateEditor(ctx, org, p.ID, playbooks.CreateInput{Inline: strings.Replace(markedPlaybook, "8080", "8081", 1)}); err != nil {
		t.Fatal(err)
	}
	edited, err := svc.VarSource(ctx, org, p.ID)
	if err != nil || edited.Rev == src.Rev || fmt.Sprint(edited.Playbook.Fields[0].Default) != "8081" {
		t.Fatalf("edited VarSource = %+v, %v; want a new Rev and the new default", edited, err)
	}
}

// TestCreateEditorParseProblemsNeverFailTheSave: a playbook whose variable
// sources cannot be read, or whose parse result cannot be stored, still
// saves; the result is read from the text instead.
func TestCreateEditorParseProblemsNeverFailTheSave(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, newFakeRepos(nil))

	// A marker the parser cannot read is a note, not a refusal.
	bad := "- hosts: all\n  vars:\n    x: 1  # pulsix: enum[a\n  tasks: []\n"
	p, err := svc.CreateEditor(ctx, org, playbooks.CreateInput{Inline: bad})
	if err != nil {
		t.Fatalf("CreateEditor(unreadable marker): %v", err)
	}
	src, err := svc.VarSource(ctx, org, p.ID)
	if err != nil || !src.Found || len(src.Playbook.Notes) == 0 {
		t.Fatalf("VarSource(unreadable marker) = %+v, %v; want a note", src, err)
	}

	// A default carrying NUL: the database's JSON type cannot hold it, so
	// nothing is stored and the text is parsed on read.
	nul := "- hosts: all\n  vars:\n    x: \"a\\0b\"  # pulsix: X\n  tasks: []\n"
	p, err = svc.CreateEditor(ctx, org, playbooks.CreateInput{Inline: nul})
	if err != nil {
		t.Fatalf("CreateEditor(NUL default): %v", err)
	}
	if store.byID[p.ID].InlineSource != nil {
		t.Fatalf("stored source = %s, want none", store.byID[p.ID].InlineSource)
	}
	src, err = svc.VarSource(ctx, org, p.ID)
	if err != nil || !src.Found || len(src.Playbook.Fields) != 1 || src.Playbook.Fields[0].Default != "a\x00b" {
		t.Fatalf("VarSource(NUL default) = %+v, %v", src, err)
	}
}

func TestUpdateEditorReparsesAndApplies(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, newFakeRepos(nil))
	logger, logs := logBuffer()
	svc.SetLogger(logger)

	p, err := svc.CreateEditor(ctx, org, playbooks.CreateInput{Inline: validPlaybook})
	if err != nil {
		t.Fatal(err)
	}
	applier := &recordingApplier{}
	svc.SetInlineApplier(applier)

	if _, err := svc.UpdateEditor(ctx, org, p.ID, playbooks.CreateInput{Inline: markedPlaybook}); err != nil {
		t.Fatalf("UpdateEditor: %v", err)
	}
	if len(applier.calls) != 1 || applier.calls[0] != p.ID {
		t.Fatalf("applier calls = %v, want one with %v", applier.calls, p.ID)
	}
	src, err := svc.VarSource(ctx, org, p.ID)
	if err != nil || len(src.Playbook.Fields) != 1 || src.Playbook.Fields[0].Name != "http_port" {
		t.Fatalf("VarSource after the update = %+v, %v; want the new field", src, err)
	}

	// An applier failure is logged; the save stands.
	applier.err = errors.New("apply boom")
	updated, err := svc.UpdateEditor(ctx, org, p.ID, playbooks.CreateInput{Inline: validPlaybook})
	if err != nil {
		t.Fatalf("UpdateEditor with a failing applier = %v, want the save to succeed", err)
	}
	if updated.Inline == nil || *updated.Inline != validPlaybook || *store.byID[p.ID].Inline != validPlaybook {
		t.Fatalf("updated = %+v", updated)
	}
	if len(applier.calls) != 2 {
		t.Fatalf("applier calls = %d, want 2", len(applier.calls))
	}
	if n := strings.Count(logs.String(), "apply boom"); n != 1 {
		t.Fatalf("log lines naming the failure = %d, want 1:\n%s", n, logs.String())
	}

	// A refused update applies nothing.
	if _, err := svc.UpdateEditor(ctx, org, p.ID, playbooks.CreateInput{Inline: "hosts: all\n"}); !errors.Is(err, playbooks.ErrInvalidInput) {
		t.Fatalf("invalid update = %v", err)
	}
	if len(applier.calls) != 2 {
		t.Fatalf("applier calls after a refused update = %d, want 2", len(applier.calls))
	}
}

func TestVarSourceParsesALegacyInlinePlaybook(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, newFakeRepos(nil))

	for name, stored := range map[string]json.RawMessage{
		"saved before the column": nil,
		"no longer decodes":       json.RawMessage(`{"fields": "not a list"}`),
	} {
		t.Run(name, func(t *testing.T) {
			text := markedPlaybook
			id := uuid.Must(uuid.NewV7())
			store.byID[id] = playbooks.Playbook{ID: id, OrgID: org, Source: playbooks.SourceEditor, Inline: &text, InlineSource: stored}

			src, err := svc.VarSource(ctx, org, id)
			if err != nil || !src.Found || src.Kind != playbooks.VarSourceInline {
				t.Fatalf("VarSource = %+v, %v", src, err)
			}
			if got := fieldNames(src.Playbook); len(got) != 1 || got[0] != "http_port" {
				t.Fatalf("fields = %v, want [http_port]", got)
			}
			// Parsed on read, not written.
			if !bytes.Equal(store.byID[id].InlineSource, stored) {
				t.Fatalf("stored source = %s, want it untouched", store.byID[id].InlineSource)
			}
		})
	}

	if _, err := svc.VarSource(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, playbooks.ErrNotFound) {
		t.Fatalf("unknown playbook = %v, want ErrNotFound", err)
	}
}

func TestVarSourceRepoPlaybook(t *testing.T) {
	org, repoID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rs := newFakeRepos(map[uuid.UUID]repos.Repo{repoID: {ID: repoID, OrgID: org}})
	svc := newService(newFakeStore(), rs)
	synced := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

	deploy, err := svc.CreateRepo(ctx, org, repoID, "deploy.yml")
	if err != nil {
		t.Fatal(err)
	}
	missing, err := svc.CreateRepo(ctx, org, repoID, "missing.yml")
	if err != nil {
		t.Fatal(err)
	}

	// No record yet.
	src, err := svc.VarSource(ctx, org, deploy.ID)
	if err != nil {
		t.Fatalf("VarSource without a record: %v", err)
	}
	if src.Kind != playbooks.VarSourceRepo || src.Found || src.WorkerReads || src.Rev != "" || src.SyncedAt != nil ||
		src.RepoID == nil || *src.RepoID != repoID || src.Path != "deploy.yml" {
		t.Fatalf("VarSource without a record = %+v", src)
	}

	rs.sources[repoID] = fakeSources{
		rec: repos.SourcesRecord{Commit: "abc123", SyncedAt: synced, WorkerReads: true, Incomplete: true},
		playbooks: []varsource.Playbook{{Path: "deploy.yml", Fields: []schema.Field{
			{Name: "app_version", Type: schema.TypeString, Origin: schema.OriginSource},
		}}},
	}
	src, err = svc.VarSource(ctx, org, deploy.ID)
	if err != nil {
		t.Fatalf("VarSource: %v", err)
	}
	if !src.Found || src.Rev != "abc123" || src.SyncedAt == nil || !src.SyncedAt.Equal(synced) || !src.WorkerReads || !src.Incomplete {
		t.Fatalf("VarSource = %+v", src)
	}
	if got := fieldNames(src.Playbook); len(got) != 1 || got[0] != "app_version" {
		t.Fatalf("fields = %v", got)
	}

	// The record does not name the path: not found, the record still told.
	src, err = svc.VarSource(ctx, org, missing.ID)
	if err != nil || src.Found || src.Rev != "abc123" || src.SyncedAt == nil || !src.WorkerReads {
		t.Fatalf("VarSource(missing.yml) = %+v, %v", src, err)
	}
}

func TestDiscovered(t *testing.T) {
	org, repoID, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rs := newFakeRepos(map[uuid.UUID]repos.Repo{repoID: {ID: repoID, OrgID: org}})
	svc := newService(newFakeStore(), rs)

	// No sync recorded anything yet.
	d, err := svc.Discovered(ctx, org, repoID)
	if err != nil || d.Record != nil || len(d.Playbooks) != 0 {
		t.Fatalf("Discovered without a record = %+v, %v", d, err)
	}

	registered, err := svc.CreateRepo(ctx, org, repoID, "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	warn := varsource.Note{Level: varsource.LevelWarning, Code: "x", Message: "x"}
	hint := varsource.Note{Level: varsource.LevelHint, Code: "y", Message: "y"}
	rs.sources[repoID] = fakeSources{
		rec: repos.SourcesRecord{Commit: "abc", WorkerReads: true, Incomplete: true, LeftOut: []string{"kept.yml", "never.yml"}},
		playbooks: []varsource.Playbook{
			{Path: "broken.yml", Failed: true, Notes: []varsource.Note{warn}},
			{Path: "kept.yml", Fields: []schema.Field{{Name: "a"}}},
			{Path: "site.yml", Fields: []schema.Field{{Name: "a"}, {Name: "b"}}, Notes: []varsource.Note{warn, hint, warn}},
		},
	}
	d, err = svc.Discovered(ctx, org, repoID)
	if err != nil || d.Record == nil || d.Record.Commit != "abc" {
		t.Fatalf("Discovered = %+v, %v", d, err)
	}
	want := []playbooks.DiscoveredPlaybook{
		{Path: "broken.yml", Warnings: 1, Failed: true},
		{Path: "kept.yml", Fields: 1, LeftOut: true},
		{Path: "site.yml", Fields: 2, Warnings: 2, PlaybookID: &registered.ID},
	}
	if len(d.Playbooks) != len(want) {
		t.Fatalf("playbooks = %+v, want %+v", d.Playbooks, want)
	}
	for i, got := range d.Playbooks {
		w := want[i]
		if got.Path != w.Path || got.Fields != w.Fields || got.Warnings != w.Warnings || got.Failed != w.Failed ||
			got.LeftOut != w.LeftOut || (got.PlaybookID == nil) != (w.PlaybookID == nil) ||
			(got.PlaybookID != nil && *got.PlaybookID != *w.PlaybookID) {
			t.Fatalf("playbook %d = %+v, want %+v", i, got, w)
		}
	}

	if _, err := svc.Discovered(ctx, other, repoID); !errors.Is(err, playbooks.ErrNotFound) {
		t.Fatalf("another organization's repository = %v, want ErrNotFound", err)
	}
}

func TestEnsureRepo(t *testing.T) {
	org, repoID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rs := newFakeRepos(map[uuid.UUID]repos.Repo{repoID: {ID: repoID, OrgID: org}})
	resolver := &fakeTemplateResolver{}
	svc := newServiceWithResolver(newFakeStore(), rs, resolver)

	first, created, err := svc.EnsureRepo(ctx, org, repoID, "site.yml")
	if err != nil || !created || first.Path == nil || *first.Path != "site.yml" {
		t.Fatalf("first EnsureRepo = %+v, %v, %v", first, created, err)
	}
	again, created, err := svc.EnsureRepo(ctx, org, repoID, "site.yml")
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("second EnsureRepo = %+v, %v, %v; want the same row", again, created, err)
	}
	if len(resolver.calls) != 1 {
		t.Fatalf("resolver calls = %d, want 1 (only for the new row)", len(resolver.calls))
	}
	if _, _, err := svc.EnsureRepo(ctx, org, repoID, "../x.yml"); !errors.Is(err, playbooks.ErrInvalidInput) {
		t.Fatalf("../x.yml = %v, want ErrInvalidInput", err)
	}
	if _, _, err := svc.EnsureRepo(ctx, org, uuid.Must(uuid.NewV7()), "site.yml"); !errors.Is(err, playbooks.ErrNotFound) {
		t.Fatalf("unknown repository = %v, want ErrNotFound", err)
	}
}
