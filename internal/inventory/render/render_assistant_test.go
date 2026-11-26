package render_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/sshconf"
)

const planted = "{{ lookup('pipe','id') }}"

func unsafe(s string) map[string]any { return map[string]any{"__ansible_unsafe": s} }

// assistantInput is one group with group- and host-scoped fields and two
// members: an assistant wrote some of the group's values and defaults and
// some of web-01's membership values; everything about web-02 is a
// person's.
func assistantInput() render.Input {
	sc := schema.Schema{Fields: []schema.Field{
		{Name: "greeting", Type: schema.TypeString},
		{Name: "motd", Type: schema.TypeString, Default: "hello {{ inventory_hostname }}"},
		{Name: "owner", Type: schema.TypeString},
		{Name: "role", Type: schema.TypeString, Scope: schema.ScopeHost, Default: "web {{ x }}"},
		{Name: "note", Type: schema.TypeString, Scope: schema.ScopeHost},
		{Name: "cfg", Type: schema.TypeObject, Scope: schema.ScopeHost, Fields: []schema.Field{
			{Name: "a", Type: schema.TypeString},
			{Name: "__ansible_vault", Type: schema.TypeString},
		}},
	}}
	return render.Input{
		SSH: map[uuid.UUID]sshconf.HostSSH{}, Bastions: map[uuid.UUID]sshconf.Bastion{}, ZoneBastions: map[uuid.UUID]uuid.UUID{},
		CredentialUsers: map[uuid.UUID]string{},
		Groups: []render.Group{{
			ID: gWebserver, Name: "web", Schema: sc,
			Vars:          map[string]any{"greeting": planted, "owner": "ops {{ team }}"},
			AssistantKeys: []string{"greeting"}, AssistantDefaultKeys: []string{"motd", "role"},
		}},
		Hosts: []render.Host{
			{ID: hWeb01, ZoneID: zoneFra, Name: "web-01", Managed: true},
			{ID: hWeb02, ZoneID: zoneFra, Name: "web-02", Managed: true},
		},
		Memberships: []render.Membership{
			{GroupID: gWebserver, HostID: hWeb01, Vars: map[string]any{
				"note": planted, "cfg": map[string]any{"a": "b", "__ansible_vault": "x"},
			}, AssistantKeys: []string{"note", "cfg"}},
			{GroupID: gWebserver, HostID: hWeb02, Vars: map[string]any{"note": "by a person {{ y }}", "role": "db"}},
		},
	}
}

// TestRenderEmitsAssistantValuesInert pins decision D6 (spec 2026-11-25
// assistant API gaps): every value an assistant wrote — group vars,
// membership vars, and a schema default it set wherever it fills in —
// reaches Ansible in the inert form (every string marked unsafe, a key
// Ansible's JSON loader reads as a marker dropped), so a planted lookup
// stays literal text. A person's values render as before, and the
// inventory lists which values are plain text.
func TestRenderEmitsAssistantValuesInert(t *testing.T) {
	inv, err := render.Render(assistantInput())
	if err != nil {
		t.Fatal(err)
	}
	wantGroup := map[string]any{"greeting": unsafe(planted), "motd": unsafe("hello {{ inventory_hostname }}"), "owner": "ops {{ team }}"}
	if got := group(t, inv, "web")["vars"]; !reflect.DeepEqual(got, wantGroup) {
		t.Errorf("group vars =\n%#v\nwant\n%#v", got, wantGroup)
	}
	web01 := hostvars(t, inv, "web-01")
	if !reflect.DeepEqual(web01["note"], unsafe(planted)) || !reflect.DeepEqual(web01["role"], unsafe("web {{ x }}")) ||
		!reflect.DeepEqual(web01["cfg"], map[string]any{"a": unsafe("b")}) {
		t.Errorf("web-01 hostvars = %#v", web01)
	}
	web02 := hostvars(t, inv, "web-02")
	if web02["note"] != "by a person {{ y }}" || web02["role"] != "db" {
		t.Errorf("web-02 hostvars = %#v; a person's values render unchanged", web02)
	}
	want := []render.AssistantValue{
		{Group: "web", Key: "greeting"},
		{Group: "web", Key: "motd"},
		{Group: "web", Host: "web-01", Key: "cfg"},
		{Group: "web", Host: "web-01", Key: "note"},
		{Group: "web", Host: "web-01", Key: "role"},
	}
	if !reflect.DeepEqual(inv.AssistantWritten, want) {
		t.Errorf("AssistantWritten =\n%+v\nwant\n%+v", inv.AssistantWritten, want)
	}
}

// TestRenderWithoutMarksListsNothing: an inventory no assistant wrote to
// renders as before, with no plain-text values listed.
func TestRenderWithoutMarksListsNothing(t *testing.T) {
	in := assistantInput()
	in.Groups[0].AssistantKeys, in.Groups[0].AssistantDefaultKeys = nil, nil
	in.Memberships[0].AssistantKeys = nil
	inv, err := render.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if inv.AssistantWritten != nil {
		t.Errorf("AssistantWritten = %+v", inv.AssistantWritten)
	}
	if got := group(t, inv, "web")["vars"].(map[string]any)["greeting"]; got != planted {
		t.Errorf("greeting = %#v", got)
	}
}
