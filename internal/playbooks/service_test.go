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

func newService(store *fakeStore, rs *fakeRepos) *playbooks.Service {
	return playbooks.NewService(store, rs)
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
