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
