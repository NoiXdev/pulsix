package checkplugins_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestPHPFPMDescriptor: urls is the ItemParam (each entry one pool, its
// item), item "" is always asked (it carries phpfpm.pools_down), and the
// one default pool is shown as the example, since a list carries no
// Default.
func TestPHPFPMDescriptor(t *testing.T) {
	d, ok := checkplugins.Get("phpfpm")
	if !ok || d.Runs != checkplugins.Agent || d.ItemParam != "urls" || !d.HostItemAlwaysAsked || d.ItemEntry != nil {
		t.Fatalf("Get(phpfpm) = %+v, %v", d, ok)
	}
	urls := d.Params[0]
	if urls.Name != "urls" || urls.Type != checkplugins.TypeList || urls.MaxEntries != 16 || urls.MaxBytes != 500 ||
		urls.Format != checkplugins.FormatStatusURL || urls.Default != nil || urls.Example != "http://127.0.0.1/status" || urls.Required {
		t.Fatalf("urls = %+v", urls)
	}
	if !d.UsesSecret() || d.Params[1].Required {
		t.Fatalf("secret = %+v", d.Params[1])
	}
	if v := d.Params[2]; v.Name != "verify_tls" || v.Type != checkplugins.TypeBool || v.Default != true {
		t.Fatalf("verify_tls = %+v", v)
	}
}

func TestValidatePHPFPMParams(t *testing.T) {
	sixteen := make([]any, 16)
	for i := range sixteen {
		sixteen[i] = fmt.Sprintf("http://127.0.0.1/status-%d", i)
	}
	runParamCases(t, []paramCase{
		{"no parameters accepted", "phpfpm", nil, true},
		{"an empty list accepted", "phpfpm", map[string]any{"urls": []any{}}, true},
		{"two pools, a secret and verify_tls accepted", "phpfpm", map[string]any{
			"urls": []any{"http://127.0.0.1/status", "https://localhost:8443/fpm-b?full"}, "secret": "fpm-login", "verify_tls": false}, true},
		{"sixteen pools accepted", "phpfpm", map[string]any{"urls": sixteen}, true},
		{"seventeen pools refused", "phpfpm", map[string]any{"urls": append(sixteen, "http://h/x")}, false},
		{"an entry of 501 bytes refused", "phpfpm", map[string]any{"urls": []any{"http://h/" + strings.Repeat("x", 492)}}, false},
		{"an entry with user info refused", "phpfpm", map[string]any{"urls": []any{"http://u:p@127.0.0.1/status"}}, false},
		{"an entry with a fragment refused", "phpfpm", map[string]any{"urls": []any{"http://127.0.0.1/status#x"}}, false},
		{"an entry that is not http refused", "phpfpm", map[string]any{"urls": []any{"unix:///run/php-fpm.sock"}}, false},
		{"an empty entry refused", "phpfpm", map[string]any{"urls": []any{""}}, false},
		{"urls that is a string refused", "phpfpm", map[string]any{"urls": "http://127.0.0.1/status"}, false},
		{"url (singular) refused", "phpfpm", map[string]any{"url": "http://127.0.0.1/status"}, false},
	})
}
