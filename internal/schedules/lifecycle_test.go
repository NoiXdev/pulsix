package schedules_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
)

func TestUnknownSchedulesAreNotFound(t *testing.T) {
	w := newWorld(t)
	id := uuid.Must(uuid.NewV7())
	if _, err := w.svc.Get(ctx, w.org, id); !errors.Is(err, schedules.ErrNotFound) {
		t.Errorf("Get: %v", err)
	}
	if _, err := w.svc.Update(ctx, w.org, id, 1, validInput(w.templateID)); !errors.Is(err, schedules.ErrNotFound) {
		t.Errorf("Update: %v", err)
	}
	if _, err := w.svc.Resume(ctx, w.org, id); !errors.Is(err, schedules.ErrNotFound) {
		t.Errorf("Resume: %v", err)
	}
	if _, err := w.svc.Runs(ctx, w.org, id, 10); !errors.Is(err, schedules.ErrNotFound) {
		t.Errorf("Runs: %v", err)
	}
	if _, err := w.svc.RunNow(ctx, w.org, id, w.actor); !errors.Is(err, schedules.ErrNotFound) {
		t.Errorf("RunNow: %v", err)
	}
	if err := w.svc.Delete(ctx, w.org, id); !errors.Is(err, schedules.ErrNotFound) {
		t.Errorf("Delete: %v", err)
	}
}

func TestGetAndDelete(t *testing.T) {
	w := newWorld(t)
	s, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := w.svc.Get(ctx, w.org, s.ID); err != nil || got.ID != s.ID {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := w.svc.Get(ctx, uuid.Must(uuid.NewV7()), s.ID); !errors.Is(err, schedules.ErrNotFound) {
		t.Fatalf("another org's schedule must be invisible: %v", err)
	}
	if err := w.svc.Delete(ctx, w.org, s.ID); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateKeepsAPausedSchedulePaused(t *testing.T) {
	w := newWorld(t)
	s, _ := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	p, _ := w.svc.Pause(ctx, w.org, s.ID)
	up, err := w.svc.Update(ctx, w.org, s.ID, p.Revision, validInput(w.templateID))
	if err != nil || !up.Paused || up.NextRunAt != nil {
		t.Fatalf("update of a paused schedule = %+v, %v", up, err)
	}
	bad := validInput(w.templateID)
	bad.Cron = "nope"
	if _, err := w.svc.Update(ctx, w.org, s.ID, up.Revision, bad); !errors.Is(err, schedules.ErrInvalidInput) {
		t.Fatalf("invalid update: %v", err)
	}
}

func TestTemplateErrorsAreMapped(t *testing.T) {
	ve := &schema.ValidationError{Problems: []schema.Problem{{Path: "region", Message: "is required"}}}
	cases := []struct {
		err  error
		want error
	}{
		{ve, ve},
		{fmt.Errorf("%w: limit too long", templates.ErrInvalidInput), schedules.ErrInvalidInput},
		{errors.New("database down"), nil},
	}
	for _, c := range cases {
		w := newWorld(t)
		w.tmpl.validErr = c.err
		_, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%v → %v, want %v", c.err, err, c.want)
		}
		if c.want == nil && (err == nil || errors.Is(err, schedules.ErrInvalidInput)) {
			t.Errorf("%v → %v, want the error unchanged", c.err, err)
		}
	}
}

func TestRunNowFailureReasons(t *testing.T) {
	cases := map[error]string{
		jobs.ErrHostKeyChanged: "every targeted host has a changed host key",
		jobs.ErrNotFound:       "the job template no longer exists",
		&schema.ValidationError{Problems: []schema.Problem{{Path: "x", Message: "unknown field"}}}: "the schedule's overrides no longer match the template",
		errors.New("boom"): "could not enqueue the run (see the manager log)",
	}
	for cause, want := range cases {
		w := newWorld(t)
		s, _ := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
		w.runs.err = cause
		_, _ = w.svc.RunNow(ctx, w.org, s.ID, w.actor)
		runs, _ := w.svc.Runs(ctx, w.org, s.ID, 1)
		if len(runs) != 1 || runs[0].Reason != want {
			t.Errorf("%v → %+v, want reason %q", cause, runs, want)
		}
	}
}

func TestPreviewDescribesCronMode(t *testing.T) {
	w := newWorld(t)
	p, err := w.svc.Preview(schedules.PreviewInput{Mode: schedules.ModeCron, Cron: "0 */6 * * *", Timezone: "UTC"})
	if err != nil || p.Description != "every 6 h" {
		t.Fatalf("preview = %+v, %v", p, err)
	}
	p, err = w.svc.Preview(schedules.PreviewInput{Mode: schedules.ModeCron, Cron: "0 9-17 * * 1-5", Timezone: "UTC"})
	if err != nil || p.Description != "" {
		t.Fatalf("preview of a non-simple shape = %+v, %v", p, err)
	}
}
