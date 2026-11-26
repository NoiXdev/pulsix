package api_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/schedules"
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
// web-2's values in group db pauses template other, not deploy. A host
// joining or leaving a group, with the same token, pauses nothing.
func TestAnAssistantsMembershipChangePausesOnlyWhatRunsOnThatHost(t *testing.T) {
	f := newInventoryReviewFixture(t, "inv-review-member")
	member := func(g uuid.UUID, host uuid.UUID) string { return f.groupsPath(g) + "/members/" + host.String() }
	if resp := f.s.AssistantDo(t, http.MethodPut, f.invTok, member(f.dbGroup.ID, f.hostA), "", map[string]any{"vars": map[string]any{"note": "joined"}}); resp.Code != http.StatusOK {
		t.Fatalf("join = %d %s", resp.Code, resp.Body)
	}
	if resp := f.s.AssistantDo(t, http.MethodDelete, f.invTok, member(f.dbGroup.ID, f.hostA), "", nil); resp.Code != http.StatusOK && resp.Code != http.StatusNoContent {
		t.Fatalf("leave = %d %s", resp.Code, resp.Body)
	}
	f.wantRunning(t, "after a host joined and left")

	if resp := f.s.AssistantDo(t, http.MethodPut, f.invTok, member(f.dbGroup.ID, f.hostB), "", map[string]any{"vars": map[string]any{"note": "changed"}}); resp.Code != http.StatusOK {
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
	if ev := f.events(t, "pauseScheduleForReview"); len(ev) != 1 || !strings.Contains(ev[0], "the values of host web-2 in inventory group db (vars.note)") {
		t.Fatalf("events = %v", ev)
	}
}
