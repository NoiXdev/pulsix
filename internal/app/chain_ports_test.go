package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/chain"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
)

type fakeSchedules struct {
	sch schedules.Schedule
	err error
}

func (f fakeSchedules) Get(context.Context, uuid.UUID, uuid.UUID) (schedules.Schedule, error) {
	return f.sch, f.err
}

func TestChainScheduleGuardsMaintenanceMode(t *testing.T) {
	boom := errors.New("boom")
	withMode := func(m schedules.Maintenance) schedules.Schedule {
		return schedules.Schedule{Guards: schedules.Guards{Maintenance: m}}
	}
	for _, tc := range []struct {
		name      string
		in        fakeSchedules
		mode      string
		found     bool
		wantError bool
	}{
		{"not found", fakeSchedules{err: schedules.ErrNotFound}, "", false, false},
		{"unset guard reads as empty", fakeSchedules{sch: withMode("")}, "", true, false},
		{"skip", fakeSchedules{sch: withMode(schedules.MaintenanceSkip)}, "skip", true, false},
		{"exclude", fakeSchedules{sch: withMode(schedules.MaintenanceExcludeHosts)}, "exclude_hosts", true, false},
		{"other error", fakeSchedules{err: boom}, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode, found, err := NewChainScheduleGuards(tc.in).MaintenanceMode(context.Background(), uuid.New(), uuid.New())
			if (err != nil) != tc.wantError || mode != tc.mode || found != tc.found {
				t.Fatalf("got (%q, %v, %v), want (%q, %v, error=%v)", mode, found, err, tc.mode, tc.found, tc.wantError)
			}
			if tc.wantError && !errors.Is(err, boom) {
				t.Fatalf("error %v does not wrap the cause", err)
			}
		})
	}
}

type fakeTemplates struct {
	t   templates.Template
	err error
}

func (f fakeTemplates) Get(context.Context, uuid.UUID, uuid.UUID) (templates.Template, error) {
	return f.t, f.err
}

func TestChainTemplateNames(t *testing.T) {
	ctx := context.Background()
	name, err := NewChainTemplateNames(fakeTemplates{t: templates.Template{Name: "build"}}).TemplateName(ctx, uuid.New(), uuid.New())
	if err != nil || name != "build" {
		t.Fatalf("got (%q, %v)", name, err)
	}
	_, err = NewChainTemplateNames(fakeTemplates{err: templates.ErrNotFound}).TemplateName(ctx, uuid.New(), uuid.New())
	if !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("deleted template: %v, want jobs.ErrNotFound", err)
	}
	boom := errors.New("boom")
	_, err = NewChainTemplateNames(fakeTemplates{err: boom}).TemplateName(ctx, uuid.New(), uuid.New())
	if !errors.Is(err, boom) || errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("other error: %v", err)
	}
}

type recordingOutbox struct{ got notify.FollowUpDecision }

func (r *recordingOutbox) FollowUpDecision(_ context.Context, _ pgx.Tx, d notify.FollowUpDecision) error {
	r.got = d
	return nil
}

func TestChainNotifierMapsTheNotice(t *testing.T) {
	for cat, want := range map[string]notify.Category{chain.NoticeSkipped: notify.CategorySkipped, chain.NoticeFailure: notify.CategoryFailure} {
		out := &recordingOutbox{}
		n := chain.SkipNotice{
			OrgID: uuid.New(), ScheduleID: uuid.New(), RowID: uuid.New(), TemplateID: uuid.New(), ParentTemplateID: uuid.New(),
			Category: cat, Reason: "why", DecidedAt: time.Now(),
		}
		if err := NewChainNotifier(out).FollowUpSkipped(context.Background(), nil, n); err != nil {
			t.Fatal(err)
		}
		g := out.got
		if g.OrgID != n.OrgID || g.ScheduleID != n.ScheduleID || g.RowID != n.RowID || g.TemplateID != n.TemplateID ||
			g.ParentTemplateID != n.ParentTemplateID || g.Reason != "why" || !g.DecidedAt.Equal(n.DecidedAt) || g.Category != want {
			t.Fatalf("%s: got %+v from %+v", cat, g, n)
		}
	}
}
