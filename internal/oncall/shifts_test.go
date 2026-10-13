package oncall_test

import (
	"errors"
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

// The roster range is bounded. GET /orgs/{org}/oncall-rotations/{id}/shifts
// parses from/to straight out of the query string and hands them here, so
// without a cap "from=0001-01-01&to=9999-12-31&period_days=1" walks roughly
// 3.6 million handovers, sorts them, and builds as many segments and DTOs —
// a gigabyte and tens of seconds for one request from anyone holding
// channels.read, against the process that is also the alert follower
// (whole-branch review, Critical #1). internal/calendar's own MaxRange is
// the precedent this follows, down to the ErrInvalidRange-style sentinel so
// the API answers 400 rather than 500.
func TestShiftsRejectsARangeLongerThanMaxRange(t *testing.T) {
	a := uuid.New()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rot := oncall.Rotation{PeriodDays: 1, Anchor: from, Timezone: "UTC", Channels: []uuid.UUID{a}}

	if _, err := oncall.Shifts(rot, nil, from, from.Add(oncall.MaxRange+time.Hour)); !errors.Is(err, oncall.ErrInvalidInput) {
		t.Errorf("one hour past MaxRange = %v, want ErrInvalidInput", err)
	}
	// The extreme the review names, the one an unbounded walk answers with
	// a gigabyte: it must cost nothing at all.
	ancient := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	distant := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	if _, err := oncall.Shifts(rot, nil, ancient, distant); !errors.Is(err, oncall.ErrInvalidInput) {
		t.Errorf("the whole representable range = %v, want ErrInvalidInput", err)
	}
	if _, err := oncall.Shifts(rot, nil, from, from.Add(oncall.MaxRange)); err != nil {
		t.Errorf("exactly MaxRange = %v, want it accepted", err)
	}
}

// An anchor far from the range asked about must cost no more than a near
// one. handoverIndexAt seeds its search with target.Sub(anchor), and a
// Duration saturates at ±292 years: past that the seed stops tracking the
// real distance at all and the correction loop walks the remainder a single
// period at a time — millions of AddDate calls for an anchor in year 1,
// 3.7 s for the fourteen-day roster the GUI itself asks for (whole-branch
// review, Critical #1). Counting whole calendar days instead is exact for
// any pair of instants, so the loop only ever corrects the rounding.
func TestShiftsWithAFarAnchorCostsNoMoreThanANearOne(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	rot := oncall.Rotation{
		PeriodDays: 1,
		Anchor:     time.Date(1, 1, 1, 9, 0, 0, 0, time.UTC),
		Timezone:   "Europe/Berlin",
		Channels:   []uuid.UUID{a, b},
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	start := time.Now()
	got, err := oncall.Shifts(rot, nil, from, from.AddDate(0, 0, 14))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Shifts: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("Shifts() returned nothing for a fourteen-day range")
	}
	// Generous by three orders of magnitude against the arithmetic this
	// pins (microseconds), and still far under the seconds the saturating
	// seed costs.
	if elapsed > time.Second {
		t.Errorf("a fourteen-day roster off an anchor in year 1 took %s, want well under a second", elapsed)
	}
}

// The calendar-day count the search is seeded with has to be right, not
// merely bounded: a rotation anchored centuries back must still name the
// channel the cycle actually reaches at an instant exactly N handovers
// later.
func TestChannelAtCountsWholeCalendarDaysOverCenturies(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	anchor := time.Date(1600, 1, 1, 9, 0, 0, 0, time.UTC)
	rot := oncall.Rotation{PeriodDays: 1, Anchor: anchor, Timezone: "UTC", Channels: []uuid.UUID{a, b, c}}

	// 100 000 days past the anchor, plus an hour so the instant sits
	// inside that turn rather than exactly on its handover.
	at := anchor.AddDate(0, 0, 100_000).Add(time.Hour)
	got, err := oncall.ChannelAt(rot, nil, at)
	if err != nil {
		t.Fatalf("ChannelAt: %v", err)
	}
	if want := []uuid.UUID{a, b, c}[100_000%3]; got != want {
		t.Errorf("ChannelAt 100 000 days past a 17th-century anchor = %v, want %v", got, want)
	}
}
