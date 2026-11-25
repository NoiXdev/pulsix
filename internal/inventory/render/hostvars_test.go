package render_test

import (
	"reflect"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/render"
)

// SetHostVar writes one variable into a rendered host's own variables (the
// claim's enrollment token, spec 2026-11-25 B): it replaces what a group
// or membership gave the host under that name, leaves every other host and
// the group vars alone, and refuses a host the document does not carry.
func TestSetHostVar(t *testing.T) {
	inv := render.Inventory{Doc: map[string]any{
		"web": map[string]any{"vars": map[string]any{"pulsix_agent_enroll_tokens": map[string]any{"web-1": "stale"}}, "hosts": []string{"web-1", "web-2"}},
		"_meta": map[string]any{"hostvars": map[string]any{
			"web-1": map[string]any{"ansible_host": "192.0.2.1", "pulsix_agent_enroll_token": "from-a-membership"},
			"web-2": map[string]any{"ansible_host": "192.0.2.2"},
		}},
	}}
	if err := inv.SetHostVar("web-1", "pulsix_agent_enroll_token", "pxa1.secret"); err != nil {
		t.Fatal(err)
	}
	if err := inv.SetHostVar("web-1", "pulsix_agent_enroll_tokens", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	hostvars := inv.Doc["_meta"].(map[string]any)["hostvars"].(map[string]any)
	want := map[string]any{"ansible_host": "192.0.2.1", "pulsix_agent_enroll_token": "pxa1.secret", "pulsix_agent_enroll_tokens": map[string]any{}}
	if got := hostvars["web-1"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("web-1 = %#v, want %#v", got, want)
	}
	if got := hostvars["web-2"]; !reflect.DeepEqual(got, map[string]any{"ansible_host": "192.0.2.2"}) {
		t.Fatalf("web-2 = %#v, want it untouched", got)
	}
	if got := inv.Doc["web"].(map[string]any)["vars"]; !reflect.DeepEqual(got, map[string]any{"pulsix_agent_enroll_tokens": map[string]any{"web-1": "stale"}}) {
		t.Fatalf("group vars = %#v, want them untouched", got)
	}
	if err := inv.SetHostVar("db-1", "pulsix_agent_enroll_token", "x"); err == nil {
		t.Fatal("a host the document does not carry was set, want an error")
	}
	if err := (&render.Inventory{Doc: map[string]any{}}).SetHostVar("web-1", "x", "y"); err == nil {
		t.Fatal("a document without hostvars was set, want an error")
	}
}
