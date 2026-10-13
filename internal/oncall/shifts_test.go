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

func TestShiftsMergeCollapsesHandoversThatDoNotChangeTheAnswer(t *testing.T) {
	// A one-channel rotation still hands over on schedule — handoverIndex
	// still produces a boundary at each turn — but the answer never
	// changes, so every one of those boundaries must collapse away rather
	// than being reported as segments a reader would wonder about.
	a := uuid.New()
	berlin, _ := time.LoadLocation("Europe/Berlin")
	anchor := time.Date(2026, 1, 5, 9, 0, 0, 0, berlin)
	rot := oncall.Rotation{PeriodDays: 7, Anchor: anchor, Timezone: "Europe/Berlin",
		Channels: []uuid.UUID{a}}

	from := anchor
	to := anchor.AddDate(0, 0, 21) // three handovers in range, same channel throughout

	got, err := oncall.Shifts(rot, nil, from, to)
	if err != nil {
		t.Fatalf("Shifts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Shifts() = %d segments, want 1 (every handover collapses, nothing ever changes)", len(got))
	}
	if !got[0].StartsAt.Equal(from) || !got[0].EndsAt.Equal(to) {
		t.Errorf("the single segment = %+v, want it to span the whole range %s..%s", got[0], from, to)
	}
	if got[0].ChannelID != a || got[0].StandIn {
		t.Errorf("the single segment = %+v, want the cycle's only channel and no stand-in", got[0])
	}
}

func TestShiftsMarksStandInEvenWhenItEchoesTheCycleChannel(t *testing.T) {
	// Decision: StandIn must come from asking winningException directly,
	// not from comparing channel ids across the boundary — a stand-in
	// naming the SAME channel the cycle would have picked anyway must
	// still split the turn into three and still carry StandIn: true on the
	// middle segment. A channel-comparison implementation would see no
	// change at either edge and merge all three into one, reporting a
	// stand-in as indistinguishable from ordinary cycle time.
	a := uuid.New()
	berlin, _ := time.LoadLocation("Europe/Berlin")
	anchor := time.Date(2026, 1, 5, 9, 0, 0, 0, berlin)
	rot := oncall.Rotation{PeriodDays: 7, Anchor: anchor, Timezone: "Europe/Berlin",
		Channels: []uuid.UUID{a}}

	exc := []oncall.Exception{{
		ID:        uuid.New(),
		StartsAt:  time.Date(2026, 1, 6, 0, 0, 0, 0, berlin),
		EndsAt:    time.Date(2026, 1, 8, 0, 0, 0, 0, berlin),
		ChannelID: a, // same channel the cycle would have answered anyway
	}}
	from := time.Date(2026, 1, 5, 9, 0, 0, 0, berlin)
	to := time.Date(2026, 1, 12, 9, 0, 0, 0, berlin)

	got, err := oncall.Shifts(rot, exc, from, to)
	if err != nil {
		t.Fatalf("Shifts: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Shifts() = %d segments, want 3 (cycle, stand-in, cycle) even though the channel never changes", len(got))
	}
	if got[1].ChannelID != a || !got[1].StandIn {
		t.Errorf("the middle segment = %+v, want channel %v with StandIn true", got[1], a)
	}
	if got[0].StandIn || got[2].StandIn {
		t.Errorf("the segments either side of the stand-in must not be marked as one: got[0]=%+v got[2]=%+v", got[0], got[2])
	}
}
