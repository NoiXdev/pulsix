package e2e

import (
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// automationWrites calls the schedule tools (spec 2026-11-25 assistant
// API gaps, C): create, update and resume leave a pending request each
// and change nothing; pause and delete act at once, checked through REST.
// The seeded schedule stays paused and unchanged for the later phases.
func (a *assistantSuite) automationWrites(cs *sdk.ClientSession) {
	s := a.s
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()
	type requested struct {
		RequestID string `json:"request_id"`
		Status    string `json:"status"`
	}
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"create_schedule", map[string]any{"template_id": a.seeded.templateA, "name": "asst-requested", "mode": "cron", "cron": "0 4 1 1 *", "timezone": "UTC", "reason": "e2e every tool"}},
		{"update_schedule", map[string]any{"schedule_id": a.seeded.scheduleID, "cron": "0 5 1 1 *", "reason": "e2e every tool"}},
		{"resume_schedule", map[string]any{"schedule_id": a.seeded.scheduleID, "reason": "e2e every tool"}},
	} {
		var out requested
		a.recordOK(cs, call.tool, call.args, &out)
		if out.Status != "pending" || out.RequestID == "" {
			s.t.Fatalf("%s = %+v, want a pending request", call.tool, out)
		}
	}
	type schedRead struct {
		Schedule struct {
			Cron   string `json:"cron"`
			Paused bool   `json:"paused"`
		} `json:"schedule"`
	}
	var seeded schedRead
	s.get(prefix+"/schedules/"+a.seeded.scheduleID, &seeded)
	if seeded.Schedule.Cron != "0 3 1 1 *" || !seeded.Schedule.Paused {
		s.t.Fatalf("the seeded schedule changed before an approval: %+v", seeded.Schedule)
	}
	a.recordOK(cs, "pause_schedule", map[string]any{"schedule_id": a.seeded.scheduleID}, nil)

	var doomed struct {
		Schedule struct {
			ID string `json:"id"`
		} `json:"schedule"`
	}
	s.post(prefix+"/schedules", map[string]any{
		"template_id": a.seeded.templateA, "name": "asst-doomed", "mode": "cron", "cron": "0 3 1 1 *", "timezone": "UTC",
	}, &doomed)
	s.post(prefix+"/schedules/"+doomed.Schedule.ID+"/pause", struct{}{}, nil)
	a.recordOK(cs, "delete_schedule", map[string]any{"schedule_id": doomed.Schedule.ID}, nil)
	if code := s.api.status(http.MethodGet, prefix+"/schedules/"+doomed.Schedule.ID, nil); code != http.StatusNotFound {
		s.t.Fatalf("GET of the deleted schedule = %d, want 404", code)
	}
}
