package api

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestCheckPluginFieldDoc: the write bodies' plugin doc string, built from
// the registry, reads exactly as the literal it replaced.
func TestCheckPluginFieldDoc(t *testing.T) {
	want := "One of cpu, memory, disk, service, network, uptime, docker, temperature, smart, raid, zfs, postgres, mysql, redis, nginx, apache, caddy, phpfpm, script (run by the host's agent) or icmp, tcp, http, tls, dns, snmp (probed from the manager)"
	if got := checkPluginFieldDoc(); got != want {
		t.Fatalf("checkPluginFieldDoc() =\n%s\nwant\n%s", got, want)
	}
}

// TestCheckParamsFieldDoc pins the write bodies' params doc string the
// descriptors generate: one sentence per plugin, its parameters in form
// order with their bounds, format and default.
func TestCheckParamsFieldDoc(t *testing.T) {
	// What every synthetic plugin ends with (backlog 3.13a).
	const vantage = "vantage_manager (true or false; default true), " +
		"vantage_groups (list; at most 8 entries; each a lower-case UUID), " +
		"agents_per_group (1 to 10; default 3), fail_policy (any, majority or all; default any). "
	want := "Each parameter is optional unless marked required. " +
		"cpu, memory and uptime take none. " +
		"disk: mounts (list; each not blank). " +
		"service: units (required; list; not empty; each not blank). " +
		"network: interfaces (list; each not blank). " +
		"docker: selection (all or labeled; default all), label (key or key=value; default pulsix.monitor=true), " +
		"socket (at most 255 bytes; an absolute path or npipe:////./pipe/name; default /var/run/docker.sock), " +
		"resources (true or false; default false). " +
		"temperature: sensors (list; at most 64 entries; each at most 255 bytes; each without control characters). " +
		"smart: devices (list; at most 64 entries; each at most 255 bytes; each a /dev/... path). " +
		"raid: arrays (list; at most 64 entries; each at most 255 bytes; each without control characters). " +
		"zfs: pools (list; at most 64 entries; each at most 255 bytes; each without control characters), " +
		"optional_pools (list; at most 64 entries; each at most 255 bytes; each without control characters). " +
		"postgres: secret (required; not empty; at most 64 bytes; the name of a secret in the agent's agent.yaml: 1 to 64 letters, digits, _ . -), " +
		"databases (list; at most 64 entries; each at most 63 bytes; each without control characters). " +
		"mysql: secret (required; not empty; at most 64 bytes; the name of a secret in the agent's agent.yaml: 1 to 64 letters, digits, _ . -), " +
		"schema_sizes (true or false; default false). " +
		"redis: secret (not empty; at most 64 bytes; the name of a secret in the agent's agent.yaml: 1 to 64 letters, digits, _ . -). " +
		"nginx: url (at most 500 bytes; an http or https URL without user info or fragment; default http://127.0.0.1/nginx_status), " +
		"secret (not empty; at most 64 bytes; the name of a secret in the agent's agent.yaml: 1 to 64 letters, digits, _ . -), " +
		"verify_tls (true or false; default true). " +
		"apache: url (at most 500 bytes; an http or https URL without user info or fragment; default http://127.0.0.1/server-status?auto), " +
		"secret (not empty; at most 64 bytes; the name of a secret in the agent's agent.yaml: 1 to 64 letters, digits, _ . -), " +
		"verify_tls (true or false; default true). " +
		"caddy: url (at most 500 bytes; an http or https URL without user info or fragment; default http://127.0.0.1:2019/metrics), " +
		"secret (not empty; at most 64 bytes; the name of a secret in the agent's agent.yaml: 1 to 64 letters, digits, _ . -), " +
		"verify_tls (true or false; default true). " +
		"phpfpm: urls (list; at most 16 entries; each at most 500 bytes; each an http or https URL without user info or fragment, or a FastCGI address fcgi://host:port/path or unix:/socket:/path whose path is / and letters, digits, _ / - without a dot), " +
		"secret (not empty; at most 64 bytes; the name of a secret in the agent's agent.yaml: 1 to 64 letters, digits, _ . -), " +
		"verify_tls (true or false; default true). " +
		"script: commands (required; list; not empty; at most 32 entries; each the name of a command under commands: in the agent's agent.yaml: 1 to 64 letters, digits, _ . -), " +
		"args (list; at most 64 entries; each at most 640 bytes; each command:placeholder=value, a value for a placeholder the command's definition in the host's agent.yaml declares: " +
		"the command one listed in commands, the placeholder a lower-case letter followed by up to 31 lower-case letters, digits or _, the value 1 to 512 bytes without control, format or separator characters; the host's own rule judges the value), " +
		"format (nagios or json; default nagios), timeout_seconds (1 to 60; default 10). " +
		"icmp: count (1 to 10; default 3), timeout_s (1 to 30; default 5), " + vantage +
		"tcp: port (required; 1 to 65535), send (at most 500 bytes), expect (not empty; at most 500 bytes), timeout_s (1 to 30; default 5), " +
		"assertions (list; at most 32 entries; each at most 512 bytes; each name=assertion [@warn] as for http, the subject one of connect_ms, response_ms, response (the first line the target sent) such as connect_ms < 500), " + vantage +
		"http: url (required; at most 500 bytes; an http or https URL that may contain {address}), method (GET, HEAD, POST, PUT, PATCH, DELETE or OPTIONS; default GET), " +
		"headers (list; at most 16 entries; each at most 1024 bytes; each Name: value, the name a header name other than Host, Content-Length, Transfer-Encoding, Connection, User-Agent, " +
		"Keep-Alive, Proxy-Connection, TE, Trailer, Upgrade, Expect, Accept-Encoding, Content-Type, Authorization, Proxy-Authorization, Cookie, a name containing token, secret, apikey, api-key, api_key, password, access-key, access_key or session or the segment auth, " +
		"X-Forwarded and X-Forwarded-*, Forwarded, X-Real-IP, X-Original-URL, X-Rewrite-URL and the method overrides, the value UTF-8 without control characters), " +
		"body (not empty; at most 8192 bytes), content_type (at most 255 bytes; a media type such as application/json (application/json when a body is sent without one)), " +
		"secret (not empty; at most 64 bytes; the name of a secret in the organization's secret store: 1 to 64 letters, digits, _ . -), " +
		"body_contains (not empty; at most 500 bytes), " +
		"json_values (list; at most 32 entries; each name=JSONPath, the name 1 to 64 letters, digits, _ . -, the path at most 512 bytes in the supported subset of RFC 9535 JSONPath (one selector per bracket, at most one .., filters comparing @ paths with literals) such as $.status), " +
		"assertions (list; at most 32 entries; each at most 512 bytes; each name=assertion [@warn], the name 1 to 64 letters, digits, _ . -, " +
		"the assertion a subject (status, response_ms, ttfb_ms, size, redirects, cert_days, cert_issuer, body, header(\"Name\"), json(\"$.path\"), " +
		"html(\"selector\"), html(\"selector\") count), an operator and an operand such as status == 200), " +
		"timeout_s (1 to 60; default 10), " +
		"follow_redirects (true or false; default true), verify_tls (true or false; default true), " + vantage +
		"tls: port (1 to 65535; default 443), server_name (at most 500 bytes; not blank), timeout_s (1 to 30; default 10), " +
		"assertions (list; at most 32 entries; each at most 512 bytes; each name=assertion [@warn] as for http, the subject one of handshake_ms, cert_valid, cert_days, cert_issuer, cert_subject, cert_sans, version (1.0 to 1.3), cipher such as version >= 1.2), " + vantage +
		"dns: name (at most 500 bytes; a DNS name that may contain {address}; default {address}), " +
		"type (A, AAAA, CNAME, MX, TXT or NS; default A), server (at most 500 bytes; an IP address with an optional port), " +
		"expect (not empty; at most 500 bytes), timeout_s (1 to 30; default 5), " +
		"assertions (list; at most 32 entries; each at most 512 bytes; each name=assertion [@warn] as for http, the subject one of resolve_ms, answer_count, answers (sorted, joined with \", \"), answer(\"value\") (exists or !exists only) such as answer(\"192.0.2.1\") exists), " + vantage +
		"snmp: secret (required; not empty; at most 64 bytes; the name of a secret in the organization's secret store: 1 to 64 letters, digits, _ . -), " +
		"version (v2c or v3; default v2c), port (1 to 65535; default 161), timeout_seconds (1 to 30; default 5), " +
		"oids (list; at most 32 entries; each name=OID, the name 1 to 64 letters, digits, _ . -, the OID numeric such as 1.3.6.1.2.1.1.3.0), " +
		"interfaces (true or false; default false), " + strings.TrimSuffix(vantage, " ")
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
					Enum schemaEnum `json:"enum"`
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
