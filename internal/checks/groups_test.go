package checks_test

import (
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

var ctx = context.Background()

// fakeStore is an in-memory checks.Store: a name conflict within an
// organization mirrors the unique (org_id, name) constraint the real
// store enforces, and Create/Update stamp CreatedAt/UpdatedAt the way the
// real SQLStore's DEFAULT now() columns do — the service itself sets
// neither (see Service's own doc comment).
type fakeStore struct {
	mu       sync.Mutex
	byID     map[uuid.UUID]checks.Group
	defsByID map[uuid.UUID]checks.Definition
	err      error
	now      func() time.Time
	// recordCalls counts RecordObservations calls that actually reached
	// the store — what samples_test.go's contract-bound tests assert did
	// NOT happen for a batch the service refused up front.
	recordCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		byID: map[uuid.UUID]checks.Group{}, defsByID: map[uuid.UUID]checks.Definition{}, now: time.Now,
	}
}

func (f *fakeStore) nameTaken(orgID, exceptID uuid.UUID, name string) bool {
	for _, g := range f.byID {
		if g.OrgID == orgID && g.ID != exceptID && g.Name == name {
			return true
		}
	}
	return false
}

func (f *fakeStore) Create(_ context.Context, g checks.Group) (checks.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return checks.Group{}, f.err
	}
	if f.nameTaken(g.OrgID, g.ID, g.Name) {
		return checks.Group{}, checks.ErrNameTaken
	}
	now := f.now()
	g.CreatedAt, g.UpdatedAt = now, now
	f.byID[g.ID] = g
	return g, nil
}

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID) ([]checks.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []checks.Group
	for _, g := range f.byID {
		if g.OrgID == orgID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeStore) Update(_ context.Context, g checks.Group) (checks.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return checks.Group{}, f.err
	}
	cur, ok := f.byID[g.ID]
	if !ok || cur.OrgID != g.OrgID {
		return checks.Group{}, checks.ErrNotFound
	}
	if f.nameTaken(g.OrgID, g.ID, g.Name) {
		return checks.Group{}, checks.ErrNameTaken
	}
	g.CreatedAt, g.UpdatedAt = cur.CreatedAt, f.now()
	f.byID[g.ID] = g
	return g, nil
}

func (f *fakeStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.byID[id]
	if !ok || g.OrgID != orgID {
		return checks.ErrNotFound
	}
	delete(f.byID, id)
	return nil
}

// fakeMatcher is the HostMatcher test double. err, when set, simulates the
// port failing (e.g. the hosts store being down); calls records every
// invocation, so a test can assert Preview short-circuits before ever
// reaching it.
type fakeMatcher struct {
	hosts []uuid.UUID
	err   error
	calls int
}

func (f *fakeMatcher) MatchingHosts(_ context.Context, _ uuid.UUID, _ string) ([]uuid.UUID, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.hosts, nil
}

func TestCreateGroupStoresAValidGroup(t *testing.T) {
	store := newFakeStore()
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())

	g, err := svc.CreateGroup(ctx, org, checks.GroupInput{Name: "web", Selector: `role=web`})
	if err != nil {
		t.Fatal(err)
	}
	if g.ID == uuid.Nil || g.OrgID != org || g.Name != "web" || g.Selector != "role=web" {
		t.Fatalf("created group = %+v", g)
	}
	// Priority default: an input that never sets Priority stores 0, not
	// some other value the zero-value happens to collide with.
	if g.Priority != 0 {
		t.Fatalf("priority = %d, want 0 (the zero value default)", g.Priority)
	}
	if !g.CreatedAt.Equal(now) || !g.UpdatedAt.Equal(now) {
		t.Fatalf("timestamps = %+v, want %v", g, now)
	}
}

func TestCreateGroupRejectsAnUnparseableSelector(t *testing.T) {
	store := newFakeStore()
	matcher := &fakeMatcher{}
	svc := checks.NewService(store, checks.WithMatcher(matcher))
	org := uuid.Must(uuid.NewV7())

	_, err := svc.CreateGroup(ctx, org, checks.GroupInput{Name: "broken", Selector: "role="})
	if !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if list, _ := svc.ListGroups(ctx, org); len(list) != 0 {
		t.Fatalf("an invalid selector must never be stored, got %v", list)
	}
	if matcher.calls != 0 {
		t.Fatalf("validating a selector must not touch the matcher, got %d calls", matcher.calls)
	}
}

func TestCreateGroupRejectsAnEmptyName(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())

	if _, err := svc.CreateGroup(ctx, org, checks.GroupInput{Name: "  ", Selector: "role=web"}); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateGroupRejectsAnOutOfRangePriority(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())

	_, err := svc.CreateGroup(ctx, org, checks.GroupInput{Name: "web", Selector: "role=web", Priority: math.MaxInt32 + 1})
	if !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

// errReader always fails: newID's entropy-failure panic (mirrored on
// uuid.Must elsewhere in the codebase) is otherwise never exercised.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCreateGroupPanicsWhenEntropyFails(t *testing.T) {
	svc := checks.NewService(newFakeStore(), checks.WithRand(errReader{}))
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic when the entropy source fails")
		}
	}()
	_, _ = svc.CreateGroup(ctx, uuid.Must(uuid.NewV7()), checks.GroupInput{Name: "web", Selector: "role=web"})
}

func TestCreateGroupNameTakenWithinOrg(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())

	if _, err := svc.CreateGroup(ctx, org, checks.GroupInput{Name: "web", Selector: "role=web"}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.CreateGroup(ctx, org, checks.GroupInput{Name: "web", Selector: "role=web2"})
	if !errors.Is(err, checks.ErrNameTaken) {
		t.Fatalf("err = %v, want ErrNameTaken", err)
	}

	// A different organization may reuse the same name — the constraint
	// is (org_id, name), not name alone.
	other := uuid.Must(uuid.NewV7())
	if _, err := svc.CreateGroup(ctx, other, checks.GroupInput{Name: "web", Selector: "role=web"}); err != nil {
		t.Fatalf("a name taken in another org must not block this one: %v", err)
	}
}

func TestUpdateGroupReplacesFieldsAndKeepsCreatedAt(t *testing.T) {
	store := newFakeStore()
	created := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	updated := created.Add(24 * time.Hour)
	tick := created
	store.now = func() time.Time { return tick }
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())

	g, err := svc.CreateGroup(ctx, org, checks.GroupInput{Name: "web", Selector: "role=web", Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	tick = updated

	got, err := svc.UpdateGroup(ctx, org, g.ID, checks.GroupInput{Name: "web2", Selector: "role=db", Priority: 5})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "web2" || got.Selector != "role=db" || got.Priority != 5 {
		t.Fatalf("updated group = %+v", got)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt = %v, want unchanged %v", got.CreatedAt, created)
	}
}

func TestUpdateGroupUnknownID(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())

	_, err := svc.UpdateGroup(ctx, org, uuid.Must(uuid.NewV7()), checks.GroupInput{Name: "web", Selector: "role=web"})
	if !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdateGroupRejectsAnUnparseableSelector(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())
	g, err := svc.CreateGroup(ctx, org, checks.GroupInput{Name: "web", Selector: "role=web"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.UpdateGroup(ctx, org, g.ID, checks.GroupInput{Name: "web", Selector: "role="})
	if !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	// The stored row must be untouched by the rejected update.
	list, err := svc.ListGroups(ctx, org)
	if err != nil || len(list) != 1 || list[0].Selector != "role=web" {
		t.Fatalf("list = %+v, %v; the invalid update must not have applied", list, err)
	}
}

func TestDeleteGroup(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())
	g, err := svc.CreateGroup(ctx, org, checks.GroupInput{Name: "web", Selector: "role=web"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteGroup(ctx, org, g.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteGroup(ctx, org, g.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("deleting twice = %v, want ErrNotFound", err)
	}
}

func TestListGroupsIsOrgScoped(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	orgA, orgB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := svc.CreateGroup(ctx, orgA, checks.GroupInput{Name: "a", Selector: "role=web"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateGroup(ctx, orgB, checks.GroupInput{Name: "b", Selector: "role=db"}); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListGroups(ctx, orgA)
	if err != nil || len(list) != 1 || list[0].Name != "a" {
		t.Fatalf("list = %+v, %v, want only orgA's group", list, err)
	}
}

func TestPreviewWithoutMatcherRefuses(t *testing.T) {
	svc := checks.NewService(newFakeStore())
	if _, err := svc.Preview(ctx, uuid.Must(uuid.NewV7()), "role=web"); err == nil {
		t.Fatal("Preview without WithMatcher must refuse, not panic")
	}
}

func TestPreviewRejectsAnUnparseableSelectorBeforeTheMatcher(t *testing.T) {
	matcher := &fakeMatcher{hosts: []uuid.UUID{uuid.Must(uuid.NewV7())}}
	svc := checks.NewService(newFakeStore(), checks.WithMatcher(matcher))
	_, err := svc.Preview(ctx, uuid.Must(uuid.NewV7()), "role=")
	if !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if matcher.calls != 0 {
		t.Fatalf("an unparseable selector must never reach the matcher, got %d calls", matcher.calls)
	}
}

func TestPreviewDelegatesToTheMatcher(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	want := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	matcher := &fakeMatcher{hosts: want}
	svc := checks.NewService(newFakeStore(), checks.WithMatcher(matcher))
	got, err := svc.Preview(ctx, org, "role=web")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got = %v, want %v", got, want)
	}
	if matcher.calls != 1 {
		t.Fatalf("calls = %d, want 1", matcher.calls)
	}

	matcher.err = errors.New("hosts store down")
	if _, err := svc.Preview(ctx, org, "role=web"); err == nil || !strings.Contains(err.Error(), "hosts store down") {
		t.Fatalf("err = %v, want the matcher's failure to surface", err)
	}
}

func TestGetGroupIsOrgScoped(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	orgA, orgB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	g, err := svc.CreateGroup(ctx, orgA, checks.GroupInput{Name: "web", Selector: "role=web", Priority: 7})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetGroup(ctx, orgA, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != g.ID || got.Name != "web" || got.Priority != 7 {
		t.Fatalf("got = %+v, want the created group", got)
	}
	// Another organization's group reads exactly like one that never
	// existed — invisible, not merely forbidden (ErrNotFound's own
	// contract).
	if _, err := svc.GetGroup(ctx, orgB, g.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("cross-org get = %v, want ErrNotFound", err)
	}
	if _, err := svc.GetGroup(ctx, orgA, uuid.Must(uuid.NewV7())); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("unknown id = %v, want ErrNotFound", err)
	}
}

func TestGetGroupSurfacesAStoreFailure(t *testing.T) {
	store := newFakeStore()
	store.err = errors.New("groups store down")
	svc := checks.NewService(store)
	if _, err := svc.GetGroup(ctx, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())); err == nil ||
		errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("err = %v, want the store's own failure, never ErrNotFound", err)
	}
}
