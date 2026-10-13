// SQLStore is the only part of this package that touches a database: rotation
// and exception CRUD against migration 00054's oncall_rotations,
// oncall_rotation_channels and oncall_exceptions tables. Modelled on
// internal/alertroutes/store_sql.go throughout — how it reaches *db.DB, how
// it maps "no rows" to ErrNotFound and "zero rows updated after a successful
// read" to ErrRevisionMismatch.
//
// Every read and write below goes through gen.New(s.d) (or gen.New(tx) for
// Resolve, the caller-owned transaction the follower runs on), never
// s.d.Pool() directly: a handler-bound request context carries its
// organization's row-level-security scope (db.WithOrgScope), and reaching
// for the bare pool would silently drop that layer even though the
// hand-written org_id predicate every query below carries would still
// narrow the result on its own — exactly the gap alertroutes' own
// store_sql.go doc comment describes. The explicit predicate and the
// composite foreign key migration 00054 puts on oncall_rotation_channels
// are what make TestStoreIsolatesOrganizations and
// TestStoreRejectsAForeignChannel pass under a plain, unscoped context the
// same way alertroutes' own org-guard tests do; RLS is the second,
// production-only layer on top of it.
package oncall

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// Store is the rotation store. The service (this package's own front door)
// builds exactly one of these on the shared *db.DB, the same shape
// alertroutes.Store takes.
type Store struct{ d *db.DB }

// NewSQLStore builds the store on d.
func NewSQLStore(d *db.DB) *Store { return &Store{d: d} }

// toRotation assembles the package's own Rotation from one gen.OncallRotation
// row and its already-ordered channel list.
func toRotation(r gen.OncallRotation, channels []uuid.UUID) Rotation {
	return Rotation{
		ID:         r.ID,
		OrgID:      r.OrgID,
		Name:       r.Name,
		PeriodDays: int(r.PeriodDays),
		Anchor:     r.Anchor,
		Timezone:   r.Timezone,
		Channels:   channels,
		Revision:   r.Revision,
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
	}
}

// toException assembles the package's own Exception from one
// gen.OncallException row.
func toException(e gen.OncallException) Exception {
	return Exception{
		ID:         e.ID,
		OrgID:      e.OrgID,
		RotationID: e.RotationID,
		ChannelID:  e.ChannelID,
		StartsAt:   e.StartsAt,
		EndsAt:     e.EndsAt,
		Note:       e.Note,
		CreatedAt:  e.CreatedAt,
	}
}

// channelsFor reads rotationID's cycle back in cycle order — ListRotationChannels'
// own ORDER BY position is what position exists for in the first place: the
// array index a caller gave InsertRotationChannels, recovered exactly.
func (s *Store) channelsFor(ctx context.Context, q *gen.Queries, orgID, rotationID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.ListRotationChannels(ctx, gen.ListRotationChannelsParams{OrgID: orgID, RotationID: rotationID})
	if err != nil {
		return nil, fmt.Errorf("list rotation channels: %w", err)
	}
	out := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		out[i] = r.ChannelID
	}
	return out, nil
}

// replaceChannels replaces rotationID's whole cycle inside the caller's own
// transaction q runs on, deleting the old list before inserting the new one
// as two separate statements rather than oncall.sql's originally-committed
// single WITH-based ReplaceRotationChannels. That shape combined the DELETE
// and the INSERT as two sub-statements of one WITH clause with the INSERT
// not reading from the DELETE's result, which Postgres documents as running
// against one shared snapshot with the two statements' relative order
// unspecified (queries/oncall.sql's own comment on DeleteRotationChannels
// has the link). oncall_rotation_channels' primary key is
// (rotation_id, position), and a cycle's new list almost always reuses
// position 0 — the same key the old list's position-0 row already holds —
// so this was not a rare interleaving but a reproducible "duplicate key"
// failure on every update that kept any channel at the same position,
// caught by TestStoreUpdateReplacesChannelsAndRotationFields. Two
// statements sent in sequence on this transaction remove the ambiguity: the
// DELETE's index change is complete before the INSERT ever runs. The two
// still commit or roll back together, inside Create's or Update's own
// transaction.
//
// A foreign-key violation can only be InsertRotationChannels' own composite
// key onto notification_channels, so it is reported as ErrInvalidInput
// naming the channel — the same mapping alertroutes.replaceSelections gives
// its own "unknown channel" case.
func (s *Store) replaceChannels(ctx context.Context, q *gen.Queries, orgID, rotationID uuid.UUID, channels []uuid.UUID) error {
	if err := q.DeleteRotationChannels(ctx, gen.DeleteRotationChannelsParams{
		OrgID: orgID, RotationID: rotationID,
	}); err != nil {
		return fmt.Errorf("delete rotation channels: %w", err)
	}
	if err := q.InsertRotationChannels(ctx, gen.InsertRotationChannelsParams{
		OrgID: orgID, RotationID: rotationID, ChannelIds: channels,
	}); err != nil {
		if db.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: a channel does not belong to this organization", ErrInvalidInput)
		}
		return fmt.Errorf("insert rotation channels: %w", err)
	}
	return nil
}

// Create inserts id's row and its whole channel cycle in one transaction: a
// rotation that exists with no channels because the second statement failed
// cannot answer who is on call — exactly the silently-ineffective state this
// feature exists to prevent — so the whole thing commits together or not at
// all.
func (s *Store) Create(ctx context.Context, orgID, id uuid.UUID, in Input, now time.Time) (Rotation, error) {
	tx, err := s.d.Begin(ctx)
	if err != nil {
		return Rotation{}, fmt.Errorf("begin create rotation: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	row, err := q.CreateRotation(ctx, gen.CreateRotationParams{
		ID: id, OrgID: orgID, Name: in.Name, PeriodDays: int32(in.PeriodDays), //nolint:gosec // Validate bounds PeriodDays > 0 well under int32
		Anchor: in.Anchor, Timezone: in.Timezone, CreatedAt: now, UpdatedAt: now,
	})
	if db.IsUniqueViolation(err) {
		return Rotation{}, fmt.Errorf("%w: %s", ErrNameTaken, in.Name)
	}
	if err != nil {
		return Rotation{}, fmt.Errorf("create rotation: %w", err)
	}

	if err := s.replaceChannels(ctx, q, orgID, id, in.Channels); err != nil {
		return Rotation{}, err
	}
	channels, err := s.channelsFor(ctx, q, orgID, id)
	if err != nil {
		return Rotation{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Rotation{}, fmt.Errorf("commit create rotation: %w", err)
	}
	return toRotation(row, channels), nil
}

// Update replaces id's row and its whole channel cycle in one transaction,
// the same all-or-nothing posture Create takes. revision is the If-Match
// style optimistic lock: UpdateRotation is :execrows, not :one with
// RETURNING, the same choice alertroutes.Store.Update makes for the same
// reason — zero rows affected is the one signal this needs, and re-reading
// the row (still inside this transaction) is what tells a stale revision
// (ErrRevisionMismatch) from an id that no longer exists at all
// (ErrNotFound) rather than trusting the statement to distinguish them
// itself.
func (s *Store) Update(ctx context.Context, orgID, id uuid.UUID, revision int64, in Input, now time.Time) (Rotation, error) {
	tx, err := s.d.Begin(ctx)
	if err != nil {
		return Rotation{}, fmt.Errorf("begin update rotation: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	n, err := q.UpdateRotation(ctx, gen.UpdateRotationParams{
		Name: in.Name, PeriodDays: int32(in.PeriodDays), //nolint:gosec // Validate bounds PeriodDays > 0 well under int32
		Anchor: in.Anchor, Timezone: in.Timezone, UpdatedAt: now,
		OrgID: orgID, ID: id, Revision: revision,
	})
	if db.IsUniqueViolation(err) {
		return Rotation{}, fmt.Errorf("%w: %s", ErrNameTaken, in.Name)
	}
	if err != nil {
		return Rotation{}, fmt.Errorf("update rotation: %w", err)
	}
	if n == 0 {
		if _, gerr := q.GetRotation(ctx, gen.GetRotationParams{OrgID: orgID, ID: id}); gerr != nil {
			if errors.Is(gerr, pgx.ErrNoRows) {
				return Rotation{}, ErrNotFound
			}
			return Rotation{}, fmt.Errorf("re-read rotation after conflict: %w", gerr)
		}
		return Rotation{}, ErrRevisionMismatch
	}

	if err := s.replaceChannels(ctx, q, orgID, id, in.Channels); err != nil {
		return Rotation{}, err
	}

	row, err := q.GetRotation(ctx, gen.GetRotationParams{OrgID: orgID, ID: id})
	if err != nil {
		return Rotation{}, fmt.Errorf("re-read updated rotation: %w", err)
	}
	channels, err := s.channelsFor(ctx, q, orgID, id)
	if err != nil {
		return Rotation{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Rotation{}, fmt.Errorf("commit update rotation: %w", err)
	}
	return toRotation(row, channels), nil
}

// Get implements the Store's single-rotation read. ErrNotFound covers both a
// rotation that never existed and one that belongs to another organization —
// the org_id predicate in GetRotation makes the two indistinguishable by
// design, the same posture alertroutes.Store.Get already takes.
func (s *Store) Get(ctx context.Context, orgID, id uuid.UUID) (Rotation, error) {
	q := gen.New(s.d)
	row, err := q.GetRotation(ctx, gen.GetRotationParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Rotation{}, ErrNotFound
	}
	if err != nil {
		return Rotation{}, fmt.Errorf("get rotation: %w", err)
	}
	channels, err := s.channelsFor(ctx, q, orgID, id)
	if err != nil {
		return Rotation{}, err
	}
	return toRotation(row, channels), nil
}

// List returns every one of org's rotations, by name (ListRotations' own
// ORDER BY), each with its own channel cycle attached. There is no
// bulk-by-organization channel query the way alertroutes.selectionsByRoute
// has for its own three selection tables, so this reads each rotation's
// cycle with its own round trip; an organization's rotation count is small
// enough (one GUI page lists them all) that this costs nothing worth a new
// query over.
func (s *Store) List(ctx context.Context, orgID uuid.UUID) ([]Rotation, error) {
	q := gen.New(s.d)
	rows, err := q.ListRotations(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list rotations: %w", err)
	}
	out := make([]Rotation, len(rows))
	for i, r := range rows {
		channels, err := s.channelsFor(ctx, q, orgID, r.ID)
		if err != nil {
			return nil, err
		}
		out[i] = toRotation(r, channels)
	}
	return out, nil
}

// Delete removes id and its channel cycle and exceptions, which cascade off
// oncall_rotations' own id (migration 00054's composite FKs) — DeleteRotation's
// own doc comment says so. ErrNotFound when org_id/id matches nothing.
func (s *Store) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := gen.New(s.d).DeleteRotation(ctx, gen.DeleteRotationParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete rotation: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateException inserts a stand-in. A foreign-key violation can only be
// CreateException's own composite keys onto oncall_rotations (rotationID)
// or notification_channels (in.ChannelID) — the same shape replaceChannels
// maps for the cycle itself — so it is reported as ErrInvalidInput rather
// than a bare wrapped database error.
func (s *Store) CreateException(ctx context.Context, orgID, rotationID, id uuid.UUID, in ExceptionInput, now time.Time) (Exception, error) {
	row, err := gen.New(s.d).CreateException(ctx, gen.CreateExceptionParams{
		ID: id, OrgID: orgID, RotationID: rotationID,
		StartsAt: in.StartsAt, EndsAt: in.EndsAt, ChannelID: in.ChannelID,
		Note: in.Note, CreatedAt: now,
	})
	if db.IsForeignKeyViolation(err) {
		return Exception{}, fmt.Errorf("%w: unknown rotation or channel", ErrInvalidInput)
	}
	if err != nil {
		return Exception{}, fmt.Errorf("create exception: %w", err)
	}
	return toException(row), nil
}

// ListExceptions returns rotationID's stand-ins, earliest start first
// (ListExceptions' own ORDER BY) — the order Shifts' own boundary walk wants
// them in, though Shifts does not depend on it since it sorts its own
// boundaries regardless.
func (s *Store) ListExceptions(ctx context.Context, orgID, rotationID uuid.UUID) ([]Exception, error) {
	rows, err := gen.New(s.d).ListExceptions(ctx, gen.ListExceptionsParams{OrgID: orgID, RotationID: rotationID})
	if err != nil {
		return nil, fmt.Errorf("list exceptions: %w", err)
	}
	out := make([]Exception, len(rows))
	for i, r := range rows {
		out[i] = toException(r)
	}
	return out, nil
}

// DeleteException removes one stand-in by id, scoped to rotationID as well
// as orgID — review finding (v0.4c Task 8, fix round 2): an exception id
// that exists in this organization but belongs to a DIFFERENT rotation must
// answer ErrNotFound, the same as one that does not exist at all, rather
// than being deleted anyway. The API's path names a rotation
// (.../oncall-rotations/{id}/exceptions/{exceptionID}), and this predicate
// is what makes that id load-bearing instead of decorative.
func (s *Store) DeleteException(ctx context.Context, orgID, rotationID, id uuid.UUID) error {
	n, err := gen.New(s.d).DeleteException(ctx, gen.DeleteExceptionParams{OrgID: orgID, RotationID: rotationID, ID: id})
	if err != nil {
		return fmt.Errorf("delete exception: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ChannelNames resolves ids to their current display name, scoped to orgID
// — Service.Roster's own join, composed with Get and ListExceptions so the
// API's GET .../shifts handler never has to stitch a channel's name onto a
// shift itself (ListChannelNames' own doc comment, oncall.sql). An empty
// ids is answered with an empty map and no round trip: Roster always calls
// this with the distinct channel ids its own shifts actually carry, which
// is only ever empty for a range Shifts itself already refuses (from must
// be before to), but a future caller doing the same is not left issuing a
// query that would return nothing anyway.
func (s *Store) ChannelNames(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]string{}, nil
	}
	rows, err := gen.New(s.d).ListChannelNames(ctx, gen.ListChannelNamesParams{OrgID: orgID, ChannelIds: ids})
	if err != nil {
		return nil, fmt.Errorf("list channel names: %w", err)
	}
	out := make(map[uuid.UUID]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Name
	}
	return out, nil
}

// Resolve reads rotationID and its exceptions on the caller's own
// transaction tx, for the follower: a background ticker deciding who is on
// call right now composes this with its other per-tick reads so the whole
// decision is made against one consistent snapshot, the same shape
// alertroutes.Store.ListEnabled takes a caller-owned pgx.Tx for.
func (s *Store) Resolve(ctx context.Context, tx pgx.Tx, orgID, rotationID uuid.UUID) (Rotation, []Exception, error) {
	q := gen.New(tx)
	row, err := q.GetRotation(ctx, gen.GetRotationParams{OrgID: orgID, ID: rotationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Rotation{}, nil, ErrNotFound
	}
	if err != nil {
		return Rotation{}, nil, fmt.Errorf("resolve: get rotation: %w", err)
	}
	channels, err := s.channelsFor(ctx, q, orgID, rotationID)
	if err != nil {
		return Rotation{}, nil, err
	}
	excRows, err := q.ListExceptions(ctx, gen.ListExceptionsParams{OrgID: orgID, RotationID: rotationID})
	if err != nil {
		return Rotation{}, nil, fmt.Errorf("resolve: list exceptions: %w", err)
	}
	exc := make([]Exception, len(excRows))
	for i, r := range excRows {
		exc[i] = toException(r)
	}
	return toRotation(row, channels), exc, nil
}
