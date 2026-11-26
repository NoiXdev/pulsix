package api_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type markedGroup struct {
	Group struct {
		Revision             int64    `json:"revision"`
		AssistantKeys        []string `json:"assistant_keys"`
		AssistantDefaultKeys []string `json:"assistant_default_keys"`
	} `json:"group"`
}

type markedMember struct {
	Member struct {
		AssistantKeys []string `json:"assistant_keys"`
	} `json:"member"`
}

func decodeAssistant[T any](t *testing.T, resp *apitest.Response, want int) T {
	t.Helper()
	if resp.Code != want {
		t.Fatalf("status = %d, want %d: %s", resp.Code, want, resp.Body)
	}
	var out T
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func assistantLocations(t *testing.T, resp *apitest.Response) []string {
	t.Helper()
	p := decodeAssistant[problemBody](t, resp, http.StatusUnprocessableEntity)
	var out []string
	for _, e := range p.Errors {
		out = append(out, e.Location)
	}
	return out
}

// TestAssistantInventoryWritesAreMarkedAndPlainText is decision D6 through
// the API (spec 2026-11-25 assistant API gaps): what an assistant token
// writes into group vars, membership vars and schema defaults is marked
// (assistant_keys, assistant_default_keys), refused under names Ansible or
// Pulsix act on, and shown as plain text in both previews
// (assistant_written, the inert form in the document); a person's save
// clears the marks of what it saves.
func TestAssistantInventoryWritesAreMarkedAndPlainText(t *testing.T) {
	const slug = "inv-assistant-marks"
	s := apitest.New(t)
	owner := s.SeedUser(slug+"-owner@example.test", "correct horse battery")
	orgID := s.SeedOrg(slug, owner)
	f := invFixture{s: s, c: s.LoginAs(owner), orgID: orgID, base: s.HTTP.URL + "/api/v1/orgs/" + slug}
	f.zone = decodeZone(t, postJSON(t, f.c, f.base+"/zones", map[string]any{"slug": "fra", "name": "fra"})).ID
	tok := s.AssistantTokenFor(t, orgID, owner, tenancy.RoleAdmin, tenancy.AreaInventoryWrite)
	rel := "/api/v1/orgs/" + slug + "/inventory/groups/"

	web := f.group(t, "web", nil)
	host := f.host(t, "web-01", true)
	fields := []any{
		map[string]any{"name": "greeting", "type": "string"},
		map[string]any{"name": "omit", "type": "string"},
		map[string]any{"name": "motd", "type": "string", "default": "hello"},
		map[string]any{"name": "note", "type": "string", "scope": "host"},
	}
	if resp := requestWithHeaders(t, f.c, http.MethodPut, f.base+"/inventory/groups/"+web.Group.ID+"/schema",
		map[string]any{"schema": map[string]any{"fields": fields}}, map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	const planted = "{{ lookup('pipe','id') }}"

	g := decodeAssistant[markedGroup](t, s.AssistantDo(t, http.MethodPut, tok, rel+web.Group.ID+"/vars", `"2"`,
		map[string]any{"vars": map[string]any{"greeting": planted}}), http.StatusOK)
	if !slices.Equal(g.Group.AssistantKeys, []string{"greeting"}) {
		t.Fatalf("assistant_keys = %v", g.Group.AssistantKeys)
	}
	locs := assistantLocations(t, s.AssistantDo(t, http.MethodPut, tok, rel+web.Group.ID+"/vars", `"3"`,
		map[string]any{"vars": map[string]any{"greeting": planted, "omit": "x"}}))
	if !slices.Equal(locs, []string{"body.vars.omit"}) {
		t.Fatalf("reserved name refused at %v", locs)
	}

	m := decodeAssistant[markedMember](t, s.AssistantDo(t, http.MethodPut, tok, rel+web.Group.ID+"/members/"+host, "",
		map[string]any{"vars": map[string]any{"note": planted}}), http.StatusOK)
	if !slices.Equal(m.Member.AssistantKeys, []string{"note"}) {
		t.Fatalf("member assistant_keys = %v", m.Member.AssistantKeys)
	}

	fields[2] = map[string]any{"name": "motd", "type": "string", "default": "hi {{ x }}"}
	g = decodeAssistant[markedGroup](t, s.AssistantDo(t, http.MethodPut, tok, rel+web.Group.ID+"/schema", `"3"`,
		map[string]any{"schema": map[string]any{"fields": fields}}), http.StatusOK)
	if !slices.Equal(g.Group.AssistantDefaultKeys, []string{"motd"}) || !slices.Equal(g.Group.AssistantKeys, []string{"greeting"}) {
		t.Fatalf("after the assistant's schema: default marks %v, value marks %v", g.Group.AssistantDefaultKeys, g.Group.AssistantKeys)
	}
	withHostvars := append(slices.Clone(fields), map[string]any{"name": "hostvars", "type": "string", "default": "x"})
	locs = assistantLocations(t, s.AssistantDo(t, http.MethodPut, tok, rel+web.Group.ID+"/schema", `"4"`,
		map[string]any{"schema": map[string]any{"fields": withHostvars}}))
	if !slices.Equal(locs, []string{"body.schema.fields[4].default"}) {
		t.Fatalf("reserved default refused at %v", locs)
	}

	// Both previews say what is plain text and carry it inert.
	var preview struct {
		Inventory        map[string]any   `json:"inventory"`
		AssistantWritten []map[string]any `json:"assistant_written"`
	}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, f.base+"/inventory/preview", nil), &preview)
	wantWritten := []map[string]any{
		{"group": "web", "key": "greeting"}, {"group": "web", "key": "motd"}, {"group": "web", "host": "web-01", "key": "note"},
	}
	if !reflect.DeepEqual(preview.AssistantWritten, wantWritten) {
		t.Fatalf("preview assistant_written = %v", preview.AssistantWritten)
	}
	if got := preview.Inventory["web"].(map[string]any)["vars"].(map[string]any)["greeting"]; !reflect.DeepEqual(got, map[string]any{"__ansible_unsafe": planted}) {
		t.Fatalf("preview greeting = %#v", got)
	}
	var hostPreview struct {
		AssistantWritten []string `json:"assistant_written"`
	}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, f.base+"/hosts/"+host+"/inventory/preview", nil), &hostPreview)
	if !slices.Equal(hostPreview.AssistantWritten, []string{"greeting", "motd", "note"}) {
		t.Fatalf("host preview assistant_written = %v", hostPreview.AssistantWritten)
	}

	// A person saves the group's values and schema: those marks clear; the
	// membership keeps its own.
	resp := requestWithHeaders(t, f.c, http.MethodPut, f.base+"/inventory/groups/"+web.Group.ID+"/vars",
		map[string]any{"vars": map[string]any{"greeting": planted, "omit": "x"}}, map[string]string{"If-Match": `"4"`})
	var pg markedGroup
	decodeInto(t, resp, &pg)
	if pg.Group.AssistantKeys == nil || len(pg.Group.AssistantKeys) != 0 {
		t.Fatalf("after a person's vars save: %#v", pg.Group.AssistantKeys)
	}
	resp = requestWithHeaders(t, f.c, http.MethodPut, f.base+"/inventory/groups/"+web.Group.ID+"/schema",
		map[string]any{"schema": map[string]any{"fields": fields}}, map[string]string{"If-Match": `"5"`})
	decodeInto(t, resp, &pg)
	if len(pg.Group.AssistantDefaultKeys) != 0 {
		t.Fatalf("after a person's schema save: %v", pg.Group.AssistantDefaultKeys)
	}
	var hostGroups struct {
		Groups []struct {
			AssistantKeys []string `json:"assistant_keys"`
		} `json:"groups"`
	}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, f.base+"/hosts/"+host+"/inventory/groups", nil), &hostGroups)
	if len(hostGroups.Groups) != 1 || !slices.Equal(hostGroups.Groups[0].AssistantKeys, []string{"note"}) {
		t.Fatalf("host groups = %+v", hostGroups.Groups)
	}
}
