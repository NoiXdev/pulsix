package shared_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/shared"
)

// --- fake Store ---

// fakeStore is shared.Store without a database: it exists to prove
// Service's own rules (which calls it makes, in what order, with what
// arguments) independently of store_sql_test.go's real-Postgres
// transactional guarantees, the same division of labour
// internal/repos/service_test.go's own fakeStore and
// internal/repos/vault_test.go's real-DB tests draw.
type fakeStore struct {
	defs         map[uuid.UUID]shared.RepoDefinition
	gitRepoNames []shared.OrgName

	existingNamesExcludes []uuid.UUID // every exclude CreateRepo/UpdateRepo passed, in call order
	createCalls           []shared.RepoDefinition
	updateCalls           []shared.RepoDefinition
	deleteCalls           []uuid.UUID
	projectCalls          []projectCall

	forceExistingNamesErr error
	forceCreateErr        error
	forceUpdateErr        error
	forceDeleteErr        error
	forceListErr          error
	// forceProjectErrForOrg, when non-nil, makes ProjectInto fail for
	// exactly that organization and succeed for every other one — the
	// fake's equivalent of forcing a specific organization's write to fail,
	// the same technique store_sql_test.go uses for real for
	// UpdateAndPropagate.
	forceProjectErrForOrg *uuid.UUID
	forceProjectErr       error

	// --- job templates (Task 6), the same shape as the repo fields above ---
	templateDefs                  map[uuid.UUID]shared.TemplateDefinition
	jobTemplateNames              []shared.OrgName
	existingTemplateExcludes      []uuid.UUID
	createTemplateCalls           []shared.TemplateDefinition
	updateTemplateCalls           []shared.TemplateDefinition
	deleteTemplateCalls           []uuid.UUID
	projectTemplateCalls          []projectTemplateCall
	forceExistingTemplatesErr     error
	forceCreateTemplateErr        error
	forceUpdateTemplateErr        error
	forceDeleteTemplateErr        error
	forceListTemplatesErr         error
	forceProjectTemplateErrForOrg *uuid.UUID
	forceProjectTemplateErr       error

	// resolveTemplatePlaybooksCalls records every ResolveTemplatePlaybooksForNewPath
	// call — Service.ResolveTemplatePlaybooks is a thin, one-line delegation
	// (like ProjectAllTemplatesInto's own loop body), so its own test only
	// needs to prove the call reaches the store with the right arguments.
	resolveTemplatePlaybooksCalls    []resolveTemplatePlaybooksCall
	forceResolveTemplatePlaybooksErr error
}

type resolveTemplatePlaybooksCall struct {
	orgID, repoID, playbookID uuid.UUID
	path                      string
}

type projectCall struct {
	def   shared.RepoDefinition
	orgID uuid.UUID
}

type projectTemplateCall struct {
	def   shared.TemplateDefinition
	orgID uuid.UUID
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		defs:         map[uuid.UUID]shared.RepoDefinition{},
		templateDefs: map[uuid.UUID]shared.TemplateDefinition{},
	}
}

// CreateWithProjections no longer takes an organization list (store_sql.go
// now reads it live, inside its own transaction — the fix for the
// round-trip gap the review caught); the fake has no organizations of its
// own to fan out into, since that behaviour now lives entirely in
// SQLStore and is proven for real in store_sql_test.go. This fake only
// proves Service builds the right RepoDefinition and calls this exactly
// once.
func (f *fakeStore) CreateWithProjections(_ context.Context, def shared.RepoDefinition) (shared.RepoDefinition, error) {
	if f.forceCreateErr != nil {
		return shared.RepoDefinition{}, f.forceCreateErr
	}
	f.createCalls = append(f.createCalls, def)
	f.defs[def.ID] = def
	return def, nil
}

func (f *fakeStore) UpdateAndPropagate(_ context.Context, def shared.RepoDefinition) (shared.RepoDefinition, error) {
	if f.forceUpdateErr != nil {
		return shared.RepoDefinition{}, f.forceUpdateErr
	}
	if _, ok := f.defs[def.ID]; !ok {
		return shared.RepoDefinition{}, shared.ErrNotFound
	}
	f.updateCalls = append(f.updateCalls, def)
	f.defs[def.ID] = def
	return def, nil
}

func (f *fakeStore) DeleteWithProjections(_ context.Context, id uuid.UUID) error {
	if f.forceDeleteErr != nil {
		return f.forceDeleteErr
	}
	if _, ok := f.defs[id]; !ok {
		return shared.ErrNotFound
	}
	f.deleteCalls = append(f.deleteCalls, id)
	delete(f.defs, id)
	return nil
}

func (f *fakeStore) ListDefinitions(_ context.Context) ([]shared.RepoDefinition, error) {
	if f.forceListErr != nil {
		return nil, f.forceListErr
	}
	out := make([]shared.RepoDefinition, 0, len(f.defs))
	for _, d := range f.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out, nil
}

func (f *fakeStore) GetDefinition(_ context.Context, id uuid.UUID) (shared.RepoDefinition, error) {
	d, ok := f.defs[id]
	if !ok {
		return shared.RepoDefinition{}, shared.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) ProjectInto(_ context.Context, def shared.RepoDefinition, orgID uuid.UUID) error {
	if f.forceProjectErrForOrg != nil && *f.forceProjectErrForOrg == orgID {
		return f.forceProjectErr
	}
	f.projectCalls = append(f.projectCalls, projectCall{def: def, orgID: orgID})
	return nil
}

func (f *fakeStore) ExistingNames(_ context.Context, exclude uuid.UUID) ([]shared.OrgName, error) {
	f.existingNamesExcludes = append(f.existingNamesExcludes, exclude)
	if f.forceExistingNamesErr != nil {
		return nil, f.forceExistingNamesErr
	}
	return f.gitRepoNames, nil
}

// --- job templates (Task 6): the same fake shape as the repo methods
// above, one definition kind reusing the same tx.go mechanism the other
// already proved.

func (f *fakeStore) CreateTemplateWithProjections(_ context.Context, def shared.TemplateDefinition) (shared.TemplateDefinition, error) {
	if f.forceCreateTemplateErr != nil {
		return shared.TemplateDefinition{}, f.forceCreateTemplateErr
	}
	f.createTemplateCalls = append(f.createTemplateCalls, def)
	f.templateDefs[def.ID] = def
	return def, nil
}

func (f *fakeStore) UpdateTemplateAndPropagate(_ context.Context, def shared.TemplateDefinition) (shared.TemplateDefinition, error) {
	if f.forceUpdateTemplateErr != nil {
		return shared.TemplateDefinition{}, f.forceUpdateTemplateErr
	}
	if _, ok := f.templateDefs[def.ID]; !ok {
		return shared.TemplateDefinition{}, shared.ErrNotFound
	}
	f.updateTemplateCalls = append(f.updateTemplateCalls, def)
	f.templateDefs[def.ID] = def
	return def, nil
}

func (f *fakeStore) DeleteTemplateWithProjections(_ context.Context, id uuid.UUID) error {
	if f.forceDeleteTemplateErr != nil {
		return f.forceDeleteTemplateErr
	}
	if _, ok := f.templateDefs[id]; !ok {
		return shared.ErrNotFound
	}
	f.deleteTemplateCalls = append(f.deleteTemplateCalls, id)
	delete(f.templateDefs, id)
	return nil
}

func (f *fakeStore) ListTemplateDefinitions(_ context.Context) ([]shared.TemplateDefinition, error) {
	if f.forceListTemplatesErr != nil {
		return nil, f.forceListTemplatesErr
	}
	out := make([]shared.TemplateDefinition, 0, len(f.templateDefs))
	for _, d := range f.templateDefs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out, nil
}

func (f *fakeStore) GetTemplateDefinition(_ context.Context, id uuid.UUID) (shared.TemplateDefinition, error) {
	d, ok := f.templateDefs[id]
	if !ok {
		return shared.TemplateDefinition{}, shared.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) ProjectTemplateInto(_ context.Context, def shared.TemplateDefinition, orgID uuid.UUID) error {
	if f.forceProjectTemplateErrForOrg != nil && *f.forceProjectTemplateErrForOrg == orgID {
		return f.forceProjectTemplateErr
	}
	f.projectTemplateCalls = append(f.projectTemplateCalls, projectTemplateCall{def: def, orgID: orgID})
	return nil
}

func (f *fakeStore) ExistingTemplateNames(_ context.Context, exclude uuid.UUID) ([]shared.OrgName, error) {
	f.existingTemplateExcludes = append(f.existingTemplateExcludes, exclude)
	if f.forceExistingTemplatesErr != nil {
		return nil, f.forceExistingTemplatesErr
	}
	return f.jobTemplateNames, nil
}

func (f *fakeStore) ResolveTemplatePlaybooksForNewPath(_ context.Context, orgID, repoID, playbookID uuid.UUID, path string) error {
	if f.forceResolveTemplatePlaybooksErr != nil {
		return f.forceResolveTemplatePlaybooksErr
	}
	f.resolveTemplatePlaybooksCalls = append(f.resolveTemplatePlaybooksCalls, resolveTemplatePlaybooksCall{
		orgID: orgID, repoID: repoID, playbookID: playbookID, path: path,
	})
	return nil
}

// --- CreateRepo ---

func TestCreateRepoCallsCreateWithProjectionsOnce(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	got, err := svc.CreateRepo(ctx, shared.RepoDefinitionInput{
		Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main", Subpath: "ansible",
	})
	if err != nil {
		t.Fatalf("CreateRepo() error = %v, want nil", err)
	}
	if len(store.createCalls) != 1 {
		t.Fatalf("CreateWithProjections called %d times, want 1", len(store.createCalls))
	}
	call := store.createCalls[0]
	if call.Name != "infra" || call.URL != "https://git.example.com/infra.git" || call.Branch != "main" || call.Subpath != "ansible" {
		t.Fatalf("CreateWithProjections called with %+v, want the input's own values", call)
	}
	if got.Name != "infra" || got.Branch != "main" {
		t.Fatalf("CreateRepo() = %+v, unexpected", got)
	}
}

func TestCreateRepoDefaultsEmptyBranch(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	got, err := svc.CreateRepo(ctx, shared.RepoDefinitionInput{Name: "infra", URL: "https://git.example.com/infra.git"})
	if err != nil {
		t.Fatalf("CreateRepo() error = %v, want nil", err)
	}
	if got.Branch != shared.DefaultBranch {
		t.Fatalf("CreateRepo() branch = %q, want default %q", got.Branch, shared.DefaultBranch)
	}
}

func TestCreateRepoRefusesACollidingName(t *testing.T) {
	store := newFakeStore()
	orgA := uuid.Must(uuid.NewV7())
	store.gitRepoNames = []shared.OrgName{{OrgID: orgA, Name: "ansible-base"}}
	svc := shared.NewService(store)

	_, err := svc.CreateRepo(ctx, shared.RepoDefinitionInput{Name: "ansible-base", URL: "https://git.example.com/x.git"})
	if err == nil {
		t.Fatal("CreateRepo() error = nil, want a collision")
	}
	if !errors.Is(err, shared.ErrNameTaken) {
		t.Fatalf("CreateRepo() error = %v, want errors.Is(_, ErrNameTaken)", err)
	}
	var collision *shared.NameCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("CreateRepo() error = %v, want *NameCollisionError", err)
	}
	if !reflect.DeepEqual(collision.Orgs, []uuid.UUID{orgA}) {
		t.Fatalf("NameCollisionError.Orgs = %v, want [%v]", collision.Orgs, orgA)
	}
	// Refused before any write: a collision is caught before the
	// definition is created at all, never created-then-rolled-back.
	if len(store.createCalls) != 0 {
		t.Fatalf("CreateWithProjections called %d times, want 0 after a collision", len(store.createCalls))
	}
}

func TestCreateRepoPropagatesAStoreError(t *testing.T) {
	store := newFakeStore()
	wantErr := errors.New("boom")
	store.forceCreateErr = wantErr
	svc := shared.NewService(store)

	_, err := svc.CreateRepo(ctx, shared.RepoDefinitionInput{Name: "infra", URL: "https://git.example.com/infra.git"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("CreateRepo() error = %v, want %v", err, wantErr)
	}
}

// --- UpdateRepo ---

func TestUpdateRepoExcludesItsOwnProjectionsFromTheCollisionCheck(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.defs[id] = shared.RepoDefinition{ID: id, Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main"}
	svc := shared.NewService(store)

	if _, err := svc.UpdateRepo(ctx, id, shared.RepoDefinitionInput{Name: "infra-v2", URL: "https://git.example.com/infra.git", Branch: "main"}); err != nil {
		t.Fatalf("UpdateRepo() error = %v, want nil", err)
	}
	if len(store.existingNamesExcludes) != 1 || store.existingNamesExcludes[0] != id {
		t.Fatalf("ExistingNames excludes = %v, want exactly [%v] — a rename must exclude its own current projections", store.existingNamesExcludes, id)
	}
}

func TestUpdateRepoRefusesACollidingName(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.defs[id] = shared.RepoDefinition{ID: id, Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main"}
	orgB := uuid.Must(uuid.NewV7())
	store.gitRepoNames = []shared.OrgName{{OrgID: orgB, Name: "taken"}}
	svc := shared.NewService(store)

	_, err := svc.UpdateRepo(ctx, id, shared.RepoDefinitionInput{Name: "taken", URL: "https://git.example.com/infra.git"})
	var collision *shared.NameCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("UpdateRepo() error = %v, want *NameCollisionError", err)
	}
	if !reflect.DeepEqual(collision.Orgs, []uuid.UUID{orgB}) {
		t.Fatalf("NameCollisionError.Orgs = %v, want [%v]", collision.Orgs, orgB)
	}
	if len(store.updateCalls) != 0 {
		t.Fatalf("UpdateAndPropagate called %d times, want 0 after a collision", len(store.updateCalls))
	}
}

func TestUpdateRepoPropagatesNotFound(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	_, err := svc.UpdateRepo(ctx, uuid.Must(uuid.NewV7()), shared.RepoDefinitionInput{Name: "infra", URL: "https://git.example.com/infra.git"})
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("UpdateRepo() error = %v, want ErrNotFound", err)
	}
}

// --- DeleteRepo ---

func TestDeleteRepoDelegatesToTheStore(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.defs[id] = shared.RepoDefinition{ID: id, Name: "infra"}
	svc := shared.NewService(store)

	if err := svc.DeleteRepo(ctx, id); err != nil {
		t.Fatalf("DeleteRepo() error = %v, want nil", err)
	}
	if len(store.deleteCalls) != 1 || store.deleteCalls[0] != id {
		t.Fatalf("DeleteWithProjections calls = %v, want [%v]", store.deleteCalls, id)
	}
}

func TestDeleteRepoPropagatesErrInUse(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.forceDeleteErr = shared.ErrInUse
	svc := shared.NewService(store)

	if err := svc.DeleteRepo(ctx, id); !errors.Is(err, shared.ErrInUse) {
		t.Fatalf("DeleteRepo() error = %v, want ErrInUse", err)
	}
}

// --- ProjectAllInto ---

func TestProjectAllIntoProjectsEveryDefinition(t *testing.T) {
	store := newFakeStore()
	idA, idB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store.defs[idA] = shared.RepoDefinition{ID: idA, Name: "infra"}
	store.defs[idB] = shared.RepoDefinition{ID: idB, Name: "tools"}
	org := uuid.Must(uuid.NewV7())
	svc := shared.NewService(store)

	if err := svc.ProjectAllInto(ctx, org); err != nil {
		t.Fatalf("ProjectAllInto() error = %v, want nil", err)
	}
	if len(store.projectCalls) != 2 {
		t.Fatalf("ProjectInto called %d times, want 2 (one per definition)", len(store.projectCalls))
	}
	for _, c := range store.projectCalls {
		if c.orgID != org {
			t.Fatalf("ProjectInto org = %v, want %v", c.orgID, org)
		}
	}
}

func TestProjectAllIntoPropagatesAFailureForOneDefinition(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.defs[id] = shared.RepoDefinition{ID: id, Name: "infra"}
	org := uuid.Must(uuid.NewV7())
	store.forceProjectErrForOrg = &org
	store.forceProjectErr = shared.ErrNameTaken
	svc := shared.NewService(store)

	if err := svc.ProjectAllInto(ctx, org); !errors.Is(err, shared.ErrNameTaken) {
		t.Fatalf("ProjectAllInto() error = %v, want ErrNameTaken", err)
	}
}

// --- GetRepo / ListRepos ---

func TestGetRepoNotFound(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	if _, err := svc.GetRepo(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("GetRepo() error = %v, want ErrNotFound", err)
	}
}

func TestListReposReturnsEveryDefinition(t *testing.T) {
	store := newFakeStore()
	idA, idB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store.defs[idA] = shared.RepoDefinition{ID: idA, Name: "infra"}
	store.defs[idB] = shared.RepoDefinition{ID: idB, Name: "tools"}
	svc := shared.NewService(store)

	got, err := svc.ListRepos(ctx)
	if err != nil {
		t.Fatalf("ListRepos() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListRepos() = %v, want 2 definitions", got)
	}
}

// --- job templates (Task 6) ---

func TestCreateTemplateCallsCreateTemplateWithProjectionsOnce(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)
	repoID := uuid.Must(uuid.NewV7())

	got, err := svc.CreateTemplate(ctx, shared.TemplateDefinitionInput{
		Name: "deploy-site", SharedRepoID: repoID, PlaybookPath: "site.yml", Limit: "web*",
	})
	if err != nil {
		t.Fatalf("CreateTemplate() error = %v, want nil", err)
	}
	if len(store.createTemplateCalls) != 1 {
		t.Fatalf("CreateTemplateWithProjections called %d times, want 1", len(store.createTemplateCalls))
	}
	call := store.createTemplateCalls[0]
	if call.Name != "deploy-site" || call.SharedRepoID != repoID || call.PlaybookPath != "site.yml" || call.Limit != "web*" {
		t.Fatalf("CreateTemplateWithProjections called with %+v, want the input's own values", call)
	}
	if got.ExtraVars == nil {
		t.Fatal("CreateTemplate() ExtraVars = nil, want a non-nil empty map by default")
	}
}

func TestCreateTemplateRefusesASecretTypedSchemaField(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	_, err := svc.CreateTemplate(ctx, shared.TemplateDefinitionInput{
		Name: "deploy-site", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml",
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{{Name: "token", Type: schema.TypeSecret}}},
	})
	if !errors.Is(err, shared.ErrInvalidInput) {
		t.Fatalf("CreateTemplate() with a secret-typed field error = %v, want ErrInvalidInput", err)
	}
	if len(store.createTemplateCalls) != 0 {
		t.Fatalf("CreateTemplateWithProjections called %d times, want 0 — a shared definition carries no secret by construction", len(store.createTemplateCalls))
	}
}

func TestCreateTemplateRefusesASecretFieldNestedInARepeater(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	_, err := svc.CreateTemplate(ctx, shared.TemplateDefinitionInput{
		Name: "deploy-site", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml",
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{{
			Name: "accounts", Type: schema.TypeRepeater,
			Item: &schema.Field{Name: "password", Type: schema.TypeSecret},
		}}},
	})
	if !errors.Is(err, shared.ErrInvalidInput) {
		t.Fatalf("CreateTemplate() with a nested secret field error = %v, want ErrInvalidInput", err)
	}
}

func TestCreateTemplateRefusesAForbiddenExtraVarName(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	_, err := svc.CreateTemplate(ctx, shared.TemplateDefinitionInput{
		Name: "deploy-site", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml",
		ExtraVars: map[string]any{"ansible_ssh_common_args": "-o foo"},
	})
	if !errors.Is(err, shared.ErrInvalidInput) {
		t.Fatalf("CreateTemplate() with a forbidden extra var error = %v, want ErrInvalidInput", err)
	}
}

func TestCreateTemplateRefusesACollidingName(t *testing.T) {
	store := newFakeStore()
	orgA := uuid.Must(uuid.NewV7())
	store.jobTemplateNames = []shared.OrgName{{OrgID: orgA, Name: "deploy-site"}}
	svc := shared.NewService(store)

	_, err := svc.CreateTemplate(ctx, shared.TemplateDefinitionInput{Name: "deploy-site", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml"})
	if err == nil {
		t.Fatal("CreateTemplate() error = nil, want a collision")
	}
	if !errors.Is(err, shared.ErrNameTaken) {
		t.Fatalf("CreateTemplate() error = %v, want errors.Is(_, ErrNameTaken)", err)
	}
	var collision *shared.NameCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("CreateTemplate() error = %v, want *NameCollisionError", err)
	}
	if !reflect.DeepEqual(collision.Orgs, []uuid.UUID{orgA}) {
		t.Fatalf("NameCollisionError.Orgs = %v, want [%v]", collision.Orgs, orgA)
	}
	if len(store.createTemplateCalls) != 0 {
		t.Fatalf("CreateTemplateWithProjections called %d times, want 0 after a collision", len(store.createTemplateCalls))
	}
}

func TestUpdateTemplateExcludesItsOwnProjectionsFromTheCollisionCheck(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.templateDefs[id] = shared.TemplateDefinition{ID: id, Name: "deploy-site", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml"}
	svc := shared.NewService(store)

	if _, err := svc.UpdateTemplate(ctx, id, shared.TemplateDefinitionInput{Name: "deploy-site-v2", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml"}); err != nil {
		t.Fatalf("UpdateTemplate() error = %v, want nil", err)
	}
	if len(store.existingTemplateExcludes) != 1 || store.existingTemplateExcludes[0] != id {
		t.Fatalf("ExistingTemplateNames excludes = %v, want exactly [%v]", store.existingTemplateExcludes, id)
	}
}

func TestUpdateTemplateRefusesACollidingName(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.templateDefs[id] = shared.TemplateDefinition{ID: id, Name: "deploy-site", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml"}
	orgB := uuid.Must(uuid.NewV7())
	store.jobTemplateNames = []shared.OrgName{{OrgID: orgB, Name: "taken"}}
	svc := shared.NewService(store)

	_, err := svc.UpdateTemplate(ctx, id, shared.TemplateDefinitionInput{Name: "taken", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml"})
	var collision *shared.NameCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("UpdateTemplate() error = %v, want *NameCollisionError", err)
	}
	if !reflect.DeepEqual(collision.Orgs, []uuid.UUID{orgB}) {
		t.Fatalf("NameCollisionError.Orgs = %v, want [%v]", collision.Orgs, orgB)
	}
	if len(store.updateTemplateCalls) != 0 {
		t.Fatalf("UpdateTemplateAndPropagate called %d times, want 0 after a collision", len(store.updateTemplateCalls))
	}
}

func TestUpdateTemplatePropagatesNotFound(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	_, err := svc.UpdateTemplate(ctx, uuid.Must(uuid.NewV7()), shared.TemplateDefinitionInput{Name: "deploy-site", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml"})
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("UpdateTemplate() error = %v, want ErrNotFound", err)
	}
}

func TestDeleteTemplateDelegatesToTheStore(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.templateDefs[id] = shared.TemplateDefinition{ID: id, Name: "deploy-site"}
	svc := shared.NewService(store)

	if err := svc.DeleteTemplate(ctx, id); err != nil {
		t.Fatalf("DeleteTemplate() error = %v, want nil", err)
	}
	if len(store.deleteTemplateCalls) != 1 || store.deleteTemplateCalls[0] != id {
		t.Fatalf("DeleteTemplateWithProjections calls = %v, want [%v]", store.deleteTemplateCalls, id)
	}
}

func TestDeleteTemplatePropagatesErrInUse(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.forceDeleteTemplateErr = shared.ErrInUse
	svc := shared.NewService(store)

	if err := svc.DeleteTemplate(ctx, id); !errors.Is(err, shared.ErrInUse) {
		t.Fatalf("DeleteTemplate() error = %v, want ErrInUse", err)
	}
}

func TestProjectAllTemplatesIntoProjectsEveryDefinition(t *testing.T) {
	store := newFakeStore()
	idA, idB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store.templateDefs[idA] = shared.TemplateDefinition{ID: idA, Name: "deploy-site"}
	store.templateDefs[idB] = shared.TemplateDefinition{ID: idB, Name: "backup"}
	org := uuid.Must(uuid.NewV7())
	svc := shared.NewService(store)

	if err := svc.ProjectAllTemplatesInto(ctx, org); err != nil {
		t.Fatalf("ProjectAllTemplatesInto() error = %v, want nil", err)
	}
	if len(store.projectTemplateCalls) != 2 {
		t.Fatalf("ProjectTemplateInto called %d times, want 2 (one per definition)", len(store.projectTemplateCalls))
	}
	for _, c := range store.projectTemplateCalls {
		if c.orgID != org {
			t.Fatalf("ProjectTemplateInto org = %v, want %v", c.orgID, org)
		}
	}
}

func TestProjectAllTemplatesIntoPropagatesAFailureForOneDefinition(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.templateDefs[id] = shared.TemplateDefinition{ID: id, Name: "deploy-site"}
	org := uuid.Must(uuid.NewV7())
	store.forceProjectTemplateErrForOrg = &org
	store.forceProjectTemplateErr = shared.ErrNameTaken
	svc := shared.NewService(store)

	if err := svc.ProjectAllTemplatesInto(ctx, org); !errors.Is(err, shared.ErrNameTaken) {
		t.Fatalf("ProjectAllTemplatesInto() error = %v, want ErrNameTaken", err)
	}
}

func TestGetTemplateDefNotFound(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	if _, err := svc.GetTemplateDef(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("GetTemplateDef() error = %v, want ErrNotFound", err)
	}
}

func TestListTemplateDefsReturnsEveryDefinition(t *testing.T) {
	store := newFakeStore()
	idA, idB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store.templateDefs[idA] = shared.TemplateDefinition{ID: idA, Name: "deploy-site"}
	store.templateDefs[idB] = shared.TemplateDefinition{ID: idB, Name: "backup"}
	svc := shared.NewService(store)

	got, err := svc.ListTemplateDefs(ctx)
	if err != nil {
		t.Fatalf("ListTemplateDefs() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListTemplateDefs() = %v, want 2 definitions", got)
	}
}

// TestResolveTemplatePlaybooksDelegatesToTheStore proves
// Service.ResolveTemplatePlaybooks — playbooks.TemplateResolver's own
// method, the organization-side half of playbook_id resolution a review of
// Task 6's first draft found missing — is exactly the one-line delegation
// its own doc comment says: the store sees the call with the orgID, repoID,
// playbookID and path it was given, unchanged, and a store error comes back
// unwrapped so playbooks.Service.CreateRepo's own %w-wrap stays the only
// place that adds context.
func TestResolveTemplatePlaybooksDelegatesToTheStore(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)
	orgID, repoID, playbookID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	if err := svc.ResolveTemplatePlaybooks(ctx, orgID, repoID, playbookID, "site.yml"); err != nil {
		t.Fatalf("ResolveTemplatePlaybooks() error = %v, want nil", err)
	}
	if len(store.resolveTemplatePlaybooksCalls) != 1 {
		t.Fatalf("store calls = %d, want 1", len(store.resolveTemplatePlaybooksCalls))
	}
	call := store.resolveTemplatePlaybooksCalls[0]
	if call.orgID != orgID || call.repoID != repoID || call.playbookID != playbookID || call.path != "site.yml" {
		t.Fatalf("store called with %+v, want orgID=%v repoID=%v playbookID=%v path=site.yml", call, orgID, repoID, playbookID)
	}
}

func TestResolveTemplatePlaybooksPropagatesAStoreError(t *testing.T) {
	store := newFakeStore()
	store.forceResolveTemplatePlaybooksErr = errors.New("boom")
	svc := shared.NewService(store)

	err := svc.ResolveTemplatePlaybooks(ctx, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "site.yml")
	if !errors.Is(err, store.forceResolveTemplatePlaybooksErr) {
		t.Fatalf("ResolveTemplatePlaybooks() error = %v, want %v", err, store.forceResolveTemplatePlaybooksErr)
	}
}

var ctx = context.Background()
