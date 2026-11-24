package assistant_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistant"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func seedOrg(t *testing.T, d *db.DB, slug string) uuid.UUID {
	t.Helper()
	org, err := gen.New(d.Pool()).CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// set applies c through the service; the round-trip tests only need the error.
func set(t *testing.T, svc *assistant.Service, ctx context.Context, orgID uuid.UUID, c assistant.Change) error {
	t.Helper()
	_, _, err := svc.Update(ctx, orgID, c)
	return err
}

func TestSQLStoreDefaultRoundTripAndIsolation(t *testing.T) {
	d := testdb.Open(t)
	svc := assistant.NewService(assistant.NewSQLStore(d))
	a, b := seedOrg(t, d, "asst-a"), seedOrg(t, d, "asst-b")
	ctxA, ctxB := db.WithOrgScope(context.Background(), a), db.WithOrgScope(context.Background(), b)

	if on, err := svc.Enabled(ctxA, a); err != nil || on {
		t.Fatalf("before any write = %v, %v; want off", on, err)
	}
	if err := set(t, svc, ctxA, a, assistant.Change{Enabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if on, err := svc.Enabled(ctxA, a); err != nil || !on {
		t.Fatalf("after turning on = %v, %v", on, err)
	}
	if on, err := svc.Enabled(ctxB, b); err != nil || on {
		t.Fatalf("the other org = %v, %v; want off", on, err)
	}
	// Row-level security: org b's scope cannot see org a's row.
	if on, err := svc.Enabled(ctxB, a); err != nil || on {
		t.Fatalf("org a read inside org b's scope = %v, %v; want off", on, err)
	}
	if err := set(t, svc, ctxA, a, assistant.Change{Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if on, err := svc.Enabled(ctxA, a); err != nil || on {
		t.Fatalf("after turning off = %v, %v", on, err)
	}
}

func TestStartsFollowUps(t *testing.T) {
	d := testdb.Open(t)
	svc := assistant.NewService(assistant.NewSQLStore(d))
	a, b := seedOrg(t, d, "fu-a"), seedOrg(t, d, "fu-b")
	ctxA, ctxB := db.WithOrgScope(context.Background(), a), db.WithOrgScope(context.Background(), b)

	if on, err := svc.StartsFollowUps(ctxA, a); err != nil || on {
		t.Fatalf("absent row = %v, %v; want false", on, err)
	}
	if err := set(t, svc, ctxA, a, assistant.Change{StartsFollowUps: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if on, err := svc.StartsFollowUps(ctxA, a); err != nil || !on {
		t.Fatalf("after setting = %v, %v; want true", on, err)
	}
	if on, err := svc.Enabled(ctxA, a); err != nil || on {
		t.Fatalf("assistant switch after setting follow-ups = %v, %v; want off", on, err)
	}
	if err := set(t, svc, ctxA, a, assistant.Change{Enabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if on, err := svc.StartsFollowUps(ctxA, a); err != nil || !on {
		t.Fatalf("follow-ups after turning access on = %v, %v; want kept true", on, err)
	}
	if on, err := svc.StartsFollowUps(ctxB, b); err != nil || on {
		t.Fatalf("the other org = %v, %v; want false", on, err)
	}
	if on, err := svc.StartsFollowUps(ctxB, a); err != nil || on {
		t.Fatalf("org a read inside org b's scope = %v, %v; want false", on, err)
	}
}

// TestUpdateReportsBeforeAndAfter: the update returns the stored settings it
// started from, an absent row reading as both off, and what it stored.
func TestUpdateReportsBeforeAndAfter(t *testing.T) {
	d := testdb.Open(t)
	svc := assistant.NewService(assistant.NewSQLStore(d))
	a := seedOrg(t, d, "upd-a")
	ctxA := db.WithOrgScope(context.Background(), a)

	before, after, err := svc.Update(ctxA, a, assistant.Change{StartsFollowUps: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if before != (assistant.Settings{}) || after != (assistant.Settings{StartsFollowUps: true}) {
		t.Fatalf("first update = %+v -> %+v", before, after)
	}
	before, after, err = svc.Update(ctxA, a, assistant.Change{Enabled: ptr(true), StartsFollowUps: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if before != (assistant.Settings{StartsFollowUps: true}) || after != (assistant.Settings{Enabled: true}) {
		t.Fatalf("second update = %+v -> %+v", before, after)
	}
	if _, _, err := svc.Update(ctxA, a, assistant.Change{}); !errors.Is(err, assistant.ErrNothingToChange) {
		t.Fatalf("empty change = %v, want ErrNothingToChange", err)
	}
}

// TestConcurrentUpdatesKeepBothChanges: one update changing only access and
// one changing only follow-ups, run at the same time, both land — neither
// writes back the stale value of the field it was not given.
func TestConcurrentUpdatesKeepBothChanges(t *testing.T) {
	d := testdb.Open(t)
	svc := assistant.NewService(assistant.NewSQLStore(d))
	a := seedOrg(t, d, "conc-a")
	ctxA := db.WithOrgScope(context.Background(), a)

	for round := range 20 {
		on := round%2 == 0
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, c := range []assistant.Change{{Enabled: ptr(on)}, {StartsFollowUps: ptr(on)}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, errs[i] = svc.Update(ctxA, a, c)
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
		enabled, err := svc.Enabled(ctxA, a)
		if err != nil {
			t.Fatal(err)
		}
		followUps, err := svc.StartsFollowUps(ctxA, a)
		if err != nil {
			t.Fatal(err)
		}
		if enabled != on || followUps != on {
			t.Fatalf("round %d: access %t, follow-ups %t; want both %t", round, enabled, followUps, on)
		}
	}
}

// TestSQLStoreReadErrorsAreReturned: a failing query surfaces wrapped,
// never as the default off.
func TestSQLStoreReadErrorsAreReturned(t *testing.T) {
	d := testdb.Open(t)
	svc := assistant.NewService(assistant.NewSQLStore(d))
	a := seedOrg(t, d, "err-a")
	ctx, cancel := context.WithCancel(db.WithOrgScope(context.Background(), a))
	cancel()

	if on, err := svc.StartsFollowUps(ctx, a); err == nil || on || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "get follow-up setting") {
		t.Fatalf("StartsFollowUps on a cancelled context = %v, %v; want the wrapped error", on, err)
	}
	if on, err := svc.Enabled(ctx, a); err == nil || on || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "get assistant switch") {
		t.Fatalf("Enabled on a cancelled context = %v, %v; want the wrapped error", on, err)
	}
	if _, _, err := svc.Update(ctx, a, assistant.Change{Enabled: ptr(true)}); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Update on a cancelled context = %v; want the wrapped error", err)
	}
}
