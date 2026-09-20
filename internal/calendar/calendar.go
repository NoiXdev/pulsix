// Package calendar lays scheduled runs and maintenance windows out over
// a time range (spec 2026-09-18 §8): future occurrences computed from each
// active schedule's timetable, past ones from the recorded decisions with
// their run's status, and every maintenance window's stretches.
package calendar

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/schedules"
)

// Bounds: a range is at most six weeks (spec §8); an every-minute
// schedule over six weeks would be 60 000 occurrences, so each schedule
// contributes at most maxPerSchedule planned ones and the whole view at
// most maxEntries — Truncated says when either cut applied.
//
// maintenance.Service.OccurrencesBetween caps each window at 500
// occurrences on its own (backlog carry-over): an org with many windows
// could still return an unbounded total, so maxMaintenance bounds the
// whole view the same way maxEntries bounds schedule entries.
//
// MaxRange is an absolute bound on what a caller may ask for, but the SPA
// asks for the six week rows it draws — 42 *calendar* days, built with
// setDate — and 42 calendar days are not a fixed number of hours: a span
// containing an autumn fall-back transition is 1009 h, one containing a
// spring-forward 1007 h (final review, Critical #1). A bare 42x24 h bound
// therefore refused exactly the months an operator in a DST zone needs, so
// the bound carries one hour of slack: enough for the single transition
// six weeks can contain, and short of the 43rd calendar day by 23 h. The
// SPA mirrors this value as MAX_RANGE_MS and clamps its own request
// against it (web/src/features/calendar/calendar.ts), so the two sides
// cannot disagree about what is acceptable.
const (
	MaxRange       = 42*24*time.Hour + dstSlack
	dstSlack       = time.Hour
	maxPerSchedule = 500
	maxEntries     = 5000
	maxMaintenance = 5000
)

// ErrInvalidRange is a from/to pair that is empty, reversed or too long.
var ErrInvalidRange = errors.New("invalid range")

// StatePlanned is an occurrence still ahead; past entries carry their
// decision's outcome instead.
const StatePlanned = "planned"

// ScheduleSource is the slice of schedules.Service the calendar reads.
type ScheduleSource interface {
	List(ctx context.Context, orgID uuid.UUID, templateID *uuid.UUID) ([]schedules.Schedule, map[uuid.UUID]schedules.Run, error)
	RunsBetween(ctx context.Context, orgID uuid.UUID, from, to time.Time) ([]schedules.Run, error)
}

// RunGroups summarises run groups (jobs.Service).
type RunGroups interface {
	GroupSummaries(ctx context.Context, orgID uuid.UUID, groupIDs []uuid.UUID) (map[uuid.UUID]jobs.GroupSummary, error)
}

// Windows lists maintenance windows' stretches (maintenance.Service).
type Windows interface {
	OccurrencesBetween(ctx context.Context, orgID uuid.UUID, from, to time.Time) ([]maintenance.Occurrence, bool, error)
}

// Entry is one schedule occurrence in the range.
type Entry struct {
	ScheduleID   uuid.UUID
	ScheduleName string
	TemplateID   uuid.UUID
	At           time.Time
	// State is StatePlanned or the recorded decision's outcome.
	State      string
	Reason     string
	JobGroupID *uuid.UUID
	JobID      *uuid.UUID
	JobStatus  string
}

// View is the calendar of one range.
type View struct {
	From, To    time.Time
	Entries     []Entry
	Maintenance []maintenance.Occurrence
	Truncated   bool
}

// Service builds calendar views.
type Service struct {
	schedules ScheduleSource
	groups    RunGroups
	windows   Windows
	now       func() time.Time
}

// NewService wires the service; nil now is time.Now.
func NewService(s ScheduleSource, groups RunGroups, windows Windows, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{schedules: s, groups: groups, windows: windows, now: now}
}

// Range returns the calendar of [from, to).
func (s *Service) Range(ctx context.Context, orgID uuid.UUID, from, to time.Time) (View, error) {
	if !to.After(from) || to.Sub(from) > MaxRange {
		return View{}, fmt.Errorf("%w: to must be after from, at most 6 weeks later", ErrInvalidRange)
	}
	now := s.now()
	list, _, err := s.schedules.List(ctx, orgID, nil)
	if err != nil {
		return View{}, err
	}
	byID := make(map[uuid.UUID]schedules.Schedule, len(list))
	for _, sch := range list {
		byID[sch.ID] = sch
	}
	view := View{From: from, To: to}
	if err := s.past(ctx, orgID, from, to, byID, &view); err != nil {
		return View{}, err
	}
	for _, sch := range list {
		s.planned(sch, from, to, now, &view)
	}
	slices.SortFunc(view.Entries, func(a, b Entry) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return strings.Compare(a.ScheduleName, b.ScheduleName)
	})
	if len(view.Entries) > maxEntries {
		view.Entries, view.Truncated = view.Entries[:maxEntries], true
	}
	maintOcc, maintCapped, err := s.windows.OccurrencesBetween(ctx, orgID, from, to)
	if err != nil {
		return View{}, err
	}
	// A single window that hit its own per-window cap truncates the view
	// just as the whole-view cap does (final review, Important #3).
	view.Truncated = view.Truncated || maintCapped
	if len(maintOcc) > maxMaintenance {
		maintOcc, view.Truncated = maintOcc[:maxMaintenance], true
	}
	view.Maintenance = maintOcc
	return view, nil
}

// past adds the recorded decisions due in the range, with their run
// group's first job and status.
func (s *Service) past(ctx context.Context, orgID uuid.UUID, from, to time.Time, byID map[uuid.UUID]schedules.Schedule, view *View) error {
	runs, err := s.schedules.RunsBetween(ctx, orgID, from, to)
	if err != nil {
		return err
	}
	var groups []uuid.UUID
	for _, r := range runs {
		if r.JobGroupID != nil {
			groups = append(groups, *r.JobGroupID)
		}
	}
	summaries, err := s.groups.GroupSummaries(ctx, orgID, groups)
	if err != nil {
		return err
	}
	for _, r := range runs {
		sch, ok := byID[r.ScheduleID]
		if !ok {
			continue
		}
		e := Entry{ScheduleID: sch.ID, ScheduleName: sch.Name, TemplateID: sch.TemplateID, At: r.DueAt, State: string(r.Outcome), Reason: r.Reason, JobGroupID: r.JobGroupID}
		if r.JobGroupID != nil {
			if g, ok := summaries[*r.JobGroupID]; ok {
				first := g.FirstJobID
				e.JobID, e.JobStatus = &first, string(g.Status)
			}
		}
		view.Entries = append(view.Entries, e)
	}
	return nil
}

// planned adds an active schedule's occurrences after now within the
// range. A stored timetable that no longer parses contributes nothing.
func (s *Service) planned(sch schedules.Schedule, from, to, now time.Time, view *View) {
	if sch.Paused {
		return
	}
	expr, err := cronexpr.Parse(sch.Cron)
	if err != nil {
		return
	}
	loc, err := time.LoadLocation(sch.Timezone)
	if err != nil {
		return
	}
	// Occurrences are whole minutes: starting one second before from
	// keeps an occurrence exactly at from.
	after := from.Add(-time.Second)
	if now.After(after) {
		after = now
	}
	n := 0
	for t := expr.Next(after, loc); !t.IsZero() && t.Before(to); t = expr.Next(t, loc) {
		if n == maxPerSchedule {
			view.Truncated = true
			return
		}
		view.Entries = append(view.Entries, Entry{ScheduleID: sch.ID, ScheduleName: sch.Name, TemplateID: sch.TemplateID, At: t, State: StatePlanned})
		n++
	}
}
