package checkplugins

import (
	"errors"
	"fmt"
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
func httpPlugin() Descriptor {
	secret := SecretParam()
	secret.Example = "api-token"
	return Descriptor{
		Name: "http",
		Runs: Server,
		Params: []ParamSpec{
			{Name: "url", Type: TypeString, Required: true, MaxBytes: maxParamStringLen, Format: FormatURL, Example: "https://{address}/"},
			{Name: "method", Type: TypeEnum, Options: []string{"GET", "HEAD"}, Default: "GET"},
			secret,
			syntheticText("body_contains", true),
			{Name: "json_values", Type: TypeList, MaxEntries: maxHTTPJSONValues, Format: FormatJSONValue, Example: "status=$.status"},
			syntheticTimeout(60, 10),
			{Name: "follow_redirects", Type: TypeBool, Default: true},
			{Name: "verify_tls", Type: TypeBool, Default: true},
		},
		ItemParam:           "json_values",
		HostItemAlwaysAsked: true,
		ItemAsked:           httpItemAsked,
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
			// gzip decoding), counted to 16 MiB; past that an error.
			{Name: "http.content_bytes", Kind: Numeric},
			// The final hop's leaf certificate; absent over http.
			{Name: "http.cert_expiry_days", Kind: Numeric},
			{Name: "http.cert_issuer", Kind: Discrete},
			{Name: "http.body_match", Kind: Numeric},
			// Item: a json_values entry's name.
			{Name: "http.json.value", Kind: Numeric},
			{Name: "http.json.text", Kind: Discrete},
		},
	}
}

// httpItemAsked: only the names of the json_values entries are asked for
// (the whole-host item "" always is, see HostItemAlwaysAsked).
func httpItemAsked(_ map[string]any, entries []string, item string) bool {
	return namedEntryAsked(entries, item)
}

// httpCheck: two json_values entries must not report on the same item,
// and a JSON value needs a body, which HEAD does not fetch.
func httpCheck(params map[string]any) error {
	entries, _ := stringList(params["json_values"])
	if name, twice := duplicateEntryName(entries); twice {
		return fmt.Errorf("json_values names %q twice", name)
	}
	if len(entries) > 0 && params["method"] == "HEAD" {
		return errors.New("json_values needs method GET: a HEAD response has no body")
	}
	return nil
}
