package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// seedAlertWithDetail opens one alert carrying detail, as the alerts
// executor does when it captured one (backlog 3.13b).
func (f alertFixture) seedAlertWithDetail(t *testing.T, sub alerts.Subject, detail string, at time.Time) uuid.UUID {
	t.Helper()
	st := alerts.NewSQLStore(f.s.DB)
	ctx := context.Background()
	tx, err := f.s.DB.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := st.Execute(ctx, tx, f.orgID, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: at, Severity: rules.StateCrit, Detail: detail},
	}); err != nil {
		t.Fatalf("seed alert: %v", err)
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
			return a.ID
		}
	}
	t.Fatalf("no seeded alert for %+v", sub)
	return uuid.Nil
}

// TestAlertsSayWhyTheyFired (backlog 3.13b): the list, the alert page and
// the alert's timeline events carry detail; an alert without one, and an
// event without one, leave the field out.
func TestAlertsSayWhyTheyFired(t *testing.T) {
	f := newAlertFixture(t, "alert-detail")
	opened := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	withDetail := f.seedAlertWithDetail(t, tupleSubject(f.hostA, "shop", "home", "http.assertion"), "status == 200 — saw 503", opened)
	without := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used_percent"), rules.StateWarn, opened)

	resp := requestJSON(t, f.viewerC, http.MethodGet, f.alertsURL(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var list struct {
		Alerts []map[string]any `json:"alerts"`
	}
	decodeInto(t, resp, &list)
	byID := map[string]map[string]any{}
	for _, a := range list.Alerts {
		byID[a["id"].(string)] = a
	}
	if got := byID[withDetail.String()]["detail"]; got != "status == 200 — saw 503" {
		t.Errorf("list detail = %v", got)
	}
	if got, present := byID[without.String()]["detail"]; present {
		t.Errorf("list detail of an alert without one = %v, want the field left out", got)
	}

	resp = requestJSON(t, f.viewerC, http.MethodGet, f.alertsURL("/"+withDetail.String()), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var got struct {
		Alert struct {
			Detail   string `json:"detail"`
			Timeline []struct {
				Event map[string]json.RawMessage `json:"event"`
			} `json:"timeline"`
		} `json:"alert"`
	}
	decodeInto(t, resp, &got)
	if got.Alert.Detail != "status == 200 — saw 503" {
		t.Errorf("get detail = %q", got.Alert.Detail)
	}
	if len(got.Alert.Timeline) != 1 || string(got.Alert.Timeline[0].Event["detail"]) != `"status == 200 — saw 503"` {
		t.Errorf("timeline = %+v, want the opened event with its detail", got.Alert.Timeline)
	}

	_, plain := f.getAlert(t, f.viewerC, without)
	if len(plain.Timeline) != 1 || plain.Timeline[0].Event == nil {
		t.Fatalf("timeline of the alert without detail = %+v", plain.Timeline)
	}
	resp = requestJSON(t, f.viewerC, http.MethodGet, f.alertsURL("/"+without.String()), nil)
	var raw struct {
		Alert struct {
			Timeline []struct {
				Event map[string]json.RawMessage `json:"event"`
			} `json:"timeline"`
		} `json:"alert"`
	}
	decodeInto(t, resp, &raw)
	if _, present := raw.Alert.Timeline[0].Event["detail"]; present {
		t.Errorf("an event without a detail carries the field: %s", raw.Alert.Timeline[0].Event["detail"])
	}
}
