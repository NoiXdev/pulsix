// Backlog 4.39 through the real evaluation stack (lifecycle_sql_test.go's
// rig): a check switched off and on again within the hour keeps its
// alert's acknowledgement; one switched back on later does not.
package alerts_test

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/rules"
)

// setDiskEnabled switches the rig's disk check on or off through
// checks.Service, the way the API does.
func (r *lifecycleRig) setDiskEnabled(t *testing.T, enabled bool) {
	t.Helper()
	if _, err := r.checks.UpdateDefinition(r.scopedCtx, r.org, r.diskDefID, checks.DefinitionInput{
		Key: checks.PluginDisk, Plugin: checks.PluginDisk,
		Interval: time.Minute, Enabled: enabled, BindingKind: checks.BindingOrg,
	}); err != nil {
		t.Fatalf("set disk check enabled=%v: %v", enabled, err)
	}
}

// toggleDisk acknowledges the open crit alert, switches the disk check off
// until its alert closes stale, waits `off`, switches it on again with the
// tuple still crit and returns the acknowledged alert and the new one.
func (r *lifecycleRig) toggleDisk(t *testing.T, off time.Duration) (first, second alerts.Alert) {
	t.Helper()
	first = r.openEpisode(t)
	user := seedUser(t, r.d)
	if err := alerts.NewSQLStore(r.d).Acknowledge(context.Background(), r.org, first.ID, user, "on it", r.now); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	r.setDiskEnabled(t, false)
	r.advance(4 * time.Minute)
	r.makeDue(t)
	r.tick(t)
	if closed, reason := r.closure(t, first.ID); !closed || reason != string(alerts.Stale) {
		t.Fatalf("after disabling the check: closed=%v reason=%q, want closed stale", closed, reason)
	}

	r.setDiskEnabled(t, true)
	r.advance(off)
	r.report(t, 96)
	r.tick(t)
	second = r.openTupleAlert(t)
	if second.ID == first.ID {
		t.Fatal("the alert re-opened in place; want a new episode")
	}
	if second.Severity != rules.StateCrit {
		t.Fatalf("new episode severity = %q, want crit", second.Severity)
	}
	return first, second
}

func TestACheckSwitchedOffAndOnWithinTheHourKeepsTheAcknowledgement(t *testing.T) {
	r := newLifecycleRig(t)
	first, second := r.toggleDisk(t, 10*time.Minute)

	if second.ContinuesAlertID == nil || *second.ContinuesAlertID != first.ID {
		t.Fatalf("continues_alert_id = %v, want %s", second.ContinuesAlertID, first.ID)
	}
	if second.AckSeverity != rules.StateCrit || second.AckAt == nil || second.AckNote != "on it" {
		t.Fatalf("new episode acknowledgement = %v/%q/%q, want acknowledged at crit with the old note", second.AckAt, second.AckSeverity, second.AckNote)
	}
}

func TestACheckSwitchedBackOnAfterTheHourStartsUnacknowledged(t *testing.T) {
	r := newLifecycleRig(t)
	_, second := r.toggleDisk(t, 61*time.Minute)

	if second.ContinuesAlertID != nil || second.AckAt != nil {
		t.Fatalf("new episode continues %v with ack %v, want a plain opening", second.ContinuesAlertID, second.AckAt)
	}
}
