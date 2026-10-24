package schedules_test

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
)

var ctx = context.Background()

type fakeStore struct {
	mu           sync.Mutex
	byID         map[uuid.UUID]schedules.Schedule
	runs         []schedules.Run
	recordRunErr error
	trimCalls    int
}

func newFakeStore() *fakeStore { return &fakeStore{byID: map[uuid.UUID]schedules.Schedule{}} }

func (f *fakeStore) Create(_ context.Context, s schedules.Schedule) (schedules.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.byID {
		if o.OrgID == s.OrgID && o.Name == s.Name {
			return schedules.Schedule{}, schedules.ErrNameTaken
		}
	}
	s.Revision = 1
	f.byID[s.ID] = s
	return s, nil
}

func (f *fakeStore) Get(_ context.Context, orgID, id uuid.UUID) (schedules.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok || s.OrgID != orgID {
		return schedules.Schedule{}, schedules.ErrNotFound
	}
	return s, nil
}

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID, templateID *uuid.UUID) ([]schedules.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []schedules.Schedule
	for _, s := range f.byID {
		if s.OrgID == orgID && (templateID == nil || s.TemplateID == *templateID) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeStore) Update(_ context.Context, s schedules.Schedule, revision int64) (schedules.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.byID[s.ID]
	if !ok || cur.OrgID != s.OrgID {
		return schedules.Schedule{}, schedules.ErrNotFound
	}
	if cur.Revision != revision {
		return schedules.Schedule{}, schedules.ErrRevisionMismatch
	}
	// The real UpdateSchedule writes template, name, mode, cron,
	// simple, timezone, guards, limit, extra vars and next_run_at —
	// and nothing else — then RETURNING *, so paused, created_by and
	// last_run_at come back from the ROW whatever the caller passed.
	// Echoing the input instead let the service look correct only
	// because it happened to copy those fields in by hand (backlog
	// 2.68).
	s.Paused, s.CreatedBy, s.LastRunAt = cur.Paused, cur.CreatedBy, cur.LastRunAt
	s.Revision = revision + 1
	f.byID[s.ID] = s
	return s, nil
}

func (f *fakeStore) SetPaused(_ context.Context, orgID, id uuid.UUID, paused bool, next *time.Time) (schedules.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok || s.OrgID != orgID {
		return schedules.Schedule{}, schedules.ErrNotFound
	}
	s.Paused, s.NextRunAt, s.Revision = paused, next, s.Revision+1
	f.byID[id] = s
	return s, nil
}

func (f *fakeStore) PauseAll(_ context.Context, orgID uuid.UUID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for id, s := range f.byID {
		if s.OrgID == orgID && !s.Paused {
			s.Paused, s.NextRunAt = true, nil
			f.byID[id] = s
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok || s.OrgID != orgID {
		return schedules.ErrNotFound
	}
	delete(f.byID, id)
	return nil
}

func (f *fakeStore) RecordRun(_ context.Context, r schedules.Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordRunErr != nil {
		return f.recordRunErr
	}
	f.runs = append(f.runs, r)
	return nil
}

// TrimRuns keeps the newest keep runs of the schedule, decided_at then id
// descending — mirroring TrimScheduleRuns' own tie-break (store_sql.go) so
// a test asserting trim survivors against this fake matches the real store.
func (f *fakeStore) TrimRuns(_ context.Context, orgID, scheduleID uuid.UUID, keep int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trimCalls++
	var mine, others []schedules.Run
	for _, r := range f.runs {
		if r.OrgID == orgID && r.ScheduleID == scheduleID {
			mine = append(mine, r)
		} else {
			others = append(others, r)
		}
	}
	sort.Slice(mine, func(i, j int) bool {
		if !mine[i].DecidedAt.Equal(mine[j].DecidedAt) {
			return mine[i].DecidedAt.After(mine[j].DecidedAt)
		}
		return mine[i].ID.String() > mine[j].ID.String()
	})
	if len(mine) > keep {
		mine = mine[:keep]
	}
	kept := make([]schedules.Run, 0, len(others)+len(mine))
	kept = append(kept, others...)
	kept = append(kept, mine...)
	f.runs = kept
	return nil
}

func (f *fakeStore) Runs(_ context.Context, _, scheduleID uuid.UUID, limit int) ([]schedules.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []schedules.Run
	for i := len(f.runs) - 1; i >= 0 && len(out) < limit; i-- {
		if f.runs[i].ScheduleID == scheduleID {
			out = append(out, f.runs[i])
		}
	}
	return out, nil
}

func (f *fakeStore) LatestRuns(_ context.Context, _ uuid.UUID) (map[uuid.UUID]schedules.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uuid.UUID]schedules.Run{}
	for _, r := range f.runs {
		out[r.ScheduleID] = r
	}
	return out, nil
}

func (f *fakeStore) RunsBetween(_ context.Context, orgID uuid.UUID, from, to time.Time) ([]schedules.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []schedules.Run
	for _, r := range f.runs {
		if r.OrgID == orgID && !r.DueAt.Before(from) && r.DueAt.Before(to) {
			out = append(out, r)
		}
	}
	return out, nil
}

// fakeTemplates records the options it was called with; "sealing" wraps a
// value in a marker map so tests can see it happened.
type fakeTemplates struct {
	mu       sync.Mutex
	lastOpt  templates.OverrideOptions
	validErr error
	reveals  int
	// onValidate runs inside ValidateOverrides, so a test can make the
	// real world's delay there — a template read and a seal — visible
	// to the clock (backlog 2.61).
	onValidate func()
}

func (f *fakeTemplates) ValidateOverrides(_ context.Context, _, _ uuid.UUID, in templates.Overrides, opt templates.OverrideOptions) (templates.Overrides, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastOpt = opt
	if f.onValidate != nil {
		f.onValidate()
	}
	if f.validErr != nil {
		return templates.Overrides{}, f.validErr
	}
	out := templates.Overrides{Limit: in.Limit, ExtraVars: map[string]any{}}
	for k, v := range in.ExtraVars {
		if opt.SealAAD != nil {
			v = map[string]any{"sealed-with": string(opt.SealAAD), "v": v}
		}
		out.ExtraVars[k] = v
	}
	if len(out.ExtraVars) == 0 {
		out.ExtraVars = nil
	}
	return out, nil
}

func (f *fakeTemplates) RevealOverrides(_ context.Context, _, _ uuid.UUID, stored templates.Overrides, aad []byte) (templates.Overrides, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reveals++
	out := templates.Overrides{Limit: stored.Limit, ExtraVars: map[string]any{}}
	for k, v := range stored.ExtraVars {
		m := v.(map[string]any)
		if m["sealed-with"] != string(aad) {
			return templates.Overrides{}, errWrongAAD
		}
		out.ExtraVars[k] = m["v"]
	}
	return out, nil
}

type fakeRuns struct {
	mu         sync.Mutex
	requests   []jobs.RunRequest
	active     bool
	activeErr  error
	err        error
	partial    bool
	group      uuid.UUID
	targets    []uuid.UUID
	targetsErr error
	summaries  map[uuid.UUID]jobs.GroupSummary
	// groups models jobs' job_idempotency_keys ledger (migration 00067) at
	// the RunEnqueuer boundary: a request carrying IdempotencyKey gets the
	// SAME group back on every later call with the same key, instead of a
	// fresh one, the way the real store's ReserveRunGroup does. Requests
	// without a key (every test that predates backlog 2.60, and RunNow)
	// keep the single shared f.group every call reused before.
	groups map[string]uuid.UUID
}

func (f *fakeRuns) EnqueueRunWith(_ context.Context, orgID, templateID uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		// partial models a multi-zone enqueue interrupted after some
		// zones already had jobs: the error comes back WITH what was
		// created, which is what the real EnqueueRunWith does since
		// backlog 2.60.
		if f.partial {
			f.requests = append(f.requests, req)
			return []jobs.Job{{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, TemplateID: &templateID, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: req.Trigger}}, f.err
		}
		return nil, f.err
	}
	f.requests = append(f.requests, req)
	group := f.group
	switch {
	case req.IdempotencyKey != "":
		if f.groups == nil {
			f.groups = map[string]uuid.UUID{}
		}
		if g, ok := f.groups[req.IdempotencyKey]; ok {
			group = g
		} else {
			group = uuid.Must(uuid.NewV7())
			f.groups[req.IdempotencyKey] = group
		}
	case f.group == uuid.Nil:
		group = uuid.Must(uuid.NewV7())
		f.group = group
	}
	return []jobs.Job{{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, TemplateID: &templateID, RunGroupID: group, Trigger: req.Trigger}}, nil
}

func (f *fakeRuns) GroupSummaries(_ context.Context, _ uuid.UUID, groupIDs []uuid.UUID) (map[uuid.UUID]jobs.GroupSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uuid.UUID]jobs.GroupSummary{}
	for _, id := range groupIDs {
		if g, ok := f.summaries[id]; ok {
			out[id] = g
		}
	}
	return out, nil
}

func (f *fakeRuns) RunTargets(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.targets, f.targetsErr
}

func (f *fakeRuns) HasActiveRun(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.activeErr != nil {
		return false, f.activeErr
	}
	return f.active, nil
}

// fakeMaintenance reports the hosts of inWindow as in maintenance.
type fakeMaintenance struct {
	inWindow map[uuid.UUID]bool
	err      error
}

func (f *fakeMaintenance) HostsInMaintenance(_ context.Context, _ uuid.UUID, hostIDs []uuid.UUID, _ time.Time) (map[uuid.UUID]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]bool{}
	for _, h := range hostIDs {
		if f.inWindow[h] {
			out[h] = true
		}
	}
	return out, nil
}

type fakeAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (f *fakeAudit) Record(_ context.Context, e audit.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return nil
}
