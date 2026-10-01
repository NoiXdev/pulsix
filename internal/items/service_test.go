package items_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/items"
)

// testActor stands in for the authenticated principal every CreateDecision
// call in this file passes. TestCreateDecisionValid is the one test that
// asserts on it directly (CreatedBy == testActor); TestUpdateDecisionValid
// seeds its own, different fixed CreatedBy directly on the stored row
// instead, to pin the separate claim that Update preserves whatever was
// already there rather than re-stamping Update's own caller.
var testActor = uuid.Must(uuid.NewV7())

// fakeStore is an in-memory items.Store: no database, so the whole
// validation table and the preview tests run without Docker. It keeps
// items and decisions in separate maps, the same split the real Store's
// two tables (check_items, check_item_decisions) keep.
type fakeStore struct {
	mu        sync.Mutex
	views     map[uuid.UUID][]items.View // orgID -> every item discovered for it
	decisions map[uuid.UUID]items.Decision

	// forceXErr, when non-nil, makes the matching method fail regardless
	// of its arguments — the storage-failure branches CreateDecision,
	// UpdateDecision and Preview each have, alongside their validation
	// and not-found ones.
	forceCreateErr error
	forceUpdateErr error
	forceItemsErr  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		views:     map[uuid.UUID][]items.View{},
		decisions: map[uuid.UUID]items.Decision{},
	}
}

func (f *fakeStore) Discover(context.Context, uuid.UUID, uuid.UUID, []items.Seen) error {
	return errors.New("fakeStore: Discover not used by the decisions service")
}

func (f *fakeStore) ItemsForHost(_ context.Context, orgID, hostID uuid.UUID) ([]items.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forceItemsErr != nil {
		return nil, f.forceItemsErr
	}
	var out []items.View
	for _, v := range f.views[orgID] {
		if v.HostID == hostID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeStore) ItemsForOrg(_ context.Context, orgID uuid.UUID, filter items.Filter) ([]items.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forceItemsErr != nil {
		return nil, f.forceItemsErr
	}
	var out []items.View
	for _, v := range f.views[orgID] {
		if filter.CheckKey != "" && v.CheckKey != filter.CheckKey {
			continue
		}
		if filter.ItemGlob != "" && !items.Match(filter.ItemGlob, v.Name) {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeStore) Decisions(_ context.Context, orgID uuid.UUID, hostID *uuid.UUID) ([]items.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []items.Decision
	for _, d := range f.decisions {
		if d.OrgID != orgID {
			continue
		}
		if d.Scope == items.ScopeOrg || (hostID != nil && d.HostID != nil && *d.HostID == *hostID) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateDecision(_ context.Context, d items.Decision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forceCreateErr != nil {
		return f.forceCreateErr
	}
	f.decisions[d.ID] = d
	return nil
}

func (f *fakeStore) UpdateDecision(_ context.Context, d items.Decision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forceUpdateErr != nil {
		return f.forceUpdateErr
	}
	existing, ok := f.decisions[d.ID]
	if !ok || existing.OrgID != d.OrgID {
		return items.ErrNotFound
	}
	f.decisions[d.ID] = d
	return nil
}

func (f *fakeStore) DeleteDecision(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.decisions[id]
	if !ok || existing.OrgID != orgID {
		return items.ErrNotFound
	}
	delete(f.decisions, id)
	return nil
}

func (f *fakeStore) GetDecision(_ context.Context, orgID, id uuid.UUID) (items.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.decisions[id]
	if !ok || d.OrgID != orgID {
		return items.Decision{}, items.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) ListDecisions(_ context.Context, orgID uuid.UUID) ([]items.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []items.Decision
	for _, d := range f.decisions {
		if d.OrgID == orgID {
			out = append(out, d)
		}
	}
	return out, nil
}

// seedItems installs n items under orgID (and, for a host-scoped test,
// all on the one hostID) directly into the fake store, bypassing Discover.
func (f *fakeStore) seedItems(orgID, hostID uuid.UUID, checkKey string, names []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, name := range names {
		f.views[orgID] = append(f.views[orgID], items.View{
			Item: items.Item{OrgID: orgID, HostID: hostID, CheckKey: checkKey, Name: name},
		})
	}
}

func fixedClock(t time.Time) items.Option {
	return items.WithClock(func() time.Time { return t })
}

// fakeCatalogue stands in for rules.Catalogue()'s check keys: internal/items
// must not import internal/rules (the evaluation ticker needs the reverse
// edge, rules -> items, so items -> rules would cycle), so NewService takes
// the valid check_key set as a required constructor argument. "disk" is the
// only key every test needs; a second, "memory", exists so a test can prove
// an unrelated known key does not make an unrecognised one pass.
func fakeCatalogue() map[string]struct{} {
	return map[string]struct{}{"disk": {}, "memory": {}}
}

var ctx = context.Background()

// validInput returns a valid, org-scoped decision input watching nothing
// in particular (glob "*", check "disk") — the baseline every validation
// test narrows with its own single broken field.
func validInput() items.DecisionInput {
	return items.DecisionInput{
		Scope:     items.ScopeOrg,
		CheckKey:  "disk",
		MatchKind: items.MatchGlob,
		Pattern:   "/var/*",
		Verdict:   items.Ignore,
		Note:      "deliberately full backup volume",
	}
}

func TestCreateDecisionValidation(t *testing.T) {
	host := uuid.Must(uuid.NewV7())

	tests := []struct {
		name    string
		in      items.DecisionInput
		wantMsg string
	}{
		{
			"unknown scope",
			func() items.DecisionInput { in := validInput(); in.Scope = "planet"; return in }(),
			`scope must be "org" or "host"`,
		},
		{
			"host scope without host id",
			func() items.DecisionInput { in := validInput(); in.Scope = items.ScopeHost; return in }(),
			`scope "host" must set a host`,
		},
		{
			"org scope with host id set",
			func() items.DecisionInput { in := validInput(); in.HostID = &host; return in }(),
			`scope "org" must not set a host`,
		},
		{
			"unknown check key",
			func() items.DecisionInput { in := validInput(); in.CheckKey = "nonsense"; return in }(),
			`unknown check key "nonsense"`,
		},
		{
			"unknown match kind",
			func() items.DecisionInput { in := validInput(); in.MatchKind = "regex"; return in }(),
			`match kind must be "exact" or "glob"`,
		},
		{
			"exact pattern with a star",
			func() items.DecisionInput {
				in := validInput()
				in.MatchKind, in.Pattern = items.MatchExact, "sd*1"
				return in
			}(),
			`exact match pattern must not contain "*"; did you mean match kind "glob"?`,
		},
		{
			"empty pattern",
			func() items.DecisionInput { in := validInput(); in.Pattern = ""; return in }(),
			"pattern must not be empty",
		},
		{
			"pattern too long",
			func() items.DecisionInput {
				in := validInput()
				in.Pattern = strings.Repeat("a", items.MaxPatternLen+1)
				return in
			}(),
			fmt.Sprintf("pattern exceeds %d characters", items.MaxPatternLen),
		},
		{
			"note too long",
			func() items.DecisionInput { in := validInput(); in.Note = strings.Repeat("a", 501); return in }(),
			"note must be at most 500 characters",
		},
		{
			"unknown verdict",
			func() items.DecisionInput { in := validInput(); in.Verdict = "mute"; return in }(),
			`verdict must be "ignore" or "watch"`,
		},
		{
			// validInput() is already org-scoped, so overriding only
			// Verdict is enough to hit this combination — an
			// organization-wide watch changes nothing, since watching is
			// already what happens to anything no decision names.
			"organization-wide watch has no effect",
			func() items.DecisionInput { in := validInput(); in.Verdict = items.Watch; return in }(),
			`scope "org" with verdict "watch" would have no effect`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := items.NewService(newFakeStore(), fakeCatalogue)
			_, err := svc.CreateDecision(ctx, uuid.Must(uuid.NewV7()), testActor, tt.in)
			if !errors.Is(err, items.ErrInvalidInput) {
				t.Fatalf("CreateDecision(%+v) error = %v, want ErrInvalidInput", tt.in, err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("CreateDecision(%+v) error = %q, want it to contain %q", tt.in, err.Error(), tt.wantMsg)
			}
		})
	}
}

// TestCreateDecisionCheckKeyComesFromInjectedCatalogue proves CheckKey is
// validated against whatever checkKeys the caller supplied to NewService,
// not a set this package knows on its own: the same CheckKey is accepted
// by one Service and refused by another, differing only in which function
// they were constructed with.
func TestCreateDecisionCheckKeyComesFromInjectedCatalogue(t *testing.T) {
	in := validInput()
	in.CheckKey = "service" // a real rules.Catalogue() check key, absent from fakeCatalogue

	withService := items.NewService(newFakeStore(), func() map[string]struct{} {
		return map[string]struct{}{"service": {}}
	})
	if _, err := withService.CreateDecision(ctx, uuid.Must(uuid.NewV7()), testActor, in); err != nil {
		t.Fatalf("CreateDecision() error = %v, want nil: the injected catalogue knows %q", err, in.CheckKey)
	}

	withoutService := items.NewService(newFakeStore(), fakeCatalogue)
	if _, err := withoutService.CreateDecision(ctx, uuid.Must(uuid.NewV7()), testActor, in); !errors.Is(err, items.ErrInvalidInput) {
		t.Fatalf("CreateDecision() error = %v, want ErrInvalidInput: fakeCatalogue does not know %q", err, in.CheckKey)
	}
}

func TestCreateDecisionValid(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	host := uuid.Must(uuid.NewV7())
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	store := newFakeStore()
	svc := items.NewService(store, fakeCatalogue, fixedClock(now))

	in := validInput()
	in.Scope, in.HostID = items.ScopeHost, &host

	got, err := svc.CreateDecision(ctx, org, testActor, in)
	if err != nil {
		t.Fatalf("CreateDecision() error = %v, want nil", err)
	}
	if got.ID == uuid.Nil {
		t.Fatal("CreateDecision() did not assign an id")
	}
	if got.OrgID != org {
		t.Fatalf("CreateDecision() OrgID = %v, want %v", got.OrgID, org)
	}
	if got.HostID == nil || *got.HostID != host {
		t.Fatalf("CreateDecision() HostID = %v, want %v", got.HostID, host)
	}
	if !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Fatalf("CreateDecision() timestamps = %v/%v, want both %v", got.CreatedAt, got.UpdatedAt, now)
	}
	if got.CreatedBy == nil || *got.CreatedBy != testActor {
		t.Fatalf("CreateDecision() CreatedBy = %v, want %v", got.CreatedBy, testActor)
	}

	stored, err := store.GetDecision(ctx, org, got.ID)
	if err != nil {
		t.Fatalf("store.GetDecision() error = %v", err)
	}
	if stored.Pattern != in.Pattern {
		t.Fatalf("stored.Pattern = %q, want %q", stored.Pattern, in.Pattern)
	}
}

// TestCreateDecisionAcceptsNoteAtExactlyTheBoundary pins the valid side of
// the 500-rune note boundary: TestCreateDecisionValidation's "note too
// long" case already pins 501 failing, but nothing before this asserted
// that exactly 500 still succeeds.
func TestCreateDecisionAcceptsNoteAtExactlyTheBoundary(t *testing.T) {
	in := validInput()
	in.Note = strings.Repeat("a", 500)

	if _, err := items.NewService(newFakeStore(), fakeCatalogue).CreateDecision(ctx, uuid.Must(uuid.NewV7()), testActor, in); err != nil {
		t.Fatalf("CreateDecision() error = %v, want nil for a 500-rune note", err)
	}
}

func TestUpdateDecisionCrossOrgIsNotFound(t *testing.T) {
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := items.NewService(store, fakeCatalogue)

	created, err := svc.CreateDecision(ctx, orgA, testActor, validInput())
	if err != nil {
		t.Fatalf("CreateDecision() error = %v", err)
	}

	if _, err := svc.UpdateDecision(ctx, orgB, created.ID, validInput()); !errors.Is(err, items.ErrNotFound) {
		t.Fatalf("UpdateDecision() across orgs error = %v, want ErrNotFound", err)
	}
}

func TestGetDecisionCrossOrgIsNotFound(t *testing.T) {
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := items.NewService(store, fakeCatalogue)

	created, err := svc.CreateDecision(ctx, orgA, testActor, validInput())
	if err != nil {
		t.Fatalf("CreateDecision() error = %v", err)
	}

	if _, err := svc.GetDecision(ctx, orgB, created.ID); !errors.Is(err, items.ErrNotFound) {
		t.Fatalf("GetDecision() across orgs error = %v, want ErrNotFound", err)
	}
}

func TestDeleteDecisionCrossOrgIsNotFound(t *testing.T) {
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := items.NewService(store, fakeCatalogue)

	created, err := svc.CreateDecision(ctx, orgA, testActor, validInput())
	if err != nil {
		t.Fatalf("CreateDecision() error = %v", err)
	}

	if err := svc.DeleteDecision(ctx, orgB, created.ID); !errors.Is(err, items.ErrNotFound) {
		t.Fatalf("DeleteDecision() across orgs error = %v, want ErrNotFound", err)
	}
}

// TestUpdateDecisionValid pins the happy path: a full replace that keeps
// CreatedBy and CreatedAt from the existing row while advancing UpdatedAt
// and persisting every field the caller changed.
func TestUpdateDecisionValid(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	host := uuid.Must(uuid.NewV7())
	createdBy := uuid.Must(uuid.NewV7())
	created := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	store := newFakeStore()
	svc := items.NewService(store, fakeCatalogue, fixedClock(created))

	d, err := svc.CreateDecision(ctx, org, testActor, validInput())
	if err != nil {
		t.Fatalf("CreateDecision() error = %v", err)
	}
	// Simulate an existing row with a recorded author: UpdateDecision must
	// preserve it even though DecisionInput carries no CreatedBy field.
	stored, err := store.GetDecision(ctx, org, d.ID)
	if err != nil {
		t.Fatalf("store.GetDecision() error = %v", err)
	}
	stored.CreatedBy = &createdBy
	if err := store.UpdateDecision(ctx, stored); err != nil {
		t.Fatalf("store.UpdateDecision() seed error = %v", err)
	}

	svc = items.NewService(store, fakeCatalogue, fixedClock(updated))
	in := validInput()
	// Scoped to a host, not org: Watch only has an effect as a host-scoped
	// overrule (an organization-wide watch is refused outright — see
	// TestCreateDecisionValidation's "organization-wide watch has no
	// effect" case), and this test wants a verdict change that is
	// actually valid to prove the replace persists it.
	in.Scope, in.HostID, in.Pattern, in.Verdict = items.ScopeHost, &host, "/mnt/*", items.Watch

	got, err := svc.UpdateDecision(ctx, org, d.ID, in)
	if err != nil {
		t.Fatalf("UpdateDecision() error = %v", err)
	}
	if got.Pattern != "/mnt/*" || got.Verdict != items.Watch {
		t.Fatalf("UpdateDecision() = %+v, want the new pattern and verdict", got)
	}
	if got.CreatedBy == nil || *got.CreatedBy != createdBy {
		t.Fatalf("UpdateDecision() CreatedBy = %v, want %v preserved", got.CreatedBy, createdBy)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("UpdateDecision() CreatedAt = %v, want %v preserved", got.CreatedAt, created)
	}
	if !got.UpdatedAt.Equal(updated) {
		t.Fatalf("UpdateDecision() UpdatedAt = %v, want %v", got.UpdatedAt, updated)
	}
}

// TestCreateDecisionStoreError proves a storage failure propagates past
// CreateDecision unwrapped by any validation logic.
func TestCreateDecisionStoreError(t *testing.T) {
	store := newFakeStore()
	wantErr := errors.New("boom")
	store.forceCreateErr = wantErr
	svc := items.NewService(store, fakeCatalogue)

	if _, err := svc.CreateDecision(ctx, uuid.Must(uuid.NewV7()), testActor, validInput()); !errors.Is(err, wantErr) {
		t.Fatalf("CreateDecision() error = %v, want %v", err, wantErr)
	}
}

// TestUpdateDecisionStoreError proves a storage failure on the final write
// propagates past UpdateDecision, distinct from the not-found and
// validation failures covered elsewhere.
func TestUpdateDecisionStoreError(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := items.NewService(store, fakeCatalogue)

	d, err := svc.CreateDecision(ctx, org, testActor, validInput())
	if err != nil {
		t.Fatalf("CreateDecision() error = %v", err)
	}

	wantErr := errors.New("boom")
	store.forceUpdateErr = wantErr
	if _, err := svc.UpdateDecision(ctx, org, d.ID, validInput()); !errors.Is(err, wantErr) {
		t.Fatalf("UpdateDecision() error = %v, want %v", err, wantErr)
	}
}

// TestPreviewStoreError proves a storage failure propagates past Preview
// wrapped with context, rather than being swallowed.
func TestPreviewStoreError(t *testing.T) {
	store := newFakeStore()
	wantErr := errors.New("boom")
	store.forceItemsErr = wantErr
	svc := items.NewService(store, fakeCatalogue)

	if _, err := svc.Preview(ctx, uuid.Must(uuid.NewV7()), validInput()); !errors.Is(err, wantErr) {
		t.Fatalf("Preview() error = %v, want it to wrap %v", err, wantErr)
	}
}

// errReader always fails: newID's entropy-failure panic (mirrored on
// uuid.Must elsewhere in the codebase, and on rules.Service's own newID) is
// otherwise never exercised. Mirrors rules' own errReader test.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

// TestCreateDecisionPanicsWhenEntropyFails proves WithRand actually drives
// id generation: an entropy source that always fails must reach newID's
// panic, not NewService's own default (crypto/rand).
func TestCreateDecisionPanicsWhenEntropyFails(t *testing.T) {
	svc := items.NewService(newFakeStore(), fakeCatalogue, items.WithRand(errReader{}))
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic when the entropy source fails")
		}
	}()
	_, _ = svc.CreateDecision(ctx, uuid.Must(uuid.NewV7()), testActor, validInput())
}

func TestUpdateDecisionValidationReusesCreateRules(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := items.NewService(store, fakeCatalogue)

	created, err := svc.CreateDecision(ctx, org, testActor, validInput())
	if err != nil {
		t.Fatalf("CreateDecision() error = %v", err)
	}

	bad := validInput()
	bad.Verdict = "mute"
	if _, err := svc.UpdateDecision(ctx, org, created.ID, bad); !errors.Is(err, items.ErrInvalidInput) {
		t.Fatalf("UpdateDecision() error = %v, want ErrInvalidInput", err)
	}
}

func TestListDecisionsScopedToOrg(t *testing.T) {
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := items.NewService(store, fakeCatalogue)

	if _, err := svc.CreateDecision(ctx, orgA, testActor, validInput()); err != nil {
		t.Fatalf("CreateDecision() error = %v", err)
	}
	if _, err := svc.CreateDecision(ctx, orgB, testActor, validInput()); err != nil {
		t.Fatalf("CreateDecision() error = %v", err)
	}

	got, err := svc.ListDecisions(ctx, orgA)
	if err != nil {
		t.Fatalf("ListDecisions() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListDecisions() = %d decisions, want 1", len(got))
	}
}

func TestPreviewExactPatternMatchesOne(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	host := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	store.seedItems(org, host, "disk", []string{"/var", "/var/log", "/home"})
	svc := items.NewService(store, fakeCatalogue)

	in := validInput()
	in.MatchKind, in.Pattern = items.MatchExact, "/var"

	got, err := svc.Preview(ctx, org, in)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if got.Total != 1 || len(got.Matches) != 1 || got.Capped {
		t.Fatalf("Preview() = %+v, want Total=1, 1 match, not capped", got)
	}
	if got.Matches[0].Item != "/var" {
		t.Fatalf("Preview() matched %q, want /var", got.Matches[0].Item)
	}
}

func TestPreviewGlobMatchesSeveral(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	host := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	store.seedItems(org, host, "disk", []string{"/var", "/var/log", "/home"})
	svc := items.NewService(store, fakeCatalogue)

	in := validInput()
	in.Pattern = "/var*"

	got, err := svc.Preview(ctx, org, in)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if got.Total != 2 || len(got.Matches) != 2 || got.Capped {
		t.Fatalf("Preview() = %+v, want Total=2, 2 matches, not capped", got)
	}
}

func TestPreviewGlobMatchesNone(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	host := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	store.seedItems(org, host, "disk", []string{"/var", "/home"})
	svc := items.NewService(store, fakeCatalogue)

	in := validInput()
	in.Pattern = "/opt*"

	got, err := svc.Preview(ctx, org, in)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if got.Total != 0 || len(got.Matches) != 0 || got.Capped {
		t.Fatalf("Preview() = %+v, want Total=0, no matches, not capped", got)
	}
}

// TestPreviewCapsMatchesButReportsTrueTotal is review focus 1: a pattern of
// "*" over more items than PreviewLimit must still report the real count,
// not the length of whatever list the service happened to build. Capped
// is the only signal a caller has that Total is more than what it is
// holding; if it silently equalled len(Matches), a glob that caught the
// whole fleet would look no different from one that caught 200 items on
// purpose.
func TestPreviewCapsMatchesButReportsTrueTotal(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	host := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	const total = items.PreviewLimit + 37
	names := make([]string, total)
	for i := range names {
		names[i] = fmt.Sprintf("item-%d", i)
	}
	store.seedItems(org, host, "disk", names)
	svc := items.NewService(store, fakeCatalogue)

	in := validInput()
	in.Pattern = "*"

	got, err := svc.Preview(ctx, org, in)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if got.Total != total {
		t.Fatalf("Preview() Total = %d, want %d (the true count)", got.Total, total)
	}
	if !got.Capped {
		t.Fatal("Preview() Capped = false, want true")
	}
	if len(got.Matches) != items.PreviewLimit {
		t.Fatalf("Preview() len(Matches) = %d, want %d", len(got.Matches), items.PreviewLimit)
	}
}

// TestPreviewAtExactlyLimitIsNotCapped pins the boundary review focus 1's
// capped case does not: a buggy `Capped := len(Matches) == PreviewLimit`
// would pass the over-the-limit test above (which seeds PreviewLimit+37
// items) but wrongly report Capped at exactly PreviewLimit matches, where
// nothing was actually left out.
func TestPreviewAtExactlyLimitIsNotCapped(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	host := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	names := make([]string, items.PreviewLimit)
	for i := range names {
		names[i] = fmt.Sprintf("item-%d", i)
	}
	store.seedItems(org, host, "disk", names)
	svc := items.NewService(store, fakeCatalogue)

	in := validInput()
	in.Pattern = "*"

	got, err := svc.Preview(ctx, org, in)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if got.Total != items.PreviewLimit {
		t.Fatalf("Preview() Total = %d, want %d", got.Total, items.PreviewLimit)
	}
	if got.Capped {
		t.Fatal("Preview() Capped = true at exactly PreviewLimit matches, want false")
	}
	if len(got.Matches) != items.PreviewLimit {
		t.Fatalf("Preview() len(Matches) = %d, want %d", len(got.Matches), items.PreviewLimit)
	}
}

// TestPreviewHostScopeOnlyLooksAtThatHost pins the controller's decision
// that a host-scoped input previews against that host's items, not the
// whole organization's — a second host's matching item must never appear.
func TestPreviewHostScopeOnlyLooksAtThatHost(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	hostA := uuid.Must(uuid.NewV7())
	hostB := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	store.seedItems(org, hostA, "disk", []string{"/var"})
	store.seedItems(org, hostB, "disk", []string{"/var"})
	svc := items.NewService(store, fakeCatalogue)

	in := validInput()
	in.Scope, in.HostID, in.Pattern = items.ScopeHost, &hostA, "/var"

	got, err := svc.Preview(ctx, org, in)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if got.Total != 1 || len(got.Matches) != 1 {
		t.Fatalf("Preview() = %+v, want exactly the one item on hostA", got)
	}
	if got.Matches[0].HostID != hostA {
		t.Fatalf("Preview() matched host %v, want %v", got.Matches[0].HostID, hostA)
	}
}

// TestPreviewWritesNothing lists decisions before and after a Preview call
// to prove it never stores anything, even on the happy path.
func TestPreviewWritesNothing(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	host := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	store.seedItems(org, host, "disk", []string{"/var"})
	svc := items.NewService(store, fakeCatalogue)

	before, err := svc.ListDecisions(ctx, org)
	if err != nil {
		t.Fatalf("ListDecisions() error = %v", err)
	}

	if _, err := svc.Preview(ctx, org, validInput()); err != nil {
		t.Fatalf("Preview() error = %v", err)
	}

	after, err := svc.ListDecisions(ctx, org)
	if err != nil {
		t.Fatalf("ListDecisions() error = %v", err)
	}
	if len(before) != 0 || len(after) != 0 {
		t.Fatalf("Preview() changed the decision list: before=%d after=%d, want both 0", len(before), len(after))
	}
}

// TestPreviewValidatesInput proves Preview still refuses an input broken
// on a field that decides what it matches (CheckKey here; scope, host id,
// match kind and pattern are exercised by TestCreateDecisionValidation's
// own table, which Preview's validateMatch path shares verbatim).
func TestPreviewValidatesInput(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	svc := items.NewService(newFakeStore(), fakeCatalogue)

	in := validInput()
	in.CheckKey = "nonsense"
	if _, err := svc.Preview(ctx, org, in); !errors.Is(err, items.ErrInvalidInput) {
		t.Fatalf("Preview() error = %v, want ErrInvalidInput", err)
	}
}

// TestPreviewIgnoresVerdictAndNoteValidation is the ruling this fix
// implements: Preview answers what a pattern matches, which Verdict and
// Note cannot change, so an unset verdict and a too-long note — both
// unremarkable while an operator is still shaping the pattern in a GUI —
// must not turn a preview into ErrInvalidInput. CreateDecision still
// refuses both (TestCreateDecisionValidation's "note too long" and
// "unknown verdict" cases).
func TestPreviewIgnoresVerdictAndNoteValidation(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	host := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	store.seedItems(org, host, "disk", []string{"/var/log"}) // validInput's pattern is "/var/*"
	svc := items.NewService(store, fakeCatalogue)

	in := validInput()
	in.Verdict = ""                    // unset: the GUI's state before a radio is picked
	in.Note = strings.Repeat("a", 501) // over buildDecision's 500-rune bound

	got, err := svc.Preview(ctx, org, in)
	if err != nil {
		t.Fatalf("Preview() error = %v, want nil: Verdict/Note must not be validated here", err)
	}
	if got.Total != 1 {
		t.Fatalf("Preview() Total = %d, want 1", got.Total)
	}
}

func TestItemsForHostAndItemsForOrgPassThrough(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	host := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	store.seedItems(org, host, "disk", []string{"/var", "/home"})
	svc := items.NewService(store, fakeCatalogue)

	hostViews, err := svc.ItemsForHost(ctx, org, host)
	if err != nil {
		t.Fatalf("ItemsForHost() error = %v", err)
	}
	if len(hostViews) != 2 {
		t.Fatalf("ItemsForHost() = %d items, want 2", len(hostViews))
	}

	orgViews, err := svc.ItemsForOrg(ctx, org, items.Filter{CheckKey: "disk"})
	if err != nil {
		t.Fatalf("ItemsForOrg() error = %v", err)
	}
	if len(orgViews) != 2 {
		t.Fatalf("ItemsForOrg() = %d items, want 2", len(orgViews))
	}
}
