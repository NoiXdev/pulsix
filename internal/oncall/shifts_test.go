package oncall_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/oncall"
)

func TestShiftsCoverTheRangeWithoutGaps(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	berlin, _ := time.LoadLocation("Europe/Berlin")
	anchor := time.Date(2026, 1, 5, 9, 0, 0, 0, berlin)
	rot := oncall.Rotation{PeriodDays: 7, Anchor: anchor, Timezone: "Europe/Berlin",
		Channels: []uuid.UUID{a, b}}

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, berlin)
	to := time.Date(2026, 2, 1, 0, 0, 0, 0, berlin)
	got, err := oncall.Shifts(rot, nil, from, to)
	if err != nil {
		t.Fatalf("Shifts: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("Shifts() returned nothing for a month-long range")
	}
	if !got[0].StartsAt.Equal(from) {
		t.Errorf("the first shift starts at %s, want the range's own start", got[0].StartsAt)
	}
	if !got[len(got)-1].EndsAt.Equal(to) {
		t.Errorf("the last shift ends at %s, want the range's own end", got[len(got)-1].EndsAt)
	}
	for i := 1; i < len(got); i++ {
		if !got[i].StartsAt.Equal(got[i-1].EndsAt) {
			t.Errorf("gap or overlap between shift %d and %d: %s vs %s",
				i-1, i, got[i-1].EndsAt, got[i].StartsAt)
		}
		if got[i].ChannelID == got[i-1].ChannelID {
			t.Errorf("shifts %d and %d name the same channel — they should have been one", i-1, i)
		}
	}
}

func TestShiftsSplitAroundAStandIn(t *testing.T) {
	a, stand := uuid.New(), uuid.New()
	berlin, _ := time.LoadLocation("Europe/Berlin")
	anchor := time.Date(2026, 1, 5, 9, 0, 0, 0, berlin)
	rot := oncall.Rotation{PeriodDays: 7, Anchor: anchor, Timezone: "Europe/Berlin",
		Channels: []uuid.UUID{a}}

	exc := []oncall.Exception{{
		ID:        uuid.New(),
		StartsAt:  time.Date(2026, 1, 6, 0, 0, 0, 0, berlin),
		EndsAt:    time.Date(2026, 1, 8, 0, 0, 0, 0, berlin),
		ChannelID: stand,
	}}
	from := time.Date(2026, 1, 5, 9, 0, 0, 0, berlin)
	to := time.Date(2026, 1, 12, 9, 0, 0, 0, berlin)

	got, err := oncall.Shifts(rot, exc, from, to)
	if err != nil {
		t.Fatalf("Shifts: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Shifts() = %d segments, want 3 (cycle, stand-in, cycle)", len(got))
	}
	if got[1].ChannelID != stand || !got[1].StandIn {
		t.Errorf("the middle segment = %+v, want the stand-in marked as one", got[1])
	}
	if got[0].ChannelID != a || got[2].ChannelID != a {
		t.Error("the segments either side of a stand-in must return to the cycle")
	}
}
