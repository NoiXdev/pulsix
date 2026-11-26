package mcp_test

// Deterministic drives of the two re-checks in tools_write.go. The tools
// dispatch into a router; this one wraps the real API router and runs a
// competing write exactly between a tool's check and the request it is
// about to make (before) or has just made (after).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/mcp"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// hookRouter forwards to next and fires each hook once, on the first
// request whose method and path suffix match.
type hookRouter struct {
	next http.Handler
	mu   sync.Mutex
	hook []*routerHook
}

type routerHook struct {
	method, suffix string
	after          bool
	run            func()
	fired          bool
}

func (h *hookRouter) arm(method, suffix string, after bool, run func()) *routerHook {
	rh := &routerHook{method: method, suffix: suffix, after: after, run: run}
	h.mu.Lock()
	h.hook = append(h.hook, rh)
	h.mu.Unlock()
	return rh
}

func (h *hookRouter) fire(r *http.Request, after bool) {
	h.mu.Lock()
	var due []*routerHook
	for _, rh := range h.hook {
		if !rh.fired && rh.after == after && rh.method == r.Method && strings.HasSuffix(r.URL.Path, rh.suffix) {
			rh.fired = true
			due = append(due, rh)
		}
	}
	h.mu.Unlock()
	for _, rh := range due {
		rh.run()
	}
}

func (h *hookRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.fire(r, false)
	h.next.ServeHTTP(w, r)
	h.fire(r, true)
}

// hookedSession serves the assistant endpoint of f's organization in front
// of a hookRouter around the real API, and connects a client to it.
func hookedSession(t *testing.T, f orgFixture) (*sdk.ClientSession, *hookRouter) {
	t.Helper()
	hr := &hookRouter{next: f.s.HTTP.Config.Handler}
	slug := f.org.String()
	h := mcp.New(mcp.Deps{
		Router: hr, Tokens: f.s.Deps.Tokens, Assistant: f.s.Deps.Assistant,
		Orgs:    fakeSlugs{f.org: slug},
		Access:  fakeAccess{slugs: map[string]uuid.UUID{slug: f.org}, org: map[uuid.UUID]tenancy.Role{f.org: tenancy.RoleOperator}},
		Version: "test", PublicURL: f.s.PublicURL,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	client := sdk.NewClient(&sdk.Implementation{Name: "pulsix-test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{
		Endpoint:   srv.URL,
		HTTPClient: &http.Client{Transport: bearerTransport{token: f.token, base: http.DefaultTransport}},
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, hr
}

func raceSchema(secretY bool) schema.Schema {
	y := schema.Field{Name: "y", Type: schema.TypeString, Scope: schema.ScopeHost}
	if secretY {
		y.Type = schema.TypeSecret
	}
	return schema.Schema{Fields: []schema.Field{y}}
}

func schemaArg(secretY bool) map[string]any {
	typ := "string"
	if secretY {
		typ = "secret"
	}
	return map[string]any{"fields": []any{map[string]any{"name": "y", "type": typ, "scope": "host"}}}
}

func (f orgFixture) setSchema(t *testing.T, sc schema.Schema) {
	t.Helper()
	g, err := f.s.Deps.Inventory.GetGroup(f.scoped, f.org, f.group)
	if err != nil {
		t.Errorf("read group: %v", err)
		return
	}
	if _, _, err := f.s.Deps.Inventory.SetSchema(f.scoped, f.org, f.group, g.Revision, sc); err != nil {
		t.Errorf("set schema: %v", err)
	}
}

func (f orgFixture) memberVars(t *testing.T) map[string]any {
	t.Helper()
	ms, err := f.s.Deps.Inventory.Members(f.scoped, f.org, f.group)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.HostID == f.host {
			return m.Vars
		}
	}
	t.Fatal("host is not a member")
	return nil
}

// A membership written between set_group_schema's check and its schema PUT
// holds a plain value under a field that is secret afterwards: the tool
// takes it out again and says so.
func TestSetGroupSchemaClearsValueWrittenBeforeThePut(t *testing.T) {
	f := newOrgFixture(t)
	cs, hr := hookedSession(t, f)
	f.setSchema(t, raceSchema(false))
	rh := hr.arm(http.MethodPut, "/schema", false, func() {
		if _, err := f.s.Deps.Inventory.Assign(f.scoped, f.org, f.group, f.host, map[string]any{"y": "plain-by-a-person"}, nil); err != nil {
			t.Errorf("competing membership write: %v", err)
		}
	})
	text, isErr := call(t, cs, "set_group_schema", map[string]any{"group_id": f.group.String(), "schema": schemaArg(true)})
	if !rh.fired {
		t.Fatal("the competing write never ran")
	}
	if !isErr || !strings.Contains(text, "taken out again") {
		t.Fatalf("tool result = %v %q, want the clear-out refusal", isErr, text)
	}
	if v, held := f.memberVars(t)["y"]; held {
		t.Errorf("the plain value under the new secret field survived: %v", v)
	}
}

// A schema change landing right after add_group_member's write makes the
// written value's field secret: the tool takes the value out again.
func TestAddGroupMemberClearsValueUnderFieldMadeSecretMeanwhile(t *testing.T) {
	f := newOrgFixture(t)
	cs, hr := hookedSession(t, f)
	f.setSchema(t, raceSchema(false))
	rh := hr.arm(http.MethodPut, "/members/"+f.host.String(), true, func() {
		f.setSchema(t, raceSchema(true))
	})
	text, isErr := call(t, cs, "add_group_member", map[string]any{
		"group_id": f.group.String(), "host_id": f.host.String(), "vars": map[string]any{"y": "plain-by-assistant"}})
	if !rh.fired {
		t.Fatal("the competing write never ran")
	}
	if !isErr || !strings.Contains(text, "taken out again") {
		t.Fatalf("tool result = %v %q, want the clear-out refusal", isErr, text)
	}
	if v, held := f.memberVars(t)["y"]; held {
		t.Errorf("the plain value under the now secret field survived: %v", v)
	}
}

// set_member_vars' clear-out write is guarded by the revision its own write
// produced: a person's edit landing between the two is not overwritten.
func TestSetMemberVarsClearOutIsRevisionGuarded(t *testing.T) {
	f := newOrgFixture(t)
	cs, hr := hookedSession(t, f)
	f.setSchema(t, raceSchema(false))
	hr.arm(http.MethodPut, "/members/"+f.host.String(), true, func() {
		f.setSchema(t, raceSchema(true))
		hr.arm(http.MethodPut, "/members/"+f.host.String(), false, func() {
			if _, err := f.s.Deps.Inventory.Assign(f.scoped, f.org, f.group, f.host, map[string]any{"y": "person-secret"}, nil); err != nil {
				t.Errorf("a person's edit: %v", err)
			}
		})
	})
	text, isErr := call(t, cs, "set_member_vars", map[string]any{
		"group_id": f.group.String(), "host_id": f.host.String(), "revision": f.memberRevision(t), "vars": map[string]any{"y": "plain-by-assistant"}})
	if !isErr || !strings.Contains(text, "take out the values of fields that became secret") {
		t.Fatalf("tool result = %v %q, want the clear-out write refused as stale", isErr, text)
	}
}
