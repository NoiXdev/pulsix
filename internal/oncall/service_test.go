package oncall_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/oncall"
)

// fakeStore implements oncall.RotationStore for TestRosterReturnsShifts,
// which only exercises Get and ListExceptions (through Service.Roster); the
// remaining RotationStore methods return zero values since no other test
// here calls them.
type fakeStore struct {
	rot oncall.Rotation
	exc []oncall.Exception
}

func (f *fakeStore) Create(context.Context, uuid.UUID, uuid.UUID, oncall.Input, time.Time) (oncall.Rotation, error) {
	return oncall.Rotation{}, nil
}

func (f *fakeStore) Update(context.Context, uuid.UUID, uuid.UUID, int64, oncall.Input, time.Time) (oncall.Rotation, error) {
	return oncall.Rotation{}, nil
}

func (f *fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (oncall.Rotation, error) {
	return f.rot, nil
}

func (f *fakeStore) List(context.Context, uuid.UUID) ([]oncall.Rotation, error) {
	return nil, nil
}

func (f *fakeStore) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (f *fakeStore) CreateException(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, oncall.ExceptionInput, time.Time) (oncall.Exception, error) {
	return oncall.Exception{}, nil
}

func (f *fakeStore) ListExceptions(context.Context, uuid.UUID, uuid.UUID) ([]oncall.Exception, error) {
	return f.exc, nil
}

func (f *fakeStore) DeleteException(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func TestRosterReturnsShifts(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	berlin, _ := time.LoadLocation("Europe/Berlin")
	anchor := time.Date(2026, 1, 5, 9, 0, 0, 0, berlin)
	store := &fakeStore{rot: oncall.Rotation{
		ID: uuid.New(), PeriodDays: 7, Anchor: anchor, Timezone: "Europe/Berlin",
		Channels: []uuid.UUID{a, b},
	}}
	svc := oncall.NewService(store, time.Now, uuid.New)

	got, err := svc.Roster(context.Background(), uuid.New(), store.rot.ID,
		anchor, anchor.AddDate(0, 0, 21))
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("Roster over three periods = %d shifts, want 3", len(got))
	}
}

func TestRosterWrapsAGetError(t *testing.T) {
	store := &spyRotationStore{getErr: errors.New("get boom")}
	svc := oncall.NewService(store, time.Now, uuid.New)

	if _, err := svc.Roster(context.Background(), uuid.New(), uuid.New(), time.Now(), time.Now().Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "get boom") {
		t.Fatalf("Roster() error = %v, want it to carry the store's own get error", err)
	}
}

func TestRosterWrapsAListExceptionsError(t *testing.T) {
	store := &spyRotationStore{
		getOut:     oncall.Rotation{PeriodDays: 7, Anchor: time.Now(), Timezone: "UTC", Channels: []uuid.UUID{uuid.New()}},
		listExcErr: errors.New("list exceptions boom"),
	}
	svc := oncall.NewService(store, time.Now, uuid.New)

	if _, err := svc.Roster(context.Background(), uuid.New(), uuid.New(), time.Now(), time.Now().Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "list exceptions boom") {
		t.Fatalf("Roster() error = %v, want it to carry the store's own list-exceptions error", err)
	}
}

// spyRotationStore implements oncall.RotationStore for the Service-level
// CRUD tests below: unlike fakeStore, which only ever backs Get and
// ListExceptions (through Roster), every method here records whether and
// how it was called and returns whatever the test wired it to — the shape a
// test needs to tell "Validate rejected this before the store was ever
// reached" from "the store was reached with the right arguments".
type spyRotationStore struct {
	createCalled          bool
	createOrgID, createID uuid.UUID
	createNow             time.Time
	createOut             oncall.Rotation
	createErr             error

	updateCalled          bool
	updateOrgID, updateID uuid.UUID
	updateRev             int64
	updateErr             error

	getCalled       bool
	getOrgID, getID uuid.UUID
	getOut          oncall.Rotation
	getErr          error

	listCalled bool
	listOrgID  uuid.UUID
	listOut    []oncall.Rotation
	listErr    error

	deleteCalled          bool
	deleteOrgID, deleteID uuid.UUID
	deleteErr             error

	createExcCalled                     bool
	createExcOrgID, createExcRotationID uuid.UUID
	createExcID                         uuid.UUID
	createExcNow                        time.Time
	createExcOut                        oncall.Exception
	createExcErr                        error

	listExcCalled              bool
	listExcOrgID, listExcRotID uuid.UUID
	listExcOut                 []oncall.Exception
	listExcErr                 error

	deleteExcCalled             bool
	deleteExcOrgID, deleteExcID uuid.UUID
	deleteExcErr                error
}

func (s *spyRotationStore) Create(_ context.Context, orgID, id uuid.UUID, _ oncall.Input, now time.Time) (oncall.Rotation, error) {
	s.createCalled = true
	s.createOrgID, s.createID, s.createNow = orgID, id, now
	return s.createOut, s.createErr
}

func (s *spyRotationStore) Update(_ context.Context, orgID, id uuid.UUID, revision int64, _ oncall.Input, _ time.Time) (oncall.Rotation, error) {
	s.updateCalled = true
	s.updateOrgID, s.updateID, s.updateRev = orgID, id, revision
	return oncall.Rotation{}, s.updateErr
}

func (s *spyRotationStore) Get(_ context.Context, orgID, id uuid.UUID) (oncall.Rotation, error) {
	s.getCalled = true
	s.getOrgID, s.getID = orgID, id
	return s.getOut, s.getErr
}

func (s *spyRotationStore) List(_ context.Context, orgID uuid.UUID) ([]oncall.Rotation, error) {
	s.listCalled = true
	s.listOrgID = orgID
	return s.listOut, s.listErr
}

func (s *spyRotationStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	s.deleteCalled = true
	s.deleteOrgID, s.deleteID = orgID, id
	return s.deleteErr
}

func (s *spyRotationStore) CreateException(_ context.Context, orgID, rotationID, id uuid.UUID, _ oncall.ExceptionInput, now time.Time) (oncall.Exception, error) {
	s.createExcCalled = true
	s.createExcOrgID, s.createExcRotationID, s.createExcID, s.createExcNow = orgID, rotationID, id, now
	return s.createExcOut, s.createExcErr
}

func (s *spyRotationStore) ListExceptions(_ context.Context, orgID, rotationID uuid.UUID) ([]oncall.Exception, error) {
	s.listExcCalled = true
	s.listExcOrgID, s.listExcRotID = orgID, rotationID
	return s.listExcOut, s.listExcErr
}

func (s *spyRotationStore) DeleteException(_ context.Context, orgID, id uuid.UUID) error {
	s.deleteExcCalled = true
	s.deleteExcOrgID, s.deleteExcID = orgID, id
	return s.deleteExcErr
}

// validInput is the minimal Input Validate accepts — every test below that
// exercises a store call rather than Validate's own rejection starts from
// it, modifying only what that test is about.
func validInput(ch uuid.UUID) oncall.Input {
	return oncall.Input{
		Name: "db team", PeriodDays: 7, Anchor: time.Now(), Timezone: "UTC",
		Channels: []uuid.UUID{ch},
	}
}

func validExceptionInput(ch uuid.UUID) oncall.ExceptionInput {
	now := time.Now()
	return oncall.ExceptionInput{StartsAt: now, EndsAt: now.Add(time.Hour), ChannelID: ch}
}

func TestServiceCreateRejectsInvalidInputWithoutTouchingTheStore(t *testing.T) {
	store := &spyRotationStore{}
	svc := oncall.NewService(store, time.Now, uuid.New)

	if _, err := svc.Create(context.Background(), uuid.New(), oncall.Input{}); !errors.Is(err, oncall.ErrInvalidInput) {
		t.Fatalf("Create() error = %v, want ErrInvalidInput", err)
	}
	if store.createCalled {
		t.Fatal("Create reached the store despite Validate rejecting the input")
	}
}

func TestServiceCreateMintsIDAndStampsNow(t *testing.T) {
	fixedID := uuid.New()
	fixedNow := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	store := &spyRotationStore{createOut: oncall.Rotation{Name: "db team"}}
	svc := oncall.NewService(store, func() time.Time { return fixedNow }, func() uuid.UUID { return fixedID })
	orgID, ch := uuid.New(), uuid.New()

	got, err := svc.Create(context.Background(), orgID, validInput(ch))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !store.createCalled || store.createOrgID != orgID || store.createID != fixedID || !store.createNow.Equal(fixedNow) {
		t.Fatalf("store.Create called with org=%v id=%v now=%v, want org=%v id=%v now=%v",
			store.createOrgID, store.createID, store.createNow, orgID, fixedID, fixedNow)
	}
	if got.Name != "db team" {
		t.Fatalf("Create() = %+v, want the store's own return value passed through unchanged", got)
	}
}

func TestServiceCreateWrapsAStoreErrorRatherThanSwallowingIt(t *testing.T) {
	store := &spyRotationStore{createErr: oncall.ErrNameTaken}
	svc := oncall.NewService(store, time.Now, uuid.New)

	if _, err := svc.Create(context.Background(), uuid.New(), validInput(uuid.New())); !errors.Is(err, oncall.ErrNameTaken) {
		t.Fatalf("Create() error = %v, want it to still carry ErrNameTaken", err)
	}
}

func TestServiceUpdateRejectsInvalidInputWithoutTouchingTheStore(t *testing.T) {
	store := &spyRotationStore{}
	svc := oncall.NewService(store, time.Now, uuid.New)

	if _, err := svc.Update(context.Background(), uuid.New(), uuid.New(), 1, oncall.Input{}); !errors.Is(err, oncall.ErrInvalidInput) {
		t.Fatalf("Update() error = %v, want ErrInvalidInput", err)
	}
	if store.updateCalled {
		t.Fatal("Update reached the store despite Validate rejecting the input")
	}
}

func TestServiceUpdatePassesRevisionAndWrapsAStoreError(t *testing.T) {
	store := &spyRotationStore{updateErr: oncall.ErrRevisionMismatch}
	svc := oncall.NewService(store, time.Now, uuid.New)
	orgID, id := uuid.New(), uuid.New()

	if _, err := svc.Update(context.Background(), orgID, id, 7, validInput(uuid.New())); !errors.Is(err, oncall.ErrRevisionMismatch) {
		t.Fatalf("Update() error = %v, want it to still carry ErrRevisionMismatch", err)
	}
	if !store.updateCalled || store.updateOrgID != orgID || store.updateID != id || store.updateRev != 7 {
		t.Fatalf("store.Update called with org=%v id=%v rev=%v, want org=%v id=%v rev=7",
			store.updateOrgID, store.updateID, store.updateRev, orgID, id)
	}
}

func TestServiceGetListDeleteDelegateToTheStore(t *testing.T) {
	wantRotation := oncall.Rotation{Name: "db team"}
	store := &spyRotationStore{getOut: wantRotation, listOut: []oncall.Rotation{wantRotation}}
	svc := oncall.NewService(store, time.Now, uuid.New)
	orgID, id := uuid.New(), uuid.New()

	gotRotation, err := svc.Get(context.Background(), orgID, id)
	if err != nil || gotRotation.Name != "db team" || !store.getCalled || store.getOrgID != orgID || store.getID != id {
		t.Fatalf("Get() = %+v, %v; store called=%v org=%v id=%v", gotRotation, err, store.getCalled, store.getOrgID, store.getID)
	}

	gotList, err := svc.List(context.Background(), orgID)
	if err != nil || len(gotList) != 1 || !store.listCalled || store.listOrgID != orgID {
		t.Fatalf("List() = %+v, %v; store called=%v org=%v", gotList, err, store.listCalled, store.listOrgID)
	}

	if err := svc.Delete(context.Background(), orgID, id); err != nil || !store.deleteCalled || store.deleteOrgID != orgID || store.deleteID != id {
		t.Fatalf("Delete() = %v; store called=%v org=%v id=%v", err, store.deleteCalled, store.deleteOrgID, store.deleteID)
	}
}

func TestServiceGetAndListPassAStoreErrorThroughUnwrapped(t *testing.T) {
	boom := errors.New("boom")
	store := &spyRotationStore{getErr: oncall.ErrNotFound, listErr: boom}
	svc := oncall.NewService(store, time.Now, uuid.New)

	if _, err := svc.Get(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, oncall.ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound unwrapped", err)
	}
	if _, err := svc.List(context.Background(), uuid.New()); !errors.Is(err, boom) {
		t.Fatalf("List() error = %v, want the store's own error untouched", err)
	}
}

func TestServiceCreateExceptionRejectsInvalidInputWithoutTouchingTheStore(t *testing.T) {
	store := &spyRotationStore{}
	svc := oncall.NewService(store, time.Now, uuid.New)

	if _, err := svc.CreateException(context.Background(), uuid.New(), uuid.New(), oncall.ExceptionInput{}); !errors.Is(err, oncall.ErrInvalidInput) {
		t.Fatalf("CreateException() error = %v, want ErrInvalidInput", err)
	}
	if store.createExcCalled {
		t.Fatal("CreateException reached the store despite ValidateException rejecting the input")
	}
}

func TestServiceCreateExceptionMintsIDAndStampsNow(t *testing.T) {
	fixedID := uuid.New()
	fixedNow := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	store := &spyRotationStore{createExcOut: oncall.Exception{Note: "cover"}}
	svc := oncall.NewService(store, func() time.Time { return fixedNow }, func() uuid.UUID { return fixedID })
	orgID, rotationID, ch := uuid.New(), uuid.New(), uuid.New()

	got, err := svc.CreateException(context.Background(), orgID, rotationID, validExceptionInput(ch))
	if err != nil {
		t.Fatalf("CreateException: %v", err)
	}
	if !store.createExcCalled || store.createExcOrgID != orgID || store.createExcRotationID != rotationID ||
		store.createExcID != fixedID || !store.createExcNow.Equal(fixedNow) {
		t.Fatalf("store.CreateException called with org=%v rotation=%v id=%v now=%v, want org=%v rotation=%v id=%v now=%v",
			store.createExcOrgID, store.createExcRotationID, store.createExcID, store.createExcNow,
			orgID, rotationID, fixedID, fixedNow)
	}
	if got.Note != "cover" {
		t.Fatalf("CreateException() = %+v, want the store's own return value passed through unchanged", got)
	}
}

func TestServiceListAndDeleteExceptionDelegateToTheStore(t *testing.T) {
	wantExc := oncall.Exception{Note: "cover"}
	store := &spyRotationStore{listExcOut: []oncall.Exception{wantExc}}
	svc := oncall.NewService(store, time.Now, uuid.New)
	orgID, rotationID, id := uuid.New(), uuid.New(), uuid.New()

	gotList, err := svc.ListExceptions(context.Background(), orgID, rotationID)
	if err != nil || len(gotList) != 1 || !store.listExcCalled || store.listExcOrgID != orgID || store.listExcRotID != rotationID {
		t.Fatalf("ListExceptions() = %+v, %v; store called=%v org=%v rotation=%v",
			gotList, err, store.listExcCalled, store.listExcOrgID, store.listExcRotID)
	}

	if err := svc.DeleteException(context.Background(), orgID, id); err != nil || !store.deleteExcCalled ||
		store.deleteExcOrgID != orgID || store.deleteExcID != id {
		t.Fatalf("DeleteException() = %v; store called=%v org=%v id=%v", err, store.deleteExcCalled, store.deleteExcOrgID, store.deleteExcID)
	}
}
