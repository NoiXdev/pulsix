package maintenance

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalize(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	host := uuid.Must(uuid.NewV7())
	start, end := now.Add(time.Hour), now.Add(3*time.Hour)
	oneOff := Input{Name: " db upgrade ", TargetKind: TargetHost, TargetID: &host, StartsAt: &start, EndsAt: &end}
	w, err := normalize(oneOff, now)
	if err != nil || w.Name != "db upgrade" || w.Recurring() || !w.StartsAt.Equal(start) {
		t.Fatalf("one-off = %+v, %v", w, err)
	}
	recurring := Input{Name: "nightly", TargetKind: TargetOrg, Cron: "@daily", Duration: "90m", Timezone: "Europe/Berlin"}
	w, err = normalize(recurring, now)
	if err != nil || w.Cron != "@daily" || w.Duration != 90*time.Minute || w.TargetID != nil {
		t.Fatalf("recurring = %+v, %v", w, err)
	}
	past := now.Add(-time.Hour)
	bad := map[string]Input{
		"no name":             {TargetKind: TargetOrg, Cron: "@daily", Duration: "1h", Timezone: "UTC"},
		"long name":           {Name: strings.Repeat("x", 101), TargetKind: TargetOrg, Cron: "@daily", Duration: "1h", Timezone: "UTC"},
		"control name":        {Name: "a\nb", TargetKind: TargetOrg, Cron: "@daily", Duration: "1h", Timezone: "UTC"},
		"long note":           {Name: "n", Note: strings.Repeat("x", 1001), TargetKind: TargetOrg, Cron: "@daily", Duration: "1h", Timezone: "UTC"},
		"host without id":     {Name: "n", TargetKind: TargetHost, Cron: "@daily", Duration: "1h", Timezone: "UTC"},
		"org with id":         {Name: "n", TargetKind: TargetOrg, TargetID: &host, Cron: "@daily", Duration: "1h", Timezone: "UTC"},
		"unknown kind":        {Name: "n", TargetKind: "zone", Cron: "@daily", Duration: "1h", Timezone: "UTC"},
		"neither mode":        {Name: "n", TargetKind: TargetOrg},
		"both modes":          {Name: "n", TargetKind: TargetOrg, StartsAt: &start, EndsAt: &end, Cron: "@daily", Duration: "1h", Timezone: "UTC"},
		"end before start":    {Name: "n", TargetKind: TargetOrg, StartsAt: &start, EndsAt: &past},
		"too long one-off":    {Name: "n", TargetKind: TargetOrg, StartsAt: &start, EndsAt: ptrTime(start.Add(367 * 24 * time.Hour))},
		"duration on one-off": {Name: "n", TargetKind: TargetOrg, StartsAt: &start, EndsAt: &end, Duration: "1h"},
		"bad cron":            {Name: "n", TargetKind: TargetOrg, Cron: "61 * * * *", Duration: "1h", Timezone: "UTC"},
		"short duration":      {Name: "n", TargetKind: TargetOrg, Cron: "@daily", Duration: "30s", Timezone: "UTC"},
		"long duration":       {Name: "n", TargetKind: TargetOrg, Cron: "@daily", Duration: "745h", Timezone: "UTC"},
		"odd duration":        {Name: "n", TargetKind: TargetOrg, Cron: "@daily", Duration: "90m30s", Timezone: "UTC"},
		"no timezone":         {Name: "n", TargetKind: TargetOrg, Cron: "@daily", Duration: "1h"},
		"local timezone":      {Name: "n", TargetKind: TargetOrg, Cron: "@daily", Duration: "1h", Timezone: "Local"},
		"unknown timezone":    {Name: "n", TargetKind: TargetOrg, Cron: "@daily", Duration: "1h", Timezone: "Mars/Olympus"},
		"never fires":         {Name: "n", TargetKind: TargetOrg, Cron: "0 0 31 2 *", Duration: "1h", Timezone: "UTC"},
	}
	for name, in := range bad {
		if _, err := normalize(in, now); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// TestWindowDefaultsToMaintenanceReason covers the migration's own
// guarantee: an input that says nothing about reason stores "maintenance",
// so every window written before reason existed keeps meaning what it
// meant.
func TestWindowDefaultsToMaintenanceReason(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	in := Input{Name: "nightly", TargetKind: TargetOrg, Cron: "@daily", Duration: "1h", Timezone: "UTC"}
	w, err := normalize(in, now)
	if err != nil || w.Reason != ReasonMaintenance {
		t.Fatalf("reason = %q, %v; want %q", w.Reason, err, ReasonMaintenance)
	}
}

// TestWindowKeepsAnExplicitSilenceReason is backlog 4.22. The two tests
// around it cover the default (an empty reason becomes maintenance) and
// the refusal (anything else is ErrInvalidInput) — but nothing drove the
// one arm that matters most, an explicit silence surviving normalize.
//
// The silence tests elsewhere build Window literals directly and never
// reach this function, so deleting ReasonSilence from the switch would
// have sent every silence window to the default branch and been refused
// as an unknown reason, with the suite still green: the refusal test
// would pass for the wrong input, and the default test never names a
// reason at all.
func TestWindowKeepsAnExplicitSilenceReason(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	in := Input{Name: "nightly", TargetKind: TargetOrg, Cron: "@daily", Duration: "1h", Timezone: "UTC", Reason: ReasonSilence}
	w, err := normalize(in, now)
	if err != nil {
		t.Fatalf("normalize with an explicit silence reason: %v", err)
	}
	if w.Reason != ReasonSilence {
		t.Fatalf("reason = %q, want %q — a silence must not be normalized into a maintenance window", w.Reason, ReasonSilence)
	}
}

func TestWindowRejectsAnUnknownReason(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	in := Input{Name: "nightly", TargetKind: TargetOrg, Cron: "@daily", Duration: "1h", Timezone: "UTC", Reason: "snooze"}
	if _, err := normalize(in, now); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

// TestMonitoringGroupWindowNeedsATargetID holds a monitoring-group window
// to the same shape as a host or inventory-group one.
func TestMonitoringGroupWindowNeedsATargetID(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	start, end := now.Add(time.Hour), now.Add(3*time.Hour)
	in := Input{Name: "silence", TargetKind: TargetMonitoringGroup, StartsAt: &start, EndsAt: &end}
	if _, err := normalize(in, now); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	group := uuid.Must(uuid.NewV7())
	in.TargetID = &group
	w, err := normalize(in, now)
	if err != nil || w.TargetKind != TargetMonitoringGroup || w.TargetID == nil || *w.TargetID != group {
		t.Fatalf("w = %+v, %v", w, err)
	}
}
