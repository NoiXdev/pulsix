package api

import (
	"fmt"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// The check DTOs' plugin schema and doc strings, derived from the plugin
// registry (backlog 3.54) instead of struct-tag literals, so a plugin added
// to internal/checkplugins reaches the OpenAPI document — and through it
// the SPA's generated types — with no edit here.
//
// Two huma hooks carry it, each where it fits:
//
//   - checkPluginName implements huma.SchemaProvider: a named string type
//     whose schema is the string enum of checkplugins.Names(), in that
//     order. The read DTOs' plugin fields use it in place of an `enum:"…"`
//     tag. A non-struct type is inlined, never a $ref, so the document is
//     byte-for-byte what the tag produced.
//   - The two write bodies implement huma.SchemaTransformer to set the
//     plugin and params descriptions. Doc strings are struct tags, which
//     cannot be computed, and a body's plugin field deliberately has no
//     enum (internal/checks answers for a bad plugin, see
//     checkDefinitionBody), so a provider type would be the wrong tool
//     there.

// checkPluginName is a check plugin's name on the read side of the API.
type checkPluginName string

// Schema makes the plugin a closed enum of every registered plugin.
func (checkPluginName) Schema(huma.Registry) *huma.Schema {
	names := checkplugins.Names()
	enum := make([]any, len(names))
	for i, n := range names {
		enum[i] = n
	}
	return &huma.Schema{Type: huma.TypeString, Enum: enum}
}

// withCheckPluginDocs documents a write body's plugin and params fields.
func withCheckPluginDocs(s *huma.Schema) *huma.Schema {
	if p, ok := s.Properties["plugin"]; ok {
		p.Description = checkPluginFieldDoc()
	}
	if p, ok := s.Properties["params"]; ok {
		p.Description = checkParamsFieldDoc()
	}
	return s
}

// TransformSchema documents plugin and params from the registry.
func (checkDefinitionBody) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	return withCheckPluginDocs(s)
}

// TransformSchema documents plugin and params from the registry.
func (checkDefinitionDefBody) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	return withCheckPluginDocs(s)
}

// checkPluginFieldDoc lists the plugins by where they run: "One of cpu, …
// (run by the host's agent) or icmp, … (probed from the manager)".
func checkPluginFieldDoc() string {
	var agent, server []string
	for _, d := range checkplugins.All() {
		if d.Runs == checkplugins.Server {
			server = append(server, d.Name)
		} else {
			agent = append(agent, d.Name)
		}
	}
	return "One of " + strings.Join(agent, ", ") + " (run by the host's agent) or " +
		strings.Join(server, ", ") + " (probed from the manager)"
}

// checkParamsFieldDoc describes every plugin's parameters: the plugins
// without any in one sentence, then one sentence per plugin naming its
// parameters in form order.
func checkParamsFieldDoc() string {
	var none []string
	var sentences []string
	for _, d := range checkplugins.All() {
		if len(d.Params) == 0 {
			none = append(none, d.Name)
			continue
		}
		params := make([]string, len(d.Params))
		for i, p := range d.Params {
			params[i] = describeCheckParamOf(d.Runs, p)
		}
		sentences = append(sentences, d.Name+": "+strings.Join(params, ", ")+".")
	}
	head := "Each parameter is optional unless marked required."
	if len(none) > 0 {
		head += " " + joinWords(none, "and") + " take none."
	}
	return strings.Join(append([]string{head}, sentences...), " ")
}

// checkFormatPhrases says in words what each format accepts. A format
// without a phrase is named as it is ("format x"), see describeCheckParam.
var checkFormatPhrases = map[checkplugins.Format]string{
	checkplugins.FormatNotBlank:      "not blank",
	checkplugins.FormatNoControl:     "without control characters",
	checkplugins.FormatDevicePath:    "a /dev/... path",
	checkplugins.FormatURL:           "an http or https URL that may contain {address}",
	checkplugins.FormatStatusURL:     "an http or https URL without user info or fragment",
	checkplugins.FormatPHPFPMPool:    "an http or https URL without user info or fragment, or a FastCGI address fcgi://host:port/path or unix:/socket:/path whose path is / and letters, digits, _ / - without a dot",
	checkplugins.FormatDNSName:       "a DNS name that may contain {address}",
	checkplugins.FormatServer:        "an IP address with an optional port",
	checkplugins.FormatDockerLabel:   "key or key=value",
	checkplugins.FormatDockerSocket:  "an absolute path or npipe:////./pipe/name",
	checkplugins.FormatSecretName:    "the name of a secret in the agent's agent.yaml: 1 to 64 letters, digits, _ . -",
	checkplugins.FormatCommandName:   "the name of a command under commands: in the agent's agent.yaml: 1 to 64 letters, digits, _ . -",
	checkplugins.FormatSNMPOID:       "name=OID, the name 1 to 64 letters, digits, _ . -, the OID numeric such as 1.3.6.1.2.1.1.3.0",
	checkplugins.FormatUUID:          "a lower-case UUID",
	checkplugins.FormatHTTPHeader:    "Name: value, the name a header name other than Host, Content-Length, Transfer-Encoding, Connection, User-Agent, Keep-Alive, Proxy-Connection, TE, Trailer, Upgrade, Expect, Accept-Encoding, Content-Type, Authorization, Proxy-Authorization, Cookie, a name containing token, secret, apikey, api-key, api_key, password, access-key, access_key or session or the segment auth, X-Forwarded and X-Forwarded-*, Forwarded, X-Real-IP, X-Original-URL, X-Rewrite-URL and the method overrides, the value UTF-8 without control characters",
	checkplugins.FormatMediaType:     "a media type such as application/json (application/json when a body is sent without one)",
	checkplugins.FormatScriptArg:     "command:placeholder=value, a value for a placeholder the command's definition in the host's agent.yaml declares: the command one listed in commands, the placeholder a lower-case letter followed by up to 31 lower-case letters, digits or _, the value 1 to 512 bytes without control, format or separator characters; the host's own rule judges the value",
	checkplugins.FormatJSONValue:     "name=JSONPath, the name 1 to 64 letters, digits, _ . -, the path at most 512 bytes in the supported subset of RFC 9535 JSONPath (one selector per bracket, at most one .., filters comparing @ paths with literals) such as $.status",
	checkplugins.FormatAssertion:     "name=assertion [@warn], the name 1 to 64 letters, digits, _ . -, the assertion a subject (status, response_ms, ttfb_ms, size, redirects, cert_days, cert_issuer, body, header(\"Name\"), json(\"$.path\"), html(\"selector\"), html(\"selector\") count), an operator and an operand such as status == 200",
	checkplugins.FormatTCPAssertion:  "name=assertion [@warn] as for http, the subject one of connect_ms, response_ms, response (the first line the target sent) such as connect_ms < 500",
	checkplugins.FormatDNSAssertion:  "name=assertion [@warn] as for http, the subject one of resolve_ms, answer_count, answers (sorted, joined with \", \"), answer(\"value\") (exists or !exists only) such as answer(\"192.0.2.1\") exists",
	checkplugins.FormatTLSAssertion:  "name=assertion [@warn] as for http, the subject one of handshake_ms, cert_valid, cert_days, cert_issuer, cert_subject, cert_sans, version (1.0 to 1.3), cipher such as version >= 1.2",
	checkplugins.FormatSNMPAssertion: "name=assertion [@warn] as for http, the subject one of value(\"name\") (the oids entry name, compared as a number or as text by its operator and operand), response_ms, uptime (seconds) such as value(\"load\") < 80",
}

// serverSecretPhrase is FormatSecretName's phrase for a plugin the
// manager probes: its secret is in the organization's secret store
// (backlog 3.13d), not in an agent.yaml.
const serverSecretPhrase = "the name of a secret in the organization's secret store: 1 to 64 letters, digits, _ . -"

// describeCheckParamOf is describeCheckParam for a parameter of a plugin
// that runs where runs says.
func describeCheckParamOf(runs checkplugins.Runs, p checkplugins.ParamSpec) string {
	if runs == checkplugins.Server && p.Format == checkplugins.FormatSecretName {
		return describeCheckParamWith(p, serverSecretPhrase)
	}
	return describeCheckParam(p)
}

// describeCheckParam renders one parameter as "name (part; part; …)":
// required, its shape, emptiness, size bounds, format and default, each
// only where the spec sets it. Parts are separated by semicolons because
// an enum's options are a comma list of their own.
func describeCheckParam(p checkplugins.ParamSpec) string {
	return describeCheckParamWith(p, "")
}

// describeCheckParamWith is describeCheckParam with formatPhrase in place
// of the format's own phrase when it is not "".
func describeCheckParamWith(p checkplugins.ParamSpec, formatPhrase string) string {
	list := p.Type == checkplugins.TypeList
	each := func(s string) string {
		if list {
			return "each " + s
		}
		return s
	}
	var parts []string
	if p.Required {
		parts = append(parts, "required")
	}
	switch p.Type {
	case checkplugins.TypeList:
		parts = append(parts, "list")
	case checkplugins.TypeInteger:
		if p.Min != nil && p.Max != nil {
			parts = append(parts, fmt.Sprintf("%d to %d", *p.Min, *p.Max))
		}
	case checkplugins.TypeBool:
		parts = append(parts, "true or false")
	case checkplugins.TypeEnum:
		parts = append(parts, joinWords(p.Options, "or"))
	}
	if p.NonEmpty {
		parts = append(parts, "not empty")
	}
	if p.MaxEntries > 0 {
		parts = append(parts, fmt.Sprintf("at most %d entries", p.MaxEntries))
	}
	if p.MaxBytes > 0 {
		parts = append(parts, each(fmt.Sprintf("at most %d bytes", p.MaxBytes)))
	}
	if p.Format != "" {
		phrase, ok := formatPhrase, formatPhrase != ""
		if !ok {
			phrase, ok = checkFormatPhrases[p.Format]
		}
		if !ok {
			phrase = "format " + string(p.Format)
		}
		parts = append(parts, each(phrase))
	}
	if p.Default != nil {
		parts = append(parts, fmt.Sprintf("default %v", p.Default))
	}
	if len(parts) == 0 {
		return p.Name
	}
	return p.Name + " (" + strings.Join(parts, "; ") + ")"
}

// joinWords joins words as "a, b and c" (or "a, b or c").
func joinWords(words []string, conj string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " " + conj + " " + words[len(words)-1]
}
