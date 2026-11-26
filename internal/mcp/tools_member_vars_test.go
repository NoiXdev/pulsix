package mcp_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// memberRevision is the fixture host's current revision in the fixture group.
func (f orgFixture) memberRevision(t *testing.T) int64 {
	t.Helper()
	ms, err := f.s.Deps.Inventory.Members(f.scoped, f.org, f.group)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.HostID == f.host {
			return m.Revision
		}
	}
	t.Fatal("host is not a member")
	return 0
}

var memberVarsSchema = map[string]any{"fields": []any{
	map[string]any{"name": "node_role", "type": "string", "scope": "host"},
	map[string]any{"name": "node_token", "type": "secret", "scope": "host"},
	map[string]any{"name": "env", "type": "string"},
}}

// TestSetMemberVarsAndGetHostVars: set_member_vars replaces a membership's
// values under the revision guard and the secret rules, and get_host_vars
// shows the merged result masked, with the group each value comes from.
func TestSetMemberVarsAndGetHostVars(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	g, h := f.group.String(), f.host.String()
	callOK(t, cs, "set_group_schema", map[string]any{"group_id": g, "schema": memberVarsSchema}, nil)
	callOK(t, cs, "set_group_vars", map[string]any{"group_id": g, "vars": map[string]any{"env": "prod"}}, nil)

	// A person stored a membership secret; the assistant keeps it.
	if _, err := f.s.Deps.Inventory.Assign(f.scoped, f.org, f.group, f.host, map[string]any{"node_token": "stored-secret-9c1"}, nil); err != nil {
		t.Fatal(err)
	}
	rev := f.memberRevision(t)
	keep := map[string]any{"$secret": "keep"}
	callOK(t, cs, "set_member_vars", map[string]any{"group_id": g, "host_id": h, "revision": rev, "vars": map[string]any{"node_role": "primary", "node_token": keep}}, nil)
	if got := f.memberVars(t)["node_role"]; got != "primary" {
		t.Fatalf("stored node_role = %v", got)
	}

	// The revision moved: the old one is refused and nothing changes.
	if text, isErr := call(t, cs, "set_member_vars", map[string]any{"group_id": g, "host_id": h, "revision": rev, "vars": map[string]any{"node_role": "replica"}}); !isErr {
		t.Fatalf("a stale revision was accepted: %s", text)
	}
	if got := f.memberVars(t)["node_role"]; got != "primary" {
		t.Fatalf("node_role after a stale write = %v", got)
	}
	// revision is required.
	if text, isErr := call(t, cs, "set_member_vars", map[string]any{"group_id": g, "host_id": h, "vars": map[string]any{}}); !isErr {
		t.Fatalf("a call without a revision was accepted: %s", text)
	}
	// A secret field never takes a value.
	text, isErr := call(t, cs, "set_member_vars", map[string]any{"group_id": g, "host_id": h, "revision": f.memberRevision(t), "vars": map[string]any{"node_token": "hunter2"}})
	if !isErr || !strings.Contains(text, "secrets are set by a person in Pulsix") {
		t.Fatalf("a secret value = %v %s, want the secret refusal", isErr, text)
	}
	// A group-scope field that a host may not override is refused too.
	if text, isErr := call(t, cs, "set_member_vars", map[string]any{"group_id": g, "host_id": h, "revision": f.memberRevision(t), "vars": map[string]any{"env": "staging"}}); !isErr {
		t.Fatalf("a group-scope key was accepted: %s", text)
	}
	if got := f.groupEnv(t); got != "prod" {
		t.Fatalf("group env = %v", got)
	}
	// A key the schema does not declare for hosts is refused by the route.
	if text, isErr := call(t, cs, "set_member_vars", map[string]any{"group_id": g, "host_id": h, "revision": f.memberRevision(t), "vars": map[string]any{"nope": "x"}}); !isErr {
		t.Fatalf("an undeclared key was accepted: %s", text)
	}

	var out struct {
		Vars             map[string]any    `json:"vars"`
		Sources          map[string]string `json:"sources"`
		AssistantWritten []string          `json:"assistant_written"`
	}
	raw := callOK(t, cs, "get_host_vars", map[string]any{"host_id": h}, &out)
	// The assistant wrote both values, so a run receives them as plain
	// text (decision D6) and get_host_vars says so.
	if !reflect.DeepEqual(out.Vars["node_role"], map[string]any{"__ansible_unsafe": "primary"}) || out.Sources["node_role"] != "webservers" {
		t.Fatalf("get_host_vars = %s", raw)
	}
	// The group's own value is part of the host's effective variables.
	if !reflect.DeepEqual(out.Vars["env"], map[string]any{"__ansible_unsafe": "prod"}) || out.Sources["env"] != "webservers" {
		t.Fatalf("env = %v from %q in %s", out.Vars["env"], out.Sources["env"], raw)
	}
	if !slices.Equal(out.AssistantWritten, []string{"env", "node_role"}) {
		t.Fatalf("assistant_written = %v in %s", out.AssistantWritten, raw)
	}
	if tok, _ := out.Vars["node_token"].(map[string]any); tok["$secret"] != "set" || out.Sources["node_token"] != "webservers" {
		t.Fatalf("node_token = %v from %q in %s", out.Vars["node_token"], out.Sources["node_token"], raw)
	}
	if strings.Contains(raw, "stored-secret-9c1") || strings.Contains(raw, "********") {
		t.Fatalf("get_host_vars shows a secret or the preview mask: %s", raw)
	}
	if out.Sources["ansible_host"] != "host" {
		t.Fatalf("ansible_host source = %q in %s", out.Sources["ansible_host"], raw)
	}
	for name := range out.Vars {
		if out.Sources[name] == "" {
			t.Errorf("%s has no source in %s", name, raw)
		}
	}
	if b, _ := json.Marshal(out.Vars); strings.Contains(string(b), "hunter2") {
		t.Fatalf("a secret shows in %s", b)
	}
}

// TestMemberVarsToolsNeedTheirArea: set_member_vars needs inventory.write,
// get_host_vars inventory.read.
func TestMemberVarsToolsNeedTheirArea(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaInventoryRead)
	names := toolNames(t, mustConnect(t, f.s, f.token))
	if names["set_member_vars"] || !names["get_host_vars"] {
		t.Fatalf("read-only token sees set_member_vars=%v get_host_vars=%v", names["set_member_vars"], names["get_host_vars"])
	}
	g := newOrgFixture(t, tenancy.AreaJobsRead)
	if toolNames(t, mustConnect(t, g.s, g.token))["get_host_vars"] {
		t.Fatal("get_host_vars listed without inventory.read")
	}
}

func (f orgFixture) groupEnv(t *testing.T) any {
	t.Helper()
	gv, err := f.s.Deps.Inventory.GetGroup(f.scoped, f.org, f.group)
	if err != nil {
		t.Fatal(err)
	}
	return gv.Vars["env"]
}

// TestAssistantInventoryNamesAreRefused: through the assistant, a value or
// a schema default under a name Ansible or Pulsix act on is refused
// (decision D6's write-time check), and get_group names what the
// assistant wrote.
func TestAssistantInventoryNamesAreRefused(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	g := f.group.String()
	// An assistant may not add a field under such a name.
	if text, isErr := call(t, cs, "set_group_schema", map[string]any{"group_id": g, "schema": map[string]any{"fields": []any{
		map[string]any{"name": "omit", "type": "string"},
	}}}); !isErr || !strings.Contains(text, "reserved") {
		t.Fatalf("adding omit = %v %s, want the reserved-name refusal", isErr, text)
	}
	// A person may; the assistant then may not set its value or default.
	cur, err := f.s.Deps.Inventory.GetGroup(f.scoped, f.org, f.group)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.Deps.Inventory.SetSchema(f.scoped, f.org, f.group, cur.Revision, schema.Schema{Fields: []schema.Field{
		{Name: "omit", Type: schema.TypeString}, {Name: "greeting", Type: schema.TypeString},
	}}); err != nil {
		t.Fatal(err)
	}
	if text, isErr := call(t, cs, "set_group_vars", map[string]any{"group_id": g, "vars": map[string]any{"omit": "x"}}); !isErr || !strings.Contains(text, "reserved") {
		t.Fatalf("omit = %v %s, want the reserved-name refusal", isErr, text)
	}
	if text, isErr := call(t, cs, "set_group_schema", map[string]any{"group_id": g, "schema": map[string]any{"fields": []any{
		map[string]any{"name": "omit", "type": "string", "default": "x"},
		map[string]any{"name": "greeting", "type": "string"},
	}}}); !isErr || !strings.Contains(text, "reserved") {
		t.Fatalf("a default for omit = %v %s, want the reserved-name refusal", isErr, text)
	}
	callOK(t, cs, "set_group_vars", map[string]any{"group_id": g, "vars": map[string]any{"greeting": "{{ lookup('pipe','id') }}"}}, nil)
	var out struct {
		Group struct {
			Group struct {
				AssistantKeys []string `json:"assistant_keys"`
			} `json:"group"`
		} `json:"group"`
	}
	raw := callOK(t, cs, "get_group", map[string]any{"group_id": g}, &out)
	if !slices.Equal(out.Group.Group.AssistantKeys, []string{"greeting"}) {
		t.Fatalf("get_group = %s", raw)
	}
}
