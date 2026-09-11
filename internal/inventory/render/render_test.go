package render_test

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/sshconf"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json")

// id returns a fixed, valid UUIDv7-shaped id so the golden file is stable.
func id(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-%012d", n))
}

func ptr[T any](v T) *T { return &v }

// Fixture ids.
var (
	bastionA, bastionB, bastionZ = id(1), id(2), id(3)
	zoneFra, zoneHel             = id(10), id(11)
	cred1, cred2                 = id(20), id(21)
	gProduction, gWebserver      = id(30), id(31)
	gDbserver, gEdge, gDmz       = id(32), id(33), id(34)
	hWeb01, hWeb02, hWeb03       = id(40), id(41), id(42)
	hDb01, hDb02                 = id(43), id(44)
	hLonely, hSwitch             = id(45), id(46)
)

func fixture() render.Input {
	webSchema := schema.Schema{Fields: []schema.Field{
		{Name: "php_default", Type: schema.TypeEnum, Options: []string{"8.3", "8.4"}, Default: "8.3", Overridable: true},
		{Name: "max_upload_mb", Type: schema.TypeInt, Default: 64.0},
		{Name: "vhosts", Type: schema.TypeRepeater, Scope: schema.ScopeHost, Required: true, Fields: []schema.Field{
			{Name: "domain", Type: schema.TypeString, Required: true},
			{Name: "ssl", Type: schema.TypeBool, Default: true},
			{Name: "basic_auth", Type: schema.TypeObject, Fields: []schema.Field{
				{Name: "user", Type: schema.TypeString},
				{Name: "password", Type: schema.TypeSecret},
			}},
		}},
	}}
	roleField := func(options ...string) schema.Schema {
		return schema.Schema{Fields: []schema.Field{{Name: "db_role", Type: schema.TypeEnum, Options: options, Scope: schema.ScopeHost, Required: true}}}
	}
	return render.Input{
		Bastions: map[uuid.UUID]sshconf.Bastion{
			bastionA: {ID: bastionA, Name: "bastion-a", Address: "bastion-a.example", Port: 22, Username: "ops"},
			bastionB: {ID: bastionB, Name: "bastion-b", Address: "bastion-b.example", Port: 2222, Username: "ops", ViaBastionID: ptr(bastionA)},
			bastionZ: {ID: bastionZ, Name: "zone-jump", Address: "zone-jump.example", Port: 22, Username: "jump"},
		},
		ZoneBastions:    map[uuid.UUID]uuid.UUID{zoneFra: bastionZ},
		CredentialUsers: map[uuid.UUID]string{cred1: "ansible", cred2: "ubuntu"},
		Groups: []render.Group{
			{ID: gProduction, Name: "production", BastionID: ptr(bastionA),
				Schema: schema.Schema{Fields: []schema.Field{{Name: "env", Type: schema.TypeString, Default: "prod"}}}, Vars: map[string]any{}},
			{ID: gWebserver, ParentID: ptr(gProduction), Name: "webserver", Schema: webSchema, Vars: map[string]any{"stale_key": "dropped"}},
			{ID: gDbserver, Name: "dbserver", Schema: roleField("primary", "replica"), Vars: map[string]any{}},
			{ID: gEdge, Name: "edge", BastionID: ptr(bastionB), Schema: roleField("primary", "replica", "witness"), Vars: map[string]any{}},
			{ID: gDmz, Name: "dmz", BastionID: ptr(bastionZ), Schema: schema.Schema{Fields: []schema.Field{}}, Vars: map[string]any{}},
		},
		Hosts: []render.Host{
			{ID: hWeb01, ZoneID: zoneFra, Name: "web-01", Address: "10.0.0.11", Managed: true, Facts: map[string]any{"os": "debian"}},
			{ID: hWeb02, ZoneID: zoneFra, Name: "web-02", Managed: true},
			{ID: hWeb03, ZoneID: zoneHel, Name: "web-03", Managed: true},
			{ID: hDb01, ZoneID: zoneFra, Name: "db-01", Managed: true},
			{ID: hDb02, ZoneID: zoneHel, Name: "db-02", Managed: true},
			{ID: hLonely, ZoneID: zoneHel, Name: "lonely", Managed: true},
			{ID: hSwitch, ZoneID: zoneHel, Name: "switch-01"},
		},
		SSH: map[uuid.UUID]sshconf.HostSSH{
			hWeb01:  {HostID: hWeb01, Username: "deploy", Port: 2222, CredentialID: ptr(cred1), BastionMode: sshconf.ModeInherit},
			hWeb02:  {HostID: hWeb02, Port: 22, BastionMode: sshconf.ModeNone},
			hLonely: {HostID: hLonely, Port: 22, CredentialID: ptr(cred2), BastionMode: sshconf.ModeInherit},
		},
		Memberships: []render.Membership{
			{GroupID: gWebserver, HostID: hWeb01, Vars: map[string]any{
				"php_default": "8.4",
				"vhosts":      []any{map[string]any{"domain": "acme.io", "basic_auth": map[string]any{"user": "u", "password": render.MaskedSecret}}},
			}},
			{GroupID: gWebserver, HostID: hWeb02, Vars: map[string]any{"vhosts": []any{map[string]any{"domain": "shop.acme.io", "ssl": false}}}},
			{GroupID: gDbserver, HostID: hDb01, Vars: map[string]any{"db_role": "primary"}},
			{GroupID: gDbserver, HostID: hDb02, Vars: map[string]any{"db_role": "replica"}},
			{GroupID: gEdge, HostID: hDb02, Vars: map[string]any{"db_role": "witness"}},
			{GroupID: gDmz, HostID: hWeb03, Vars: map[string]any{}},
			{GroupID: gEdge, HostID: hWeb03, Vars: map[string]any{"db_role": "primary"}},
			{GroupID: gDbserver, HostID: hSwitch, Vars: map[string]any{"db_role": "primary"}},
		},
	}
}

func hostvars(t *testing.T, inv render.Inventory, host string) map[string]any {
	t.Helper()
	hv, ok := inv.Doc["_meta"].(map[string]any)["hostvars"].(map[string]any)[host].(map[string]any)
	if !ok {
		t.Fatalf("no hostvars for %s in %v", host, inv.Doc["_meta"])
	}
	return hv
}

func group(t *testing.T, inv render.Inventory, name string) map[string]any {
	t.Helper()
	g, ok := inv.Doc[name].(map[string]any)
	if !ok {
		t.Fatalf("no group %s in %v", name, inv.Doc)
	}
	return g
}

func hasWarning(inv render.Inventory, host, substr string) bool {
	for _, w := range inv.Warnings {
		if w.Host == host && strings.Contains(w.Message, substr) {
			return true
		}
	}
	return false
}

func TestRenderWholeOrganization(t *testing.T) {
	inv, err := render.Render(fixture())
	if err != nil {
		t.Fatal(err)
	}
	if got := group(t, inv, "all")["children"]; !reflect.DeepEqual(got, []string{"dbserver", "dmz", "edge", "production", "ungrouped"}) {
		t.Errorf("all.children = %v", got)
	}
	if got := group(t, inv, "production"); !reflect.DeepEqual(got["children"], []string{"webserver"}) || !reflect.DeepEqual(got["vars"], map[string]any{"env": "prod"}) {
		t.Errorf("production = %v", got)
	}
	web := group(t, inv, "webserver")
	if !reflect.DeepEqual(web["hosts"], []string{"web-01", "web-02"}) {
		t.Errorf("webserver.hosts = %v", web["hosts"])
	}
	if !reflect.DeepEqual(web["vars"], map[string]any{"php_default": "8.3", "max_upload_mb": 64.0}) {
		t.Errorf("webserver.vars must hold defaults and drop undeclared keys: %v", web["vars"])
	}
	if got := group(t, inv, "dbserver")["hosts"]; !reflect.DeepEqual(got, []string{"db-01", "db-02"}) {
		t.Errorf("dbserver.hosts must skip the unmanaged host: %v", got)
	}
	if got := group(t, inv, "ungrouped")["hosts"]; !reflect.DeepEqual(got, []string{"lonely"}) {
		t.Errorf("ungrouped = %v", got)
	}
	if _, ok := inv.Doc["dmz"].(map[string]any)["vars"]; ok {
		t.Error("a group without vars must not carry an empty vars key")
	}

	w1 := hostvars(t, inv, "web-01")
	want := map[string]any{
		"ansible_host": "10.0.0.11", "ansible_user": "deploy", "ansible_port": 2222,
		"ansible_ssh_common_args": "-o ProxyJump=ops@bastion-a.example:22",
		"php_default":             "8.4",
		"vhosts":                  []any{map[string]any{"domain": "acme.io", "ssl": true, "basic_auth": map[string]any{"user": "u", "password": "********"}}},
		"pulsix_facts":            map[string]any{"os": "debian"},
	}
	if !reflect.DeepEqual(w1, want) {
		t.Errorf("web-01 hostvars =\n%#v\nwant\n%#v", w1, want)
	}
	if c := inv.Connections["web-01"]; c.BastionSource != "group:production" || !slices.Equal(c.Bastions, []uuid.UUID{bastionA}) || *c.CredentialID != cred1 {
		t.Errorf("web-01 connection = %+v", c)
	}

	w2 := hostvars(t, inv, "web-02")
	if w2["ansible_host"] != "web-02" || w2["ansible_ssh_common_args"] != nil || inv.Connections["web-02"].BastionSource != render.SourceNone {
		t.Errorf("web-02 (mode none, no address) = %v %+v", w2, inv.Connections["web-02"])
	}
	if _, ok := w2["ansible_port"]; ok {
		t.Error("port 22 must not be rendered")
	}

	if d1 := hostvars(t, inv, "db-01"); d1["ansible_ssh_common_args"] != "-o ProxyJump=jump@zone-jump.example:22" || inv.Connections["db-01"].BastionSource != render.SourceZone {
		t.Errorf("db-01 must use the zone default: %v", d1)
	}

	d2 := hostvars(t, inv, "db-02")
	if d2["ansible_ssh_common_args"] != "-o ProxyJump=ops@bastion-a.example:22,ops@bastion-b.example:2222" || inv.Connections["db-02"].BastionSource != "group:edge" {
		t.Errorf("db-02 must use the chained bastion of edge: %v", d2)
	}
	if d2["db_role"] != "witness" || !hasWarning(inv, "db-02", "db_role is set by groups dbserver and edge; edge wins") {
		t.Errorf("db-02 conflict: db_role=%v warnings=%+v", d2["db_role"], inv.Warnings)
	}

	w3 := hostvars(t, inv, "web-03")
	if w3["ansible_ssh_common_args"] != "-o ProxyJump=jump@zone-jump.example:22" || inv.Connections["web-03"].BastionSource != "group:dmz" {
		t.Errorf("web-03 must take dmz's jump host (tie broken by name): %v", w3)
	}
	if !hasWarning(inv, "web-03", "groups dmz and edge set different jump hosts; dmz wins") {
		t.Errorf("web-03 tie warning missing: %+v", inv.Warnings)
	}

	if hostvars(t, inv, "lonely")["ansible_user"] != "ubuntu" {
		t.Error("the credential's username applies when the host names none")
	}
	if !hasWarning(inv, "switch-01", "not managed") {
		t.Errorf("unmanaged member warning missing: %+v", inv.Warnings)
	}
	if _, ok := inv.Connections["switch-01"]; ok {
		t.Error("an unmanaged host must not be rendered")
	}
}

func TestRenderScopes(t *testing.T) {
	in := fixture()
	in.Scope = render.Scope{GroupID: ptr(gProduction)}
	inv, err := render.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := group(t, inv, "all")["children"]; !reflect.DeepEqual(got, []string{"production"}) {
		t.Errorf("all.children = %v", got)
	}
	for _, name := range []string{"dbserver", "edge", "dmz", "ungrouped"} {
		if _, ok := inv.Doc[name]; ok {
			t.Errorf("%s must not be rendered for the production scope", name)
		}
	}
	if len(inv.Connections) != 2 || len(inv.Warnings) != 0 {
		t.Errorf("connections = %v warnings = %v", inv.Connections, inv.Warnings)
	}

	in = fixture()
	in.Scope = render.Scope{HostIDs: []uuid.UUID{hDb02}}
	inv, err = render.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := group(t, inv, "all")["children"]; !reflect.DeepEqual(got, []string{"dbserver", "edge"}) {
		t.Errorf("host scope all.children = %v", got)
	}

	in = fixture()
	in.Scope = render.Scope{GroupID: ptr(gProduction), HostIDs: []uuid.UUID{hWeb02, hDb01}}
	inv, err = render.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Connections) != 1 || inv.Connections["web-02"].HostID != hWeb02 {
		t.Errorf("group and host scope must intersect: %v", inv.Connections)
	}

	in = fixture()
	in.Scope = render.Scope{GroupID: ptr(uuid.Must(uuid.NewV7()))}
	if _, err := render.Render(in); !errors.Is(err, render.ErrUnknownGroup) {
		t.Errorf("unknown scope group: %v", err)
	}
}

// TestRenderScopesWarnAboutUnmanagedMembersInScope proves the unmanaged-
// member warning fires whenever the host would otherwise be in the rendered
// scope, not only for a whole-organization render: switch-01 is a member of
// dbserver only, so a group scope must warn exactly when it contains
// dbserver, and a host scope must warn exactly when it names switch-01.
func TestRenderScopesWarnAboutUnmanagedMembersInScope(t *testing.T) {
	in := fixture()
	in.Scope = render.Scope{GroupID: ptr(gDbserver)}
	inv, err := render.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(inv, "switch-01", "not managed") {
		t.Errorf("group scope containing switch-01's group must warn: %+v", inv.Warnings)
	}

	in = fixture()
	in.Scope = render.Scope{GroupID: ptr(gProduction)}
	inv, err = render.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if hasWarning(inv, "switch-01", "not managed") {
		t.Errorf("group scope not containing switch-01's group must stay silent: %+v", inv.Warnings)
	}

	in = fixture()
	in.Scope = render.Scope{HostIDs: []uuid.UUID{hSwitch}}
	inv, err = render.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(inv, "switch-01", "not managed") {
		t.Errorf("host scope naming switch-01 must warn: %+v", inv.Warnings)
	}

	in = fixture()
	in.Scope = render.Scope{HostIDs: []uuid.UUID{hDb02}}
	inv, err = render.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if hasWarning(inv, "switch-01", "not managed") {
		t.Errorf("host scope not naming switch-01 must stay silent: %+v", inv.Warnings)
	}
}

func TestRenderFailsClosedOnBrokenJumpHosts(t *testing.T) {
	in := fixture()
	in.SSH[hWeb02] = sshconf.HostSSH{HostID: hWeb02, Port: 22, BastionMode: sshconf.ModeExplicit, BastionID: ptr(uuid.Must(uuid.NewV7()))}
	_, err := render.Render(in)
	if !errors.Is(err, sshconf.ErrInvalidInput) || !errors.Is(err, render.ErrUnrenderable) || !strings.Contains(err.Error(), "host web-02") {
		t.Errorf("explicit unknown bastion: %v", err)
	}
	in = fixture()
	in.SSH[hWeb02] = sshconf.HostSSH{HostID: hWeb02, Port: 22, BastionMode: sshconf.ModeExplicit}
	if _, err := render.Render(in); !errors.Is(err, render.ErrUnrenderable) || !strings.Contains(err.Error(), "host web-02") {
		t.Errorf("explicit mode without a bastion must fail as unrenderable: %v", err)
	}
}

func TestRenderGolden(t *testing.T) {
	inv, err := render.Render(fixture())
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(struct {
		Inventory   map[string]any               `json:"inventory"`
		Connections map[string]render.Connection `json:"connections"`
		Warnings    []render.Warning             `json:"warnings"`
	}{inv.Doc, inv.Connections, inv.Warnings}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const path = "testdata/inventory.golden.json"
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file (run with -update once, then review it): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("inventory differs from %s; if the change is intended, run with -update and review the diff", path)
	}
}
