package api

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestCheckPluginFieldDoc: the write bodies' plugin doc string, built from
// the registry, reads exactly as the literal it replaced.
func TestCheckPluginFieldDoc(t *testing.T) {
	want := "One of cpu, memory, disk, service, network, uptime, docker, temperature, smart, raid (run by the host's agent) or icmp, tcp, http, tls, dns (probed from the manager)"
	if got := checkPluginFieldDoc(); got != want {
		t.Fatalf("checkPluginFieldDoc() =\n%s\nwant\n%s", got, want)
	}
}

// TestCheckParamsFieldDoc pins the write bodies' params doc string the
// descriptors generate: one sentence per plugin, its parameters in form
// order with their bounds, format and default.
func TestCheckParamsFieldDoc(t *testing.T) {
	want := "Each parameter is optional unless marked required. " +
		"cpu, memory and uptime take none. " +
		"disk: mounts (list; each not blank). " +
		"service: units (required; list; not empty; each not blank). " +
		"network: interfaces (list; each not blank). " +
		"docker: selection (all or labeled; default all), label (key or key=value; default pulsix.monitor=true), " +
		"socket (at most 255 bytes; an absolute path or npipe:////./pipe/name; default /var/run/docker.sock). " +
		"temperature: sensors (list; at most 64 entries; each at most 255 bytes; each without control characters). " +
		"smart: devices (list; at most 64 entries; each at most 255 bytes; each a /dev/... path). " +
		"raid: arrays (list; at most 64 entries; each at most 255 bytes; each without control characters). " +
		"icmp: count (1 to 10; default 3), timeout_s (1 to 30; default 5). " +
		"tcp: port (required; 1 to 65535), send (at most 500 bytes), expect (not empty; at most 500 bytes), timeout_s (1 to 30; default 5). " +
		"http: url (required; at most 500 bytes; an http or https URL that may contain {address}), method (GET or HEAD; default GET), " +
		"body_contains (not empty; at most 500 bytes), timeout_s (1 to 60; default 10), " +
		"follow_redirects (true or false; default true), verify_tls (true or false; default true). " +
		"tls: port (1 to 65535; default 443), server_name (at most 500 bytes; not blank), timeout_s (1 to 30; default 10). " +
		"dns: name (at most 500 bytes; a DNS name that may contain {address}; default {address}), " +
		"type (A, AAAA, CNAME, MX, TXT or NS; default A), server (at most 500 bytes; an IP address with an optional port), " +
		"expect (not empty; at most 500 bytes), timeout_s (1 to 30; default 5)."
	if got := checkParamsFieldDoc(); got != want {
		t.Fatalf("checkParamsFieldDoc() =\n%s\nwant\n%s", got, want)
	}
}

// TestDescribeParamFallsBackForAnUnknownFormat: a format added to the
// descriptors before it gets a phrase here is still named, never dropped.
func TestDescribeParamFallsBackForAnUnknownFormat(t *testing.T) {
	got := describeCheckParam(checkplugins.ParamSpec{Name: "x", Type: checkplugins.TypeString, Format: "brandNew"})
	if want := "x (format brandNew)"; got != want {
		t.Fatalf("describeCheckParam = %q, want %q", got, want)
	}
	got = describeCheckParam(checkplugins.ParamSpec{Name: "y", Type: checkplugins.TypeString})
	if want := "y"; got != want {
		t.Fatalf("describeCheckParam = %q, want %q", got, want)
	}
}

// TestCheckBodiesCarryTheGeneratedDocs: both write bodies — an
// organization's own and an installation-owned definition's — document
// plugin and params with the generated strings.
func TestCheckBodiesCarryTheGeneratedDocs(t *testing.T) {
	doc, err := Document()
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CheckDefinitionBody", "CheckDefinitionDefBody"} {
		schema, ok := parsed.Components.Schemas[name]
		if !ok {
			t.Fatalf("no %s schema", name)
		}
		if got := schema.Properties["plugin"].Description; got != checkPluginFieldDoc() {
			t.Errorf("%s.plugin description = %q", name, got)
		}
		if got := schema.Properties["params"].Description; got != checkParamsFieldDoc() {
			t.Errorf("%s.params description = %q", name, got)
		}
	}
}

// TestCheckPluginSchemaEnumsCoverTheDescriptors: the closed sets the
// GET /check-plugins schema declares (runs, a parameter's type, a metric's
// kind) admit every value a descriptor carries.
func TestCheckPluginSchemaEnumsCoverTheDescriptors(t *testing.T) {
	doc, err := Document()
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatal(err)
	}
	enum := func(schema, prop string) []string {
		t.Helper()
		e := parsed.Components.Schemas[schema].Properties[prop].Enum
		if len(e) == 0 {
			t.Fatalf("%s.%s has no enum", schema, prop)
		}
		return e
	}
	runs, types, kinds, names := enum("CheckPluginDTO", "runs"), enum("CheckPluginParamDTO", "type"), enum("CheckPluginMetricDTO", "kind"), enum("CheckPluginDTO", "name")
	if !slices.Equal(names, checkplugins.Names()) {
		t.Errorf("CheckPluginDTO.name enum = %v, want %v", names, checkplugins.Names())
	}
	for _, d := range checkplugins.All() {
		if !slices.Contains(runs, string(d.Runs)) {
			t.Errorf("%s runs %q, not in %v", d.Name, d.Runs, runs)
		}
		for _, p := range d.Params {
			if !slices.Contains(types, string(p.Type)) {
				t.Errorf("%s.%s type %q, not in %v", d.Name, p.Name, p.Type, types)
			}
		}
		for _, m := range d.Metrics {
			if !slices.Contains(kinds, string(m.Kind)) {
				t.Errorf("%s metric %s kind %q, not in %v", d.Name, m.Name, m.Kind, kinds)
			}
		}
	}
}

// TestEveryCheckFormatHasAPhrase: the params doc says in words what each
// format accepts; a format without a phrase would fall back to its bare
// name ("format x") in the API document.
func TestEveryCheckFormatHasAPhrase(t *testing.T) {
	for _, f := range checkplugins.Formats() {
		if checkFormatPhrases[f] == "" {
			t.Errorf("format %q has no phrase in checkFormatPhrases", f)
		}
	}
	if len(checkFormatPhrases) != len(checkplugins.Formats()) {
		t.Errorf("checkFormatPhrases has %d entries, checkplugins.Formats() %d", len(checkFormatPhrases), len(checkplugins.Formats()))
	}
}
