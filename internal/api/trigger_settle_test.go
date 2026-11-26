package api_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/triggers"
)

type settleTriggerDTO struct {
	Trigger struct {
		ID             string `json:"id"`
		SettleSeconds  int    `json:"settle_seconds"`
		MaxWaitSeconds *int   `json:"max_wait_seconds"`
		Revision       int64  `json:"revision"`
	} `json:"trigger"`
}

type pendingDTO struct {
	Pending *struct {
		ID         string    `json:"id"`
		FirstAt    time.Time `json:"first_at"`
		LastAt     time.Time `json:"last_at"`
		DueAt      time.Time `json:"due_at"`
		DeadlineAt time.Time `json:"deadline_at"`
		Deliveries int       `json:"deliveries"`
	} `json:"pending"`
}

type settleDeliveries struct {
	Deliveries []struct {
		Outcome      string `json:"outcome"`
		Reason       string `json:"reason"`
		JobGroupID   string `json:"job_group_id"`
		PendingID    string `json:"pending_id"`
		SupersededBy string `json:"superseded_by"`
	} `json:"deliveries"`
}

// The settle delay is set on create, shown on read, and kept by an update
// that leaves it out (backlog 2.137): the edit form of an older client,
// or any caller that does not know the fields, never turns it off.
func TestTriggerSettleDelayThroughTheAPI(t *testing.T) {
	f := newRunFixture(t, "trig-settle")
	c := f.operatorC
	base := func() map[string]any {
		return map[string]any{"kind": "template_run", "template_id": f.templateID.String(), "name": "settling", "preset": "github"}
	}
	body := base()
	body["settle_seconds"], body["max_wait_seconds"] = 30, 600
	resp := postJSON(t, c, f.triggersURL(), body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var created settleTriggerDTO
	decodeInto(t, resp, &created)
	if created.Trigger.SettleSeconds != 30 || created.Trigger.MaxWaitSeconds == nil || *created.Trigger.MaxWaitSeconds != 600 {
		t.Fatalf("created = %+v", created.Trigger)
	}
	url := f.triggersURL() + "/" + created.Trigger.ID
	rev := created.Trigger.Revision
	put := func(body map[string]any, want int) settleTriggerDTO {
		t.Helper()
		resp := requestWithHeaders(t, c, http.MethodPut, url, body, map[string]string{"If-Match": `"` + strconv.FormatInt(rev, 10) + `"`})
		if resp.StatusCode != want {
			t.Fatalf("PUT %v = %d %s, want %d", body, resp.StatusCode, mustReadBody(t, resp), want)
		}
		var out settleTriggerDTO
		if want == http.StatusOK {
			decodeInto(t, resp, &out)
			rev = out.Trigger.Revision
		}
		return out
	}
	if got := put(base(), http.StatusOK); got.Trigger.SettleSeconds != 30 || got.Trigger.MaxWaitSeconds == nil || *got.Trigger.MaxWaitSeconds != 600 {
		t.Fatalf("an update without the settle fields = %+v", got.Trigger)
	}
	var read settleTriggerDTO
	decodeInto(t, requestJSON(t, f.viewerC, http.MethodGet, url, nil), &read)
	if read.Trigger.SettleSeconds != 30 || *read.Trigger.MaxWaitSeconds != 600 {
		t.Fatalf("read = %+v", read.Trigger)
	}
	body = base()
	body["max_wait_seconds"] = 0
	if got := put(body, http.StatusOK); got.Trigger.SettleSeconds != 30 || got.Trigger.MaxWaitSeconds != nil {
		t.Fatalf("max wait 0 (the default) = %+v", got.Trigger)
	}
	body = base()
	body["settle_seconds"] = 4
	put(body, http.StatusBadRequest)
	body["settle_seconds"], body["max_wait_seconds"] = 60, 30
	put(body, http.StatusBadRequest)
	body = base()
	body["settle_seconds"] = 0
	if got := put(body, http.StatusOK); got.Trigger.SettleSeconds != 0 || got.Trigger.MaxWaitSeconds != nil {
		t.Fatalf("settle off = %+v", got.Trigger)
	}
}

// A settling trigger's collected run through the API: the hook answers
// collected with the time it runs, the trigger shows its pending run and
// the deliveries name it; "Run now" starts it as the person (the test
// route's permission), "Discard" drops the next one.
func TestTriggerPendingRunThroughTheAPI(t *testing.T) {
	f := newRunFixture(t, "trig-pending")
	tr := f.createTrigger(t, func(in *triggers.Input) { in.SettleSeconds = 60 })
	url := f.triggersURL() + "/" + tr.Trigger.ID.String()
	readPending := func() pendingDTO {
		t.Helper()
		resp := requestJSON(t, f.viewerC, http.MethodGet, url+"/pending", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("pending: %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var p pendingDTO
		decodeInto(t, resp, &p)
		return p
	}
	if p := readPending(); p.Pending != nil {
		t.Fatalf("pending before any delivery = %+v", p.Pending)
	}
	for _, ref := range []string{"v1", "v2"} {
		body := []byte(`{"ref":"` + ref + `"}`)
		resp := hook(t, tr.URL, body, signedHeaders(tr, body))
		var out struct {
			Status string    `json:"status"`
			DueAt  time.Time `json:"due_at"`
		}
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("hook: %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		decodeInto(t, resp, &out)
		if out.Status != "collected" || out.DueAt.IsZero() {
			t.Fatalf("hook answer = %+v", out)
		}
	}
	p := readPending()
	if p.Pending == nil || p.Pending.Deliveries != 2 || p.Pending.DueAt.Before(p.Pending.LastAt) || p.Pending.DeadlineAt.Before(p.Pending.DueAt) {
		t.Fatalf("pending = %+v", p.Pending)
	}
	var ds settleDeliveries
	decodeInto(t, requestJSON(t, f.viewerC, http.MethodGet, url+"/deliveries", nil), &ds)
	if len(ds.Deliveries) != 2 || ds.Deliveries[0].Outcome != "collected" || ds.Deliveries[0].PendingID != p.Pending.ID || ds.Deliveries[0].JobGroupID != "" {
		t.Fatalf("deliveries = %+v", ds)
	}
	if resp := postJSON(t, f.viewerC, url+"/pending/run", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer run now: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.viewerC, http.MethodDelete, url+"/pending", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer discard: %d", resp.StatusCode)
	}
	resp := postJSON(t, f.operatorC, url+"/pending/run", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run now: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var run struct {
		Outcome string `json:"outcome"`
		RunID   string `json:"run_id"`
	}
	decodeInto(t, resp, &run)
	if run.Outcome != "triggered" || run.RunID == "" {
		t.Fatalf("run now = %+v", run)
	}
	decodeInto(t, requestJSON(t, f.viewerC, http.MethodGet, url+"/deliveries", nil), &ds)
	for _, d := range ds.Deliveries {
		if d.Outcome != "collected" || d.JobGroupID == "" {
			t.Fatalf("after run now = %+v", ds)
		}
	}
	if p := readPending(); p.Pending != nil {
		t.Fatalf("pending after run now = %+v", p.Pending)
	}
	if resp := postJSON(t, f.operatorC, url+"/pending/run", nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("run now with nothing pending: %d", resp.StatusCode)
	}
	body := []byte(`{"ref":"v3"}`)
	hook(t, tr.URL, body, signedHeaders(tr, body))
	if resp := requestJSON(t, f.operatorC, http.MethodDelete, url+"/pending", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("discard: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if p := readPending(); p.Pending != nil {
		t.Fatalf("pending after discard = %+v", p.Pending)
	}
	if resp := requestJSON(t, f.operatorC, http.MethodDelete, url+"/pending", nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("discard with nothing pending: %d", resp.StatusCode)
	}
	for _, action := range []string{"POST runPendingTrigger", "DELETE discardPendingTrigger"} {
		assertActivityContains(t, f, action)
	}
}

// A collected delivery whose window a later delivery superseded (the
// settle delay was turned off while it waited) names the run that took its
// place.
func TestTriggerDeliverySupersededBy(t *testing.T) {
	f := newRunFixture(t, "trig-superseded")
	tr := f.createTrigger(t, func(in *triggers.Input) { in.SettleSeconds = 60 })
	body := []byte(`{"ref":"v1"}`)
	hook(t, tr.URL, body, signedHeaders(tr, body))
	ctx := db.WithOrgScope(context.Background(), f.orgID)
	cur, err := f.s.Deps.Triggers.Get(ctx, f.orgID, tr.Trigger.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := triggers.Input{Kind: cur.Kind, TemplateID: cur.TemplateID, Name: cur.Name, Enabled: true, Preset: cur.Preset, Mapping: cur.Mapping}
	if _, err := f.s.Deps.Triggers.Update(ctx, f.orgID, cur.ID, cur.Revision, in); err != nil {
		t.Fatal(err)
	}
	body = []byte(`{"ref":"v2"}`)
	if resp := hook(t, tr.URL, body, signedHeaders(tr, body)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("hook: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var ds settleDeliveries
	decodeInto(t, requestJSON(t, f.viewerC, http.MethodGet, f.triggersURL()+"/"+tr.Trigger.ID.String()+"/deliveries", nil), &ds)
	if len(ds.Deliveries) != 2 || ds.Deliveries[0].Outcome != "triggered" || ds.Deliveries[1].Outcome != "collected" ||
		ds.Deliveries[1].SupersededBy == "" || ds.Deliveries[1].SupersededBy != ds.Deliveries[0].JobGroupID {
		t.Fatalf("deliveries = %+v", ds)
	}
}

// An assistant's trigger request keeps the settle delay it does not name
// and changes the one it does, as the picture shows (backlog 2.137).
func TestTriggerRequestKeepsAndChangesTheSettleDelay(t *testing.T) {
	f := newScheduleRequestFixture(t, "tr-settle")
	c := f.createTrigger(t, func(in *triggers.Input) { in.Name, in.SettleSeconds, in.MaxWaitSeconds = "hook", 30, intPtr(600) })
	id := c.Trigger.ID.String()
	upd, _ := f.askTrigger(t, "/triggers/"+id+"/update-requests", map[string]any{"name": "release hook", "reason": "clearer name"}, http.StatusCreated)
	pt := upd.Picture.Trigger
	for which, s := range map[string]string{"current": string(pt.Current), "new": string(pt.New)} {
		if !strings.Contains(s, `"settle_seconds":30`) || !strings.Contains(s, `"max_wait_seconds":600`) {
			t.Fatalf("%s pictured = %s", which, s)
		}
	}
	f.approveTrigger(t, upd.Request.ID, http.StatusOK)
	if tr := f.storedTrigger(t, id); tr.Name != "release hook" || tr.SettleSeconds != 30 || tr.MaxWaitSeconds == nil || *tr.MaxWaitSeconds != 600 {
		t.Fatalf("a rename turned the settle delay off: %+v", tr)
	}
	upd, _ = f.askTrigger(t, "/triggers/"+id+"/update-requests", map[string]any{"settle_seconds": 10, "max_wait_seconds": 0, "reason": "react faster"}, http.StatusCreated)
	if n := string(upd.Picture.Trigger.New); !strings.Contains(n, `"settle_seconds":10`) || strings.Contains(n, "max_wait_seconds") {
		t.Fatalf("new pictured = %s", n)
	}
	f.approveTrigger(t, upd.Request.ID, http.StatusOK)
	if tr := f.storedTrigger(t, id); tr.SettleSeconds != 10 || tr.MaxWaitSeconds != nil {
		t.Fatalf("changed settle delay = %+v", tr)
	}
	body := f.triggerBody()
	body["name"], body["settle_seconds"], body["max_wait_seconds"] = "settling hook", 20, 120
	created, _ := f.askTrigger(t, "/trigger-requests", body, http.StatusCreated)
	if n := string(created.Picture.Trigger.New); !strings.Contains(n, `"settle_seconds":20`) || !strings.Contains(n, `"max_wait_seconds":120`) {
		t.Fatalf("create pictured = %s", n)
	}
	f.approveTrigger(t, created.Request.ID, http.StatusOK)
	if tr := f.storedTrigger(t, created.Request.TargetID); tr.SettleSeconds != 20 || *tr.MaxWaitSeconds != 120 {
		t.Fatalf("created = %+v", tr)
	}
}

func intPtr(v int) *int { return &v }
