package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/rules"
)

type alertPageBody struct {
	Alerts     []alertOut `json:"alerts"`
	NextCursor string     `json:"next_cursor"`
	Total      *int       `json:"total"`
}

func (f alertFixture) page(t *testing.T, query string) alertPageBody {
	t.Helper()
	resp := requestJSON(t, f.operatorC, http.MethodGet, f.alertsURL("?"+query), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET alerts?%s = %d %s", query, resp.StatusCode, mustReadBody(t, resp))
	}
	var out alertPageBody
	decodeInto(t, resp, &out)
	return out
}

func alertPageTime(t time.Time) *time.Time { return &t }

func alertIDs(as []alertOut) []string {
	ids := make([]string, len(as))
	for i, a := range as {
		ids[i] = a.ID
	}
	return ids
}

// addWindow creates a maintenance window on host that covers now.
func (f alertFixture) addWindow(t *testing.T, host uuid.UUID, in maintenance.Input) {
	t.Helper()
	in.Name, in.TargetKind, in.TargetID = "w-"+uuid.NewString()[:8], maintenance.TargetHost, &host
	if _, err := f.s.Deps.Maintenance.Create(db.WithOrgScope(context.Background(), f.orgID), f.orgID, uuid.Nil, in); err != nil {
		t.Fatalf("create window: %v", err)
	}
}

func (f alertFixture) post(t *testing.T, path string, body any) {
	t.Helper()
	resp := requestJSON(t, f.operatorC, http.MethodPost, f.alertsURL(path), body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s = %d %s", path, resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestAlertsPageMutedFilterTotal: muted rows (snoozed, or on a host inside a
// maintenance window) span page boundaries and the total counts exactly the
// matching rows, in both directions; a closed alert on the maintenance host is
// never muted.
func TestAlertsPageMutedFilterTotal(t *testing.T) {
	f := newAlertFixture(t, "alertpage-muted")
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	f.addWindow(t, f.hostA, maintenance.Input{
		StartsAt: alertPageTime(time.Now().UTC().Add(-time.Hour)), EndsAt: alertPageTime(time.Now().UTC().Add(time.Hour)),
	})
	var wantMuted, wantUnmuted []string
	// 11 open alerts on the maintenance host, interleaved with 12 on the plain one.
	for i := range 11 {
		id := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", fmt.Sprintf("m%02d", i)), rules.StateWarn, base.Add(time.Duration(2*i)*time.Minute))
		wantMuted = append(wantMuted, id.String())
	}
	for i := range 12 {
		id := f.seedOpenAlert(t, f.hostB, tupleSubject(f.hostB, "cpu", "", fmt.Sprintf("p%02d", i)), rules.StateWarn, base.Add(time.Duration(2*i+1)*time.Minute))
		if i < 2 { // two of them snoozed
			f.post(t, "/"+id.String()+"/snooze", map[string]any{"until_change": true})
			wantMuted = append(wantMuted, id.String())
		} else {
			wantUnmuted = append(wantUnmuted, id.String())
		}
	}
	closed := f.seedClosedAlert(t, f.hostA, tupleSubject(f.hostA, "disk", "/", "used"), rules.StateWarn, base)
	wantUnmuted = append(wantUnmuted, closed.String())

	collect := func(muted string) (ids []string, total int) {
		for p := 1; p <= 4; p++ {
			got := f.page(t, fmt.Sprintf("muted=%s&page=%d&per_page=10", muted, p))
			if got.Total == nil {
				t.Fatalf("muted=%s page %d: no total", muted, p)
			}
			if total != 0 && total != *got.Total {
				t.Fatalf("total moved between pages: %d then %d", total, *got.Total)
			}
			total = *got.Total
			ids = append(ids, alertIDs(got.Alerts)...)
			for _, a := range got.Alerts {
				if a.Muted != (muted == "true") {
					t.Fatalf("muted=%s returned alert with muted=%v", muted, a.Muted)
				}
			}
		}
		return ids, total
	}
	ids, total := collect("true")
	if total != len(wantMuted) || len(ids) != len(wantMuted) {
		t.Fatalf("muted=true: total %d, %d rows, want %d", total, len(ids), len(wantMuted))
	}
	if !sameSet(ids, wantMuted) {
		t.Fatalf("muted=true rows differ: got %v want %v", ids, wantMuted)
	}
	ids, total = collect("false")
	if total != len(wantUnmuted) || !sameSet(ids, wantUnmuted) {
		t.Fatalf("muted=false: total %d rows %v, want %v", total, ids, wantUnmuted)
	}
	// No filter: everything, and a page past the end answers 200, empty, right total.
	all := f.page(t, "page=1&per_page=10")
	if all.Total == nil || *all.Total != len(wantMuted)+len(wantUnmuted) {
		t.Fatalf("unfiltered total = %v, want %d", all.Total, len(wantMuted)+len(wantUnmuted))
	}
	past := f.page(t, "muted=true&page=99&per_page=10")
	if len(past.Alerts) != 0 || past.Total == nil || *past.Total != len(wantMuted) {
		t.Fatalf("page past the end = %d rows, total %v", len(past.Alerts), past.Total)
	}
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// TestAlertsPageMutedCronWindow: a recurring window that is inside a stretch
// right now mutes its host's open alerts in the SQL filter, like a one-off one.
func TestAlertsPageMutedCronWindow(t *testing.T) {
	f := newAlertFixture(t, "alertpage-cron")
	opened := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	f.addWindow(t, f.hostA, maintenance.Input{Cron: "* * * * *", Duration: "2h", Timezone: "UTC", Reason: maintenance.ReasonSilence})
	inWindow := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used"), rules.StateCrit, opened)
	plain := f.seedOpenAlert(t, f.hostB, tupleSubject(f.hostB, "cpu", "", "used"), rules.StateCrit, opened)

	got := f.page(t, "muted=true&page=1")
	if got.Total == nil || *got.Total != 1 || len(got.Alerts) != 1 || got.Alerts[0].ID != inWindow.String() || got.Alerts[0].MuteReason != "silence" {
		t.Fatalf("muted=true = %+v total %v, want only the alert on the cron-window host", alertIDs(got.Alerts), got.Total)
	}
	got = f.page(t, "muted=false&page=1")
	if got.Total == nil || *got.Total != 1 || got.Alerts[0].ID != plain.String() {
		t.Fatalf("muted=false = %+v total %v, want only the plain alert", alertIDs(got.Alerts), got.Total)
	}
}

func TestAlertsPageNoMuteFilterNeedsNoWindowLookup(t *testing.T) {
	f := newAlertFixture(t, "alertpage-nomute")
	f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used"), rules.StateWarn, time.Now().UTC().Add(-time.Hour))
	got := f.page(t, "page=1")
	if got.Total == nil || *got.Total != 1 || len(got.Alerts) != 1 {
		t.Fatalf("page = %+v total %v", got.Alerts, got.Total)
	}
}

func TestAlertsSortTrackedSeverity(t *testing.T) {
	f := newAlertFixture(t, "alertpage-tracked")
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	unknown := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "a"), rules.StateUnknown, at)
	crit := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "b"), rules.StateCrit, at.Add(time.Minute))
	warn := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "c"), rules.StateWarn, at.Add(2*time.Minute))
	for _, tc := range []struct {
		query string
		want  []uuid.UUID
	}{
		{"sort=tracked&order=desc", []uuid.UUID{crit, warn, unknown}},
		{"sort=tracked&order=asc", []uuid.UUID{unknown, warn, crit}},
		{"sort=peak&order=desc", []uuid.UUID{crit, warn, unknown}},
		{"sort=peak", []uuid.UUID{unknown, warn, crit}},
	} {
		got := f.page(t, tc.query+"&page=1")
		var want []string
		for _, id := range tc.want {
			want = append(want, id.String())
		}
		if !slices.Equal(alertIDs(got.Alerts), want) {
			t.Errorf("%s = %v, want %v", tc.query, alertIDs(got.Alerts), want)
		}
	}
}

func TestAlertsSortHostName(t *testing.T) {
	f := newAlertFixture(t, "alertpage-host")
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	b1 := f.seedOpenAlert(t, f.hostB, tupleSubject(f.hostB, "cpu", "", "a"), rules.StateWarn, at)
	a1 := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "a"), rules.StateWarn, at.Add(time.Minute))
	a2 := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "b"), rules.StateWarn, at.Add(2*time.Minute))
	asc := f.page(t, "sort=host&order=asc&page=1")
	// web-1 (hostA) before web-2; within a host newest opened first.
	if want := []string{a2.String(), a1.String(), b1.String()}; !slices.Equal(alertIDs(asc.Alerts), want) {
		t.Errorf("host asc = %v, want %v", alertIDs(asc.Alerts), want)
	}
	desc := f.page(t, "sort=host&order=desc&page=1")
	if want := []string{b1.String(), a2.String(), a1.String()}; !slices.Equal(alertIDs(desc.Alerts), want) {
		t.Errorf("host desc = %v, want %v", alertIDs(desc.Alerts), want)
	}
}

func TestAlertsSortAcknowledgedNullsLast(t *testing.T) {
	f := newAlertFixture(t, "alertpage-ack")
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	first := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "a"), rules.StateWarn, at)
	second := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "b"), rules.StateWarn, at.Add(time.Minute))
	none := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "c"), rules.StateWarn, at.Add(2*time.Minute))
	f.post(t, "/"+first.String()+"/acknowledge", map[string]any{})
	time.Sleep(20 * time.Millisecond)
	f.post(t, "/"+second.String()+"/acknowledge", map[string]any{})
	for order, want := range map[string][]uuid.UUID{
		"asc":  {first, second, none},
		"desc": {second, first, none},
	} {
		got := f.page(t, "sort=acknowledged&order="+order+"&page=1")
		var w []string
		for _, id := range want {
			w = append(w, id.String())
		}
		if !slices.Equal(alertIDs(got.Alerts), w) {
			t.Errorf("acknowledged %s = %v, want %v", order, alertIDs(got.Alerts), w)
		}
	}
}

func TestAlertsSortEveryKeyStable(t *testing.T) {
	f := newAlertFixture(t, "alertpage-stable")
	at := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	sev := []rules.State{rules.StateWarn, rules.StateCrit, rules.StateUnknown}
	for i := range 23 {
		h := f.hostA
		if i%3 == 0 {
			h = f.hostB
		}
		f.seedOpenAlert(t, h, tupleSubject(h, "cpu", "", fmt.Sprintf("m%02d", i)), sev[i%3], at.Add(time.Duration(i/4)*time.Minute)) // ties on opened_at
	}
	for _, key := range []string{"opened", "host", "peak", "tracked", "acknowledged"} {
		for _, order := range []string{"asc", "desc"} {
			q := "sort=" + key + "&order=" + order
			whole := f.page(t, q+"&page=1&per_page=100")
			var paged []string
			for p := 1; p <= 3; p++ {
				paged = append(paged, alertIDs(f.page(t, fmt.Sprintf("%s&page=%d&per_page=10", q, p)).Alerts)...)
			}
			if !slices.Equal(paged, alertIDs(whole.Alerts)) || len(paged) != 23 {
				t.Errorf("%s: pages are not the whole list in order (%d rows)", q, len(paged))
			}
		}
	}
}

func TestAlertsPageRejectsBadSortAndCursorMix(t *testing.T) {
	f := newAlertFixture(t, "alertpage-bad")
	for _, q := range []string{"sort=mute&page=1", "sort=bogus&page=1", "page=1&cursor=abc", "page=0&per_page=7"} {
		resp := requestJSON(t, f.operatorC, http.MethodGet, f.alertsURL("?"+q), nil)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("?%s = %d, want 422", q, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

func TestAlertsCursorModeUnchanged(t *testing.T) {
	f := newAlertFixture(t, "alertpage-cursor")
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for i := range 3 {
		f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", fmt.Sprintf("m%d", i)), rules.StateWarn, at.Add(time.Duration(i)*time.Minute))
	}
	resp := requestJSON(t, f.operatorC, http.MethodGet, f.alertsURL("?limit=2"), nil)
	var raw map[string]json.RawMessage
	decodeInto(t, resp, &raw)
	if _, has := raw["total"]; has {
		t.Fatalf("cursor mode answered a total: %s", raw["total"])
	}
	var next string
	var as []alertOut
	if err := json.Unmarshal(raw["next_cursor"], &next); err != nil || next == "" {
		t.Fatalf("next_cursor = %s", raw["next_cursor"])
	}
	if err := json.Unmarshal(raw["alerts"], &as); err != nil || len(as) != 2 {
		t.Fatalf("alerts = %d (%v)", len(as), err)
	}
}
