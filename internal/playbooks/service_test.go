package playbooks_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
)

var ctx = context.Background()

// --- fakes ---

type fakeStore struct {
	mu      sync.Mutex
	byID    map[uuid.UUID]playbooks.Playbook
	nextErr error
	// inUseErr, when set, is what Delete returns — standing in for the FK
	// violation the SQL store maps.
	inUseErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{byID: map[uuid.UUID]playbooks.Playbook{}}
}

func (f *fakeStore) Create(_ context.Context, p playbooks.Playbook) (playbooks.Playbook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nextErr != nil {
		return playbooks.Playbook{}, f.nextErr
	}
	f.byID[p.ID] = p
	return p, nil
}

func (f *fakeStore) Get(_ context.Context, orgID, id uuid.UUID) (playbooks.Playbook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byID[id]
	if !ok || p.OrgID != orgID {
		return playbooks.Playbook{}, playbooks.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID) ([]playbooks.Playbook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []playbooks.Playbook
	for _, p := range f.byID {
		if p.OrgID == orgID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateInline(_ context.Context, orgID, id uuid.UUID, inline string) (playbooks.Playbook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byID[id]
	if !ok || p.OrgID != orgID {
		return playbooks.Playbook{}, playbooks.ErrNotFound
	}
	p.Inline = &inline
	f.byID[id] = p
	return p, nil
}

func (f *fakeStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byID[id]
	if !ok || p.OrgID != orgID {
		return playbooks.ErrNotFound
	}
	if f.inUseErr != nil {
		return f.inUseErr
	}
	delete(f.byID, id)
	return nil
}

type fakeRepos struct {
	byID map[uuid.UUID]repos.Repo
}

func newFakeRepos(byID map[uuid.UUID]repos.Repo) *fakeRepos {
	if byID == nil {
		byID = map[uuid.UUID]repos.Repo{}
	}
	return &fakeRepos{byID: byID}
}

func (f *fakeRepos) Get(_ context.Context, orgID, id uuid.UUID) (repos.Repo, error) {
	r, ok := f.byID[id]
	if !ok || r.OrgID != orgID {
		return repos.Repo{}, repos.ErrNotFound
	}
	return r, nil
}

// fakeTemplateResolver is playbooks.TemplateResolver's own fake: records
// every call CreateRepo makes, and what it is told to return, so tests can
// prove the organization-side resolution hook (Task 6's own review
// finding) fires exactly when a real repo playbook is stored — and not
// before, and not when storing one fails.
type fakeTemplateResolver struct {
	mu    sync.Mutex
	calls []resolveCall
	err   error
}

type resolveCall struct {
	orgID, repoID, playbookID uuid.UUID
	path                      string
}

func (f *fakeTemplateResolver) ResolveTemplatePlaybooks(_ context.Context, orgID, repoID, playbookID uuid.UUID, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, resolveCall{orgID: orgID, repoID: repoID, playbookID: playbookID, path: path})
	return nil
}

func newService(store *fakeStore, rs *fakeRepos) *playbooks.Service {
	return newServiceWithResolver(store, rs, &fakeTemplateResolver{})
}

func newServiceWithResolver(store *fakeStore, rs *fakeRepos, resolver *fakeTemplateResolver) *playbooks.Service {
	return playbooks.NewService(store, rs, resolver)
}

const validPlaybook = "- name: deploy\n  hosts: all\n  tasks: []\n"

func strPtr(s string) *string { return &s }

// --- tests ---

func TestCreateEditorStoresInlinePlaybook(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	svc := newService(newFakeStore(), newFakeRepos(nil))
	p, err := svc.CreateEditor(ctx, org, playbooks.CreateInput{Name: "deploy site", Inline: validPlaybook})
	if err != nil {
		t.Fatalf("CreateEditor: %v", err)
	}
	if p.Source != playbooks.SourceEditor || p.Inline == nil || *p.Inline != validPlaybook || p.RepoID != nil || p.Path != nil {
		t.Fatalf("created = %+v", p)
	}
	if _, err := svc.CreateEditor(ctx, org, playbooks.CreateInput{Name: "x", Inline: "hosts: all\n"}); !errors.Is(err, playbooks.ErrInvalidInput) || !strings.Contains(err.Error(), "expected a list of plays") {
		t.Fatalf("invalid inline = %v, want the validator's error", err)
	}
	if _, err := svc.CreateEditor(ctx, org, playbooks.CreateInput{Name: "x", Inline: strings.Repeat("x", playbooks.MaxInlineBytes+1)}); !errors.Is(err, playbooks.ErrInvalidInput) || !strings.Contains(err.Error(), "playbook exceeds 512 KiB") {
		t.Fatalf("oversized inline = %v", err)
	}
}

func TestCreateRepoValidation(t *testing.T) {
	org, repoID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rs := newFakeRepos(map[uuid.UUID]repos.Repo{repoID: {ID: repoID, OrgID: org, Name: "infra"}})
	unknownRepo := uuid.Must(uuid.NewV7())

	cases := []struct {
		name       string
		repoID     uuid.UUID
		path       string
		wantErr    string
		wantStatus error
	}{
		{"valid path", repoID, "playbooks/site.yml", "", nil},
		{"valid yaml extension", repoID, "site.yaml", "", nil},
		{"no extension", repoID, "playbooks/site", "invalid path", playbooks.ErrInvalidInput},
		{"wrong extension", repoID, "playbooks/site.txt", "invalid path", playbooks.ErrInvalidInput},
		{"with dotdot", repoID, "playbooks/../site.yml", "invalid path", playbooks.ErrInvalidInput},
		{"too long", repoID, strings.Repeat("a", 201) + ".yml", "invalid path", playbooks.ErrInvalidInput},
		{"empty path", repoID, "", "invalid path", playbooks.ErrInvalidInput},
		{"unknown repo", unknownRepo, "site.yml", "", playbooks.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(newFakeStore(), rs)
			p, err := svc.CreateRepo(ctx, org, tc.repoID, tc.path)
			if tc.wantStatus == nil {
				if err != nil {
					t.Fatalf("CreateRepo = %v, want valid", err)
				}
				if p.Source != playbooks.SourceRepo || p.RepoID == nil || *p.RepoID != tc.repoID ||
					p.Path == nil || *p.Path != tc.path || p.Inline != nil {
					t.Fatalf("created = %+v", p)
				}
				return
			}
			if !errors.Is(err, tc.wantStatus) || (tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("CreateRepo = %v, want %v naming %q", err, tc.wantStatus, tc.wantErr)
			}
		})
	}
}

// TestCreateRepoResolvesSharedTemplatePlaybooks is the organization-side
// half of playbook_id resolution (TemplateResolver's own doc comment: a
// review finding on Task 6's first draft, which only ever resolved
// playbook_id on a DEFINITION-side event): storing a new repo playbook
// must call the resolver exactly once, with this organization's id, the
// repository id, the NEWLY STORED playbook's own id (not a zero value —
// the resolver needs it to write it into job_templates.playbook_id) and
// the path, in that order — CreateRepo's own doc comment on why this
// happens after the store write succeeds, not before.
func TestCreateRepoResolvesSharedTemplatePlaybooks(t *testing.T) {
	org, repoID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rs := newFakeRepos(map[uuid.UUID]repos.Repo{repoID: {ID: repoID, OrgID: org}})
	resolver := &fakeTemplateResolver{}
	svc := newServiceWithResolver(newFakeStore(), rs, resolver)

	p, err := svc.CreateRepo(ctx, org, repoID, "site.yml")
	if err != nil {
		t.Fatalf("CreateRepo() error = %v, want nil", err)
	}
	if len(resolver.calls) != 1 {
		t.Fatalf("resolver calls = %d, want 1", len(resolver.calls))
	}
	call := resolver.calls[0]
	if call.orgID != org || call.repoID != repoID || call.playbookID != p.ID || call.path != "site.yml" {
		t.Fatalf("resolver called with %+v, want orgID=%v repoID=%v playbookID=%v path=site.yml", call, org, repoID, p.ID)
	}
}

// TestCreateRepoNeverResolvesWhenTheStoreWriteFails proves the resolver is
// not called at all when the playbook itself was never stored — calling it
// anyway would ask shared.Service to resolve a playbook id that names
// nothing.
func TestCreateRepoNeverResolvesWhenTheStoreWriteFails(t *testing.T) {
	org, repoID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rs := newFakeRepos(map[uuid.UUID]repos.Repo{repoID: {ID: repoID, OrgID: org}})
	store := newFakeStore()
	store.nextErr = errors.New("boom")
	resolver := &fakeTemplateResolver{}
	svc := newServiceWithResolver(store, rs, resolver)

	if _, err := svc.CreateRepo(ctx, org, repoID, "site.yml"); err == nil {
		t.Fatal("CreateRepo() error = nil, want the store's own error")
	}
	if len(resolver.calls) != 0 {
		t.Fatalf("resolver calls = %d, want 0 — a playbook that was never stored has no id to resolve with", len(resolver.calls))
	}
}

// TestCreateRepoFailsWhenResolutionFails proves a resolver error is
// reported, not swallowed (CreateRepo's own doc comment: a caller that
// successfully registered a playbook but silently failed to light up a
// template waiting on it would be a harder bug to find than one that
// surfaces here).
func TestCreateRepoFailsWhenResolutionFails(t *testing.T) {
	org, repoID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rs := newFakeRepos(map[uuid.UUID]repos.Repo{repoID: {ID: repoID, OrgID: org}})
	resolver := &fakeTemplateResolver{err: errors.New("resolve boom")}
	svc := newServiceWithResolver(newFakeStore(), rs, resolver)

	if _, err := svc.CreateRepo(ctx, org, repoID, "site.yml"); err == nil || !strings.Contains(err.Error(), "resolve boom") {
		t.Fatalf("CreateRepo() error = %v, want it to surface the resolver's own error", err)
	}
}

func TestUpdateEditor(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, newFakeRepos(nil))

	editor, err := svc.CreateEditor(ctx, org, playbooks.CreateInput{Name: "deploy", Inline: validPlaybook})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.UpdateEditor(ctx, org, editor.ID, playbooks.CreateInput{Name: "deploy", Inline: "- hosts: db\n  tasks: []\n"})
	if err != nil {
		t.Fatalf("UpdateEditor: %v", err)
	}
	if updated.Inline == nil || *updated.Inline != "- hosts: db\n  tasks: []\n" {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := svc.UpdateEditor(ctx, org, editor.ID, playbooks.CreateInput{Inline: "hosts: all\n"}); !errors.Is(err, playbooks.ErrInvalidInput) {
		t.Fatalf("invalid inline = %v", err)
	}
	if _, err := svc.UpdateEditor(ctx, org, uuid.Must(uuid.NewV7()), playbooks.CreateInput{Inline: validPlaybook}); !errors.Is(err, playbooks.ErrNotFound) {
		t.Fatalf("unknown playbook = %v, want ErrNotFound", err)
	}

	// A repo playbook is edited in its repository, not here.
	repoID := uuid.Must(uuid.NewV7())
	repoSvc := newService(store, newFakeRepos(map[uuid.UUID]repos.Repo{repoID: {ID: repoID, OrgID: org}}))
	repoPlaybook, err := repoSvc.CreateRepo(ctx, org, repoID, "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repoSvc.UpdateEditor(ctx, org, repoPlaybook.ID, playbooks.CreateInput{Inline: validPlaybook}); !errors.Is(err, playbooks.ErrInvalidInput) {
		t.Fatalf("updating a repo playbook = %v, want ErrInvalidInput", err)
	}
}

func TestDeletePropagatesStoreOutcomes(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, newFakeRepos(nil))
	editor, err := svc.CreateEditor(ctx, org, playbooks.CreateInput{Inline: validPlaybook})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, org, editor.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(ctx, org, editor.ID); !errors.Is(err, playbooks.ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
	// The fake store flags the next delete as in-use — standing in for the
	// FK violation the SQL store maps when a job template references the
	// playbook.
	store.byID[editor.ID] = editor
	store.inUseErr = playbooks.ErrInUse
	if err := svc.Delete(ctx, org, editor.ID); !errors.Is(err, playbooks.ErrInUse) {
		t.Fatalf("in-use delete = %v, want ErrInUse", err)
	}
}

func TestSummary(t *testing.T) {
	cases := []struct {
		name     string
		playbook playbooks.Playbook
		want     string
	}{
		{"named play", playbooks.Playbook{Source: playbooks.SourceEditor, Inline: strPtr("- name: deploy site\n  hosts: all\n")}, "deploy site"},
		{"unnamed play falls back to text", playbooks.Playbook{Source: playbooks.SourceEditor, Inline: strPtr("- hosts: all\n  tasks: []\n")}, "- hosts: all\n  tasks: []\n"},
		{"long inline truncated to 80", playbooks.Playbook{Source: playbooks.SourceEditor, Inline: strPtr(strings.Repeat("a", 120))}, strings.Repeat("a", 80)},
		{"repo playbook has no summary", playbooks.Playbook{Source: playbooks.SourceRepo, Path: strPtr("site.yml")}, ""},
		{"malformed stored inline", playbooks.Playbook{Source: playbooks.SourceEditor, Inline: strPtr("\tgarbage")}, "\tgarbage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.playbook.Summary(); got != tc.want {
				t.Fatalf("Summary = %q, want %q", got, tc.want)
			}
		})
	}
}
