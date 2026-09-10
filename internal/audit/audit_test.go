package audit_test

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
)

// memStore is a hand-rolled audit.Store, the same fake-store seam
// auth.TokenStore and tenancy.Store use elsewhere in this codebase: an
// in-memory slice that implements List/ListForActor's keyset pagination
// exactly the way store_sql.go's real SQL does (same cursor encoding, same
// ORDER BY at DESC, id DESC semantics), so Recorder can be driven and its
// pagination contract asserted without a database.
type memStore struct {
	events    []audit.Event
	insertErr error
}

func (s *memStore) Insert(_ context.Context, e audit.Event) error {
	if s.insertErr != nil {
		return s.insertErr
	}
	s.events = append(s.events, e)
	return nil
}

func (s *memStore) List(_ context.Context, orgID uuid.UUID, filter audit.Filter, cursor string, limit int) ([]audit.Event, string, error) {
	return s.query(cursor, limit, func(e audit.Event) bool {
		if e.OrgID == nil || *e.OrgID != orgID {
			return false
		}
		return matchesFilter(e, filter, true)
	})
}

func (s *memStore) ListForActor(_ context.Context, actorID uuid.UUID, filter audit.Filter, cursor string, limit int) ([]audit.Event, string, error) {
	return s.query(cursor, limit, func(e audit.Event) bool {
		if e.ActorID == nil || *e.ActorID != actorID {
			return false
		}
		return matchesFilter(e, filter, false)
	})
}

// matchesFilter applies Filter's ObjectType/Since, and (only when List's
// caller passed includeActor — ListForActor's actorID parameter already
// pins the actor, so Filter.ActorID has no effect there, exactly as
// audit.Store's doc comment says) Filter.ActorID.
func matchesFilter(e audit.Event, f audit.Filter, includeActor bool) bool {
	if includeActor && f.ActorID != nil && (e.ActorID == nil || *e.ActorID != *f.ActorID) {
		return false
	}
	if f.ObjectType != "" && e.ObjectType != f.ObjectType {
		return false
	}
	if f.Since != nil && e.At.Before(*f.Since) {
		return false
	}
	return true
}

// query implements the shared keyset-pagination shape both List and
// ListForActor need: sort matching events most-recent-first (At desc, then
// ID desc as the tiebreaker — mirrors ORDER BY at DESC, id DESC in
// store_sql.go/audit.sql), skip everything at or before the cursor row,
// then cut at limit and report a cursor for the next page.
func (s *memStore) query(cursor string, limit int, match func(audit.Event) bool) ([]audit.Event, string, error) {
	cursorAt, cursorID, err := audit.DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}

	var matched []audit.Event
	for _, e := range s.events {
		if match(e) {
			matched = append(matched, e)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].At.Equal(matched[j].At) {
			return matched[i].At.After(matched[j].At)
		}
		return bytes.Compare(matched[i].ID[:], matched[j].ID[:]) > 0
	})

	var page []audit.Event
	for _, e := range matched {
		if cursor != "" && !after(e, cursorAt, cursorID) {
			continue
		}
		page = append(page, e)
	}

	next := ""
	if len(page) > limit {
		next = audit.EncodeCursor(page[limit-1].At, page[limit-1].ID)
		page = page[:limit]
	}
	return page, next, nil
}

// after reports whether e sorts strictly after (at, id) in the At desc, ID
// desc order — i.e. whether e belongs on the page following a cursor
// carrying (at, id). This is the row-constructor comparison
// "(e.At, e.ID) < (at, id)" audit.sql's own cursor condition performs in
// SQL.
func after(e audit.Event, at time.Time, id uuid.UUID) bool {
	if e.At.Before(at) {
		return true
	}
	if e.At.After(at) {
		return false
	}
	return bytes.Compare(e.ID[:], id[:]) < 0
}

// tickingClock returns a clock that advances by one second on every call,
// so a sequence of Record calls produces distinct, strictly increasing At
// values without depending on real wall-clock resolution.
func tickingClock() func() time.Time {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

func TestRecordFillsIDAndAtFromClock(t *testing.T) {
	store := &memStore{}
	want := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	rec := audit.NewRecorder(store, func() time.Time { return want })

	orgID, actorID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	err := rec.Record(context.Background(), audit.Event{
		OrgID: &orgID, ActorID: &actorID, ActorKind: audit.ActorKindUser,
		Action: "PATCH updateOrganization", ObjectType: "organization", ObjectID: orgID.String(), Summary: "renamed",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("store has %d events, want 1", len(store.events))
	}
	got := store.events[0]
	if got.ID == uuid.Nil {
		t.Fatal("Record left ID as the zero UUID")
	}
	if got.ID.Version() != 7 {
		t.Fatalf("Record's ID is version %d, want 7 (UUIDv7)", got.ID.Version())
	}
	if !got.At.Equal(want) {
		t.Fatalf("At = %v, want %v (the injected clock's value)", got.At, want)
	}
}

func TestRecordDefaultsActorKindToSystem(t *testing.T) {
	store := &memStore{}
	rec := audit.NewRecorder(store, time.Now)
	if err := rec.Record(context.Background(), audit.Event{Action: "system sweep", ObjectType: "host", ObjectID: "x"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got := store.events[0].ActorKind; got != audit.ActorKindSystem {
		t.Fatalf("ActorKind = %q, want %q for an event with no ActorKind set", got, audit.ActorKindSystem)
	}
}

func TestRecordRequiresAction(t *testing.T) {
	store := &memStore{}
	rec := audit.NewRecorder(store, time.Now)
	err := rec.Record(context.Background(), audit.Event{ObjectType: "organization", ObjectID: "x"})
	if err == nil {
		t.Fatal("Record with an empty Action returned nil, want an error")
	}
	if len(store.events) != 0 {
		t.Fatal("Record with an empty Action must not reach the store")
	}
}

func TestRecordPropagatesStoreError(t *testing.T) {
	wantErr := errors.New("insert failed")
	store := &memStore{insertErr: wantErr}
	rec := audit.NewRecorder(store, time.Now)
	if err := rec.Record(context.Background(), audit.Event{Action: "a", ObjectType: "x", ObjectID: "y"}); !errors.Is(err, wantErr) {
		t.Fatalf("Record err = %v, want %v", err, wantErr)
	}
}

// TestListPaginatesByCursor is the brief's own scenario: 3 events in one
// org, limit 2 returns the 2 most recent plus a cursor, and following that
// cursor returns exactly the 1 remaining event plus an empty cursor — i.e.
// pagination terminates and never repeats or drops a row.
func TestListPaginatesByCursor(t *testing.T) {
	store := &memStore{}
	clock := tickingClock()
	rec := audit.NewRecorder(store, clock)
	orgID := uuid.Must(uuid.NewV7())

	var ids []uuid.UUID
	for i := 0; i < 3; i++ {
		if err := rec.Record(context.Background(), audit.Event{
			OrgID: &orgID, ActorKind: audit.ActorKindUser,
			Action: "PATCH updateOrganization", ObjectType: "organization", ObjectID: "o",
		}); err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
		ids = append(ids, store.events[len(store.events)-1].ID)
	}
	// Most recent first: event 2 (last recorded), then 1, then 0.
	want := []uuid.UUID{ids[2], ids[1], ids[0]}

	page1, cursor1, err := rec.List(context.Background(), orgID, audit.Filter{}, "", 2)
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(page1) != 2 || page1[0].ID != want[0] || page1[1].ID != want[1] {
		t.Fatalf("page 1 = %+v, want IDs %v", page1, want[:2])
	}
	if cursor1 == "" {
		t.Fatal("page 1's cursor is empty, want a cursor pointing at the second event")
	}

	page2, cursor2, err := rec.List(context.Background(), orgID, audit.Filter{}, cursor1, 2)
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != want[2] {
		t.Fatalf("page 2 = %+v, want exactly [%v]", page2, want[2])
	}
	if cursor2 != "" {
		t.Fatalf("page 2's cursor = %q, want empty (no more events)", cursor2)
	}
}

// TestListPaginationStableWithSharedTimestamp proves pagination does not
// depend on At values being distinct: three events sharing the exact same
// At (e.g. a burst of system-generated events, or a clock with coarse
// resolution) must still page without ever repeating or dropping a row —
// the id tiebreaker (audit.sql's ORDER BY at DESC, id DESC and this
// package's cursor encoding) is what makes that true.
func TestListPaginationStableWithSharedTimestamp(t *testing.T) {
	store := &memStore{}
	at := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rec := audit.NewRecorder(store, func() time.Time { return at })
	orgID := uuid.Must(uuid.NewV7())

	for i := 0; i < 3; i++ {
		if err := rec.Record(context.Background(), audit.Event{OrgID: &orgID, Action: "a", ObjectType: "x", ObjectID: "y"}); err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
	}

	seen := map[uuid.UUID]bool{}
	cursor := ""
	for page := 0; page < 10; page++ {
		items, next, err := rec.List(context.Background(), orgID, audit.Filter{}, cursor, 1)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(items) == 0 {
			if next != "" {
				t.Fatal("empty page carried a non-empty cursor")
			}
			break
		}
		if seen[items[0].ID] {
			t.Fatalf("event %s returned twice across pages — pagination is not stable", items[0].ID)
		}
		seen[items[0].ID] = true
		cursor = next
		if cursor == "" {
			break
		}
	}
	if len(seen) != 3 {
		t.Fatalf("saw %d distinct events across all pages, want 3", len(seen))
	}
}

func TestListForActorSpansOrgs(t *testing.T) {
	store := &memStore{}
	clock := tickingClock()
	rec := audit.NewRecorder(store, clock)
	actorID := uuid.Must(uuid.NewV7())
	orgA, orgB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	other := uuid.Must(uuid.NewV7())

	for _, e := range []audit.Event{
		{OrgID: &orgA, ActorID: &actorID, Action: "a", ObjectType: "x", ObjectID: "1"},
		{OrgID: &orgB, ActorID: &actorID, Action: "a", ObjectType: "x", ObjectID: "2"},
		{OrgID: &orgA, ActorID: &other, Action: "a", ObjectType: "x", ObjectID: "3"}, // someone else's event
	} {
		if err := rec.Record(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}

	items, _, err := rec.ListForActor(context.Background(), actorID, audit.Filter{}, "", 10)
	if err != nil {
		t.Fatalf("ListForActor: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("ListForActor returned %d events, want 2 (one per org, none of the other actor's)", len(items))
	}
	for _, it := range items {
		if it.ActorID == nil || *it.ActorID != actorID {
			t.Fatalf("ListForActor leaked an event not belonging to the actor: %+v", it)
		}
	}
}

func TestListClampsLimit(t *testing.T) {
	store := &memStore{}
	clock := tickingClock()
	rec := audit.NewRecorder(store, clock)
	orgID := uuid.Must(uuid.NewV7())
	for i := 0; i < audit.MaxListLimit+5; i++ {
		if err := rec.Record(context.Background(), audit.Event{OrgID: &orgID, Action: "a", ObjectType: "x", ObjectID: "y"}); err != nil {
			t.Fatal(err)
		}
	}

	items, _, err := rec.List(context.Background(), orgID, audit.Filter{}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != audit.DefaultListLimit {
		t.Fatalf("limit=0 returned %d items, want DefaultListLimit=%d", len(items), audit.DefaultListLimit)
	}

	items, _, err = rec.List(context.Background(), orgID, audit.Filter{}, "", audit.MaxListLimit*10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != audit.MaxListLimit {
		t.Fatalf("an oversized limit returned %d items, want the clamped MaxListLimit=%d", len(items), audit.MaxListLimit)
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	if _, _, err := audit.DecodeCursor("not-a-valid-cursor!!"); !errors.Is(err, audit.ErrInvalidCursor) {
		t.Fatalf("DecodeCursor(garbage) err = %v, want ErrInvalidCursor", err)
	}
}

func TestEncodeDecodeCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC)
	id := uuid.Must(uuid.NewV7())
	gotAt, gotID, err := audit.DecodeCursor(audit.EncodeCursor(at, id))
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if !gotAt.Equal(at) || gotID != id {
		t.Fatalf("round trip = (%v, %v), want (%v, %v)", gotAt, gotID, at, id)
	}
}
