// SQLStore's tests exercise Create, Get, List, Update and Delete against a
// real database, the same way internal/alertroutes' own store_sql_test.go
// does. seedOrg and seedChannel mirror that file's own fixtures.
package oncall_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/oncall"
)

// seedOrg creates a bare organization with a unique slug, the minimal
// fixture every test below starts from.
func seedOrg(t *testing.T, d *db.DB) uuid.UUID {
	t.Helper()
	slug := "oncall-" + uuid.Must(uuid.NewV7()).String()
	org, err := gen.New(d.Pool()).CreateOrganization(context.Background(), gen.CreateOrganizationParams{
		ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug,
	})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// seedChannel creates a bare webhook notification channel in org — the
// target oncall_rotation_channels' own composite FK needs.
func seedChannel(t *testing.T, d *db.DB, org uuid.UUID) uuid.UUID {
	t.Helper()
	ch, err := gen.New(d.Pool()).CreateNotificationChannel(context.Background(), gen.CreateNotificationChannelParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: "webhook", Name: "ops-" + uuid.Must(uuid.NewV7()).String(),
		Config: []byte(`{}`), BodyJson: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ch.ID
}

func TestStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	chA, chB := seedChannel(t, d, org), seedChannel(t, d, org)
	s := oncall.NewSQLStore(d)
	now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

	in := oncall.Input{
		Name: "db team", PeriodDays: 7, Anchor: now,
		Timezone: "Europe/Berlin", Channels: []uuid.UUID{chA, chB},
	}
	created, err := s.Create(ctx, org, uuid.New(), in, now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The channel ORDER is the cycle order; a round trip that loses it
	// silently reassigns everyone's week.
	if len(got.Channels) != 2 || got.Channels[0] != chA || got.Channels[1] != chB {
		t.Errorf("round trip changed the channel order: %v", got.Channels)
	}
	if _, err := s.Update(ctx, org, created.ID, created.Revision+99, in, now); !errors.Is(err, oncall.ErrRevisionMismatch) {
		t.Errorf("Update with a stale revision = %v, want ErrRevisionMismatch", err)
	}
}

func TestStoreIsolatesOrganizations(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	orgA, orgB := seedOrg(t, d), seedOrg(t, d)
	chA, chB := seedChannel(t, d, orgA), seedChannel(t, d, orgB)
	s := oncall.NewSQLStore(d)
	now := time.Now()

	mk := func(org, ch uuid.UUID, name string) oncall.Rotation {
		t.Helper()
		r, err := s.Create(ctx, org, uuid.New(), oncall.Input{
			Name: name, PeriodDays: 7, Anchor: now, Timezone: "UTC",
			Channels: []uuid.UUID{ch},
		}, now)
		if err != nil {
			t.Fatalf("Create in %v: %v", org, err)
		}
		return r
	}
	ra, rb := mk(orgA, chA, "a"), mk(orgB, chB, "b")

	for _, tc := range []struct{ org, id uuid.UUID }{{orgA, rb.ID}, {orgB, ra.ID}} {
		if _, err := s.Get(ctx, tc.org, tc.id); !errors.Is(err, oncall.ErrNotFound) {
			t.Errorf("Get(%v, the other org's rotation) = %v, want ErrNotFound", tc.org, err)
		}
	}
}

// TestStoreChannelNames pins ChannelNames' own org scope (review finding 1,
// v0.4c Task 8): it resolves a name for a channel belonging to the caller's
// organization, and silently drops an id naming another organization's
// channel rather than leaking its name across the boundary.
func TestStoreChannelNames(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org, foreignOrg := seedOrg(t, d), seedOrg(t, d)
	own, err := gen.New(d.Pool()).CreateNotificationChannel(ctx, gen.CreateNotificationChannelParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: "webhook", Name: "primary",
		Config: []byte(`{}`), BodyJson: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign := seedChannel(t, d, foreignOrg)
	s := oncall.NewSQLStore(d)

	names, err := s.ChannelNames(ctx, org, []uuid.UUID{own.ID, foreign})
	if err != nil {
		t.Fatalf("ChannelNames: %v", err)
	}
	if names[own.ID] != "primary" {
		t.Errorf("ChannelNames[own] = %q, want %q", names[own.ID], "primary")
	}
	if _, ok := names[foreign]; ok {
		t.Error("ChannelNames must not resolve another organization's channel")
	}
}

func TestStoreRejectsAForeignChannel(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	orgA, orgB := seedOrg(t, d), seedOrg(t, d)
	foreign := seedChannel(t, d, orgB)
	s := oncall.NewSQLStore(d)

	if _, err := s.Create(ctx, orgA, uuid.New(), oncall.Input{
		Name: "leak", PeriodDays: 7, Anchor: time.Now(), Timezone: "UTC",
		Channels: []uuid.UUID{foreign},
	}, time.Now()); err == nil {
		t.Fatal("a rotation must not reference another organization's channel")
	}
}

func TestStoreCreateRejectsADuplicateName(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	ch := seedChannel(t, d, org)
	s := oncall.NewSQLStore(d)
	in := oncall.Input{Name: "dup", PeriodDays: 7, Anchor: time.Now(), Timezone: "UTC", Channels: []uuid.UUID{ch}}

	if _, err := s.Create(ctx, org, uuid.New(), in, time.Now()); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := s.Create(ctx, org, uuid.New(), in, time.Now()); !errors.Is(err, oncall.ErrNameTaken) {
		t.Fatalf("second Create with the same name = %v, want ErrNameTaken", err)
	}
}

func TestStoreUpdateOnAMissingRotationIsNotFoundNotRevisionMismatch(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	s := oncall.NewSQLStore(d)

	_, err := s.Update(ctx, org, uuid.New(), 1, oncall.Input{
		Name: "ghost", PeriodDays: 7, Anchor: time.Now(), Timezone: "UTC",
		Channels: []uuid.UUID{seedChannel(t, d, org)},
	}, time.Now())
	if !errors.Is(err, oncall.ErrNotFound) {
		t.Fatalf("Update(missing id) = %v, want ErrNotFound", err)
	}
}

func TestStoreUpdateReplacesChannelsAndRotationFields(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	chOld, chNew1, chNew2 := seedChannel(t, d, org), seedChannel(t, d, org), seedChannel(t, d, org)
	s := oncall.NewSQLStore(d)
	now := time.Now()

	created, err := s.Create(ctx, org, uuid.New(), oncall.Input{
		Name: "before", PeriodDays: 7, Anchor: now, Timezone: "UTC", Channels: []uuid.UUID{chOld},
	}, now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := s.Update(ctx, org, created.ID, created.Revision, oncall.Input{
		Name: "after", PeriodDays: 14, Anchor: now, Timezone: "Europe/Berlin",
		Channels: []uuid.UUID{chNew2, chNew1},
	}, now)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "after" || updated.PeriodDays != 14 || updated.Timezone != "Europe/Berlin" {
		t.Fatalf("Update() = %+v, want the new fields", updated)
	}
	if len(updated.Channels) != 2 || updated.Channels[0] != chNew2 || updated.Channels[1] != chNew1 {
		t.Fatalf("Update() channels = %v, want [chNew2, chNew1] in that order", updated.Channels)
	}
	if updated.Revision != created.Revision+1 {
		t.Fatalf("Update() revision = %d, want %d", updated.Revision, created.Revision+1)
	}

	got, err := s.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if len(got.Channels) != 2 || got.Channels[0] != chNew2 || got.Channels[1] != chNew1 {
		t.Fatalf("Get() after Update = %v, want the replaced channel order to have persisted", got.Channels)
	}
}

func TestStoreList(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	orgA, orgB := seedOrg(t, d), seedOrg(t, d)
	chA, chB := seedChannel(t, d, orgA), seedChannel(t, d, orgB)
	s := oncall.NewSQLStore(d)
	now := time.Now()

	if _, err := s.Create(ctx, orgA, uuid.New(), oncall.Input{
		Name: "a", PeriodDays: 7, Anchor: now, Timezone: "UTC", Channels: []uuid.UUID{chA},
	}, now); err != nil {
		t.Fatalf("Create in orgA: %v", err)
	}
	if _, err := s.Create(ctx, orgB, uuid.New(), oncall.Input{
		Name: "b", PeriodDays: 7, Anchor: now, Timezone: "UTC", Channels: []uuid.UUID{chB},
	}, now); err != nil {
		t.Fatalf("Create in orgB: %v", err)
	}

	list, err := s.List(ctx, orgA)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "a" || len(list[0].Channels) != 1 || list[0].Channels[0] != chA {
		t.Fatalf("List(orgA) = %+v, want exactly orgA's one rotation with its channel", list)
	}
}

func TestStoreDelete(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	ch := seedChannel(t, d, org)
	s := oncall.NewSQLStore(d)

	created, err := s.Create(ctx, org, uuid.New(), oncall.Input{
		Name: "gone-soon", PeriodDays: 7, Anchor: time.Now(), Timezone: "UTC", Channels: []uuid.UUID{ch},
	}, time.Now())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Delete(ctx, org, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, org, created.ID); !errors.Is(err, oncall.ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, org, created.ID); !errors.Is(err, oncall.ErrNotFound) {
		t.Fatalf("Delete an already-deleted rotation = %v, want ErrNotFound", err)
	}
}

func TestStoreExceptionRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	ch := seedChannel(t, d, org)
	s := oncall.NewSQLStore(d)
	now := time.Now()

	rot, err := s.Create(ctx, org, uuid.New(), oncall.Input{
		Name: "with-exceptions", PeriodDays: 7, Anchor: now, Timezone: "UTC", Channels: []uuid.UUID{ch},
	}, now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	exc, err := s.CreateException(ctx, org, rot.ID, uuid.New(), oncall.ExceptionInput{
		StartsAt: now, EndsAt: now.Add(24 * time.Hour), ChannelID: ch, Note: "holiday cover",
	}, now)
	if err != nil {
		t.Fatalf("CreateException: %v", err)
	}
	if exc.RotationID != rot.ID || exc.ChannelID != ch || exc.Note != "holiday cover" {
		t.Fatalf("CreateException() = %+v, want it to carry the given fields", exc)
	}

	list, err := s.ListExceptions(ctx, org, rot.ID)
	if err != nil {
		t.Fatalf("ListExceptions: %v", err)
	}
	if len(list) != 1 || list[0].ID != exc.ID {
		t.Fatalf("ListExceptions() = %+v, want exactly the one exception created above", list)
	}

	if err := s.DeleteException(ctx, org, rot.ID, exc.ID); err != nil {
		t.Fatalf("DeleteException: %v", err)
	}
	list, err = s.ListExceptions(ctx, org, rot.ID)
	if err != nil {
		t.Fatalf("ListExceptions after Delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("ListExceptions() after Delete = %+v, want none", list)
	}
}

// TestStoreDeleteExceptionRejectsAWrongRotation pins the review finding
// (v0.4c Task 8, fix round 2) at the store layer, beneath the API-level
// proof (TestOncallExceptionDeleteRejectsAWrongRotation,
// internal/api/oncall_handlers_test.go): an exception id that exists in this
// organization but under a DIFFERENT rotation must answer ErrNotFound, not
// be deleted anyway, and the row must still be there afterward.
func TestStoreDeleteExceptionRejectsAWrongRotation(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	chA, chB := seedChannel(t, d, org), seedChannel(t, d, org)
	s := oncall.NewSQLStore(d)
	now := time.Now()

	rotA, err := s.Create(ctx, org, uuid.New(), oncall.Input{
		Name: "rotation-a", PeriodDays: 7, Anchor: now, Timezone: "UTC", Channels: []uuid.UUID{chA},
	}, now)
	if err != nil {
		t.Fatalf("Create rotA: %v", err)
	}
	rotB, err := s.Create(ctx, org, uuid.New(), oncall.Input{
		Name: "rotation-b", PeriodDays: 7, Anchor: now, Timezone: "UTC", Channels: []uuid.UUID{chB},
	}, now)
	if err != nil {
		t.Fatalf("Create rotB: %v", err)
	}

	exc, err := s.CreateException(ctx, org, rotA.ID, uuid.New(), oncall.ExceptionInput{
		StartsAt: now, EndsAt: now.Add(24 * time.Hour), ChannelID: chA,
	}, now)
	if err != nil {
		t.Fatalf("CreateException: %v", err)
	}

	if err := s.DeleteException(ctx, org, rotB.ID, exc.ID); !errors.Is(err, oncall.ErrNotFound) {
		t.Errorf("DeleteException through a different rotation = %v, want ErrNotFound", err)
	}
	list, err := s.ListExceptions(ctx, org, rotA.ID)
	if err != nil {
		t.Fatalf("ListExceptions: %v", err)
	}
	if len(list) != 1 || list[0].ID != exc.ID {
		t.Fatalf("the exception must still exist under its own rotation after the wrong-rotation delete: %+v", list)
	}
}

func TestStoreResolveReadsOnTheCallerSTransaction(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	ch := seedChannel(t, d, org)
	s := oncall.NewSQLStore(d)
	now := time.Now()

	rot, err := s.Create(ctx, org, uuid.New(), oncall.Input{
		Name: "resolved", PeriodDays: 7, Anchor: now, Timezone: "UTC", Channels: []uuid.UUID{ch},
	}, now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.CreateException(ctx, org, rot.ID, uuid.New(), oncall.ExceptionInput{
		StartsAt: now, EndsAt: now.Add(time.Hour), ChannelID: ch,
	}, now); err != nil {
		t.Fatalf("CreateException: %v", err)
	}

	tx, err := d.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // test cleanup

	gotRot, gotExc, err := s.Resolve(ctx, tx, org, rot.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotRot.ID != rot.ID || len(gotRot.Channels) != 1 || gotRot.Channels[0] != ch {
		t.Fatalf("Resolve() rotation = %+v, want %v's own channel", gotRot, rot.ID)
	}
	if len(gotExc) != 1 {
		t.Fatalf("Resolve() exceptions = %+v, want exactly the one created above", gotExc)
	}
}
