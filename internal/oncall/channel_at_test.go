package oncall_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/oncall"
)

func TestChannelAtCycles(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	// Monday 5 January 2026, 09:00 Berlin — the first handover.
	anchor := time.Date(2026, 1, 5, 9, 0, 0, 0, berlin)
	rot := oncall.Rotation{PeriodDays: 7, Anchor: anchor, Timezone: "Europe/Berlin",
		Channels: []uuid.UUID{a, b, c}}

	tests := []struct {
		name string
		at   time.Time
		want uuid.UUID
	}{
		{"at the anchor itself", anchor, a},
		{"one hour in", anchor.Add(time.Hour), a},
		{"just before the first handover", anchor.AddDate(0, 0, 7).Add(-time.Second), a},
		{"at the second handover", anchor.AddDate(0, 0, 7), b},
		{"third week", anchor.AddDate(0, 0, 14), c},
		{"fourth week wraps", anchor.AddDate(0, 0, 21), a},
		// Review Focus 1. A rotation created today for a cycle that
		// starts next Monday is ordinary, not exotic — and the naive
		// index is negative here, because Go's % keeps the sign.
		{"one period before the anchor", anchor.AddDate(0, 0, -7), c},
		{"two periods before the anchor", anchor.AddDate(0, 0, -14), b},
		{"an instant before the anchor, same period", anchor.Add(-time.Hour), c},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := oncall.ChannelAt(rot, nil, tc.at)
			if err != nil {
				t.Fatalf("ChannelAt: %v", err)
			}
			if got != tc.want {
				t.Errorf("ChannelAt(%s) = %v, want %v", tc.at.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}

func TestChannelAtSurvivesDaylightSaving(t *testing.T) {
	// Review Focus 2, and the whole reason decision 6 exists. Europe/Berlin
	// springs forward on 29 March 2026. With duration arithmetic — adding
	// 7*24h — every handover after that lands an hour late, and the error
	// never comes back. With calendar arithmetic 09:00 stays 09:00.
	a, b := uuid.New(), uuid.New()
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	anchor := time.Date(2026, 3, 23, 9, 0, 0, 0, berlin) // the Monday before
	rot := oncall.Rotation{PeriodDays: 7, Anchor: anchor, Timezone: "Europe/Berlin",
		Channels: []uuid.UUID{a, b}}

	// The handover after the clocks change is Monday 30 March at 09:00
	// LOCAL. One second earlier the previous channel must still hold.
	handover := time.Date(2026, 3, 30, 9, 0, 0, 0, berlin)
	before, err := oncall.ChannelAt(rot, nil, handover.Add(-time.Second))
	if err != nil {
		t.Fatalf("ChannelAt before: %v", err)
	}
	after, err := oncall.ChannelAt(rot, nil, handover)
	if err != nil {
		t.Fatalf("ChannelAt after: %v", err)
	}
	if before != a {
		t.Errorf("one second before the handover = %v, want the first channel", before)
	}
	if after != b {
		t.Errorf("at the handover = %v, want the second channel", after)
	}
}

func TestChannelAtHandoverInASkippedHour(t *testing.T) {
	// Review Focus 2's sharper half: on a spring-forward day 02:30 local
	// does not exist. Go normalizes it forward rather than failing, and
	// the test pins what that means so nobody is surprised by it later.
	a, b := uuid.New(), uuid.New()
	berlin, _ := time.LoadLocation("Europe/Berlin")
	anchor := time.Date(2026, 3, 22, 2, 30, 0, 0, berlin)
	rot := oncall.Rotation{PeriodDays: 7, Anchor: anchor, Timezone: "Europe/Berlin",
		Channels: []uuid.UUID{a, b}}

	// 29 March 02:30 is skipped; the handover therefore happens at the
	// first instant that does exist. Whichever side of it we ask, the
	// answer must be defined and must not panic.
	got, err := oncall.ChannelAt(rot, nil, time.Date(2026, 3, 29, 4, 0, 0, 0, berlin))
	if err != nil {
		t.Fatalf("ChannelAt across a skipped hour: %v", err)
	}
	if got != b {
		t.Errorf("after the skipped-hour handover = %v, want the second channel", got)
	}
}

func TestChannelAtAppliesExceptions(t *testing.T) {
	a, b, stand := uuid.New(), uuid.New(), uuid.New()
	berlin, _ := time.LoadLocation("Europe/Berlin")
	anchor := time.Date(2026, 1, 5, 9, 0, 0, 0, berlin)
	rot := oncall.Rotation{PeriodDays: 7, Anchor: anchor, Timezone: "Europe/Berlin",
		Channels: []uuid.UUID{a, b}}

	exc := []oncall.Exception{{
		ID:        uuid.New(),
		StartsAt:  time.Date(2026, 1, 6, 0, 0, 0, 0, berlin),
		EndsAt:    time.Date(2026, 1, 8, 0, 0, 0, 0, berlin),
		ChannelID: stand,
	}}
	inside := time.Date(2026, 1, 7, 12, 0, 0, 0, berlin)
	outside := time.Date(2026, 1, 9, 12, 0, 0, 0, berlin)

	if got, _ := oncall.ChannelAt(rot, exc, inside); got != stand {
		t.Errorf("inside the stand-in = %v, want the stand-in channel", got)
	}
	if got, _ := oncall.ChannelAt(rot, exc, outside); got != a {
		t.Errorf("outside the stand-in = %v, want the cycle's channel", got)
	}
	// The span is half-open: it ends before its end instant.
	if got, _ := oncall.ChannelAt(rot, exc, exc[0].EndsAt); got != a {
		t.Errorf("at the stand-in's end instant = %v, want the cycle's channel", got)
	}
}

func TestOverlappingExceptionsHaveAStrictTotalOrder(t *testing.T) {
	// Decision 7. Two stand-ins covering one instant must resolve the same
	// way however the slice was read — the discipline v0.4a's Mute applies
	// for the same reason.
	berlin, _ := time.LoadLocation("Europe/Berlin")
	early := oncall.Exception{
		ID:        uuid.MustParse("00000000-0000-0000-0000-0000000000ff"),
		StartsAt:  time.Date(2026, 1, 5, 0, 0, 0, 0, berlin),
		EndsAt:    time.Date(2026, 1, 10, 0, 0, 0, 0, berlin),
		ChannelID: uuid.New(),
	}
	late := oncall.Exception{
		ID:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		StartsAt:  time.Date(2026, 1, 6, 0, 0, 0, 0, berlin),
		EndsAt:    time.Date(2026, 1, 8, 0, 0, 0, 0, berlin),
		ChannelID: uuid.New(),
	}
	rot := oncall.Rotation{PeriodDays: 7, Timezone: "Europe/Berlin",
		Anchor:   time.Date(2026, 1, 5, 9, 0, 0, 0, berlin),
		Channels: []uuid.UUID{uuid.New()}}
	at := time.Date(2026, 1, 7, 12, 0, 0, 0, berlin)

	forward, err := oncall.ChannelAt(rot, []oncall.Exception{early, late}, at)
	if err != nil {
		t.Fatalf("ChannelAt: %v", err)
	}
	backward, err := oncall.ChannelAt(rot, []oncall.Exception{late, early}, at)
	if err != nil {
		t.Fatalf("ChannelAt reversed: %v", err)
	}
	if forward != backward {
		t.Fatalf("the answer depends on slice order: %v vs %v", forward, backward)
	}
	// Later start wins, even though its id sorts lower.
	if forward != late.ChannelID {
		t.Errorf("overlap resolved to %v, want the later-starting stand-in", forward)
	}
}
