package api_test

import (
	"net/http"
	"testing"
	"time"
)

type maintenanceWindowOut struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	TargetKind  string `json:"target_kind"`
	TargetID    string `json:"target_id"`
	TargetName  string `json:"target_name"`
	Cron        string `json:"cron"`
	Description string `json:"description"`
	Duration    string `json:"duration"`
	ActiveNow   bool   `json:"active_now"`
	Revision    int64  `json:"revision"`
}

func (f runFixture) maintenanceURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/maintenance-windows"
}

func TestMaintenanceWindowLifecycle(t *testing.T) {
	f := newRunFixture(t, "maint-life")
	now := time.Now().UTC()
	oneOff := map[string]any{
		"name": "db upgrade", "target_kind": "host", "target_id": f.hostA.String(),
		"starts_at": now.Add(-time.Hour).Format(time.RFC3339), "ends_at": now.Add(time.Hour).Format(time.RFC3339),
	}
	// Viewers see windows; operators plan them.
	if resp := postJSON(t, f.viewerC, f.maintenanceURL(), oneOff); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create = %d", resp.StatusCode)
	}
	resp := postJSON(t, f.operatorC, f.maintenanceURL(), oneOff)
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("create = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created struct {
		Window maintenanceWindowOut `json:"window"`
	}
	decodeInto(t, resp, &created)
	if created.Window.TargetName != "web-1" || created.Window.TargetID != f.hostA.String() {
		t.Fatalf("created = %+v", created.Window)
	}
	recurring := map[string]any{"name": "nightly", "target_kind": "org", "cron": "0 3 * * *", "duration": "90m", "timezone": "Europe/Berlin"}
	resp = postJSON(t, f.operatorC, f.maintenanceURL(), recurring)
	var rec struct {
		Window maintenanceWindowOut `json:"window"`
	}
	decodeInto(t, resp, &rec)
	if rec.Window.Duration != "1h30m" || rec.Window.Description == "" || rec.Window.TargetKind != "org" {
		t.Fatalf("recurring = %+v", rec.Window)
	}

	var list struct {
		Windows []maintenanceWindowOut `json:"windows"`
	}
	decodeInto(t, requestJSON(t, f.viewerC, http.MethodGet, f.maintenanceURL(), nil), &list)
	if len(list.Windows) != 2 || list.Windows[0].Name != "db upgrade" || !list.Windows[0].ActiveNow {
		t.Fatalf("list = %+v", list.Windows)
	}

	recurring["duration"] = "2h"
	if resp := requestWithHeaders(t, f.operatorC, http.MethodPut, f.maintenanceURL()+"/"+rec.Window.ID, recurring, map[string]string{"If-Match": `"9"`}); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale update = %d", resp.StatusCode)
	}
	resp = requestWithHeaders(t, f.operatorC, http.MethodPut, f.maintenanceURL()+"/"+rec.Window.ID, recurring, map[string]string{"If-Match": `"1"`})
	decodeInto(t, resp, &rec)
	if rec.Window.Duration != "2h" || rec.Window.Revision != 2 {
		t.Fatalf("updated = %+v", rec.Window)
	}

	for name, body := range map[string]map[string]any{
		"both modes":     {"name": "x", "target_kind": "org", "cron": "@daily", "duration": "1h", "timezone": "UTC", "starts_at": now.Format(time.RFC3339), "ends_at": now.Add(time.Hour).Format(time.RFC3339)},
		"unknown target": {"name": "y", "target_kind": "host", "target_id": "0197c0a2-0000-7000-8000-000000000001", "starts_at": now.Format(time.RFC3339), "ends_at": now.Add(time.Hour).Format(time.RFC3339)},
		"bad target id":  {"name": "z", "target_kind": "host", "target_id": "nope", "starts_at": now.Format(time.RFC3339), "ends_at": now.Add(time.Hour).Format(time.RFC3339)},
	} {
		if resp := postJSON(t, f.operatorC, f.maintenanceURL(), body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, resp.StatusCode)
		}
	}
	if resp := postJSON(t, f.operatorC, f.maintenanceURL(), oneOff); resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate name = %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.maintenanceURL()+"/"+created.Window.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.maintenanceURL()+"/"+created.Window.ID, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete = %d", resp.StatusCode)
	}
}
