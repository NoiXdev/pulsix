package checkplugins_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// headerList is n distinct valid headers entries.
func headerList(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = "X-H" + strings.Repeat("x", i) + ": v"
	}
	return out
}

// TestHTTPRequestParams: the method, headers, body and content_type
// parameters of backlog 3.84 and the Check hook's rules over them.
func TestHTTPRequestParams(t *testing.T) {
	runParamCases(t, []paramCase{
		{"POST with a body", "http", httpOK(map[string]any{"method": "POST", "body": `{"ping":1}`}), true},
		{"PUT with body and type", "http", httpOK(map[string]any{"method": "PUT", "body": "a=1", "content_type": "application/x-www-form-urlencoded"}), true},
		{"PATCH with a body", "http", httpOK(map[string]any{"method": "PATCH", "body": "x"}), true},
		{"DELETE without a body", "http", httpOK(map[string]any{"method": "DELETE"}), true},
		{"OPTIONS", "http", httpOK(map[string]any{"method": "OPTIONS"}), true},
		{"POST without a body", "http", httpOK(map[string]any{"method": "POST"}), true},
		{"headers with GET", "http", httpOK(map[string]any{"headers": []any{"X-Api-Version: 2", "Accept: application/json"}}), true},
		{"16 headers", "http", httpOK(map[string]any{"headers": headerList(16)}), true},
		{"a body of 8 KiB", "http", httpOK(map[string]any{"method": "POST", "body": strings.Repeat("b", 8192)}), true},
		{"a body with line breaks", "http", httpOK(map[string]any{"method": "POST", "body": "line 1\r\nline 2\n\ttab"}), true},
		{"headers and a secret", "http", httpOK(map[string]any{"headers": []any{"X-Tenant: a"}, "secret": "api"}), true},
		{"json values with POST", "http", httpOK(map[string]any{"method": "POST", "json_values": []any{"a=$.a"}}), true},
		{"body assertion with OPTIONS", "http", httpOK(map[string]any{"method": "OPTIONS", "assertions": []any{`b=body contains "x"`}}), true},

		{"TRACE", "http", httpOK(map[string]any{"method": "TRACE"}), false},
		{"lower-case post", "http", httpOK(map[string]any{"method": "post"}), false},
		{"17 headers", "http", httpOK(map[string]any{"headers": headerList(17)}), false},
		{"a header over 1024 bytes", "http", httpOK(map[string]any{"headers": []any{"X-Long: " + strings.Repeat("v", 1017)}}), false},
		{"a header with a line break", "http", httpOK(map[string]any{"headers": []any{"X-A: a\r\nX-B: b"}}), false},
		{"a header without a colon", "http", httpOK(map[string]any{"headers": []any{"X-A"}}), false},
		{"Host", "http", httpOK(map[string]any{"headers": []any{"Host: example.com"}}), false},
		{"Authorization", "http", httpOK(map[string]any{"headers": []any{"Authorization: Bearer x"}}), false},
		{"Cookie", "http", httpOK(map[string]any{"headers": []any{"cookie: a=b"}}), false},
		{"Content-Type", "http", httpOK(map[string]any{"method": "POST", "body": "x", "headers": []any{"Content-Type: text/plain"}}), false},
		{"a header twice", "http", httpOK(map[string]any{"headers": []any{"X-A: 1", "x-a: 2"}}), false},
		{"a body with GET", "http", httpOK(map[string]any{"body": "x"}), false},
		{"a body with HEAD", "http", httpOK(map[string]any{"method": "HEAD", "body": "x"}), false},
		{"a body with DELETE", "http", httpOK(map[string]any{"method": "DELETE", "body": "x"}), false},
		{"a body with OPTIONS", "http", httpOK(map[string]any{"method": "OPTIONS", "body": "x"}), false},
		{"an empty body", "http", httpOK(map[string]any{"method": "POST", "body": ""}), false},
		{"a body over 8 KiB", "http", httpOK(map[string]any{"method": "POST", "body": strings.Repeat("b", 8193)}), false},
		{"a content type without a body", "http", httpOK(map[string]any{"method": "POST", "content_type": "text/plain"}), false},
		{"a bad content type", "http", httpOK(map[string]any{"method": "POST", "body": "x", "content_type": "json"}), false},
		{"a content type with a line break", "http", httpOK(map[string]any{"method": "POST", "body": "x", "content_type": "text/plain\r\nX: 1"}), false},
	})
}

// TestHTTPRequestMessages pins the refusals an operator reads.
func TestHTTPRequestMessages(t *testing.T) {
	cases := []struct {
		params map[string]any
		want   string
	}{
		{httpOK(map[string]any{"headers": []any{"X-A: 1", "x-a: 2"}}), `http: headers names "X-A" twice`},
		{httpOK(map[string]any{"body": "x"}), "http: body needs method POST, PUT or PATCH"},
		{httpOK(map[string]any{"method": "POST", "content_type": "text/plain"}), "http: content_type needs a body"},
		{httpOK(map[string]any{"headers": []any{"Authorization: x"}}), "http: headers entries must not set Authorization, Proxy-Authorization or Cookie: credentials belong in the check's secret"},
		{httpOK(map[string]any{"method": "POST", "body": "x", "content_type": "json"}), "http: content_type must be a media type such as application/json or text/plain; charset=utf-8"},
		{httpOK(map[string]any{"method": "HEAD", "json_values": []any{"a=$.a"}}), "http: json_values needs a method other than HEAD: a HEAD response has no body"},
		{httpOK(map[string]any{"method": "POST", "body": "x", "vantage_groups": []any{g1}}), "http: checks that send a method other than GET or HEAD, headers or a body are probed only from the Pulsix server"},
	}
	for _, tc := range cases {
		err := checkplugins.Validate("http", tc.params)
		if !errors.Is(err, checkplugins.ErrInvalidParams) || err.Error() != tc.want {
			t.Errorf("Validate(http, %v) = %v, want %q", tc.params, err, tc.want)
		}
	}
}

// TestHTTPCustomRequestRefusesVantageGroups: an agent of an earlier
// release would drop headers and a body and send a plain GET, so a check
// with a custom request names no vantage group (R8).
func TestHTTPCustomRequestRefusesVantageGroups(t *testing.T) {
	groups := map[string]any{"vantage_groups": []any{g1}}
	with := func(extra map[string]any) map[string]any {
		p := httpOK(extra)
		for k, v := range groups {
			p[k] = v
		}
		return p
	}
	runParamCases(t, []paramCase{
		{"GET with groups", "http", with(map[string]any{"method": "GET"}), true},
		{"HEAD with groups", "http", with(map[string]any{"method": "HEAD"}), true},
		{"POST with groups", "http", with(map[string]any{"method": "POST"}), false},
		{"DELETE with groups", "http", with(map[string]any{"method": "DELETE"}), false},
		{"OPTIONS with groups", "http", with(map[string]any{"method": "OPTIONS"}), false},
		{"headers with groups", "http", with(map[string]any{"headers": []any{"X-A: 1"}}), false},
		{"an empty header list with groups", "http", with(map[string]any{"headers": []any{}}), true},
		{"a body with groups", "http", with(map[string]any{"method": "PUT", "body": "x"}), false},
	})
}

func TestProbedOnlyFromManager(t *testing.T) {
	http, _ := checkplugins.Get("http")
	tcp, _ := checkplugins.Get("tcp")
	snmp, _ := checkplugins.Get("snmp")
	cases := []struct {
		name string
		d    checkplugins.Descriptor
		p    map[string]any
		want bool
	}{
		{"plain http", http, httpOK(nil), false},
		{"http GET", http, httpOK(map[string]any{"method": "GET"}), false},
		{"http HEAD", http, httpOK(map[string]any{"method": "HEAD"}), false},
		{"http with a secret", http, httpOK(map[string]any{"secret": "s"}), true},
		{"http POST", http, httpOK(map[string]any{"method": "POST"}), true},
		{"http headers", http, httpOK(map[string]any{"headers": []any{"X-A: 1"}}), true},
		{"http Go string headers", http, httpOK(map[string]any{"headers": []string{"X-A: 1"}}), true},
		{"http empty headers", http, httpOK(map[string]any{"headers": []any{}}), false},
		{"http body", http, httpOK(map[string]any{"body": "x"}), true},
		{"http content type", http, httpOK(map[string]any{"content_type": "text/plain"}), true},
		{"tcp", tcp, map[string]any{"port": 22}, false},
		{"snmp with a secret", snmp, map[string]any{"secret": "sw"}, true},
	}
	for _, c := range cases {
		if got := c.d.ProbedOnlyFromManager(c.p); got != c.want {
			t.Errorf("%s: ProbedOnlyFromManager = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestHTTPRequestParamSpecs pins the new parameters' specs and their
// place in the form order.
func TestHTTPRequestParamSpecs(t *testing.T) {
	d, _ := checkplugins.Get("http")
	byName := map[string]checkplugins.ParamSpec{}
	var names []string
	for _, p := range d.Params {
		byName[p.Name] = p
		names = append(names, p.Name)
	}
	if m := byName["method"]; !slices.Equal(m.Options, []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}) || m.Default != "GET" {
		t.Fatalf("method = %+v", m)
	}
	if h := byName["headers"]; h.Type != checkplugins.TypeList || h.MaxEntries != 16 || h.MaxBytes != 1024 || h.Format != checkplugins.FormatHTTPHeader {
		t.Fatalf("headers = %+v", h)
	}
	if b := byName["body"]; b.Type != checkplugins.TypeString || !b.NonEmpty || b.MaxBytes != 8192 || b.Format != "" {
		t.Fatalf("body = %+v", b)
	}
	if c := byName["content_type"]; c.Type != checkplugins.TypeString || c.MaxBytes != 255 || c.Format != checkplugins.FormatMediaType || c.Default != "application/json" {
		t.Fatalf("content_type = %+v", c)
	}
	if i := slices.Index(names, "headers"); i != 2 || names[3] != "body" || names[4] != "content_type" {
		t.Fatalf("params = %v, want method, headers, body, content_type in a row", names)
	}
}
