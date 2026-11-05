package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// ListJobs pages the organization's runs, newest first. A host filter is
// answered by ListJobsForHost, which reads the job_hosts links (backlog
// 7.84); every other combination of filters by ListJobs. Both return jobs
// rows in the same (created_at, id) order, so one cursor and one mapper
// serve both.
func (s *SQLStore) ListJobs(ctx context.Context, orgID uuid.UUID, f Filter, cursor string, limit int) ([]Job, string, error) {
	cursorAt, cursorID, err := DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	params := gen.ListJobsParams{
		OrgID:      orgID,
		ZoneID:     uuidToPg(f.ZoneID),
		Status:     textToPg(statusPtr(f.Status)),
		Kind:       textToPg(kindPtr(f.Kind)),
		TemplateID: uuidToPg(f.TemplateID),
		RunGroupID: uuidToPg(f.RunGroupID),
		Trigger:    textToPg(f.Trigger),
		TriggerRef: uuidToPg(f.TriggerRef),
		CursorAt:   timeToPg(cursor, cursorAt),
		CursorID:   uuidToPg(cursorIDPtr(cursor, cursorID)),
		RowLimit:   int32(limit + 1), //nolint:gosec // limit is clamped to MaxListLimit by Service.List before it ever reaches here
	}
	rows, err := s.listJobRows(ctx, params, f.HostID)
	if err != nil {
		return nil, "", fmt.Errorf("list jobs: %w", err)
	}
	out := make([]Job, len(rows))
	for i, r := range rows {
		out[i] = toJob(r)
	}
	next := ""
	if len(out) > limit {
		last := out[limit-1]
		next = EncodeCursor(last.CreatedAt, last.ID)
		out = out[:limit]
	}
	return out, next, nil
}

// listJobRows runs ListJobs, or ListJobsForHost with the same filters when
// a host is given.
func (s *SQLStore) listJobRows(ctx context.Context, p gen.ListJobsParams, hostID *uuid.UUID) ([]gen.Job, error) {
	if hostID == nil {
		return s.q.ListJobs(ctx, p)
	}
	rows, err := s.q.ListJobsForHost(ctx, gen.ListJobsForHostParams{
		OrgID: p.OrgID, HostID: *hostID,
		ZoneID: p.ZoneID, Status: p.Status, Kind: p.Kind, TemplateID: p.TemplateID,
		RunGroupID: p.RunGroupID, Trigger: p.Trigger, TriggerRef: p.TriggerRef,
		CursorAt: p.CursorAt, CursorID: p.CursorID, RowLimit: p.RowLimit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]gen.Job, len(rows))
	for i, r := range rows {
		out[i] = r.Job
	}
	return out, nil
}

// pgtype helpers for the optional ListJobs filters and cursor, mirroring
// hosts.SQLStore's cursorToPg shape.
func uuidToPg(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func textToPg(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

func timeToPg(cursor string, at time.Time) pgtype.Timestamptz {
	if cursor == "" {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: at, Valid: true}
}

func cursorIDPtr(cursor string, id uuid.UUID) *uuid.UUID {
	if cursor == "" {
		return nil
	}
	return &id
}

func statusPtr(s *Status) *string {
	if s == nil {
		return nil
	}
	v := string(*s)
	return &v
}

func kindPtr(k *Kind) *string {
	if k == nil {
		return nil
	}
	v := string(*k)
	return &v
}
