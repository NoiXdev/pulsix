package maintenance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore is the org-scoped window store.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store on *db.DB so every statement runs in the
// request's org scope.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

// columns maps a window onto its nullable columns.
type columns struct {
	hostID, groupID  pgtype.UUID
	startsAt, endsAt pgtype.Timestamptz
	cron, timezone   *string
	duration         *int32
}

func toColumns(w Window) columns {
	var c columns
	switch w.TargetKind {
	case TargetHost:
		c.hostID = db.NullUUID(w.TargetID)
	case TargetGroup:
		c.groupID = db.NullUUID(w.TargetID)
	}
	if w.Recurring() {
		cron, tz := w.Cron, w.Timezone
		secs := int32(w.Duration / time.Second) //nolint:gosec // ≤ 31 days, validated
		c.cron, c.timezone, c.duration = &cron, &tz, &secs
	} else {
		c.startsAt, c.endsAt = db.NullTime(w.StartsAt), db.NullTime(w.EndsAt)
	}
	return c
}

func (st *SQLStore) Create(ctx context.Context, w Window) (Window, error) {
	c := toColumns(w)
	id, err := st.q.CreateMaintenanceWindow(ctx, gen.CreateMaintenanceWindowParams{
		ID: w.ID, OrgID: w.OrgID, Name: w.Name, TargetKind: string(w.TargetKind), HostID: c.hostID, GroupID: c.groupID,
		StartsAt: c.startsAt, EndsAt: c.endsAt, Cron: c.cron, DurationSeconds: c.duration, Timezone: c.timezone,
		Note: w.Note, CreatedBy: db.NullUUID(w.CreatedBy),
	})
	if err := mapWriteErr(err); err != nil {
		return Window{}, fmt.Errorf("create maintenance window: %w", err)
	}
	return st.Get(ctx, w.OrgID, id)
}

func (st *SQLStore) Get(ctx context.Context, orgID, id uuid.UUID) (Window, error) {
	row, err := st.q.GetMaintenanceWindow(ctx, gen.GetMaintenanceWindowParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Window{}, ErrNotFound
	}
	if err != nil {
		return Window{}, fmt.Errorf("get maintenance window: %w", err)
	}
	return toWindow(gen.ListMaintenanceWindowsRow(row)), nil
}

func (st *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Window, error) {
	rows, err := st.q.ListMaintenanceWindows(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list maintenance windows: %w", err)
	}
	out := make([]Window, len(rows))
	for i, r := range rows {
		out[i] = toWindow(r)
	}
	return out, nil
}

func (st *SQLStore) Update(ctx context.Context, w Window, revision int64) (Window, error) {
	c := toColumns(w)
	n, err := st.q.UpdateMaintenanceWindow(ctx, gen.UpdateMaintenanceWindowParams{
		Name: w.Name, TargetKind: string(w.TargetKind), HostID: c.hostID, GroupID: c.groupID,
		StartsAt: c.startsAt, EndsAt: c.endsAt, Cron: c.cron, DurationSeconds: c.duration, Timezone: c.timezone,
		Note: w.Note, OrgID: w.OrgID, ID: w.ID, Revision: revision,
	})
	if err := mapWriteErr(err); err != nil {
		return Window{}, fmt.Errorf("update maintenance window: %w", err)
	}
	if n == 0 {
		if _, gerr := st.Get(ctx, w.OrgID, w.ID); gerr != nil {
			return Window{}, gerr
		}
		return Window{}, ErrRevisionMismatch
	}
	return st.Get(ctx, w.OrgID, w.ID)
}

func (st *SQLStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := st.q.DeleteMaintenanceWindow(ctx, gen.DeleteMaintenanceWindowParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete maintenance window: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// mapWriteErr turns constraint violations into sentinels: a taken name,
// and a target that is not a host or group of the organization.
func mapWriteErr(err error) error {
	switch {
	case err == nil:
		return nil
	case db.IsUniqueViolation(err):
		return ErrNameTaken
	case db.IsForeignKeyViolation(err):
		return fmt.Errorf("%w: the target does not exist", ErrInvalidInput)
	}
	return err
}

func toWindow(r gen.ListMaintenanceWindowsRow) Window {
	w := Window{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, TargetKind: TargetKind(r.TargetKind), TargetName: r.TargetName,
		StartsAt: db.TimePtr(r.StartsAt), EndsAt: db.TimePtr(r.EndsAt), Note: r.Note,
		CreatedBy: db.UUIDPtr(r.CreatedBy), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Revision: r.Revision,
	}
	switch w.TargetKind {
	case TargetHost:
		w.TargetID = db.UUIDPtr(r.HostID)
	case TargetGroup:
		w.TargetID = db.UUIDPtr(r.GroupID)
	}
	if r.Cron != nil {
		w.Cron = *r.Cron
	}
	if r.Timezone != nil {
		w.Timezone = *r.Timezone
	}
	if r.DurationSeconds != nil {
		w.Duration = time.Duration(*r.DurationSeconds) * time.Second
	}
	return w
}
