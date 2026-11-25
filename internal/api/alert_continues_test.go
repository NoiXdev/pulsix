package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// seedContinuation opens an alert on sub, closes it stale, and opens the
// next episode adopting ad from it (backlog 4.39), returning both ids.
func (f alertFixture) seedContinuation(t *testing.T, sub alerts.Subject, ad alerts.Adoption, at time.Time) (prev, next uuid.UUID) {
	t.Helper()
	prev = f.seedAlertWithDetail(t, sub, "", at.Add(-time.Hour))
	st := alerts.NewSQLStore(f.s.DB)
	ctx := context.Background()
	tx, err := f.s.DB.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ad.From = prev
	if err := st.Execute(ctx, tx, f.orgID, []alerts.Action{
		{Kind: alerts.ActionClose, AlertID: prev, At: at.Add(-10 * time.Minute), Reason: alerts.Stale},
		{Kind: alerts.ActionOpen, Subject: sub, At: at, Severity: rules.StateCrit, Adopt: &ad},
	}); err != nil {
		t.Fatalf("seed continuation: %v", err)
	}
	open, err := st.OpenForHost(ctx, tx, f.orgID, sub.HostID)
	if err != nil {
		t.Fatalf("open for host: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	for _, a := range open {
		if a.Subject == sub {
			return prev, a.ID
		}
	}
	t.Fatalf("no continuing alert for %+v", sub)
	return uuid.Nil, uuid.Nil
}

// TestAnAlertSaysWhichEpisodeItContinues (backlog 4.39): the alert names
// the episode it continues, and its opened event says what was carried
// over; a plain opening says neither.
func TestAnAlertSaysWhichEpisodeItContinues(t *testing.T) {
	f := newAlertFixture(t, "alert-continues")
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	ackAt := at.Add(-30 * time.Minute)
	prev, next := f.seedContinuation(t, tupleSubject(f.hostA, "disk", "/", "used_pct"),
		alerts.Adoption{AckAt: &ackAt, AckSeverity: rules.StateCrit, SnoozeUntilChange: true}, at)

	type event struct {
		Kind              string `json:"kind"`
		ContinuesAlertID  string `json:"continues_alert_id"`
		AckCarriedOver    bool   `json:"ack_carried_over"`
		SnoozeCarriedOver bool   `json:"snooze_carried_over"`
	}
	type detail struct {
		Alert struct {
			ContinuesAlertID string `json:"continues_alert_id"`
			AckSeverity      string `json:"ack_severity"`
			Timeline         []struct {
				Event *event `json:"event"`
			} `json:"timeline"`
		} `json:"alert"`
	}
	var got detail
	resp := requestJSON(t, f.viewerC, http.MethodGet, f.alertsURL("/"+next.String()), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &got)
	if got.Alert.ContinuesAlertID != prev.String() || got.Alert.AckSeverity != "crit" {
		t.Fatalf("alert continues %q acknowledged at %q, want %s at crit", got.Alert.ContinuesAlertID, got.Alert.AckSeverity, prev)
	}
	var opened *event
	for _, e := range got.Alert.Timeline {
		if e.Event != nil && e.Event.Kind == "opened" {
			opened = e.Event
		}
	}
	if opened == nil || *opened != (event{Kind: "opened", ContinuesAlertID: prev.String(), AckCarriedOver: true, SnoozeCarriedOver: true}) {
		t.Fatalf("opened event = %+v, want it to name %s with acknowledgement and snooze carried over", opened, prev)
	}

	var plain detail
	resp = requestJSON(t, f.viewerC, http.MethodGet, f.alertsURL("/"+prev.String()), nil)
	decodeInto(t, resp, &plain)
	if plain.Alert.ContinuesAlertID != "" {
		t.Fatalf("a plain opening continues %q", plain.Alert.ContinuesAlertID)
	}
	for _, e := range plain.Alert.Timeline {
		if e.Event != nil && e.Event.Kind == "opened" && (e.Event.ContinuesAlertID != "" || e.Event.AckCarriedOver || e.Event.SnoozeCarriedOver) {
			t.Fatalf("a plain opening's event = %+v, want nothing carried over", e.Event)
		}
	}
}
