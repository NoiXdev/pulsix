package shared_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"

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
	orgIDs       []uuid.UUID

	existingNamesExcludes []uuid.UUID // every exclude CreateRepo/UpdateRepo passed, in call order
	createCalls           []createCall
	updateCalls           []shared.RepoDefinition
	deleteCalls           []uuid.UUID
	projectCalls          []projectCall

	forceExistingNamesErr error
	forceOrgIDsErr        error
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
}

type createCall struct {
	def    shared.RepoDefinition
	orgIDs []uuid.UUID
}

type projectCall struct {
	def   shared.RepoDefinition
	orgID uuid.UUID
}

func newFakeStore() *fakeStore {
	return &fakeStore{defs: map[uuid.UUID]shared.RepoDefinition{}}
}

func (f *fakeStore) CreateWithProjections(_ context.Context, def shared.RepoDefinition, orgIDs []uuid.UUID) (shared.RepoDefinition, error) {
	if f.forceCreateErr != nil {
		return shared.RepoDefinition{}, f.forceCreateErr
	}
	f.createCalls = append(f.createCalls, createCall{def: def, orgIDs: orgIDs})
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

func (f *fakeStore) OrgIDs(_ context.Context) ([]uuid.UUID, error) {
	if f.forceOrgIDsErr != nil {
		return nil, f.forceOrgIDsErr
	}
	return f.orgIDs, nil
}

// --- CreateRepo ---

func TestCreateRepoProjectsIntoEveryOrganization(t *testing.T) {
	store := newFakeStore()
	orgA, orgB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store.orgIDs = []uuid.UUID{orgA, orgB}
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
	if !reflect.DeepEqual(call.orgIDs, store.orgIDs) {
		t.Fatalf("CreateWithProjections orgIDs = %v, want every organization %v", call.orgIDs, store.orgIDs)
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
	store.orgIDs = []uuid.UUID{orgA}
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

var ctx = context.Background()
