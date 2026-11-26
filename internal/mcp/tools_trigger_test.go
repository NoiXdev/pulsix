package mcp_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/triggers"
)

type triggerRequestResult struct {
	RequestID   string `json:"request_id"`
	Status      string `json:"status"`
	ApprovalURL string `json:"approval_url"`
	Note        string `json:"note"`
	Picture     struct {
		Trigger struct {
			Action    string         `json:"action"`
			TriggerID string         `json:"trigger_id"`
			URL       string         `json:"url"`
			New       map[string]any `json:"new"`
		} `json:"trigger"`
	} `json:"picture"`
}

func (f orgFixture) approveTrigger(t *testing.T, id string) runrequests.Approved {
	t.Helper()
	rid := uuid.MustParse(id)
	_, _, fp, err := f.s.Deps.RunRequests.Get(f.scoped, f.org, rid)
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.s.Deps.RunRequests.ApproveWith(f.scoped, f.org, rid, f.owner, fp, nil, runrequests.Approver{WriteTemplates: true, WriteRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f orgFixture) seedTrigger(t *testing.T) triggers.Created {
	t.Helper()
	c, err := f.s.Deps.Triggers.Create(f.scoped, f.org, f.owner, triggers.Input{
		Kind: triggers.KindTemplateRun, TemplateID: &f.template, Name: "seeded hook", Enabled: true, Preset: triggers.PresetGitHub,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// create_trigger asks for a webhook trigger: the assistant gets the
// request, its picture and the URL, never a secret; the approval alone
// draws one. get_trigger and get_run_request show the trigger's URL and
// settings afterwards, still without it.
func TestCreateTriggerToolAsksForApproval(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaAutomationRequest, tenancy.AreaTemplatesRead)
	cs := mustConnect(t, f.s, f.token)
	var req triggerRequestResult
	text := callOK(t, cs, "create_trigger", map[string]any{
		"kind": "template_run", "template_id": f.template.String(), "name": "deploy on tag", "preset": "github",
		"allow_cidrs": []string{"192.0.2.0/24"}, "rate_limit": map[string]any{"count": 2, "per": "1m"}, "reason": "deploy every tag",
	}, &req)
	if req.Status != "pending" || !strings.HasSuffix(req.ApprovalURL, "/runs/requests/"+req.RequestID) || req.Picture.Trigger.Action != "create" ||
		req.Picture.Trigger.New["name"] != "deploy on tag" || !strings.Contains(req.Picture.Trigger.URL, "/api/v1/hooks/jobs/"+req.Picture.Trigger.TriggerID) ||
		!strings.Contains(req.Note, "secret") {
		t.Fatalf("create_trigger = %+v", req)
	}
	if strings.Contains(text, `"secret"`) || strings.Contains(text, "trigger_secret") {
		t.Fatalf("create_trigger output carries a secret field:\n%s", text)
	}
	a := f.approveTrigger(t, req.RequestID)
	if a.TriggerSecret == "" {
		t.Fatal("the approval drew no secret")
	}
	for _, tool := range []struct {
		name string
		args map[string]any
	}{
		{"get_trigger", map[string]any{"trigger_id": req.Picture.Trigger.TriggerID}},
		{"list_triggers", map[string]any{"template_id": f.template.String()}},
		{"get_run_request", map[string]any{"request_id": req.RequestID}},
	} {
		out := callOK(t, cs, tool.name, tool.args, nil)
		if strings.Contains(out, a.TriggerSecret) || !strings.Contains(out, req.Picture.Trigger.TriggerID) {
			t.Fatalf("%s output = %s", tool.name, out)
		}
	}
}

// update_trigger with only enabled false disables the trigger at once; any
// other change, enabling included, is a request. rotate_trigger_secret is
// a request too, delete_trigger acts at once.
func TestUpdateRotateDeleteTriggerTools(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaAutomationRequest, tenancy.AreaTemplatesRead)
	c := f.seedTrigger(t)
	id := c.Trigger.ID.String()
	cs := mustConnect(t, f.s, f.token)

	text := callOK(t, cs, "update_trigger", map[string]any{"trigger_id": id, "enabled": false}, nil)
	if !strings.Contains(text, `"enabled":false`) || strings.Contains(text, "request_id") {
		t.Fatalf("disable = %s", text)
	}
	if tr, _ := f.s.Deps.Triggers.Get(f.scoped, f.org, c.Trigger.ID); tr.Enabled {
		t.Fatal("update_trigger enabled=false left the trigger enabled")
	}
	if list, _ := f.s.Deps.RunRequests.List(f.scoped, f.org, nil); len(list) != 0 {
		t.Fatalf("disabling made a request: %+v", list)
	}

	var upd triggerRequestResult
	callOK(t, cs, "update_trigger", map[string]any{"trigger_id": id, "enabled": true, "name": "renamed", "reason": "back on"}, &upd)
	if upd.Status != "pending" || upd.Picture.Trigger.Action != "update" || upd.Picture.Trigger.New["name"] != "renamed" || upd.Picture.Trigger.New["enabled"] != true {
		t.Fatalf("update_trigger = %+v", upd)
	}
	f.approveTrigger(t, upd.RequestID)
	if tr, _ := f.s.Deps.Triggers.Get(f.scoped, f.org, c.Trigger.ID); !tr.Enabled || tr.Name != "renamed" {
		t.Fatalf("after the approved update = %+v", tr)
	}

	var rot triggerRequestResult
	text = callOK(t, cs, "rotate_trigger_secret", map[string]any{"trigger_id": id, "reason": "leaked"}, &rot)
	if rot.Picture.Trigger.Action != "rotate" || strings.Contains(text, c.Secret) {
		t.Fatalf("rotate_trigger_secret = %s", text)
	}
	a := f.approveTrigger(t, rot.RequestID)
	if a.TriggerSecret == "" || a.TriggerSecret == c.Secret {
		t.Fatalf("rotation = %+v", a)
	}

	callOK(t, cs, "delete_trigger", map[string]any{"trigger_id": id}, nil)
	if _, err := f.s.Deps.Triggers.Get(f.scoped, f.org, c.Trigger.ID); err == nil {
		t.Fatal("the trigger survived delete_trigger")
	}
}

// A mapping onto a name Ansible acts on reaches the assistant as a tool
// error naming it.
func TestCreateTriggerToolRefusesAReservedMapping(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaAutomationRequest)
	cs := mustConnect(t, f.s, f.token)
	text, isErr := call(t, cs, "create_trigger", map[string]any{
		"kind": "template_run", "template_id": f.template.String(), "name": "bad", "preset": "pulsix",
		"mapping": map[string]any{"extra_vars": map[string]string{"ansible_become": "$.x"}}, "reason": "x",
	})
	if !isErr || !strings.Contains(text, "ansible_become") {
		t.Fatalf("a reserved mapping = %v %s", isErr, text)
	}
}

// create_trigger and update_trigger carry the settle delay (backlog
// 2.137); a change that leaves it out keeps it; get_trigger and
// list_triggers show it.
func TestTriggerToolsCarryTheSettleDelay(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaAutomationRequest, tenancy.AreaTemplatesRead)
	cs := mustConnect(t, f.s, f.token)
	var req triggerRequestResult
	callOK(t, cs, "create_trigger", map[string]any{
		"kind": "template_run", "template_id": f.template.String(), "name": "settling hook", "preset": "github",
		"settle_seconds": 30, "max_wait_seconds": 600, "reason": "one run per burst of pushes",
	}, &req)
	if req.Picture.Trigger.New["settle_seconds"] != float64(30) || req.Picture.Trigger.New["max_wait_seconds"] != float64(600) {
		t.Fatalf("create_trigger picture = %+v", req.Picture.Trigger.New)
	}
	f.approveTrigger(t, req.RequestID)
	id := req.Picture.Trigger.TriggerID
	for _, tool := range []struct {
		name string
		args map[string]any
	}{
		{"get_trigger", map[string]any{"trigger_id": id}},
		{"list_triggers", map[string]any{"template_id": f.template.String()}},
	} {
		if out := callOK(t, cs, tool.name, tool.args, nil); !strings.Contains(out, `"settle_seconds":30`) || !strings.Contains(out, `"max_wait_seconds":600`) {
			t.Fatalf("%s output = %s", tool.name, out)
		}
	}
	var upd triggerRequestResult
	callOK(t, cs, "update_trigger", map[string]any{"trigger_id": id, "name": "renamed", "reason": "clearer"}, &upd)
	f.approveTrigger(t, upd.RequestID)
	tr, err := f.s.Deps.Triggers.Get(f.scoped, f.org, uuid.MustParse(id))
	if err != nil || tr.SettleSeconds != 30 || tr.MaxWaitSeconds == nil || *tr.MaxWaitSeconds != 600 {
		t.Fatalf("a rename changed the settle delay: %+v %v", tr, err)
	}
	upd = triggerRequestResult{}
	callOK(t, cs, "update_trigger", map[string]any{"trigger_id": id, "settle_seconds": 0, "reason": "every push again"}, &upd)
	if _, ok := upd.Picture.Trigger.New["settle_seconds"]; ok {
		t.Fatalf("settle off pictured = %+v", upd.Picture.Trigger.New)
	}
	f.approveTrigger(t, upd.RequestID)
	if tr, _ := f.s.Deps.Triggers.Get(f.scoped, f.org, uuid.MustParse(id)); tr.SettleSeconds != 0 || tr.MaxWaitSeconds != nil {
		t.Fatalf("settle off = %+v", tr)
	}
}
