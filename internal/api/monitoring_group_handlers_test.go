// The monitoring group routes' tests (v0.3b-1): the CRUD surface and its
// role matrix, the editor's live preview, the parser's own message on an
// unparseable selector, the cascade a deletion causes and the pushes each
// write makes. Driven through the full API on checksFixture
// (check_handlers_test.go), whose doc comment explains why these subtests
// share one fixture and what keeps them from colliding.
package api_test

import (
	"net/http"
	"strings"
	"testing"
)

type groupDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Selector string `json:"selector"`
	Priority int    `json:"priority"`
}

type previewOut struct {
	Count int `json:"count"`
	Hosts []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Monitored bool   `json:"monitored"`
	} `json:"hosts"`
}

// createGroup posts body and expects 201, returning the created group.
func (f checksFixture) createGroup(t *testing.T, body map[string]any) groupDTO {
	t.Helper()
	resp := postJSON(t, f.operatorC, f.groupsURL(), body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create group = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Group groupDTO `json:"group"`
	}
	decodeInto(t, resp, &out)
	return out.Group
}

func (f checksFixture) listGroups(t *testing.T, c *http.Client) []groupDTO {
	t.Helper()
	var out struct {
		Groups []groupDTO `json:"groups"`
	}
	decodeInto(t, requestJSON(t, c, http.MethodGet, f.groupsURL(), nil), &out)
	return out.Groups
}

// TestMonitoringGroupRoutes drives the whole monitoring group surface on
// one fixture; each subtest names its own group, so none depends on
// another having run.
func TestMonitoringGroupRoutes(t *testing.T) {
	f := newChecksFixture(t, "groups")

	// The editor's live match count, so a selector is never saved blind.
	// It is a read — a viewer may run it, and it writes nothing.
	t.Run("preview answers the matching hosts", func(t *testing.T) {
		resp := postJSON(t, f.viewerC, f.groupsURL()+"/preview", map[string]any{"selector": "role=web"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("preview = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var out previewOut
		decodeInto(t, resp, &out)
		if out.Count != 1 || len(out.Hosts) != 1 || out.Hosts[0].ID != f.web.String() {
			t.Fatalf("preview = %+v, want exactly web-1", out)
		}
		if out.Hosts[0].Name != "web-1" || !out.Hosts[0].Monitored {
			t.Fatalf("preview host = %+v, want the name and the monitored flag", out.Hosts[0])
		}

		// A selector that matches nothing is a successful answer of zero,
		// not an error: that is exactly the case the preview exists to show.
		decodeInto(t, postJSON(t, f.viewerC, f.groupsURL()+"/preview", map[string]any{"selector": "role=nowhere"}), &out)
		if out.Count != 0 || len(out.Hosts) != 0 {
			t.Fatalf("preview of an unmatched selector = %+v, want zero", out)
		}
		if groups := f.listGroups(t, f.viewerC); len(groups) != 0 {
			t.Fatalf("preview persisted %+v", groups)
		}
		if resp := postJSON(t, f.stranger, f.groupsURL()+"/preview", map[string]any{"selector": "role=web"}); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("non-member preview = %d, want 403", resp.StatusCode)
		}
	})

	// A selector that does not parse is the caller's own mistake, answered
	// 400 carrying the parser's message — which names what went wrong — on
	// saving and on previewing alike, so the editor never has to guess why
	// the server said no.
	t.Run("an unparseable selector is refused with the parser's message", func(t *testing.T) {
		bad := map[string]any{"name": "broken", "selector": "role=", "priority": 0}
		resp := postJSON(t, f.operatorC, f.groupsURL(), bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("create with a bad selector = %d, want 400", resp.StatusCode)
		}
		onSave := mustReadBody(t, resp)
		if !strings.Contains(onSave, "selector") && !strings.Contains(onSave, "value") {
			t.Fatalf("refusal %q does not carry the parser's own message", onSave)
		}

		resp = postJSON(t, f.viewerC, f.groupsURL()+"/preview", map[string]any{"selector": "role="})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("preview with a bad selector = %d, want 400", resp.StatusCode)
		}
		if onPreview := mustReadBody(t, resp); onPreview != onSave {
			// Not merely "both are 400": an editor that previews and then
			// saves must not be told two different things about one
			// selector.
			t.Fatalf("preview said %q, saving said %q", onPreview, onSave)
		}

		g := f.createGroup(t, map[string]any{"name": "parse tier", "selector": "role=web"})
		if resp := requestJSON(t, f.operatorC, http.MethodPut, f.groupsURL()+"/"+g.ID, bad); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("update with a bad selector = %d, want 400", resp.StatusCode)
		}
		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.groupsURL()+"/"+g.ID, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("cleanup delete = %d", resp.StatusCode)
		}
	})

	// Create, read, list, replace and delete, with the role matrix pinned
	// along the way: a viewer reads, an operator writes, a non-member sees
	// nothing.
	t.Run("lifecycle and role matrix", func(t *testing.T) {
		body := map[string]any{"name": "life tier", "selector": "role=web", "priority": 10}
		if resp := postJSON(t, f.viewerC, f.groupsURL(), body); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("viewer create = %d, want 403", resp.StatusCode)
		}
		g := f.createGroup(t, body)
		if g.Name != "life tier" || g.Selector != "role=web" || g.Priority != 10 {
			t.Fatalf("created = %+v", g)
		}

		resp := requestJSON(t, f.viewerC, http.MethodGet, f.groupsURL()+"/"+g.ID, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("viewer get = %d", resp.StatusCode)
		}
		var single struct {
			Group groupDTO `json:"group"`
		}
		decodeInto(t, resp, &single)
		if single.Group.ID != g.ID {
			t.Fatalf("get = %+v", single.Group)
		}
		if groups := f.listGroups(t, f.viewerC); len(groups) != 1 || groups[0].Name != "life tier" {
			t.Fatalf("list = %+v", groups)
		}

		if resp := requestJSON(t, f.viewerC, http.MethodPut, f.groupsURL()+"/"+g.ID, body); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("viewer update = %d, want 403", resp.StatusCode)
		}
		resp = requestJSON(t, f.operatorC, http.MethodPut, f.groupsURL()+"/"+g.ID, map[string]any{
			"name": "life tier", "selector": "role=db", "priority": 20,
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		decodeInto(t, resp, &single)
		if single.Group.Selector != "role=db" || single.Group.Priority != 20 {
			t.Fatalf("updated = %+v", single.Group)
		}

		// A second group cannot take the same name.
		if resp := postJSON(t, f.operatorC, f.groupsURL(), body); resp.StatusCode != http.StatusConflict {
			t.Fatalf("duplicate name = %d, want 409", resp.StatusCode)
		}

		for _, c := range []struct{ method, url string }{
			{http.MethodGet, f.groupsURL()},
			{http.MethodGet, f.groupsURL() + "/" + g.ID},
			{http.MethodPost, f.groupsURL()},
			{http.MethodDelete, f.groupsURL() + "/" + g.ID},
		} {
			if resp := requestJSON(t, f.stranger, c.method, c.url, body); resp.StatusCode != http.StatusForbidden {
				t.Errorf("non-member %s %s = %d, want 403", c.method, c.url, resp.StatusCode)
			}
		}

		if resp := requestJSON(t, f.viewerC, http.MethodDelete, f.groupsURL()+"/"+g.ID, nil); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("viewer delete = %d, want 403", resp.StatusCode)
		}
		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.groupsURL()+"/"+g.ID, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("delete = %d", resp.StatusCode)
		}
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.groupsURL()+"/"+g.ID, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("get after delete = %d, want 404", resp.StatusCode)
		}
	})

	// A group carries no configuration of its own, so changing one matters
	// exactly when some definition binds to it — and then it matters for
	// every host its selector covers, old and new.
	t.Run("writes push only what they change", func(t *testing.T) {
		f.s.Push.Reset()
		g := f.createGroup(t, map[string]any{"name": "push tier", "selector": "role=web", "priority": 10})
		if pushes := f.s.Push.Pushes(); len(pushes) != 0 {
			t.Fatalf("creating a group nothing binds to pushed %+v, want nothing", pushes)
		}

		f.s.Push.Reset()
		resp := requestJSON(t, f.operatorC, http.MethodPut, f.groupsURL()+"/"+g.ID, map[string]any{
			"name": "push tier", "selector": "role=db", "priority": 10,
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update = %d", resp.StatusCode)
		}
		if pushes := f.s.Push.Pushes(); len(pushes) != 0 {
			t.Fatalf("re-pointing a group nothing binds to pushed %+v, want nothing", pushes)
		}

		// Bind a definition to it, and the same edit now reaches both the
		// hosts that lose the group's checks and the ones that gain them.
		bound := f.createCheck(t, f.operatorC, map[string]any{
			"plugin": "cpu", "key": "cpu-grouppush", "interval_seconds": 15, "binding_kind": "group", "group_id": g.ID,
		})
		f.s.Push.Reset()
		resp = requestJSON(t, f.operatorC, http.MethodPut, f.groupsURL()+"/"+g.ID, map[string]any{
			"name": "push tier", "selector": "role=web", "priority": 10,
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update = %d", resp.StatusCode)
		}
		if !f.s.Push.PushedHosts(f.web, f.dbHost) {
			t.Fatalf("re-pointing a bound group pushed %+v, want both the old and the new members", f.s.Push.Pushes())
		}
		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.checksURL()+"/"+bound.ID, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("cleanup delete of the bound check = %d", resp.StatusCode)
		}
		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.groupsURL()+"/"+g.ID, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("cleanup delete of the group = %d", resp.StatusCode)
		}
	})

	// Deleting a group takes the definitions bound to it with it
	// (migration 00043's ON DELETE CASCADE), and the hosts that were in it
	// fall back to the organization's own set — which is exactly what they
	// must be told, so the push has to name them before the cascade
	// removes the only record of who they were.
	t.Run("deleting a group cascades to its checks", func(t *testing.T) {
		g := f.createGroup(t, map[string]any{"name": "cascade tier", "selector": "role=web", "priority": 10})
		bound := f.createCheck(t, f.operatorC, map[string]any{
			"plugin": "cpu", "key": "cpu", "interval_seconds": 15, "binding_kind": "group", "group_id": g.ID,
		})

		// While it exists, the group's cpu overrides the organization's.
		cpu := f.hostChecks(t, f.viewerC, f.web).byKey(t, "cpu")
		if cpu.Source != "group" || cpu.SourceID != g.ID || cpu.IntervalSeconds != 15 {
			t.Fatalf("effective cpu = %+v, want the group's 15s override", cpu)
		}
		if cpu.SourceName != "cascade tier" {
			t.Fatalf("source_name = %q, want the group's name so the GUI can say where it came from", cpu.SourceName)
		}

		f.s.Push.Reset()
		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.groupsURL()+"/"+g.ID, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("delete group = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		if pushes := f.s.Push.Pushes(); len(pushes) != 1 || pushes[0].HostID != f.web {
			t.Fatalf("deleting the group pushed %+v, want exactly the host it covered", pushes)
		}

		for _, d := range f.listChecks(t, f.ownerC) {
			if d.ID == bound.ID {
				t.Fatalf("the group-bound check survived its group: %+v", d)
			}
		}
		cpu = f.hostChecks(t, f.viewerC, f.web).byKey(t, "cpu")
		if cpu.Source != "org" || cpu.SourceID != "" || cpu.IntervalSeconds != 60 {
			t.Fatalf("effective cpu after the cascade = %+v, want the organization's own 60s check", cpu)
		}
	})

	// Create, update and delete each leave one attributable entry; the
	// preview leaves none, because it changes nothing.
	t.Run("mutations are audited", func(t *testing.T) {
		g := f.createGroup(t, map[string]any{"name": "audit tier", "selector": "role=web"})
		if resp := requestJSON(t, f.operatorC, http.MethodPut, f.groupsURL()+"/"+g.ID, map[string]any{
			"name": "audit tier", "selector": "role=web", "priority": 5,
		}); resp.StatusCode != http.StatusOK {
			t.Fatalf("update = %d", resp.StatusCode)
		}
		if resp := postJSON(t, f.operatorC, f.groupsURL()+"/preview", map[string]any{"selector": "role=web"}); resp.StatusCode != http.StatusOK {
			t.Fatalf("preview = %d", resp.StatusCode)
		}
		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.groupsURL()+"/"+g.ID, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("delete = %d", resp.StatusCode)
		}

		actions := f.auditActionsFor(t, "monitoring_group", g.ID)
		for _, action := range []string{"POST createMonitoringGroup", "PUT updateMonitoringGroup", "DELETE deleteMonitoringGroup"} {
			if actions[action] != 1 {
				t.Fatalf("action %q recorded %d times, want 1; trail for this group = %+v", action, actions[action], actions)
			}
		}
		if len(actions) != 3 {
			t.Fatalf("recorded %+v; the preview must leave no entry", actions)
		}
	})
}
