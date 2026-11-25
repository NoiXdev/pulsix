// Backlog 4.39: a tuple alert that re-opens within an hour of its
// subject's previous episode closing `stale` takes over that episode's
// acknowledgement and snooze. Every adopt and no-adopt rule reconcile.go's
// adoption names has a row here.
package alerts_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

var ackUser = uuid.MustParse("33333333-3333-4333-8333-333333333333")

// closedEpisode is sub's previous episode at severity, closed with reason
// `closed` ago relative to the evaluation's At (input's own t0 + 1h).
func closedEpisode(sub alerts.Subject, severity rules.State, reason alerts.CloseReason, ago time.Duration) alerts.Alert {
	a := openAlert(sub, severity, t0)
	closedAt := t0.Add(time.Hour).Add(-ago)
	a.ClosedAt, a.ClosedReason = &closedAt, reason
	return a
}

// acked returns a with an acknowledgement at severity.
func acked(a alerts.Alert, severity rules.State) alerts.Alert {
	at, by := t0.Add(5*time.Minute), ackUser
	a.AckAt, a.AckBy, a.AckSeverity, a.AckNote = &at, &by, severity, "on it"
	return a
}

// snoozedUntil returns a snoozed until the given instant.
func snoozedUntil(a alerts.Alert, until time.Time) alerts.Alert {
	a.SnoozedUntil = &until
	return a
}

func untilChange(a alerts.Alert) alerts.Alert {
	a.SnoozeUntilChange = true
	return a
}

// ackOf is the adoption of prev's acknowledgement alone.
func ackOf(prev alerts.Alert) *alerts.Adoption {
	return &alerts.Adoption{From: prev.ID, AckAt: prev.AckAt, AckBy: prev.AckBy, AckSeverity: prev.AckSeverity, AckNote: prev.AckNote}
}

func TestReconcileReopenAdoption(t *testing.T) {
	sub := tupleSubject("disk", "/", "used_pct")
	at := t0.Add(time.Hour) // input's own At
	stillRunning := at.Add(30 * time.Minute)

	ackedCrit := acked(closedEpisode(sub, rules.StateCrit, alerts.Stale, 30*time.Minute), rules.StateCrit)
	atTheEdge := acked(closedEpisode(sub, rules.StateCrit, alerts.Stale, time.Hour), rules.StateCrit)
	pastTheEdge := acked(closedEpisode(sub, rules.StateCrit, alerts.Stale, time.Hour+time.Microsecond), rules.StateCrit)
	resolved := acked(closedEpisode(sub, rules.StateCrit, alerts.Resolved, 30*time.Minute), rules.StateCrit)
	forgotten := acked(closedEpisode(sub, rules.StateCrit, alerts.Forgotten, 30*time.Minute), rules.StateCrit)
	ackedWarnSnoozed := snoozedUntil(acked(closedEpisode(sub, rules.StateWarn, alerts.Stale, 30*time.Minute), rules.StateWarn), stillRunning)
	timed := snoozedUntil(closedEpisode(sub, rules.StateCrit, alerts.Stale, 30*time.Minute), stillRunning)
	expired := snoozedUntil(closedEpisode(sub, rules.StateCrit, alerts.Stale, 30*time.Minute), at)
	changeSame := untilChange(closedEpisode(sub, rules.StateWarn, alerts.Stale, 30*time.Minute))
	changeOther := untilChange(closedEpisode(sub, rules.StateWarn, alerts.Stale, 30*time.Minute))
	ackedChangeOther := untilChange(acked(closedEpisode(sub, rules.StateCrit, alerts.Stale, 30*time.Minute), rules.StateCrit))
	ackedTimed := snoozedUntil(acked(closedEpisode(sub, rules.StateCrit, alerts.Stale, 30*time.Minute), rules.StateCrit), stillRunning)
	voided := closedEpisode(sub, rules.StateCrit, alerts.Stale, 30*time.Minute)
	voidedAt := t0
	voided.AckAt = &voidedAt // acknowledged once, voided since: acknowledged_severity cleared

	ackedAndTimed := ackOf(ackedTimed)
	ackedAndTimed.SnoozedUntil = ackedTimed.SnoozedUntil

	cases := []struct {
		name     string
		prev     alerts.Alert
		severity rules.State
		want     *alerts.Adoption
	}{
		{"an acknowledged episode closed stale half an hour ago", ackedCrit, rules.StateCrit, ackOf(ackedCrit)},
		{"closed exactly one hour ago is inside the window", atTheEdge, rules.StateCrit, ackOf(atTheEdge)},
		{"closed just over one hour ago is outside the window", pastTheEdge, rules.StateCrit, nil},
		{"an episode closed resolved is never adopted from", resolved, rules.StateCrit, nil},
		{"an episode closed forgotten is never adopted from", forgotten, rules.StateCrit, nil},
		{"a lower severity keeps the acknowledgement", ackedCrit, rules.StateWarn, ackOf(ackedCrit)},
		{"a severity above the acknowledged one adopts nothing, not even the snooze", ackedWarnSnoozed, rules.StateCrit, nil},
		{"a timed snooze still running is adopted", timed, rules.StateCrit, &alerts.Adoption{From: timed.ID, SnoozedUntil: timed.SnoozedUntil}},
		{"a timed snooze that has run out is not", expired, rules.StateCrit, nil},
		{"a snooze until change is adopted at the same severity", changeSame, rules.StateWarn, &alerts.Adoption{From: changeSame.ID, SnoozeUntilChange: true}},
		{"a snooze until change is not adopted at another severity", changeOther, rules.StateCrit, nil},
		{"the acknowledgement is adopted without a snooze until change at another severity", ackedChangeOther, rules.StateWarn, ackOf(ackedChangeOther)},
		{"acknowledgement and timed snooze are adopted together", ackedTimed, rules.StateCrit, ackedAndTimed},
		{"a voided acknowledgement is not adopted", voided, rules.StateCrit, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acts := alerts.Reconcile(input(stateAt(sub, tc.severity, t0)), nil, []alerts.Alert{tc.prev})
			mustBeOneOpen(t, acts, t0, tc.severity)
			if !reflect.DeepEqual(acts[0].Adopt, tc.want) {
				t.Fatalf("Adopt = %+v, want %+v", acts[0].Adopt, tc.want)
			}
		})
	}
}

// TestReconcileReopenAdoptsOnlyFromTheSameSubject: an episode of another
// tuple on the same host is not this subject's previous episode.
func TestReconcileReopenAdoptsOnlyFromTheSameSubject(t *testing.T) {
	sub := tupleSubject("disk", "/", "used_pct")
	other := acked(closedEpisode(tupleSubject("disk", "/data", "used_pct"), rules.StateCrit, alerts.Stale, 30*time.Minute), rules.StateCrit)

	acts := alerts.Reconcile(input(stateAt(sub, rules.StateCrit, t0)), nil, []alerts.Alert{other})
	mustBeOneOpen(t, acts, t0, rules.StateCrit)
	if acts[0].Adopt != nil {
		t.Fatalf("Adopt = %+v from another subject's episode, want nil", acts[0].Adopt)
	}
}

// TestReconcileHostAlertNeverAdopts: only tuple-scope alerts adopt. The
// host-scope unknown alert opens plain even when the host's previous
// unknown alert closed stale a minute ago, acknowledged.
func TestReconcileHostAlertNeverAdopts(t *testing.T) {
	prev := acked(closedEpisode(hostSubject(), rules.StateUnknown, alerts.Stale, time.Minute), rules.StateUnknown)
	in := withHost(input(), rules.HostState{HostID: hostID, State: rules.StateUnknown, Since: t0, EvaluatedAt: t0.Add(time.Hour)})

	acts := alerts.Reconcile(in, nil, []alerts.Alert{prev})
	mustBeOneOpen(t, acts, t0, rules.StateUnknown)
	if acts[0].Adopt != nil {
		t.Fatalf("host-scope Adopt = %+v, want nil", acts[0].Adopt)
	}
}
