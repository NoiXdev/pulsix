// GET /check-plugins (backlog 3.54): the check plugin descriptors the SPA
// builds its check forms from, served to any signed-in user.
package api_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/checkplugins"
)

type checkPluginParamDTO struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Required   bool     `json:"required"`
	Default    any      `json:"default"`
	Example    string   `json:"example"`
	Min        *int64   `json:"min"`
	Max        *int64   `json:"max"`
	MaxEntries int      `json:"max_entries"`
	MaxBytes   int      `json:"max_bytes"`
	NonEmpty   bool     `json:"non_empty"`
	Options    []string `json:"options"`
	Format     string   `json:"format"`
}

type checkPluginDTO struct {
	Name                string                `json:"name"`
	Runs                string                `json:"runs"`
	Params              []checkPluginParamDTO `json:"params"`
	ItemParam           string                `json:"item_param"`
	HostItemAlwaysAsked bool                  `json:"host_item_always_asked"`
	Metrics             []struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"metrics"`
}

// TestCheckPluginsServesEveryDescriptor: the body is exactly the
// descriptors' JSON view, in the registry's order, so a field added to a
// descriptor but not to the API's DTO fails here.
func TestCheckPluginsServesEveryDescriptor(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("check-plugins@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/check-plugins", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /check-plugins: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	raw := mustReadBody(t, resp)

	var generic struct {
		Plugins []any `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(checkplugins.All())
	if err != nil {
		t.Fatal(err)
	}
	var wantGeneric []any
	if err := json.Unmarshal(want, &wantGeneric); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(generic.Plugins, wantGeneric) {
		t.Fatalf("plugins = %s,\nwant the descriptors' JSON view %s", raw, want)
	}

	var body struct {
		Plugins []checkPluginDTO `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Plugins) != 25 {
		t.Fatalf("%d plugins, want 25", len(body.Plugins))
	}
	for i, name := range checkplugins.Names() {
		if body.Plugins[i].Name != name {
			t.Fatalf("plugins[%d] = %s, want %s (the registry's order)", i, body.Plugins[i].Name, name)
		}
	}

	probe := body.Plugins[21]
	if probe.Name != "http" || probe.Runs != "server" {
		t.Fatalf("plugins[21] = %s/%s, want http/server", probe.Name, probe.Runs)
	}
	url := probe.Params[0]
	if url.Name != "url" || url.Type != "string" || !url.Required || url.MaxBytes != 500 ||
		url.Format != "url" || url.Example != "https://{address}/" {
		t.Errorf("http url param = %+v", url)
	}
	method := probe.Params[1]
	if method.Name != "method" || method.Type != "enum" || method.Default != "GET" ||
		!reflect.DeepEqual(method.Options, []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}) {
		t.Errorf("http method param = %+v", method)
	}
	if p := probe.Params[2]; p.Name != "headers" || p.Type != "list" || p.MaxEntries != 16 || p.MaxBytes != 1024 || p.Format != "httpHeader" {
		t.Errorf("http headers param = %+v", p)
	}
	if p := probe.Params[3]; p.Name != "body" || p.Type != "string" || p.MaxBytes != 8192 {
		t.Errorf("http body param = %+v", p)
	}
	if p := probe.Params[4]; p.Name != "content_type" || p.Default != "application/json" || p.Format != "mediaType" {
		t.Errorf("http content_type param = %+v", p)
	}
	if p := probe.Params[5]; p.Name != "secret" || p.Required || p.Format != "secretName" {
		t.Errorf("http secret param = %+v", p)
	}
	if p := probe.Params[7]; p.Name != "json_values" || p.Type != "list" || p.MaxEntries != 32 || p.Format != "jsonValue" {
		t.Errorf("http json_values param = %+v", p)
	}
	if probe.ItemParam != "json_values" || !probe.HostItemAlwaysAsked {
		t.Errorf("http items = %q, host item always asked %v", probe.ItemParam, probe.HostItemAlwaysAsked)
	}
	if p := probe.Params[8]; p.Name != "assertions" || p.Type != "list" || p.MaxEntries != 32 || p.MaxBytes != 512 || p.Format != "assertion" {
		t.Errorf("http assertions param = %+v", p)
	}
	timeout := probe.Params[9]
	if timeout.Name != "timeout_s" || timeout.Type != "integer" || timeout.Default != float64(10) ||
		timeout.Min == nil || *timeout.Min != 1 || timeout.Max == nil || *timeout.Max != 60 {
		t.Errorf("http timeout_s param = %+v", timeout)
	}
	if m := probe.Metrics[0]; m.Name != "http.up" || m.Kind != "numeric" {
		t.Errorf("http metrics[0] = %+v, want http.up numeric", m)
	}

	smart := body.Plugins[8]
	if smart.Name != "smart" || smart.ItemParam != "devices" || !smart.HostItemAlwaysAsked {
		t.Errorf("smart = %+v", smart)
	}
	if p := smart.Params[0]; p.Type != "list" || p.MaxEntries != 64 || p.MaxBytes != 255 || p.Format != "devicePath" {
		t.Errorf("smart devices param = %+v", p)
	}
	snmp := body.Plugins[24]
	if snmp.Name != "snmp" || snmp.Runs != "server" || snmp.ItemParam != "oids" || !snmp.HostItemAlwaysAsked {
		t.Errorf("snmp = %+v", snmp)
	}
	if p := snmp.Params[0]; p.Name != "secret" || !p.Required || p.Format != "secretName" {
		t.Errorf("snmp secret param = %+v", p)
	}
	if p := snmp.Params[4]; p.Name != "oids" || p.Type != "list" || p.MaxEntries != 32 || p.Format != "snmpOID" {
		t.Errorf("snmp oids param = %+v", p)
	}
	if p := snmp.Params[5]; p.Name != "interfaces" || p.Type != "bool" || p.Default != false {
		t.Errorf("snmp interfaces param = %+v", p)
	}
	if params := body.Plugins[0].Params; params == nil || len(params) != 0 {
		t.Errorf("cpu params = %#v, want []", params)
	}
}

// TestCheckPluginsNeedsASignedInUser: no session is 401, and a session
// still owing its second factor is held back like on every other
// non-enrolment route.
func TestCheckPluginsNeedsASignedInUser(t *testing.T) {
	s := apitest.New(t)
	resp := requestJSON(t, s.Client(), http.MethodGet, s.HTTP.URL+"/api/v1/check-plugins", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /check-plugins: status %d, want 401", resp.StatusCode)
	}

	uid := s.SeedUser("check-plugins-pending@example.test", "correct horse battery")
	resp = requestJSON(t, s.LoginAsPending(uid), http.MethodGet, s.HTTP.URL+"/api/v1/check-plugins", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pending GET /check-plugins: status %d, want 403", resp.StatusCode)
	}
}
