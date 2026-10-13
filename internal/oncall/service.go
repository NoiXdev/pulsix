// Service is the package's own front door: the eight CRUD verbs the API
// handlers call (Create, Update, Get, List, Delete for rotations;
// CreateException, ListExceptions, DeleteException for stand-ins), plus
// Roster, the roster-over-a-range view a rotation's edit screen needs. It
// adds the one decision none of rotation.go or store_sql.go owns on their
// own — minting an id and a timestamp for a new rotation or exception, and
// turning a unique-name collision into ErrNameTaken — and otherwise defers
// to Validate/ValidateException (rotation.go) for shape, to Shifts
// (shifts.go) for the roster arithmetic, and to Store for persistence.
// Modelled on internal/alertroutes/service.go throughout.
package oncall

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// RotationStore is the persistence this service needs — exactly the
// methods it calls, not Store's full surface (Resolve is the follower's
// own, taken on a caller-owned transaction this service never opens).
// Declaring it here, next to Service, is what lets TestRosterReturnsShifts
// exercise Roster against a fake with no database.
type RotationStore interface {
	Create(ctx context.Context, orgID, id uuid.UUID, in Input, now time.Time) (Rotation, error)
	Update(ctx context.Context, orgID, id uuid.UUID, revision int64, in Input, now time.Time) (Rotation, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Rotation, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Rotation, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	CreateException(ctx context.Context, orgID, rotationID, id uuid.UUID, in ExceptionInput, now time.Time) (Exception, error)
	ListExceptions(ctx context.Context, orgID, rotationID uuid.UUID) ([]Exception, error)
	// DeleteException is scoped to rotationID as well as orgID and id: an
	// exception id that exists in this organization but under a different
	// rotation must answer ErrNotFound rather than be deleted anyway (review
	// finding, v0.4c Task 8 fix round 2) — the caller's own path names a
	// rotation, and that id must be load-bearing.
	DeleteException(ctx context.Context, orgID, rotationID, id uuid.UUID) error
	// ChannelNames resolves ids to their current display name, scoped to
	// orgID — Roster's own join (see Roster's doc comment), so the API's
	// GET .../shifts handler never has to join foreign data itself.
	ChannelNames(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error)
}

// Service composes a RotationStore. now and newID are both injected — never
// time.Now or uuid.New called directly — so a test can pin the id and the
// timestamp a Create or CreateException stamps.
type Service struct {
	store RotationStore
	now   func() time.Time
	newID func() uuid.UUID
}

// NewService wires store, now and newID.
func NewService(store RotationStore, now func() time.Time, newID func() uuid.UUID) *Service {
	return &Service{store: store, now: now, newID: newID}
}

// Create validates in, mints an id and stamps the current instant, then
// persists the rotation. A unique-violation on (org_id, name) — the store's
// own CreateRotation — surfaces as ErrNameTaken rather than a bare wrapped
// database error, so the API can map it to a 409 the same way every other
// named resource in this project does.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, in Input) (Rotation, error) {
	if err := Validate(in); err != nil {
		return Rotation{}, err
	}
	r, err := s.store.Create(ctx, orgID, s.newID(), in, s.now())
	if err != nil {
		return Rotation{}, fmt.Errorf("create rotation: %w", err)
	}
	return r, nil
}

// Update validates in and replaces id's row when revision is still current
// (Store.Update's own optimistic lock — ErrRevisionMismatch or ErrNotFound
// otherwise).
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, revision int64, in Input) (Rotation, error) {
	if err := Validate(in); err != nil {
		return Rotation{}, err
	}
	r, err := s.store.Update(ctx, orgID, id, revision, in, s.now())
	if err != nil {
		return Rotation{}, fmt.Errorf("update rotation: %w", err)
	}
	return r, nil
}

// Get returns one rotation.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Rotation, error) {
	return s.store.Get(ctx, orgID, id)
}

// List returns every one of org's rotations.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Rotation, error) {
	return s.store.List(ctx, orgID)
}

// Delete removes a rotation.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.Delete(ctx, orgID, id)
}

// CreateException validates in, mints an id and stamps the current instant,
// then persists the stand-in.
func (s *Service) CreateException(ctx context.Context, orgID, rotationID uuid.UUID, in ExceptionInput) (Exception, error) {
	if err := ValidateException(in); err != nil {
		return Exception{}, err
	}
	e, err := s.store.CreateException(ctx, orgID, rotationID, s.newID(), in, s.now())
	if err != nil {
		return Exception{}, fmt.Errorf("create exception: %w", err)
	}
	return e, nil
}

// ListExceptions returns rotationID's stand-ins.
func (s *Service) ListExceptions(ctx context.Context, orgID, rotationID uuid.UUID) ([]Exception, error) {
	return s.store.ListExceptions(ctx, orgID, rotationID)
}

// DeleteException removes one stand-in, scoped to rotationID as well as
// orgID: an id belonging to a different rotation answers ErrNotFound, the
// same as one that does not exist at all (RotationStore.DeleteException's
// own doc comment).
func (s *Service) DeleteException(ctx context.Context, orgID, rotationID, id uuid.UUID) error {
	return s.store.DeleteException(ctx, orgID, rotationID, id)
}

// RosterShift is one Shift (shifts.go, pure — it knows only which channel
// is on call, never its name) plus that channel's current display name:
// Roster's own return type, the way alerts.View carries HostName beside its
// row (internal/alerts/service.go) rather than Alert growing a field it
// cannot fill on its own. The name is resolved by the store
// (RotationStore.ChannelNames) in the same call Roster already composes
// Get and ListExceptions with, so the API's GET .../shifts handler is left
// with exactly one service call and no foreign data to join itself.
type RosterShift struct {
	Shift
	ChannelName string
}

// Roster answers who is on call over [from, to) for one rotation — the
// stored cycle and its stand-ins read once, then handed to Shifts
// (shifts.go) for the segment arithmetic, then each segment's channel name
// resolved in one more batched read. It is the only verb this service adds
// beyond plain CRUD: a rotation's edit screen (and the roster strip it
// feeds) needs to show who is on call BY NAME, and Shifts itself takes no
// store at all, so something has to compose the three reads with it.
func (s *Service) Roster(ctx context.Context, orgID, rotationID uuid.UUID, from, to time.Time) ([]RosterShift, error) {
	rot, err := s.store.Get(ctx, orgID, rotationID)
	if err != nil {
		return nil, fmt.Errorf("roster: get rotation: %w", err)
	}
	exc, err := s.store.ListExceptions(ctx, orgID, rotationID)
	if err != nil {
		return nil, fmt.Errorf("roster: list exceptions: %w", err)
	}
	shifts, err := Shifts(rot, exc, from, to)
	if err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	names, err := s.store.ChannelNames(ctx, orgID, distinctChannelIDs(shifts))
	if err != nil {
		return nil, fmt.Errorf("roster: channel names: %w", err)
	}
	out := make([]RosterShift, len(shifts))
	for i, sh := range shifts {
		out[i] = RosterShift{Shift: sh, ChannelName: names[sh.ChannelID]}
	}
	return out, nil
}

// distinctChannelIDs collects each shift's channel id once, in first-seen
// order — the batch ChannelNames is asked to resolve, never larger than the
// roster itself needs.
func distinctChannelIDs(shifts []Shift) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(shifts))
	ids := make([]uuid.UUID, 0, len(shifts))
	for _, sh := range shifts {
		if _, ok := seen[sh.ChannelID]; ok {
			continue
		}
		seen[sh.ChannelID] = struct{}{}
		ids = append(ids, sh.ChannelID)
	}
	return ids
}
