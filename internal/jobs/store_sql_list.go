package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/paging"
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
	var rows []gen.Job
	if f.Active {
		rows, err = s.listLive(ctx, orgID, f, params, SortCreated, true, 0, limit+1)
	} else {
		rows, err = s.listJobRows(ctx, params, f.HostID)
	}
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

// ListJobsPage implements Store: the default order (newest first) takes the
// index-friendly query, every other key the whitelisted CASE ordering, and the
// live runs (Filter.Active) their own queries over jobs_live_idx.
func (s *SQLStore) ListJobsPage(ctx context.Context, orgID uuid.UUID, f Filter, p paging.Page) ([]Job, int, error) {
	switch p.Sort {
	case SortCreated, SortStatus, SortKind, SortTrigger:
	default:
		return nil, 0, fmt.Errorf("%w: unknown sort key %q", ErrInvalidInput, p.Sort)
	}
	base := gen.ListJobsParams{
		OrgID: orgID, ZoneID: uuidToPg(f.ZoneID), Status: textToPg(statusPtr(f.Status)), Kind: textToPg(kindPtr(f.Kind)),
		TemplateID: uuidToPg(f.TemplateID), RunGroupID: uuidToPg(f.RunGroupID), Trigger: textToPg(f.Trigger), TriggerRef: uuidToPg(f.TriggerRef),
	}
	offset, limit := p.Offset(), p.Size
	var rows []gen.Job
	var total int64
	var err error
	switch {
	case f.Active:
		if rows, err = s.listLive(ctx, orgID, f, base, p.Sort, p.Desc, offset, limit); err == nil {
			total, err = s.q.CountLiveJobs(ctx, gen.CountLiveJobsParams{
				OrgID: orgID, HostID: uuidToPg(f.HostID), ZoneID: base.ZoneID, Status: base.Status, Kind: base.Kind,
				TemplateID: base.TemplateID, RunGroupID: base.RunGroupID, Trigger: base.Trigger, TriggerRef: base.TriggerRef,
			})
		}
	case f.HostID != nil:
		rows, total, err = s.pageForHost(ctx, base, *f.HostID, p)
	default:
		rows, total, err = s.pageAll(ctx, base, p)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs page: %w", err)
	}
	out := make([]Job, len(rows))
	for i, r := range rows {
		out[i] = toJob(r)
	}
	return out, int(total), nil
}

func (s *SQLStore) pageAll(ctx context.Context, b gen.ListJobsParams, p paging.Page) ([]gen.Job, int64, error) {
	offset, limit := int32(p.Offset()), int32(p.Size) //nolint:gosec // the page size is one of paging.Sizes, the offset is capped by paging.MaxPage
	var rows []gen.Job
	var err error
	if p.Sort == SortCreated && p.Desc {
		rows, err = s.q.ListJobsPage(ctx, gen.ListJobsPageParams{
			OrgID: b.OrgID, ZoneID: b.ZoneID, Status: b.Status, Kind: b.Kind, TemplateID: b.TemplateID,
			RunGroupID: b.RunGroupID, Trigger: b.Trigger, TriggerRef: b.TriggerRef, RowOffset: offset, RowLimit: limit,
		})
	} else {
		rows, err = s.q.ListJobsPageSorted(ctx, gen.ListJobsPageSortedParams{
			OrgID: b.OrgID, ZoneID: b.ZoneID, Status: b.Status, Kind: b.Kind, TemplateID: b.TemplateID,
			RunGroupID: b.RunGroupID, Trigger: b.Trigger, TriggerRef: b.TriggerRef,
			SortKey: p.Sort, SortDesc: p.Desc, RowOffset: offset, RowLimit: limit,
		})
	}
	if err != nil {
		return nil, 0, err
	}
	total, err := s.q.CountJobs(ctx, gen.CountJobsParams{
		OrgID: b.OrgID, ZoneID: b.ZoneID, Status: b.Status, Kind: b.Kind, TemplateID: b.TemplateID,
		RunGroupID: b.RunGroupID, Trigger: b.Trigger, TriggerRef: b.TriggerRef,
	})
	return rows, total, err
}

func (s *SQLStore) pageForHost(ctx context.Context, b gen.ListJobsParams, host uuid.UUID, p paging.Page) ([]gen.Job, int64, error) {
	offset, limit := int32(p.Offset()), int32(p.Size) //nolint:gosec // see pageAll
	var rows []gen.Job
	if p.Sort == SortCreated && p.Desc {
		r, err := s.q.ListJobsForHostPage(ctx, gen.ListJobsForHostPageParams{
			OrgID: b.OrgID, HostID: host, ZoneID: b.ZoneID, Status: b.Status, Kind: b.Kind, TemplateID: b.TemplateID,
			RunGroupID: b.RunGroupID, Trigger: b.Trigger, TriggerRef: b.TriggerRef, RowOffset: offset, RowLimit: limit,
		})
		if err != nil {
			return nil, 0, err
		}
		for _, x := range r {
			rows = append(rows, x.Job)
		}
	} else {
		r, err := s.q.ListJobsForHostPageSorted(ctx, gen.ListJobsForHostPageSortedParams{
			OrgID: b.OrgID, HostID: host, ZoneID: b.ZoneID, Status: b.Status, Kind: b.Kind, TemplateID: b.TemplateID,
			RunGroupID: b.RunGroupID, Trigger: b.Trigger, TriggerRef: b.TriggerRef,
			SortKey: p.Sort, SortDesc: p.Desc, RowOffset: offset, RowLimit: limit,
		})
		if err != nil {
			return nil, 0, err
		}
		for _, x := range r {
			rows = append(rows, x.Job)
		}
	}
	total, err := s.q.CountJobsForHost(ctx, gen.CountJobsForHostParams{
		OrgID: b.OrgID, HostID: host, ZoneID: b.ZoneID, Status: b.Status, Kind: b.Kind, TemplateID: b.TemplateID,
		RunGroupID: b.RunGroupID, Trigger: b.Trigger, TriggerRef: b.TriggerRef,
	})
	return rows, total, err
}

// listLive reads the live runs matching f and b's other filters and cursor.
func (s *SQLStore) listLive(ctx context.Context, orgID uuid.UUID, f Filter, b gen.ListJobsParams, sort string, desc bool, offset, limit int) ([]gen.Job, error) {
	return s.q.ListLiveJobs(ctx, gen.ListLiveJobsParams{
		OrgID: orgID, HostID: uuidToPg(f.HostID), ZoneID: b.ZoneID, Status: b.Status, Kind: b.Kind, TemplateID: b.TemplateID,
		RunGroupID: b.RunGroupID, Trigger: b.Trigger, TriggerRef: b.TriggerRef, CursorAt: b.CursorAt, CursorID: b.CursorID,
		SortKey: sort, SortDesc: desc,
		RowOffset: int32(offset), RowLimit: int32(limit), //nolint:gosec // a page offset capped by paging.MaxPage and a clamped limit
	})
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
