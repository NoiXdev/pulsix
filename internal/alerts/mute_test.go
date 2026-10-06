// Mute's tests exist to pin one thing above all else: the verdict must not
// depend on which order the caller happened to list windows in, or on
// which of snooze/silence/maintenance the caller mentions first. Every
// "which mute wins" test below either runs both slice orders explicitly or
// only ever has one candidate — so an implementation that quietly sorted
// the slice or ranged over a map would be caught here, the same promise
// v0.3b-2's rules.Resolve and this milestone's own Reconcile tests already
// make for their own precedence tables.
package alerts_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/maintenance"
)

// alertSnoozedUntil is a bare alert snoozed until the given instant — no
// subject, no severity, nothing Mute does not itself read.
func alertSnoozedUntil(until time.Time) alerts.Alert {
	return alerts.Alert{SnoozedUntil: &until}
}

// alertSnoozedUntilChange is a bare alert snoozed with no end: the one
// unbounded mute in the system.
func alertSnoozedUntilChange() alerts.Alert {
	return alerts.Alert{SnoozeUntilChange: true}
}

// windowReason maps a MuteReason back to the maintenance.Reason a fixture
// needs to build an Occurrence — the reverse of the mapping mute.go itself
// performs. Production code only ever needs the forward direction; this
// exists solely so the tests can talk in MuteReason, the vocabulary the
// test names and the brief itself use.
func windowReason(r alerts.MuteReason) maintenance.Reason {
	switch r {
	case alerts.MuteMaintenance:
		return maintenance.ReasonMaintenance
	case alerts.MuteSilence:
		return maintenance.ReasonSilence
	default:
		panic(fmt.Sprintf("test fixture: %q is not a window reason", r))
	}
}

// windowFromTo is one occurrence with every field a test might need to
// control explicitly: its id (for the uuid tie-break), its start and end
// (for coverage), and its reason.
func windowFromTo(id uuid.UUID, start, end time.Time, reason alerts.MuteReason) maintenance.Occurrence {
	return maintenance.Occurrence{WindowID: id, Start: start, End: end, Reason: windowReason(reason)}
}

// windowEnding is an occurrence that already covers t0 and ends at end —
// every "who wins" test below varies only the end, never the start, since
// the start plays no part in Mute's order.
func windowEnding(end time.Time, reason alerts.MuteReason) maintenance.Occurrence {
	return windowFromTo(uuid.Must(uuid.NewV7()), t0.Add(-time.Hour), end, reason)
}

func TestMuteReportsTheLatestEnding(t *testing.T) {
	a := alertSnoozedUntil(t0.Add(time.Hour))
	w := windowEnding(t0.Add(3*time.Hour), alerts.MuteMaintenance)

	got := alerts.Mute(a, []maintenance.Occurrence{w}, t0)

	if !got.Muted {
		t.Fatalf("got %+v, want Muted: true", got)
	}
	if got.Reason != alerts.MuteMaintenance {
		t.Fatalf("reason = %q, want the mute that lasts longest", got.Reason)
	}
	wantUntil := t0.Add(3 * time.Hour)
	if got.Until == nil || !got.Until.Equal(wantUntil) {
		t.Fatalf("until = %v, want %v", got.Until, wantUntil)
	}
	if got.WindowID == nil || *got.WindowID != w.WindowID {
		t.Fatalf("windowID = %v, want %v", got.WindowID, w.WindowID)
	}
}

func TestMuteUnboundedBeatsBounded(t *testing.T) {
	a := alertSnoozedUntilChange()
	w := windowEnding(t0.Add(99*time.Hour), alerts.MuteMaintenance)

	got := alerts.Mute(a, []maintenance.Occurrence{w}, t0)

	if got.Reason != alerts.MuteSnooze || got.Until != nil {
		t.Fatalf("got %+v, want the unbounded snooze", got)
	}
	if !got.Muted {
		t.Fatalf("got %+v, want Muted: true", got)
	}
	if got.WindowID != nil {
		t.Fatalf("windowID = %v, want nil: the winner is the snooze, not the window", got.WindowID)
	}
}

func TestMuteIgnoresWindowsThatDoNotCoverNow(t *testing.T) {
	a := alerts.Alert{}
	before := maintenance.Occurrence{
		WindowID: uuid.Must(uuid.NewV7()), Reason: maintenance.ReasonMaintenance,
		Start: t0.Add(-2 * time.Hour), End: t0.Add(-time.Hour),
	}
	after := maintenance.Occurrence{
		WindowID: uuid.Must(uuid.NewV7()), Reason: maintenance.ReasonSilence,
		Start: t0.Add(time.Hour), End: t0.Add(2 * time.Hour),
	}

	got := alerts.Mute(a, []maintenance.Occurrence{before, after}, t0)

	if got.Muted {
		t.Fatalf("got %+v, want Muted: false — neither window covers now", got)
	}
}

// TestMuteExpiredSnoozeIsNotMuted is review focus 4: a SnoozedUntil one
// second in the past must not be reported as a mute that happens to have
// already ended — it must simply not mute.
func TestMuteExpiredSnoozeIsNotMuted(t *testing.T) {
	a := alertSnoozedUntil(t0.Add(-time.Second))

	got := alerts.Mute(a, nil, t0)

	if got.Muted {
		t.Fatalf("got %+v, want Muted: false for a snooze that already ended", got)
	}
}

func TestMuteReflectsMaintenanceWindow(t *testing.T) {
	w := windowEnding(t0.Add(time.Hour), alerts.MuteMaintenance)

	got := alerts.Mute(alerts.Alert{}, []maintenance.Occurrence{w}, t0)

	if !got.Muted {
		t.Fatalf("got %+v, want Muted: true", got)
	}
	if got.Reason != alerts.MuteMaintenance {
		t.Fatalf("reason = %q, want %q", got.Reason, alerts.MuteMaintenance)
	}
	if got.Until == nil || !got.Until.Equal(w.End) {
		t.Fatalf("until = %v, want %v", got.Until, w.End)
	}
	if got.WindowID == nil || *got.WindowID != w.WindowID {
		t.Fatalf("windowID = %v, want %v", got.WindowID, w.WindowID)
	}
}

func TestMuteReflectsSilenceWindow(t *testing.T) {
	w := windowEnding(t0.Add(time.Hour), alerts.MuteSilence)

	got := alerts.Mute(alerts.Alert{}, []maintenance.Occurrence{w}, t0)

	if !got.Muted {
		t.Fatalf("got %+v, want Muted: true", got)
	}
	if got.Reason != alerts.MuteSilence {
		t.Fatalf("reason = %q, want %q", got.Reason, alerts.MuteSilence)
	}
	if got.Until == nil || !got.Until.Equal(w.End) {
		t.Fatalf("until = %v, want %v", got.Until, w.End)
	}
	if got.WindowID == nil || *got.WindowID != w.WindowID {
		t.Fatalf("windowID = %v, want %v", got.WindowID, w.WindowID)
	}
}

func TestMuteNoApplicableMuteReturnsZeroValue(t *testing.T) {
	got := alerts.Mute(alerts.Alert{}, nil, t0)

	if got.Muted {
		t.Fatalf("got %+v, want Muted: false", got)
	}
	if got.Reason != "" {
		t.Fatalf("reason = %q, want empty", got.Reason)
	}
	if got.Until != nil {
		t.Fatalf("until = %v, want nil", got.Until)
	}
	if got.WindowID != nil {
		t.Fatalf("windowID = %v, want nil", got.WindowID)
	}
}

// TestMuteTieBreakIsDeterministic is review focus 5. Two windows ending at
// the identical instant, one silence and one maintenance, must resolve to
// maintenance — the order the MuteReason consts are declared in — in
// either slice order. A third case, two windows of the same reason and the
// same end, must break on the lower uuid, again in either slice order.
func TestMuteTieBreakIsDeterministic(t *testing.T) {
	end := t0.Add(2 * time.Hour)

	t.Run("reason tie-break by declaration order", func(t *testing.T) {
		silence := windowFromTo(uuid.Must(uuid.NewV7()), t0.Add(-time.Hour), end, alerts.MuteSilence)
		maint := windowFromTo(uuid.Must(uuid.NewV7()), t0.Add(-time.Hour), end, alerts.MuteMaintenance)

		for _, order := range [][]maintenance.Occurrence{
			{silence, maint},
			{maint, silence},
		} {
			got := alerts.Mute(alerts.Alert{}, order, t0)
			if got.Reason != alerts.MuteMaintenance {
				t.Fatalf("order %v: reason = %q, want maintenance by declaration order", order, got.Reason)
			}
		}
	})

	t.Run("uuid tie-break when reason and end also tie", func(t *testing.T) {
		low := uuid.MustParse("00000000-0000-4000-8000-000000000001")
		high := uuid.MustParse("ffffffff-ffff-4fff-8fff-ffffffffffff")
		w1 := windowFromTo(low, t0.Add(-time.Hour), end, alerts.MuteMaintenance)
		w2 := windowFromTo(high, t0.Add(-time.Hour), end, alerts.MuteMaintenance)

		for _, order := range [][]maintenance.Occurrence{
			{w1, w2},
			{w2, w1},
		} {
			got := alerts.Mute(alerts.Alert{}, order, t0)
			if got.WindowID == nil || *got.WindowID != low {
				t.Fatalf("order %v: windowID = %v, want the lower uuid %v", order, got.WindowID, low)
			}
		}
	})
}

// TestMutePanicsOnUnrecognisedWindowReason is the other half of the
// explicit-switch ruling: a window reason neither ReasonMaintenance nor
// ReasonSilence must surface loudly, not fall through and silently label
// itself "maintenance".
func TestMutePanicsOnUnrecognisedWindowReason(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("Mute did not panic on an unrecognised window reason")
		}
	}()

	w := maintenance.Occurrence{
		WindowID: uuid.Must(uuid.NewV7()), Reason: maintenance.Reason("bogus"),
		Start: t0.Add(-time.Hour), End: t0.Add(time.Hour),
	}
	alerts.Mute(alerts.Alert{}, []maintenance.Occurrence{w}, t0)
}
