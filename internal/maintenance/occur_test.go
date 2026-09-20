package maintenance_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/maintenance"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr(t time.Time) *time.Time { return &t }

// nightly is 02:00-04:00 Europe/Berlin every day.
func nightly() maintenance.Window {
	return maintenance.Window{ID: uuid.Must(uuid.NewV7()), Name: "nightly", TargetKind: maintenance.TargetOrg,
		Cron: "0 2 * * *", Duration: 2 * time.Hour, Timezone: "Europe/Berlin"}
}

func TestActiveAtOneOff(t *testing.T) {
	w := maintenance.Window{StartsAt: ptr(at("2026-09-22T10:00:00Z")), EndsAt: ptr(at("2026-09-22T12:00:00Z"))}
	cases := map[string]bool{
		"2026-09-22T09:59:59Z": false,
		"2026-09-22T10:00:00Z": true, // start is inside
		"2026-09-22T11:59:59Z": true,
		"2026-09-22T12:00:00Z": false, // end is outside
	}
	for instant, want := range cases {
		if got, err := maintenance.ActiveAt(w, at(instant)); err != nil || got != want {
			t.Errorf("ActiveAt(%s) = %v, %v; want %v", instant, got, err, want)
		}
	}
	if _, err := maintenance.ActiveAt(maintenance.Window{}, at("2026-09-22T10:00:00Z")); err == nil {
		t.Fatal("a one-off window without bounds evaluated")
	}
}

func TestActiveAtRecurringAcrossDST(t *testing.T) {
	w := nightly()
	cases := map[string]bool{
		// Summer time: 02:00 CEST is 00:00 UTC.
		"2026-09-22T00:00:00Z": true,
		"2026-09-22T01:59:59Z": true,
		"2026-09-22T02:00:00Z": false,
		"2026-09-21T23:59:59Z": false,
		// Winter time: 02:00 CET is 01:00 UTC.
		"2026-11-10T00:30:00Z": false,
		"2026-11-10T01:00:00Z": true,
		"2026-11-10T02:59:00Z": true,
	}
	for instant, want := range cases {
		if got, err := maintenance.ActiveAt(w, at(instant)); err != nil || got != want {
			t.Errorf("ActiveAt(%s) = %v, %v; want %v", instant, got, err, want)
		}
	}
	w.Cron = "not cron"
	if _, err := maintenance.ActiveAt(w, at("2026-09-22T00:00:00Z")); err == nil {
		t.Fatal("a broken expression evaluated")
	}
	w.Cron, w.Timezone = "0 2 * * *", "Mars/Olympus"
	if _, err := maintenance.ActiveAt(w, at("2026-09-22T00:00:00Z")); err == nil {
		t.Fatal("an unknown zone evaluated")
	}
}

func TestOccurrences(t *testing.T) {
	w := nightly()
	// A range that starts inside an occurrence includes that occurrence.
	got, _, err := maintenance.Occurrences(w, at("2026-09-22T01:00:00Z"), at("2026-09-25T00:00:00Z"), 100)
	if err != nil || len(got) != 3 {
		t.Fatalf("occurrences = %+v, %v", got, err)
	}
	if !got[0].Start.Equal(at("2026-09-22T00:00:00Z")) || !got[0].End.Equal(at("2026-09-22T02:00:00Z")) || got[0].Name != "nightly" {
		t.Fatalf("first = %+v", got[0])
	}
	if capped, _, _ := maintenance.Occurrences(w, at("2026-09-01T00:00:00Z"), at("2026-10-01T00:00:00Z"), 5); len(capped) != 5 {
		t.Fatalf("capped = %d", len(capped))
	}
	oneOff := maintenance.Window{Name: "db upgrade", StartsAt: ptr(at("2026-09-23T20:00:00Z")), EndsAt: ptr(at("2026-09-24T02:00:00Z"))}
	if in, _, _ := maintenance.Occurrences(oneOff, at("2026-09-24T00:00:00Z"), at("2026-09-25T00:00:00Z"), 5); len(in) != 1 {
		t.Fatalf("overlapping one-off = %+v", in)
	}
	if out, _, _ := maintenance.Occurrences(oneOff, at("2026-09-24T02:00:00Z"), at("2026-09-25T00:00:00Z"), 5); len(out) != 0 {
		t.Fatalf("one-off ending at the range start = %+v", out)
	}
	if _, _, err := maintenance.Occurrences(maintenance.Window{}, at("2026-09-24T00:00:00Z"), at("2026-09-25T00:00:00Z"), 5); err == nil {
		t.Fatal("a one-off window without bounds listed")
	}
	w.Cron = "nope"
	if _, _, err := maintenance.Occurrences(w, at("2026-09-24T00:00:00Z"), at("2026-09-25T00:00:00Z"), 5); err == nil {
		t.Fatal("a broken expression listed")
	}
}

// TestOccurrencesSpringForwardGap covers an occurrence whose *end* falls on
// a wall-clock time that does not exist. Europe/Berlin jumps from 02:00 CET
// straight to 03:00 CEST on 2026-03-29; a window starting at 01:30 local
// with a one-hour duration would nominally end at 02:30 local, a time
// inside the gap. Occurrences never reconstructs the end from wall-clock
// fields (End is Start plus the duration, both absolute instants), so the
// hour is neither lost nor doubled: the occurrence still covers exactly
// one hour of real time, ending at 03:30 CEST.
func TestOccurrencesSpringForwardGap(t *testing.T) {
	w := maintenance.Window{ID: uuid.Must(uuid.NewV7()), Name: "predawn", TargetKind: maintenance.TargetOrg,
		Cron: "30 1 * * *", Duration: time.Hour, Timezone: "Europe/Berlin"}
	got, _, err := maintenance.Occurrences(w, at("2026-03-29T00:00:00Z"), at("2026-03-30T00:00:00Z"), 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("occurrences = %+v, %v", got, err)
	}
	// 01:30 CET on 2026-03-29 is 00:30 UTC; the nominal local end (02:30)
	// falls in the spring-forward gap, but the instant itself (01:30 UTC,
	// which reads as 03:30 CEST) is perfectly ordinary.
	if !got[0].Start.Equal(at("2026-03-29T00:30:00Z")) || !got[0].End.Equal(at("2026-03-29T01:30:00Z")) {
		t.Fatalf("occurrence = %+v", got[0])
	}
	if d := got[0].End.Sub(got[0].Start); d != time.Hour {
		t.Fatalf("duration not preserved across the gap: %v", d)
	}
	cases := map[string]bool{
		"2026-03-29T00:29:59Z": false,
		"2026-03-29T00:30:00Z": true,
		"2026-03-29T01:29:59Z": true,
		"2026-03-29T01:30:00Z": false, // end is outside, half-open
	}
	for instant, want := range cases {
		if got, err := maintenance.ActiveAt(w, at(instant)); err != nil || got != want {
			t.Errorf("ActiveAt(%s) = %v, %v; want %v", instant, got, err, want)
		}
	}
}

// TestOccurrencesReportsTheCap is the final review's Important #3 at its
// source: the cap and an exhausted timetable both end the loop, and only
// the first is a truncation the caller must pass on.
func TestOccurrencesReportsTheCap(t *testing.T) {
	w := maintenance.Window{ID: uuid.Must(uuid.NewV7()), Name: "agent restart", TargetKind: maintenance.TargetOrg,
		Cron: "*/15 * * * *", Duration: 5 * time.Minute, Timezone: "UTC"}
	from, to := at("2026-09-21T00:00:00Z"), at("2026-09-22T00:00:00Z") // 96 occurrences
	if occ, capped, err := maintenance.Occurrences(w, from, to, 10); err != nil || len(occ) != 10 || !capped {
		t.Fatalf("capped run = %d occurrences, capped %v, %v", len(occ), capped, err)
	}
	// Exactly as many as the range holds is not a truncation.
	if occ, capped, err := maintenance.Occurrences(w, from, to, 96); err != nil || len(occ) != 96 || capped {
		t.Fatalf("exact run = %d occurrences, capped %v, %v", len(occ), capped, err)
	}
	if occ, capped, err := maintenance.Occurrences(w, from, to, 500); err != nil || len(occ) != 96 || capped {
		t.Fatalf("roomy run = %d occurrences, capped %v, %v", len(occ), capped, err)
	}
	// A one-off window can never be capped.
	start, end := at("2026-09-21T01:00:00Z"), at("2026-09-21T02:00:00Z")
	oneOff := maintenance.Window{ID: uuid.Must(uuid.NewV7()), Name: "db upgrade", TargetKind: maintenance.TargetOrg, StartsAt: &start, EndsAt: &end}
	if occ, capped, err := maintenance.Occurrences(oneOff, from, to, 1); err != nil || len(occ) != 1 || capped {
		t.Fatalf("one-off = %d occurrences, capped %v, %v", len(occ), capped, err)
	}
}
