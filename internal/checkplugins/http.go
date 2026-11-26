package checkplugins

import (
	"errors"
	"fmt"
	"slices"

	"github.com/noixdev/pulsix/internal/probe/assert"
	"github.com/noixdev/pulsix/internal/probe/httpreq"
)

// maxHTTPJSONValues bounds http's json_values (backlog 3.6h).
// internal/probe/http trusts params that pass here, so the two must agree.
const maxHTTPJSONValues = 32

// httpPlugin: fetch a URL from the Pulsix server (backlog 3.13). There is
// no default URL; {address} in it stands for the host's address, which
// FormatURL accounts for.
//
// Backlog 3.6h (spec docs/superpowers/specs/2026-11-15-http-json-values-
// design.md) added an optional secret from the organization's secret
// store, sent as the request's credentials, and json_values: each entry
// "name=JSONPath" reads one scalar out of a JSON body and reports it on
// item "name" — a number as http.json.value, a string or boolean as
// http.json.text. The request's own metrics stay on the whole-host item "".
//
// Backlog 3.13b (spec docs/superpowers/specs/2026-11-16-web-assertions-
// design.md) added assertions: each entry "name=expression [@warn]"
// reports on item "name" whether it held (http.assertion: pass, or its
// severity) and what was seen (http.assertion_actual). Names are unique
// across json_values and assertions together.
func httpPlugin() Descriptor {
	secret := SecretParam()
	secret.Example = "api-token"
	return Descriptor{
		Name: "http",
		Runs: Server,
		Params: []ParamSpec{
			{Name: "url", Type: TypeString, Required: true, MaxBytes: maxParamStringLen, Format: FormatURL, Example: "https://{address}/"},
			{Name: "method", Type: TypeEnum, Options: slices.Clone(httpreq.Methods), Default: "GET"},
			{Name: "headers", Type: TypeList, MaxEntries: httpreq.MaxHeaders, MaxBytes: httpreq.MaxHeaderLen, Format: FormatHTTPHeader, Example: "Accept: application/json"},
			{Name: "body", Type: TypeString, NonEmpty: true, MaxBytes: httpreq.MaxBodyLen},
			{Name: "content_type", Type: TypeString, MaxBytes: httpreq.MaxContentTypeLen, Format: FormatMediaType, Default: httpreq.DefaultContentType},
			secret,
			syntheticText("body_contains", true),
			{Name: "json_values", Type: TypeList, MaxEntries: maxHTTPJSONValues, Format: FormatJSONValue, Example: "status=$.status"},
			{Name: "assertions", Type: TypeList, MaxEntries: assert.MaxEntries, MaxBytes: assert.MaxEntryLen, Format: FormatAssertion, Example: "ok=status == 200"},
			syntheticTimeout(60, 10),
			{Name: "follow_redirects", Type: TypeBool, Default: true},
			{Name: "verify_tls", Type: TypeBool, Default: true},
		},
		ItemParam:           "json_values",
		HostItemAlwaysAsked: true,
		ItemAsked:           httpItemAsked,
		MetricAsked:         httpMetricAsked,
		ManagerOnly:         httpManagerOnly,
		Check:               httpCheck,
		Metrics: []MetricSpec{
			{Name: "http.up", Kind: Numeric},
			// The failure reason of an attempt (probe.Failure*): "none" while
			// up, else why it failed.
			{Name: "http.failure", Kind: Discrete},
			{Name: "http.status_code", Kind: Numeric},
			{Name: "http.response_ms", Kind: Numeric},
			// The breakdown (backlog 3.13b): lookups, connects and TLS
			// handshakes summed over hops (dns_ms absent for an IP literal,
			// tls_ms when no hop used TLS), the last hop's time to first byte
			// and the body's transfer.
			{Name: "http.dns_ms", Kind: Numeric},
			{Name: "http.connect_ms", Kind: Numeric},
			{Name: "http.tls_ms", Kind: Numeric},
			{Name: "http.ttfb_ms", Kind: Numeric},
			{Name: "http.transfer_ms", Kind: Numeric},
			{Name: "http.redirects", Kind: Numeric},
			// The body's bytes as read (after the transport's transparent
			// gzip decoding), read to 1 MiB, or 16 MiB with a size
			// assertion; past that the declared Content-Length, else the
			// limit. Never an error.
			{Name: "http.content_bytes", Kind: Numeric},
			// The final hop's leaf certificate; absent over http.
			{Name: "http.cert_expiry_days", Kind: Numeric},
			{Name: "http.cert_issuer", Kind: Discrete},
			{Name: "http.body_match", Kind: Numeric},
			// Item: a json_values entry's name.
			{Name: "http.json.value", Kind: Numeric},
			{Name: "http.json.text", Kind: Discrete},
			// Item: an assertions entry's name. pass, or the entry's
			// severity (warn, crit) when it failed; and what was seen.
			{Name: "http.assertion", Kind: Discrete},
			{Name: "http.assertion_actual", Kind: Discrete},
		},
	}
}

// httpItemAsked: only the names of the json_values and assertions
// entries are asked for (the whole-host item "" always is, see
// HostItemAlwaysAsked). json_values is the ItemParam, so entries are its;
// the assertions are read here.
func httpItemAsked(params map[string]any, entries []string, item string) bool {
	assertions, _ := stringList(params["assertions"])
	return namedEntryAsked(entries, item) || namedEntryAsked(assertions, item)
}

// httpMetricAsked: http.body_match is reported only while body_contains
// is a non-empty string — internal/probe/http's own reading of the
// parameter (StrParam, then a test for ""); every other metric is asked.
func httpMetricAsked(params map[string]any, metric string) bool {
	if metric != "http.body_match" {
		return true
	}
	contains, _ := params["body_contains"].(string)
	return contains != ""
}

// httpManagerOnly: a check that sends a method other than GET or HEAD,
// headers or a body (backlog 3.84) is probed only from the Pulsix server.
// An agent of an earlier release fails such a method, but drops headers
// and a body without a word and would report a plain GET.
func httpManagerOnly(params map[string]any) string {
	method, _ := params["method"].(string)
	headers, _ := stringList(params["headers"])
	_, body := params["body"]
	_, contentType := params["content_type"]
	if (method != "" && method != "GET" && method != "HEAD") || len(headers) > 0 || body || contentType {
		return "send a method other than GET or HEAD, headers or a body"
	}
	return ""
}

// httpRequestCheck: no header twice, a body only with a method that takes
// one, a content type only with a body. Every value passed its spec.
func httpRequestCheck(params map[string]any) error {
	headers, _ := stringList(params["headers"])
	seen := make(map[string]bool, len(headers))
	for _, h := range headers {
		name, _, _ := httpreq.ParseHeader(h)
		if seen[name] {
			return fmt.Errorf("headers names %q twice", name)
		}
		seen[name] = true
	}
	method, _ := params["method"].(string)
	if method == "" {
		method = "GET"
	}
	_, body := params["body"]
	if body && !httpreq.TakesBody(method) {
		return errors.New("body needs method POST, PUT or PATCH")
	}
	if _, contentType := params["content_type"]; contentType && !body {
		return errors.New("content_type needs a body")
	}
	return nil
}

// httpCheck: the request rules (httpRequestCheck); no two json_values or
// assertions entries may report on the same item, and a JSON value or an
// assertion on the body needs a body, which HEAD does not fetch.
func httpCheck(params map[string]any) error {
	if err := httpRequestCheck(params); err != nil {
		return err
	}
	entries, _ := stringList(params["json_values"])
	if name, twice := duplicateEntryName(entries); twice {
		return fmt.Errorf("json_values names %q twice", name)
	}
	assertions, _ := stringList(params["assertions"])
	if name, twice := duplicateEntryName(assertions); twice {
		return fmt.Errorf("assertions names %q twice", name)
	}
	for _, a := range assertions {
		if namedEntryAsked(entries, entryName(a)) {
			return fmt.Errorf("json_values and assertions both name %q", entryName(a))
		}
	}
	if params["method"] != "HEAD" {
		return nil
	}
	if len(entries) > 0 {
		return errors.New("json_values needs a method other than HEAD: a HEAD response has no body")
	}
	for _, entry := range assertions {
		// Every entry passed its format already.
		if a, err := assert.Parse(entry); err == nil && a.Subject().ReadsBody() {
			return fmt.Errorf("assertion %q reads the body and needs a method other than HEAD: a HEAD response has no body", a.Name())
		}
	}
	return nil
}
