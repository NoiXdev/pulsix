package schedules_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/schedules"
)

var errWrongAAD = errors.New("wrong aad")

func validInput(templateID uuid.UUID) schedules.Input {
	return schedules.Input{TemplateID: templateID, Name: "Nightly OS patching", Mode: schedules.ModeCron, Cron: "0 3 * * 2,4", Timezone: "Europe/Berlin"}
}

func TestCreateValidation(t *testing.T) {
	w := newWorld(t)
	cases := []struct {
		name   string
		mutate func(*schedules.Input)
	}{
		{"empty name", func(in *schedules.Input) { in.Name = "  " }},
		{"long name", func(in *schedules.Input) { in.Name = strings.Repeat("x", 101) }},
		{"control char in name", func(in *schedules.Input) { in.Name = "a\x07b" }},
		{"unknown mode", func(in *schedules.Input) { in.Mode = "rrule" }},
		{"bad cron", func(in *schedules.Input) { in.Cron = "61 * * * *" }},
		{"never fires", func(in *schedules.Input) { in.Cron = "0 0 30 2 *" }},
		{"simple without input", func(in *schedules.Input) { in.Mode, in.Simple = schedules.ModeSimple, nil }},
		{"bad simple", func(in *schedules.Input) {
			in.Mode, in.Simple = schedules.ModeSimple, &cronexpr.Simple{Kind: "every_minutes", Every: 7}
		}},
		{"empty timezone", func(in *schedules.Input) { in.Timezone = "" }},
		{"Local timezone", func(in *schedules.Input) { in.Timezone = "Local" }},
		{"unknown timezone", func(in *schedules.Input) { in.Timezone = "Mars/Olympus" }},
		{"bad overlap", func(in *schedules.Input) { in.Guards.Overlap = "wait" }},
		{"catch-up window too long", func(in *schedules.Input) { in.Guards.CatchUp = schedules.CatchUp{Enabled: true, Window: "25h"} }},
		{"catch-up window too short", func(in *schedules.Input) { in.Guards.CatchUp = schedules.CatchUp{Enabled: true, Window: "30s"} }},
		{"catch-up window garbage", func(in *schedules.Input) { in.Guards.CatchUp = schedules.CatchUp{Window: "soon"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validInput(w.templateID)
			c.mutate(&in)
			if _, err := w.svc.Create(ctx, w.org, w.actor, in); !errors.Is(err, schedules.ErrInvalidInput) {
				t.Fatalf("Create = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestCreateNormalizesDefaults(t *testing.T) {
	w := newWorld(t)
	got, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if err != nil {
		t.Fatal(err)
	}
	if got.Guards.Overlap != schedules.OverlapSkip || got.Guards.CatchUp.Enabled || got.Guards.CatchUp.Window != "1h" {
		t.Fatalf("guards = %+v", got.Guards)
	}
	// 2026-09-18 is a Friday; the next Tue/Thu 03:00 Berlin is Tue 22 Sep.
	if got.NextRunAt == nil || got.NextRunAt.In(w.berlin).Format("2006-01-02 15:04 MST") != "2026-09-22 03:00 CEST" {
		t.Fatalf("next = %v", got.NextRunAt)
	}
	if got.CreatedBy == nil || *got.CreatedBy != w.actor {
		t.Fatalf("created_by = %v", got.CreatedBy)
	}
}

func TestCreateCompilesSimpleMode(t *testing.T) {
	w := newWorld(t)
	in := validInput(w.templateID)
	in.Mode, in.Cron = schedules.ModeSimple, "ignored"
	in.Simple = &cronexpr.Simple{Kind: "every_hours", Every: 6}
	got, err := w.svc.Create(ctx, w.org, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cron != "0 */6 * * *" || got.Simple == nil || got.Simple.Every != 6 {
		t.Fatalf("got %+v", got)
	}
}

func TestPreview(t *testing.T) {
	w := newWorld(t)
	p, err := w.svc.Preview(schedules.PreviewInput{Mode: schedules.ModeSimple, Simple: &cronexpr.Simple{Kind: "weekly", Days: []string{"tue", "thu"}, Time: "03:00"}, Timezone: "Europe/Berlin"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Cron != "0 3 * * 2,4" || p.Description != "Tue, Thu at 03:00" || len(p.Next) != 5 {
		t.Fatalf("preview = %+v", p)
	}
	if _, err := w.svc.Preview(schedules.PreviewInput{Mode: schedules.ModeCron, Cron: "nope", Timezone: "UTC"}); !errors.Is(err, schedules.ErrInvalidInput) {
		t.Fatalf("bad preview = %v", err)
	}
}
