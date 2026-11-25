package e2e

import (
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// automationWrites calls the schedule and webhook trigger tools (spec
// 2026-11-25 assistant API gaps, C): create, update, resume and rotate
// leave a pending request each and change nothing; pause, disable and
// delete act at once, checked through REST. The seeded schedule stays
// paused and unchanged for the later phases.
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
	a.triggerWrites(cs, prefix)
}

// triggerWrites calls the webhook trigger tools on two triggers the owner
// creates: create, update and rotate leave a pending request each and
// change nothing; disabling (update_trigger with enabled false alone) and
// delete act at once. The triggers' secrets join the markers no output may
// carry.
func (a *assistantSuite) triggerWrites(cs *sdk.ClientSession, prefix string) {
	s := a.s
	s.t.Helper()
	type created struct {
		Trigger struct {
			ID string `json:"id"`
		} `json:"trigger"`
		Secret string `json:"secret"`
	}
	seed := func(name string) created {
		var c created
		s.post(prefix+"/triggers", map[string]any{"kind": "template_run", "template_id": a.seeded.templateA, "name": name, "preset": "github"}, &c)
		if c.Trigger.ID == "" || c.Secret == "" {
			s.t.Fatalf("seeding trigger %s = %+v", name, c)
		}
		a.seeded.triggerSecrets = append(a.seeded.triggerSecrets, c.Secret)
		return c
	}
	hook, doomed := seed("asst-hook"), seed("asst-doomed-hook")
	type requested struct {
		RequestID string `json:"request_id"`
		Status    string `json:"status"`
	}
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"create_trigger", map[string]any{"kind": "template_run", "template_id": a.seeded.templateA, "name": "asst-requested-hook", "preset": "pulsix", "reason": "e2e every tool"}},
		{"update_trigger", map[string]any{"trigger_id": hook.Trigger.ID, "name": "asst-renamed-hook", "reason": "e2e every tool"}},
		{"rotate_trigger_secret", map[string]any{"trigger_id": hook.Trigger.ID, "reason": "e2e every tool"}},
	} {
		var out requested
		a.recordOK(cs, call.tool, call.args, &out)
		if out.Status != "pending" || out.RequestID == "" {
			s.t.Fatalf("%s = %+v, want a pending request", call.tool, out)
		}
	}
	type trigRead struct {
		Trigger struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"trigger"`
	}
	var before trigRead
	s.get(prefix+"/triggers/"+hook.Trigger.ID, &before)
	if before.Trigger.Name != "asst-hook" || !before.Trigger.Enabled {
		s.t.Fatalf("the trigger changed before an approval: %+v", before.Trigger)
	}
	a.recordOK(cs, "update_trigger", map[string]any{"trigger_id": hook.Trigger.ID, "enabled": false}, nil)
	var after trigRead
	s.get(prefix+"/triggers/"+hook.Trigger.ID, &after)
	if after.Trigger.Enabled {
		s.t.Fatal("update_trigger with enabled false left the trigger enabled")
	}
	a.recordOK(cs, "list_triggers", map[string]any{"template_id": a.seeded.templateA}, nil)
	a.recordOK(cs, "get_trigger", map[string]any{"trigger_id": hook.Trigger.ID}, nil)
	a.recordOK(cs, "delete_trigger", map[string]any{"trigger_id": doomed.Trigger.ID}, nil)
	if code := s.api.status(http.MethodGet, prefix+"/triggers/"+doomed.Trigger.ID, nil); code != http.StatusNotFound {
		s.t.Fatalf("GET of the deleted trigger = %d, want 404", code)
	}
}
