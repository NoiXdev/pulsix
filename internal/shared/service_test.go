package shared_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

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

	// --- check definitions (Task 7), the same shape as the repo/template
	// fields above ---
	checkDefs                  map[uuid.UUID]shared.CheckDefinition
	checkDefinitionNames       []shared.OrgName
	existingCheckDefExcludes   []uuid.UUID
	createCheckDefCalls        []shared.CheckDefinition
	updateCheckDefCalls        []shared.CheckDefinition
	deleteCheckDefCalls        []uuid.UUID
	projectCheckDefCalls       []projectCheckDefCall
	forceExistingCheckDefsErr  error
	forceCreateCheckDefErr     error
	forceUpdateCheckDefErr     error
	forceDeleteCheckDefErr     error
	forceListCheckDefsErr      error
	forceProjectCheckDefErrOrg *uuid.UUID
	forceProjectCheckDefErr    error
}

type projectCheckDefCall struct {
	def   shared.CheckDefinition
	orgID uuid.UUID
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
		checkDefs:    map[uuid.UUID]shared.CheckDefinition{},
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

// --- check definitions (Task 7): the same fake shape as the repo/template
// methods above, the third kind reusing the same tx.go mechanism.

func (f *fakeStore) CreateCheckDefinitionWithProjections(_ context.Context, def shared.CheckDefinition) (shared.CheckDefinition, error) {
	if f.forceCreateCheckDefErr != nil {
		return shared.CheckDefinition{}, f.forceCreateCheckDefErr
	}
	f.createCheckDefCalls = append(f.createCheckDefCalls, def)
	f.checkDefs[def.ID] = def
	return def, nil
}

func (f *fakeStore) UpdateCheckDefinitionAndPropagate(_ context.Context, def shared.CheckDefinition) (shared.CheckDefinition, error) {
	if f.forceUpdateCheckDefErr != nil {
		return shared.CheckDefinition{}, f.forceUpdateCheckDefErr
	}
	if _, ok := f.checkDefs[def.ID]; !ok {
		return shared.CheckDefinition{}, shared.ErrNotFound
	}
	f.updateCheckDefCalls = append(f.updateCheckDefCalls, def)
	f.checkDefs[def.ID] = def
	return def, nil
}

func (f *fakeStore) DeleteCheckDefinitionWithProjections(_ context.Context, id uuid.UUID) error {
	if f.forceDeleteCheckDefErr != nil {
		return f.forceDeleteCheckDefErr
	}
	if _, ok := f.checkDefs[id]; !ok {
		return shared.ErrNotFound
	}
	f.deleteCheckDefCalls = append(f.deleteCheckDefCalls, id)
	delete(f.checkDefs, id)
	return nil
}

func (f *fakeStore) ListCheckDefinitions(_ context.Context) ([]shared.CheckDefinition, error) {
	if f.forceListCheckDefsErr != nil {
		return nil, f.forceListCheckDefsErr
	}
	out := make([]shared.CheckDefinition, 0, len(f.checkDefs))
	for _, d := range f.checkDefs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out, nil
}

func (f *fakeStore) GetCheckDefinition(_ context.Context, id uuid.UUID) (shared.CheckDefinition, error) {
	d, ok := f.checkDefs[id]
	if !ok {
		return shared.CheckDefinition{}, shared.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) ProjectCheckDefinitionInto(_ context.Context, def shared.CheckDefinition, orgID uuid.UUID) error {
	if f.forceProjectCheckDefErrOrg != nil && *f.forceProjectCheckDefErrOrg == orgID {
		return f.forceProjectCheckDefErr
	}
	f.projectCheckDefCalls = append(f.projectCheckDefCalls, projectCheckDefCall{def: def, orgID: orgID})
	return nil
}

func (f *fakeStore) ExistingCheckDefinitionNames(_ context.Context, exclude uuid.UUID) ([]shared.OrgName, error) {
	f.existingCheckDefExcludes = append(f.existingCheckDefExcludes, exclude)
	if f.forceExistingCheckDefsErr != nil {
		return nil, f.forceExistingCheckDefsErr
	}
	return f.checkDefinitionNames, nil
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

// TestCreateTemplateRefusesASecretFieldNestedInAnObject is
// TestCreateTemplateRefusesASecretFieldNestedInARepeater's own test for
// schemaHasSecretField's OTHER recursive branch (f.Fields, an object's own
// nested fields) — the repeater test above exercises f.Item only, and
// neither test previously covered both of the function's two recursion
// paths.
func TestCreateTemplateRefusesASecretFieldNestedInAnObject(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	_, err := svc.CreateTemplate(ctx, shared.TemplateDefinitionInput{
		Name: "deploy-site", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml",
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{{
			Name: "database", Type: schema.TypeObject,
			Fields: []schema.Field{{Name: "password", Type: schema.TypeSecret}},
		}}},
	})
	if !errors.Is(err, shared.ErrInvalidInput) {
		t.Fatalf("CreateTemplate() with a secret field nested in an object error = %v, want ErrInvalidInput", err)
	}
}

// TestTemplateDefinitionRefusesSourceFields: a shared definition only ever
// stores manual fields (spec goal 2), on create and on update alike.
func TestTemplateDefinitionRefusesSourceFields(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)
	in := shared.TemplateDefinitionInput{
		Name: "deploy-site", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml",
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{{
			Name: "release", Type: schema.TypeString, Origin: schema.OriginAdjusted,
			Source: &schema.FieldSource{Kind: schema.SourceVarsPrompt, File: "site.yml", Line: 4},
		}}},
	}
	_, err := svc.CreateTemplate(ctx, in)
	if !errors.Is(err, shared.ErrInvalidInput) || !strings.Contains(err.Error(), "fields[0].origin") {
		t.Fatalf("CreateTemplate() with a source field error = %v, want ErrInvalidInput naming fields[0].origin", err)
	}
	if _, err := svc.UpdateTemplate(ctx, uuid.Must(uuid.NewV7()), in); !errors.Is(err, shared.ErrInvalidInput) {
		t.Fatalf("UpdateTemplate() with a source field error = %v, want ErrInvalidInput", err)
	}
	if len(store.createTemplateCalls)+len(store.updateTemplateCalls) != 0 {
		t.Fatal("nothing may be written for a definition with a source field")
	}
}

// TestTemplateDefinitionChecksItsSchema: a shared definition's schema is
// written into every organization's job_templates row unchecked by any
// other service, so it gets the full schema.Check here — a manual field
// carrying a source is refused on create and update alike, and a
// well-formed legacy schema still passes.
func TestTemplateDefinitionChecksItsSchema(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)
	in := shared.TemplateDefinitionInput{
		Name: "deploy-site", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml",
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{{
			Name: "release", Type: schema.TypeString,
			Source: &schema.FieldSource{Kind: schema.SourceVarsPrompt, File: "site.yml", Line: 4},
		}}},
	}
	_, err := svc.CreateTemplate(ctx, in)
	if !errors.Is(err, shared.ErrInvalidInput) || !strings.Contains(err.Error(), "fields[0].source") {
		t.Fatalf("CreateTemplate() with a manual field carrying a source error = %v, want ErrInvalidInput naming fields[0].source", err)
	}
	if _, err := svc.UpdateTemplate(ctx, uuid.Must(uuid.NewV7()), in); !errors.Is(err, shared.ErrInvalidInput) || !strings.Contains(err.Error(), "fields[0].source") {
		t.Fatalf("UpdateTemplate() with a manual field carrying a source error = %v, want ErrInvalidInput naming fields[0].source", err)
	}
	if len(store.createTemplateCalls)+len(store.updateTemplateCalls) != 0 {
		t.Fatal("nothing may be written for a definition whose schema fails its check")
	}

	in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{
		{Name: "release", Type: schema.TypeString, Required: true},
		{Name: "env", Type: schema.TypeEnum, Options: []string{"prod", "staging"}, Default: "prod"},
	}}
	def, err := svc.CreateTemplate(ctx, in)
	if err != nil {
		t.Fatalf("CreateTemplate() with a well-formed legacy schema: %v", err)
	}
	if _, err := svc.UpdateTemplate(ctx, def.ID, in); err != nil {
		t.Fatalf("UpdateTemplate() with a well-formed legacy schema: %v", err)
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

// --- check definitions (Task 7) ---

func TestCreateCheckDefinitionCallsCreateCheckDefinitionWithProjectionsOnce(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	got, err := svc.CreateCheckDefinition(ctx, shared.CheckDefinitionInput{
		Key: "disk-data", Plugin: "disk", Interval: 300 * time.Second,
	})
	if err != nil {
		t.Fatalf("CreateCheckDefinition() error = %v, want nil", err)
	}
	if len(store.createCheckDefCalls) != 1 {
		t.Fatalf("CreateCheckDefinitionWithProjections called %d times, want 1", len(store.createCheckDefCalls))
	}
	call := store.createCheckDefCalls[0]
	if call.Key != "disk-data" || call.Plugin != "disk" || call.Interval != 300*time.Second {
		t.Fatalf("CreateCheckDefinitionWithProjections called with %+v, want the input's own values", call)
	}
	if got.Params == nil {
		t.Fatal("CreateCheckDefinition() Params = nil, want a non-nil empty map by default")
	}
}

// TestCreateCheckDefinitionDefaultsAnEmptyKeyBeforeCheckingReservedKeys
// proves Service.CreateCheckDefinition applies the SAME key-defaulting rule
// an organization's own checks.Service.CreateDefinition does (both now
// funnel through checks.ValidateDefinitionShape) — "key defaults to the
// plugin name" is the spec's own rule for every check definition, shared or
// not. It cannot be proven by a SUCCESSFUL create here, the way the
// equivalent test for an ordinary organization-owned definition can:
// internal/checks.SeedDefinitions() uses the plugin name as the key for
// every one of today's four plugins, so defaulting an empty key for any of
// them always produces a reservedSeedKeys hit (reservedSeedKeys' own doc
// comment explains why that refusal cannot depend on whether any
// organization exists to collide with). What this test proves instead:
// defaulting still RUNS before the reserved-key check, rather than the key
// reaching that check as empty — pinned by asserting the refusal names the
// DEFAULTED key "cpu", not "", which is what a regression that skipped
// checks.ValidateDefinitionShape's own normalization would produce.
func TestCreateCheckDefinitionDefaultsAnEmptyKeyBeforeCheckingReservedKeys(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	_, err := svc.CreateCheckDefinition(ctx, shared.CheckDefinitionInput{Plugin: "cpu", Interval: 60 * time.Second})
	if !errors.Is(err, shared.ErrInvalidInput) {
		t.Fatalf("CreateCheckDefinition() with an empty key on plugin cpu error = %v, want ErrInvalidInput (reserved)", err)
	}
	if !strings.Contains(err.Error(), `"cpu"`) {
		t.Fatalf(`CreateCheckDefinition() error = %q, want it to name the DEFAULTED key "cpu"`, err)
	}
}

func TestCreateCheckDefinitionRefusesAnUnsupportedPlugin(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	_, err := svc.CreateCheckDefinition(ctx, shared.CheckDefinitionInput{Plugin: "nonsense", Interval: 60 * time.Second})
	if !errors.Is(err, shared.ErrInvalidInput) {
		t.Fatalf("CreateCheckDefinition() with an unsupported plugin error = %v, want ErrInvalidInput", err)
	}
	if len(store.createCheckDefCalls) != 0 {
		t.Fatalf("CreateCheckDefinitionWithProjections called %d times, want 0 after invalid input", len(store.createCheckDefCalls))
	}
}

// TestCreateCheckDefinitionRefusesMalformedServiceParams proves
// checks.ValidateDefinitionShape's own per-plugin parameter schema — the
// "service" plugin requires a non-empty units list — is enforced here too,
// not only for an organization's own definition: a shared definition is
// written directly into every organization's check_definitions row,
// bypassing internal/checks.Service entirely, so this package is the only
// place left to catch it.
func TestCreateCheckDefinitionRefusesMalformedServiceParams(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	_, err := svc.CreateCheckDefinition(ctx, shared.CheckDefinitionInput{Plugin: "service", Interval: 60 * time.Second})
	if !errors.Is(err, shared.ErrInvalidInput) {
		t.Fatalf("CreateCheckDefinition() with no units error = %v, want ErrInvalidInput", err)
	}
}

// TestCreateCheckDefinitionAcceptsExplicitParams is
// TestCreateCheckDefinitionRefusesMalformedServiceParams' own positive
// case: the "service" plugin's own required, non-empty params, carried all
// the way through — nothing else in this package's own test suite supplies
// a non-nil Params to Create/UpdateCheckDefinition, so checkParamsOrEmpty's
// pass-through branch (as opposed to its nil-becomes-{} default) had no
// direct test before this one.
func TestCreateCheckDefinitionAcceptsExplicitParams(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	got, err := svc.CreateCheckDefinition(ctx, shared.CheckDefinitionInput{
		Key: "sshd-watch", Plugin: "service", Interval: 60 * time.Second,
		Params: map[string]any{"units": []string{"sshd"}},
	})
	if err != nil {
		t.Fatalf("CreateCheckDefinition() error = %v, want nil", err)
	}
	units, _ := got.Params["units"].([]string)
	if len(units) != 1 || units[0] != "sshd" {
		t.Fatalf("CreateCheckDefinition() Params = %+v, want {units: [sshd]} carried through unchanged", got.Params)
	}
}

func TestCreateCheckDefinitionRefusesAnOutOfRangeInterval(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	_, err := svc.CreateCheckDefinition(ctx, shared.CheckDefinitionInput{Plugin: "cpu", Interval: 5 * time.Second})
	if !errors.Is(err, shared.ErrInvalidInput) {
		t.Fatalf("CreateCheckDefinition() with a too-short interval error = %v, want ErrInvalidInput", err)
	}
}

// TestCreateCheckDefinitionRefusesAReservedSeedKeyEvenWithNoOrganizations
// is the review's own Important finding, fixed and pinned here: cpu,
// memory, disk and service are refused OUTRIGHT, not merely when some
// organization happens to already collide — store.checkDefinitionNames is
// deliberately left empty (the fresh-install / every-organization-deleted
// case ExistingCheckDefinitionNames alone cannot protect, since there is
// nothing for it to compare against) and the refusal must still fire.
func TestCreateCheckDefinitionRefusesAReservedSeedKeyEvenWithNoOrganizations(t *testing.T) {
	for _, key := range []string{"cpu", "memory", "disk", "service"} {
		t.Run(key, func(t *testing.T) {
			store := newFakeStore() // store.checkDefinitionNames is nil — nothing to collide with
			svc := shared.NewService(store)

			_, err := svc.CreateCheckDefinition(ctx, shared.CheckDefinitionInput{Key: key, Plugin: "cpu", Interval: 60 * time.Second})
			if !errors.Is(err, shared.ErrInvalidInput) {
				t.Fatalf("CreateCheckDefinition(key=%q) error = %v, want ErrInvalidInput", key, err)
			}
			if len(store.createCheckDefCalls) != 0 {
				t.Fatalf("CreateCheckDefinitionWithProjections called %d times, want 0 for a reserved key", len(store.createCheckDefCalls))
			}
		})
	}
}

// TestUpdateCheckDefinitionRefusesAReservedSeedKey is the same refusal on a
// rename — a shared definition created under a safe key must not be
// renamed into a reserved one later, which would reach the identical
// half-provisioning window on the next organization created.
func TestUpdateCheckDefinitionRefusesAReservedSeedKey(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.checkDefs[id] = shared.CheckDefinition{ID: id, Key: "disk-data", Plugin: "disk", Interval: 300 * time.Second}
	svc := shared.NewService(store)

	_, err := svc.UpdateCheckDefinition(ctx, id, shared.CheckDefinitionInput{Key: "memory", Plugin: "disk", Interval: 300 * time.Second})
	if !errors.Is(err, shared.ErrInvalidInput) {
		t.Fatalf("UpdateCheckDefinition() renaming to a reserved key error = %v, want ErrInvalidInput", err)
	}
}

func TestCreateCheckDefinitionRefusesACollidingKey(t *testing.T) {
	store := newFakeStore()
	orgA := uuid.Must(uuid.NewV7())
	store.checkDefinitionNames = []shared.OrgName{{OrgID: orgA, Name: "disk-data"}}
	svc := shared.NewService(store)

	_, err := svc.CreateCheckDefinition(ctx, shared.CheckDefinitionInput{Key: "disk-data", Plugin: "disk", Interval: 60 * time.Second})
	if err == nil {
		t.Fatal("CreateCheckDefinition() error = nil, want a collision")
	}
	if !errors.Is(err, shared.ErrNameTaken) {
		t.Fatalf("CreateCheckDefinition() error = %v, want errors.Is(_, ErrNameTaken)", err)
	}
	var collision *shared.NameCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("CreateCheckDefinition() error = %v, want *NameCollisionError", err)
	}
	if !reflect.DeepEqual(collision.Orgs, []uuid.UUID{orgA}) {
		t.Fatalf("NameCollisionError.Orgs = %v, want [%v]", collision.Orgs, orgA)
	}
	if collision.Scope == "" {
		t.Fatal("NameCollisionError.Scope = \"\", want it to name the organization-wide scope (review finding: a bare org list leaves the provider no route to the truth for a non-org-scope collision)")
	}
	if len(store.createCheckDefCalls) != 0 {
		t.Fatalf("CreateCheckDefinitionWithProjections called %d times, want 0 after a collision", len(store.createCheckDefCalls))
	}
}

func TestUpdateCheckDefinitionExcludesItsOwnProjectionsFromTheCollisionCheck(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.checkDefs[id] = shared.CheckDefinition{ID: id, Key: "disk-data", Plugin: "disk", Interval: 300 * time.Second}
	svc := shared.NewService(store)

	if _, err := svc.UpdateCheckDefinition(ctx, id, shared.CheckDefinitionInput{Key: "disk-data-v2", Plugin: "disk", Interval: 300 * time.Second}); err != nil {
		t.Fatalf("UpdateCheckDefinition() error = %v, want nil", err)
	}
	if len(store.existingCheckDefExcludes) != 1 || store.existingCheckDefExcludes[0] != id {
		t.Fatalf("ExistingCheckDefinitionNames excludes = %v, want exactly [%v]", store.existingCheckDefExcludes, id)
	}
}

func TestUpdateCheckDefinitionRefusesACollidingKey(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.checkDefs[id] = shared.CheckDefinition{ID: id, Key: "disk-data", Plugin: "disk", Interval: 300 * time.Second}
	orgB := uuid.Must(uuid.NewV7())
	store.checkDefinitionNames = []shared.OrgName{{OrgID: orgB, Name: "taken"}}
	svc := shared.NewService(store)

	_, err := svc.UpdateCheckDefinition(ctx, id, shared.CheckDefinitionInput{Key: "taken", Plugin: "disk", Interval: 300 * time.Second})
	var collision *shared.NameCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("UpdateCheckDefinition() error = %v, want *NameCollisionError", err)
	}
	if !reflect.DeepEqual(collision.Orgs, []uuid.UUID{orgB}) {
		t.Fatalf("NameCollisionError.Orgs = %v, want [%v]", collision.Orgs, orgB)
	}
	if collision.Scope == "" {
		t.Fatal("NameCollisionError.Scope = \"\", want it to name the organization-wide scope")
	}
	if len(store.updateCheckDefCalls) != 0 {
		t.Fatalf("UpdateCheckDefinitionAndPropagate called %d times, want 0 after a collision", len(store.updateCheckDefCalls))
	}
}

func TestUpdateCheckDefinitionPropagatesNotFound(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	_, err := svc.UpdateCheckDefinition(ctx, uuid.Must(uuid.NewV7()), shared.CheckDefinitionInput{Key: "disk-data", Plugin: "cpu", Interval: 60 * time.Second})
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("UpdateCheckDefinition() error = %v, want ErrNotFound", err)
	}
}

func TestDeleteCheckDefinitionDelegatesToTheStore(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.checkDefs[id] = shared.CheckDefinition{ID: id, Key: "disk-data"}
	svc := shared.NewService(store)

	if err := svc.DeleteCheckDefinition(ctx, id); err != nil {
		t.Fatalf("DeleteCheckDefinition() error = %v, want nil", err)
	}
	if len(store.deleteCheckDefCalls) != 1 || store.deleteCheckDefCalls[0] != id {
		t.Fatalf("DeleteCheckDefinitionWithProjections calls = %v, want [%v]", store.deleteCheckDefCalls, id)
	}
}

// TestDeleteCheckDefinitionPropagatesNotFound proves Service.DeleteCheckDefinition
// is a plain delegation to the store with nothing else to add — unlike
// DeleteRepo/DeleteTemplate, which may return *InUseError, this one never
// does (the brief's own point 1, verified structurally: nothing references
// check_definitions, so there is no dependant check whose error this method
// would ever need to pass through untouched; ErrNotFound is the only
// sentinel exercised here).
func TestDeleteCheckDefinitionPropagatesNotFound(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	if err := svc.DeleteCheckDefinition(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("DeleteCheckDefinition() error = %v, want ErrNotFound", err)
	}
}

func TestProjectAllCheckDefinitionsIntoProjectsEveryDefinition(t *testing.T) {
	store := newFakeStore()
	idA, idB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store.checkDefs[idA] = shared.CheckDefinition{ID: idA, Key: "disk-data"}
	store.checkDefs[idB] = shared.CheckDefinition{ID: idB, Key: "cpu-high"}
	org := uuid.Must(uuid.NewV7())
	svc := shared.NewService(store)

	if err := svc.ProjectAllCheckDefinitionsInto(ctx, org); err != nil {
		t.Fatalf("ProjectAllCheckDefinitionsInto() error = %v, want nil", err)
	}
	if len(store.projectCheckDefCalls) != 2 {
		t.Fatalf("ProjectCheckDefinitionInto called %d times, want 2 (one per definition)", len(store.projectCheckDefCalls))
	}
	for _, c := range store.projectCheckDefCalls {
		if c.orgID != org {
			t.Fatalf("ProjectCheckDefinitionInto org = %v, want %v", c.orgID, org)
		}
	}
}

func TestProjectAllCheckDefinitionsIntoPropagatesAFailureForOneDefinition(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.checkDefs[id] = shared.CheckDefinition{ID: id, Key: "disk-data"}
	org := uuid.Must(uuid.NewV7())
	store.forceProjectCheckDefErrOrg = &org
	store.forceProjectCheckDefErr = shared.ErrNameTaken
	svc := shared.NewService(store)

	if err := svc.ProjectAllCheckDefinitionsInto(ctx, org); !errors.Is(err, shared.ErrNameTaken) {
		t.Fatalf("ProjectAllCheckDefinitionsInto() error = %v, want ErrNameTaken", err)
	}
}

func TestGetCheckDefNotFound(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	if _, err := svc.GetCheckDef(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("GetCheckDef() error = %v, want ErrNotFound", err)
	}
}

func TestListCheckDefsReturnsEveryDefinition(t *testing.T) {
	store := newFakeStore()
	idA, idB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store.checkDefs[idA] = shared.CheckDefinition{ID: idA, Key: "disk-data"}
	store.checkDefs[idB] = shared.CheckDefinition{ID: idB, Key: "cpu-high"}
	svc := shared.NewService(store)

	got, err := svc.ListCheckDefs(ctx)
	if err != nil {
		t.Fatalf("ListCheckDefs() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListCheckDefs() = %v, want 2 definitions", got)
	}
}

var ctx = context.Background()

// TestTemplateDefinitionRefusesAMalformedAnsibleMarker is backlog 2.101's
// backend half, and the same gap 2.71 closed for an organization's own
// templates. validateTemplateDefinitionInput ran only
// ansiblevars.ForbiddenIn, which looks at TOP-LEVEL names -- so a nested
// object with an "__ansible" key that is not a well-formed marker saved
// happily here and was written straight into EVERY organization's own
// job_templates.extra_vars (store_sql.go bypasses internal/templates
// entirely), breaking a run in each of them with a parse error in a
// document none of their operators wrote. One mistake, every customer.
func TestTemplateDefinitionRefusesAMalformedAnsibleMarker(t *testing.T) {
	base := func() shared.TemplateDefinitionInput {
		return shared.TemplateDefinitionInput{
			Name: "deploy-site", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "site.yml",
		}
	}
	for _, tc := range []struct {
		name string
		vars map[string]any
		want bool // true: must be refused
	}{
		{"a well-formed unsafe marker is how a definition hands over literal text",
			map[string]any{"greeting": map[string]any{"__ansible_unsafe": "{{ not_rendered }}"}}, false},
		{"a marker with a second key cannot be loaded",
			map[string]any{"greeting": map[string]any{"__ansible_unsafe": "hi", "extra": 1}}, true},
		{"an unknown __ansible key cannot be loaded",
			map[string]any{"greeting": map[string]any{"__ansible_made_up": "hi"}}, true},
		{"and it is found however deep it sits",
			map[string]any{"a": []any{map[string]any{"b": map[string]any{"__ansible_vault": 7}}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			svc := shared.NewService(store)
			in := base()
			in.ExtraVars = tc.vars
			_, err := svc.CreateTemplate(ctx, in)
			if tc.want {
				if !errors.Is(err, shared.ErrInvalidInput) {
					t.Fatalf("CreateTemplate = %v, want ErrInvalidInput", err)
				}
				if len(store.createTemplateCalls) != 0 {
					t.Fatalf("it reached the store %d times; nothing may be projected", len(store.createTemplateCalls))
				}
				// The edit path is the same gate: a definition saved
				// before this check existed must not keep a broken
				// marker through an unrelated edit.
				if _, err := svc.UpdateTemplate(ctx, uuid.Must(uuid.NewV7()), in); !errors.Is(err, shared.ErrInvalidInput) {
					t.Fatalf("UpdateTemplate = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateTemplate = %v, want it saved", err)
			}
		})
	}
}
