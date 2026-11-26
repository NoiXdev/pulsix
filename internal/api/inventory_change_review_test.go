package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/triggers"
)

// inventoryReviewFixture is the change review fixture (template "deploy"
// on web-1 with an active schedule, one a person paused and a trigger)
// with a second template "other" on web-2 and its schedule, and an
// inventory: web-1 in group web_eu below group web, web-2 in group db.
type inventoryReviewFixture struct {
	changeReviewFixture
	invTok              string
	other               uuid.UUID
	web, webEU, dbGroup inventory.GroupView
}

func newInventoryReviewFixture(t *testing.T, slug string) inventoryReviewFixture {
	t.Helper()
	f := inventoryReviewFixture{changeReviewFixture: newChangeReviewFixture(t, slug)}
	f.invTok = f.s.AssistantTokenFor(t, f.orgID, f.operator, tenancy.RoleOperator, tenancy.AreaInventoryWrite)
	tmpl, err := f.s.Deps.Templates.Create(f.scoped, f.orgID, templates.CreateInput{
		Name: "other", PlaybookID: f.playbook, Scope: templates.Scope{HostIDs: []uuid.UUID{f.hostB}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.s.Deps.Schedules.Create(f.scoped, f.orgID, f.operator, schedules.Input{
		TemplateID: tmpl.ID, Name: "other-nightly", Mode: schedules.ModeCron, Cron: "0 4 * * *", Timezone: "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.other = s.ID
	inv := f.s.Deps.Inventory
	sc := schema.Schema{Fields: []schema.Field{
		{Name: "port", Type: schema.TypeInt}, {Name: "pw", Type: schema.TypeSecret},
		{Name: "note", Type: schema.TypeString, Scope: schema.ScopeHost},
	}}
	group := func(name string, parent *uuid.UUID) inventory.GroupView {
		g, err := inv.CreateGroup(f.scoped, f.orgID, inventory.GroupInput{Name: name, ParentID: parent})
		if err != nil {
			t.Fatal(err)
		}
		if g, _, err = inv.SetSchema(f.scoped, f.orgID, g.ID, g.Revision, sc); err != nil {
			t.Fatal(err)
		}
		return g
	}
	f.web = group("web", nil)
	f.webEU = group("web_eu", &f.web.ID)
	f.dbGroup = group("db", nil)
	// A person's changes pause nothing.
	if f.web, err = inv.SetVars(f.scoped, f.orgID, f.web.ID, f.web.Revision, map[string]any{"port": 80.0, "pw": "pw-inventory-s3cret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.Assign(f.scoped, f.orgID, f.webEU.ID, f.hostA, map[string]any{"note": "a"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.Assign(f.scoped, f.orgID, f.dbGroup.ID, f.hostB, map[string]any{"note": "b"}, nil); err != nil {
		t.Fatal(err)
	}
	f.wantRunning(t, "after a person's changes")
	return f
}

func (f inventoryReviewFixture) groupsPath(id uuid.UUID) string {
	return f.orgPath() + "/inventory/groups/" + id.String()
}

// wantRunning fails unless every schedule runs and nothing was reviewed.
func (f inventoryReviewFixture) wantRunning(t *testing.T, when string) {
	t.Helper()
	for _, id := range []uuid.UUID{f.active, f.other} {
		if paused, _ := f.scheduleState(t, id); paused {
			t.Fatalf("%s: schedule %s paused", when, id)
		}
	}
	if enabled, _ := f.triggerState(t, f.hook); !enabled {
		t.Fatalf("%s: trigger disabled", when)
	}
	if r := f.reviews(t); len(r) != 0 {
		t.Fatalf("%s: reviews %v", when, r)
	}
}

// TestAnAssistantsGroupChangePausesWhatRunsOnItsMembers (decision D6)
// through the real stores: an assistant's change of group web's values
// reaches web-1, a member of its descendant group web_eu, so the
// schedules of template deploy (on web-1) are paused, its trigger
// disabled and each reviewed; template other (on web-2) keeps running.
// The review's picture shows the group, each key's old and new value with
// the secret masked, and the token.
func TestAnAssistantsGroupChangePausesWhatRunsOnItsMembers(t *testing.T) {
	f := newInventoryReviewFixture(t, "inv-review-group")
	resp := f.s.AssistantDo(t, http.MethodPut, f.invTok, f.groupsPath(f.web.ID)+"/vars", `"`+strconv.FormatInt(f.web.Revision, 10)+`"`,
		map[string]any{"vars": map[string]any{"port": 8080}})
	if resp.Code != http.StatusOK {
		t.Fatalf("assistant PUT vars = %d %s", resp.Code, resp.Body)
	}
	if paused, reason := f.scheduleState(t, f.active); !paused || reason != schedules.PausedForReview {
		t.Fatalf("deploy's schedule = %v %q", paused, reason)
	}
	if paused, reason := f.scheduleState(t, f.byPerson); !paused || reason != schedules.PausedForReview {
		t.Fatalf("deploy's paused schedule = %v %q", paused, reason)
	}
	if enabled, reason := f.triggerState(t, f.hook); enabled || reason != triggers.DisabledForReview {
		t.Fatalf("deploy's trigger = %v %q", enabled, reason)
	}
	if paused, _ := f.scheduleState(t, f.other); paused {
		t.Fatal("other's schedule paused, but no host of it is in group web")
	}
	reviews := f.reviews(t)
	if len(reviews[f.active]) != 1 || len(reviews[f.byPerson]) != 1 || len(reviews[f.hook]) != 1 || len(reviews[f.other]) != 0 {
		t.Fatalf("reviews = %v", reviews)
	}
	if ev := f.events(t, "pauseScheduleForReview"); len(ev) != 2 || !strings.Contains(ev[0], "an assistant changed inventory group web (vars.port, vars.pw)") {
		t.Fatalf("events = %v", ev)
	}
	id := strings.SplitN(reviews[f.active][0], ":", 2)[0]
	d := f.detail(t, id)
	var p struct {
		Review struct {
			Changes []struct {
				Subject string `json:"subject"`
				Name    string `json:"name"`
				TokenID string `json:"token_id"`
				Fields  []struct {
					Field string `json:"field"`
					Old   any    `json:"old"`
					New   any    `json:"new"`
				} `json:"fields"`
			} `json:"changes"`
		} `json:"review"`
	}
	if err := json.Unmarshal(d.Picture, &p); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(d.Picture), "pw-inventory-s3cret") {
		t.Fatalf("the picture shows the secret: %s", d.Picture)
	}
	c := p.Review.Changes
	if len(c) != 1 || c[0].Subject != "inventory_group" || c[0].Name != "web" || c[0].TokenID == "" || len(c[0].Fields) != 2 ||
		c[0].Fields[0].Field != "vars.port" || c[0].Fields[0].Old != 80.0 || c[0].Fields[0].New != 8080.0 ||
		c[0].Fields[1].Field != "vars.pw" || c[0].Fields[1].New != nil {
		t.Fatalf("picture changes = %s", d.Picture)
	}
	if old, _ := c[0].Fields[1].Old.(map[string]any); old["$secret"] != "set" {
		t.Fatalf("the removed secret shows as %v", c[0].Fields[1].Old)
	}
}

// A membership change reaches only its host: the assistant's change of
// web-2's values in group db pauses template other, not deploy.
func TestAnAssistantsMembershipChangePausesOnlyWhatRunsOnThatHost(t *testing.T) {
	f := newInventoryReviewFixture(t, "inv-review-member")
	if resp := f.s.AssistantDo(t, http.MethodPut, f.invTok, f.memberPath(f.dbGroup.ID, f.hostB), "", map[string]any{"vars": map[string]any{"note": "changed"}}); resp.Code != http.StatusOK {
		t.Fatalf("change = %d %s", resp.Code, resp.Body)
	}
	if paused, reason := f.scheduleState(t, f.other); !paused || reason != schedules.PausedForReview {
		t.Fatalf("other's schedule = %v %q", paused, reason)
	}
	if paused, _ := f.scheduleState(t, f.active); paused {
		t.Fatal("deploy's schedule paused, but web-1's values did not change")
	}
	if enabled, _ := f.triggerState(t, f.hook); !enabled {
		t.Fatal("deploy's trigger disabled")
	}
	if ev := f.events(t, "pauseScheduleForReview"); len(ev) != 1 || !strings.Contains(ev[0], "host web-2 in inventory group db (vars.note)") {
		t.Fatalf("events = %v", ev)
	}
}

func (f inventoryReviewFixture) memberPath(g, host uuid.UUID) string {
	return f.groupsPath(g) + "/members/" + host.String()
}

// scheduleOn adds a template with scope and a schedule of it; it returns
// the schedule.
func (f inventoryReviewFixture) scheduleOn(t *testing.T, name string, scope templates.Scope) uuid.UUID {
	t.Helper()
	tmpl, err := f.s.Deps.Templates.Create(f.scoped, f.orgID, templates.CreateInput{Name: name, PlaybookID: f.playbook, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.s.Deps.Schedules.Create(f.scoped, f.orgID, f.operator, schedules.Input{
		TemplateID: tmpl.ID, Name: name + "-nightly", Mode: schedules.ModeCron, Cron: "0 5 * * *", Timezone: "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	return s.ID
}

func (f inventoryReviewFixture) wantPaused(t *testing.T, when string, want map[uuid.UUID]bool) {
	t.Helper()
	for id, w := range want {
		if paused, _ := f.scheduleState(t, id); paused != w {
			t.Errorf("%s: schedule %s paused = %v, want %v", when, id, paused, w)
		}
	}
}

func (f inventoryReviewFixture) assistantOK(t *testing.T, method, path, ifMatch string, body any) {
	t.Helper()
	if resp := f.s.AssistantDo(t, method, f.invTok, path, ifMatch, body); resp.Code != http.StatusOK && resp.Code != http.StatusCreated && resp.Code != http.StatusNoContent {
		t.Fatalf("%s %s = %d %s", method, path, resp.Code, resp.Body)
	}
}

func revOf(rev int64) string { return `"` + strconv.FormatInt(rev, 10) + `"` }

// TestAnAssistantsInventoryChurnPausesWhatItReaches (decision D7): an
// assistant's host joining or leaving a group, its move of a group and its
// managed host each pause what runs on the hosts that then receive other
// values or reach other hosts; the bypasses the final review named are
// closed. A template's limit does not narrow it (final review I2), and a
// schema edit that drops a value pauses like a value change (I3).
func TestAnAssistantsInventoryChurnPausesWhatItReaches(t *testing.T) {
	t.Run("remove then add with new values", func(t *testing.T) {
		f := newInventoryReviewFixture(t, "inv-d7-swap")
		f.assistantOK(t, http.MethodDelete, f.memberPath(f.dbGroup.ID, f.hostB), "", nil)
		f.wantPaused(t, "after the removal", map[uuid.UUID]bool{f.other: true, f.active: false})
		f.assistantOK(t, http.MethodPut, f.memberPath(f.dbGroup.ID, f.hostB), "", map[string]any{"vars": map[string]any{"note": "swapped"}})
		reviews := f.reviews(t)
		if len(reviews[f.other]) != 1 || len(reviews[f.active]) != 0 {
			t.Fatalf("reviews = %v", reviews)
		}
		d := f.detail(t, strings.SplitN(reviews[f.other][0], ":", 2)[0])
		if n := strings.Count(string(d.Picture), `"subject":"inventory_membership"`); n != 2 || !strings.Contains(string(d.Picture), `"field":"member"`) {
			t.Fatalf("the review lists %d membership changes: %s", n, d.Picture)
		}
	})
	t.Run("values on an empty group, then members", func(t *testing.T) {
		f := newInventoryReviewFixture(t, "inv-d7-empty")
		fresh, err := f.s.Deps.Inventory.CreateGroup(f.scoped, f.orgID, inventory.GroupInput{Name: "fresh"})
		if err != nil {
			t.Fatal(err)
		}
		if fresh, _, err = f.s.Deps.Inventory.SetSchema(f.scoped, f.orgID, fresh.ID, fresh.Revision,
			schema.Schema{Fields: []schema.Field{{Name: "port", Type: schema.TypeInt}}}); err != nil {
			t.Fatal(err)
		}
		f.assistantOK(t, http.MethodPut, f.groupsPath(fresh.ID)+"/vars", revOf(fresh.Revision), map[string]any{"vars": map[string]any{"port": 1}})
		f.wantRunning(t, "after values on an empty group")
		f.assistantOK(t, http.MethodPut, f.memberPath(fresh.ID, f.hostA), "", map[string]any{"vars": map[string]any{}})
		f.wantPaused(t, "after web-1 joined", map[uuid.UUID]bool{f.active: true, f.other: false})
	})
	t.Run("update_group moves a group", func(t *testing.T) {
		f := newInventoryReviewFixture(t, "inv-d7-move")
		underWeb := f.scheduleOn(t, "web-wide", templates.Scope{GroupID: &f.web.ID})
		underDB := f.scheduleOn(t, "db-wide", templates.Scope{GroupID: &f.dbGroup.ID})
		f.wantRunning(t, "before the move")
		eu, err := f.s.Deps.Inventory.GetGroup(f.scoped, f.orgID, f.webEU.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.assistantOK(t, http.MethodPatch, f.groupsPath(eu.ID), revOf(eu.Revision), map[string]any{"name": "web_eu", "parent_id": f.dbGroup.ID.String()})
		// web-1 leaves web's line (web-wide no longer reaches it) and joins
		// db's (db-wide now does; deploy's run receives db's values).
		f.wantPaused(t, "after the move", map[uuid.UUID]bool{underWeb: true, underDB: true, f.active: true, f.other: false})
	})
	t.Run("create_host into a scoped group", func(t *testing.T) {
		f := newInventoryReviewFixture(t, "inv-d7-host")
		everywhere := f.scheduleOn(t, "everywhere", templates.Scope{})
		dbWide := f.scheduleOn(t, "db-wide", templates.Scope{GroupID: &f.dbGroup.ID})
		resp := f.s.AssistantDo(t, http.MethodPost, f.invTok, f.orgPath()+"/hosts", "",
			map[string]any{"name": "db-9", "address": "10.0.9.9", "zone_id": f.zoneA.String(), "managed": true})
		if resp.Code != http.StatusCreated && resp.Code != http.StatusOK {
			t.Fatalf("create host = %d %s", resp.Code, resp.Body)
		}
		f.wantPaused(t, "after the host was created", map[uuid.UUID]bool{everywhere: true, dbWide: false, f.active: false, f.other: false})
		var created struct {
			Host struct {
				ID string `json:"id"`
			} `json:"host"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil || created.Host.ID == "" {
			t.Fatalf("created = %s (%v)", resp.Body, err)
		}
		f.assistantOK(t, http.MethodPut, f.groupsPath(f.dbGroup.ID)+"/members/"+created.Host.ID, "", map[string]any{"vars": map[string]any{}})
		f.wantPaused(t, "after it joined db", map[uuid.UUID]bool{dbWide: true, f.other: false})
		if resp := f.s.AssistantDo(t, http.MethodPost, f.invTok, f.orgPath()+"/hosts", "",
			map[string]any{"name": "db-10", "address": "{{ lookup('pipe','id') }}", "zone_id": f.zoneA.String()}); resp.Code < 400 {
			t.Fatalf("a templated address = %d %s", resp.Code, resp.Body)
		}
	})
	t.Run("a limit does not narrow the reach", func(t *testing.T) {
		f := newInventoryReviewFixture(t, "inv-d7-limit")
		if _, err := f.s.DB.Pool().Exec(context.Background(), `UPDATE job_templates SET "limit" = 'db*' WHERE id = $1`, f.templateID); err != nil {
			t.Fatal(err)
		}
		g, err := f.s.Deps.Inventory.GetGroup(f.scoped, f.orgID, f.web.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.assistantOK(t, http.MethodPut, f.groupsPath(f.web.ID)+"/vars", revOf(g.Revision), map[string]any{"vars": map[string]any{"port": 81}})
		f.wantPaused(t, "after the change", map[uuid.UUID]bool{f.active: true, f.other: false})
	})
	t.Run("a schema edit that drops a value", func(t *testing.T) {
		f := newInventoryReviewFixture(t, "inv-d7-schema")
		g, err := f.s.Deps.Inventory.GetGroup(f.scoped, f.orgID, f.web.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.assistantOK(t, http.MethodPut, f.groupsPath(f.web.ID)+"/schema", revOf(g.Revision), map[string]any{"schema": map[string]any{"fields": []any{
			map[string]any{"name": "port", "type": "int", "active": false},
			map[string]any{"name": "pw", "type": "secret"},
			map[string]any{"name": "note", "type": "string", "scope": "host"},
		}}})
		f.wantPaused(t, "after the edit", map[uuid.UUID]bool{f.active: true, f.other: false})
		if ev := f.events(t, "pauseScheduleForReview"); len(ev) == 0 || !strings.Contains(ev[0], "vars.port") {
			t.Fatalf("events = %v", ev)
		}
	})
	t.Run("a group renamed or given a jump host", func(t *testing.T) {
		f := newInventoryReviewFixture(t, "inv-d7-rename")
		bastion, err := f.s.Deps.SSH.CreateBastion(f.scoped, f.orgID, sshconf.BastionInput{Name: "jump", Address: "10.0.0.9", Port: 22, Username: "ops"})
		if err != nil {
			t.Fatal(err)
		}
		db, err := f.s.Deps.Inventory.GetGroup(f.scoped, f.orgID, f.dbGroup.ID)
		if err != nil {
			t.Fatal(err)
		}
		// group_names and groups[...] change for web-2: other pauses.
		f.assistantOK(t, http.MethodPatch, f.groupsPath(db.ID), revOf(db.Revision), map[string]any{"name": "databases"})
		f.wantPaused(t, "after the rename", map[uuid.UUID]bool{f.other: true, f.active: false})
		eu, err := f.s.Deps.Inventory.GetGroup(f.scoped, f.orgID, f.webEU.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.assistantOK(t, http.MethodPatch, f.groupsPath(eu.ID), revOf(eu.Revision), map[string]any{"name": "web_eu", "parent_id": f.web.ID.String(), "bastion_id": bastion.ID.String()})
		f.wantPaused(t, "after the jump host", map[uuid.UUID]bool{f.active: true})
		if ev := f.events(t, "pauseScheduleForReview"); len(ev) != 3 || !strings.Contains(ev[0], "(name)") || !strings.Contains(ev[1], "(jump_host)") {
			t.Fatalf("events = %v", ev)
		}
	})
	t.Run("a host's SSH settings, zone and delete", func(t *testing.T) {
		f := newInventoryReviewFixture(t, "inv-d7-route")
		bastion, err := f.s.Deps.SSH.CreateBastion(f.scoped, f.orgID, sshconf.BastionInput{Name: "jump", Address: "10.0.0.9", Port: 22, Username: "ops"})
		if err != nil {
			t.Fatal(err)
		}
		f.assistantOK(t, http.MethodPut, f.orgPath()+"/hosts/"+f.hostB.String()+"/ssh", "",
			map[string]any{"username": "ops", "bastion_mode": "explicit", "bastion_id": bastion.ID.String()})
		f.wantPaused(t, "after web-2's SSH settings", map[uuid.UUID]bool{f.other: true, f.active: false})

		f2 := newInventoryReviewFixture(t, "inv-d7-zone")
		h, err := f2.s.Deps.Hosts.GetHost(f2.scoped, f2.orgID, f2.hostA)
		if err != nil {
			t.Fatal(err)
		}
		f2.assistantOK(t, http.MethodPatch, f2.orgPath()+"/hosts/"+h.ID.String(), revOf(h.Revision),
			map[string]any{"name": h.Name, "address": h.Address, "zone_id": f2.zoneB.String(), "managed": true})
		f2.wantPaused(t, "after web-1 moved zone", map[uuid.UUID]bool{f2.active: true, f2.other: false})

		f3 := newInventoryReviewFixture(t, "inv-d7-delete")
		if _, err := f3.s.DB.Pool().Exec(context.Background(), `UPDATE job_templates SET scope = '{}' WHERE id = $1`, f3.templateID); err != nil {
			t.Fatal(err)
		}
		spare, err := f3.s.Deps.Hosts.CreateHost(f3.scoped, f3.orgID, hosts.HostInput{Name: "spare-1", Address: "10.0.0.7", ZoneID: f3.zoneA, Managed: true})
		if err != nil {
			t.Fatal(err)
		}
		f3.assistantOK(t, http.MethodDelete, f3.orgPath()+"/hosts/"+spare.ID.String(), "", nil)
		f3.wantRunning(t, "after a host was deleted")
	})
	t.Run("a managed host's connection is a person's to change", func(t *testing.T) {
		f := newInventoryReviewFixture(t, "inv-n1-connection")
		h, err := f.s.Deps.Hosts.GetHost(f.scoped, f.orgID, f.hostA)
		if err != nil {
			t.Fatal(err)
		}
		resp := f.s.AssistantDo(t, http.MethodPatch, f.invTok, f.orgPath()+"/hosts/"+h.ID.String(), revOf(h.Revision),
			map[string]any{"name": h.Name, "address": "10.9.9.9", "zone_id": h.ZoneID.String(), "managed": true})
		if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "a person has to change where a managed host is reached") {
			t.Fatalf("address change = %d %s", resp.Code, resp.Body)
		}
		f.wantRunning(t, "after the refused address change")
		// A rename of a host with an address is allowed and pauses.
		f.assistantOK(t, http.MethodPatch, f.orgPath()+"/hosts/"+h.ID.String(), revOf(h.Revision),
			map[string]any{"name": "web-1a", "address": h.Address, "zone_id": h.ZoneID.String(), "managed": true})
		f.wantPaused(t, "after the rename", map[uuid.UUID]bool{f.active: true, f.other: false})
		// A connection test with a password to an unpinned host is refused.
		if _, err := f.s.Deps.SSH.SetHostSSH(f.scoped, f.orgID, f.hostB, sshconf.HostSSHInput{CredentialID: &f.sshCred}); err != nil {
			t.Fatal(err)
		}
		resp = f.s.AssistantDo(t, http.MethodPost, f.invTok, f.orgPath()+"/hosts/"+f.hostB.String()+"/connection-test", "", nil)
		if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "the host key must be pinned before an assistant tests with a password") {
			t.Fatalf("password test = %d %s", resp.Code, resp.Body)
		}
	})
}
