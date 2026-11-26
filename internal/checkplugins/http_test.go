package checkplugins_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// httpOK is the smallest valid http parameter set plus extra.
func httpOK(extra map[string]any) map[string]any {
	params := map[string]any{"url": "https://{address}/api/health"}
	for k, v := range extra {
		params[k] = v
	}
	return params
}

// jsonValueList is n distinct valid json_values entries.
func jsonValueList(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = "v" + strings.Repeat("x", i) + "=$.status"
	}
	return out
}

// TestValidateHTTPJSONValues: the json_values and secret rules of the
// spec's Descriptor section (docs/superpowers/specs/2026-11-15-http-json-
// values-design.md) and the Check hook.
func TestValidateHTTPJSONValues(t *testing.T) {
	runParamCases(t, []paramCase{
		{"one value", "http", httpOK(map[string]any{"json_values": []any{"status=$.status"}}), true},
		{"with method GET", "http", httpOK(map[string]any{"method": "GET", "json_values": []any{"queue=$.queues[0].depth"}}), true},
		{"32 values", "http", httpOK(map[string]any{"json_values": jsonValueList(32)}), true},
		{"Go string list", "http", httpOK(map[string]any{"json_values": []string{"a=$.a", "b=$['b c']"}}), true},
		{"empty list with HEAD", "http", httpOK(map[string]any{"method": "HEAD", "json_values": []any{}}), true},
		{"empty list", "http", httpOK(map[string]any{"json_values": []any{}}), true},
		{"a filter selector", "http", httpOK(map[string]any{"json_values": []any{"db=$.checks[?@.name=='db'].status"}}), true},
		{"a secret", "http", httpOK(map[string]any{"secret": "api-token"}), true},
		{"a secret and values", "http", httpOK(map[string]any{"secret": "api.token_1", "json_values": []any{"a=$.a"}}), true},
		{"a secret with HEAD", "http", httpOK(map[string]any{"secret": "api", "method": "HEAD"}), true},

		{"33 values", "http", httpOK(map[string]any{"json_values": jsonValueList(33)}), false},
		{"not a list", "http", httpOK(map[string]any{"json_values": "a=$.a"}), false},
		{"entry not a string", "http", httpOK(map[string]any{"json_values": []any{1.0}}), false},
		{"empty entry", "http", httpOK(map[string]any{"json_values": []any{""}}), false},
		{"entry without a name", "http", httpOK(map[string]any{"json_values": []any{"$.a"}}), false},
		{"entry with a bad name", "http", httpOK(map[string]any{"json_values": []any{"a b=$.a"}}), false},
		{"entry with a path not starting with $", "http", httpOK(map[string]any{"json_values": []any{"a=.a"}}), false},
		{"entry with an unparsable path", "http", httpOK(map[string]any{"json_values": []any{"a=$["}}), false},
		{"entry with a path over 512 bytes", "http", httpOK(map[string]any{"json_values": []any{"a=$." + strings.Repeat("p", 511)}}), false},
		{"empty secret", "http", httpOK(map[string]any{"secret": ""}), false},
		{"secret with a space", "http", httpOK(map[string]any{"secret": "my secret"}), false},
		{"secret of 65 bytes", "http", httpOK(map[string]any{"secret": strings.Repeat("s", 65)}), false},
		{"secret not a string", "http", httpOK(map[string]any{"secret": 1.0}), false},
		// The Check hook.
		{"HEAD with values", "http", httpOK(map[string]any{"method": "HEAD", "json_values": []any{"a=$.a"}}), false},
		{"duplicate names", "http", httpOK(map[string]any{"json_values": []any{"a=$.a", "a=$.b"}}), false},
		{"duplicate names, same path", "http", httpOK(map[string]any{"json_values": []any{"a=$.a", "a=$.a"}}), false},
	})
}

// TestValidateHTTPMessages pins the Check hook's refusals and the format's.
func TestValidateHTTPMessages(t *testing.T) {
	cases := []struct {
		params map[string]any
		want   string
	}{
		{httpOK(map[string]any{"method": "HEAD", "json_values": []any{"a=$.a"}}), "http: json_values needs method GET: a HEAD response has no body"},
		{httpOK(map[string]any{"json_values": []any{"a=$.a", "b=$.b", "a=$.c"}}), `http: json_values names "a" twice`},
		{httpOK(map[string]any{"json_values": []any{"a b=$.a"}}), "http: json_values entries must be name=JSONPath, the name 1 to 64 letters, digits, '_', '.' or '-'"},
		{httpOK(map[string]any{"json_values": []any{"a=.a"}}), "http: json_values entries must be name=JSONPath, the path starting with $ and at most 512 bytes"},
		{httpOK(map[string]any{"json_values": []any{"a=$." + strings.Repeat("p", 511)}}), "http: json_values entries must be name=JSONPath, the path starting with $ and at most 512 bytes"},
		{httpOK(map[string]any{"json_values": []any{"a=$..*..*"}}), "http: json_values entries must be name=JSONPath, the path a JSONPath in Pulsix's supported subset (see the docs): at byte 4: at most one descendant segment (..)"},
		{httpOK(map[string]any{"json_values": []any{"a=$[0,0]"}}), "http: json_values entries must be name=JSONPath, the path a JSONPath in Pulsix's supported subset (see the docs): at byte 3: a bracket holds one selector"},
	}
	for _, tc := range cases {
		err := checkplugins.Validate("http", tc.params)
		if !errors.Is(err, checkplugins.ErrInvalidParams) || err.Error() != tc.want {
			t.Errorf("Validate(http, %v) = %v, want %q", tc.params, err, tc.want)
		}
	}
	// A path the parser refuses quotes the parser: the expression is the
	// operator's own configuration, never a response.
	err := checkplugins.Validate("http", httpOK(map[string]any{"json_values": []any{"a=$["}}))
	const prefix = "http: json_values entries must be name=JSONPath, the path a JSONPath in Pulsix's supported subset (see the docs): "
	if !errors.Is(err, checkplugins.ErrInvalidParams) || !strings.HasPrefix(err.Error(), prefix) || len(err.Error()) == len(prefix) {
		t.Errorf("Validate(http, a=$[) = %v, want the prefix %q and the parser's reason", err, prefix)
	}
}

// TestHTTPDescriptor pins what a parameter case cannot see: the order of
// the parameters, the item list and its hooks, and the metrics with their
// kinds.
func TestHTTPDescriptor(t *testing.T) {
	d, ok := checkplugins.Get("http")
	if !ok {
		t.Fatal("Get(http) found nothing")
	}
	if d.Runs != checkplugins.Server || d.ItemParam != "json_values" || !d.HostItemAlwaysAsked || d.ItemAsked == nil || d.ItemEntry != nil || d.Check == nil {
		t.Fatalf("http descriptor = %+v", d)
	}
	if !d.UsesSecret() {
		t.Fatal("http must take the shared secret parameter")
	}
	var names []string
	byName := map[string]checkplugins.ParamSpec{}
	for _, p := range d.Params {
		names = append(names, p.Name)
		byName[p.Name] = p
	}
	want := []string{"url", "method", "secret", "body_contains", "json_values", "assertions", "timeout_s", "follow_redirects", "verify_tls", "vantage_manager", "vantage_groups", "agents_per_group", "fail_policy"}
	if !slices.Equal(names, want) {
		t.Fatalf("params = %v, want %v", names, want)
	}
	if s := byName["secret"]; !reflect.DeepEqual(s, withExample(checkplugins.SecretParam(), "api-token")) {
		t.Fatalf("secret = %+v, want the optional shared secret parameter", s)
	}
	jv := byName["json_values"]
	if jv.Type != checkplugins.TypeList || jv.MaxEntries != 32 || jv.Format != checkplugins.FormatJSONValue || jv.NonEmpty || jv.Required || jv.Default != nil || jv.Example != "status=$.status" {
		t.Fatalf("json_values = %+v", jv)
	}
	as := byName["assertions"]
	if as.Type != checkplugins.TypeList || as.MaxEntries != 32 || as.MaxBytes != 512 || as.Format != checkplugins.FormatAssertion || as.NonEmpty || as.Required || as.Default != nil || as.Example != "ok=status == 200" {
		t.Fatalf("assertions = %+v", as)
	}
	metrics := []checkplugins.MetricSpec{
		{Name: "http.up", Kind: checkplugins.Numeric},
		{Name: "http.failure", Kind: checkplugins.Discrete},
		{Name: "http.status_code", Kind: checkplugins.Numeric},
		{Name: "http.response_ms", Kind: checkplugins.Numeric},
		{Name: "http.dns_ms", Kind: checkplugins.Numeric},
		{Name: "http.connect_ms", Kind: checkplugins.Numeric},
		{Name: "http.tls_ms", Kind: checkplugins.Numeric},
		{Name: "http.ttfb_ms", Kind: checkplugins.Numeric},
		{Name: "http.transfer_ms", Kind: checkplugins.Numeric},
		{Name: "http.redirects", Kind: checkplugins.Numeric},
		{Name: "http.content_bytes", Kind: checkplugins.Numeric},
		{Name: "http.cert_expiry_days", Kind: checkplugins.Numeric},
		{Name: "http.cert_issuer", Kind: checkplugins.Discrete},
		{Name: "http.body_match", Kind: checkplugins.Numeric},
		{Name: "http.json.value", Kind: checkplugins.Numeric},
		{Name: "http.json.text", Kind: checkplugins.Discrete},
		{Name: "http.assertion", Kind: checkplugins.Discrete},
		{Name: "http.assertion_actual", Kind: checkplugins.Discrete},
	}
	if !slices.Equal(d.Metrics, metrics) {
		t.Fatalf("metrics = %v, want %v", d.Metrics, metrics)
	}
}

func withExample(p checkplugins.ParamSpec, example string) checkplugins.ParamSpec {
	p.Example = example
	return p
}

// TestJSONValueFormat: name=path split at the first '=', the name the
// secret-name pattern, the path at most 512 bytes and accepted by the
// JSONPath parser. The rows the SPA's twin agrees on are in
// testdata/formats.json; the syntax rows here are the server's alone.
func TestJSONValueFormat(t *testing.T) {
	cases := []struct {
		in    string
		valid bool
	}{
		{"status=$.status", true},
		{"a=$", true},
		{"temp_c=$.sensors[0].value", true},
		{"A_b.c-9=$['key with space']", true},
		{"a=$.a[-1]", true},
		{"a=$[*]", true},
		{"a=$..x", true},
		{"a=$..[?@.x=='a..b'].y", true},
		{"a=$[?@.x=='y'].z", true},
		{strings.Repeat("n", 64) + "=$.a", true},
		{"a=$." + strings.Repeat("p", 510), true},
		{"a=$['" + strings.Repeat("ü", 253) + "x']", true},

		{"", false},
		{"a", false},
		{"=$.a", false},
		{"a=", false},
		{"$.a", false},
		{strings.Repeat("n", 65) + "=$.a", false},
		{"a b=$.a", false},
		{"ä=$.a", false},
		{"a=.a", false},
		{"a=a", false},
		{"a=@.a", false},
		{"a= $.a", false},
		{"a=$." + strings.Repeat("p", 511), false},
		{"a=$['" + strings.Repeat("ü", 254) + "']", false},
		// Syntax only the parser judges.
		{"a=$[", false},
		{"a=$.", false},
		{"a=$..", false},
		{"a=$x", false},
		{"a=$.a=b", false},
		{"a=$['open", false},
		{"a=$[?@.x ==]", false},
		{"a=$.a\n", false},
		// More than one descendant segment, filters included.
		{"a=$..*..*", false},
		{"a=$..*..*..*", false},
		{"a=$..[?@..x]", false},
		{"a=$[?@..x && @..y]", false},
		// Outside the bounded subset (backlog 3.6h review): unions, slices,
		// $ inside a filter, functions, nested filters, wildcards in one.
		{"a=$[0,0]", false},
		{"a=$[*,*]", false},
		{"a=$[0:2]", false},
		{"a=$[?@.a == $.b]", false},
		{"a=$[0][?count($[0][*])>0]", false},
		{"a=$[?match(@, 'a.*')]", false},
		{"a=$[?search(@.a, @.b)]", false},
		{"a=$[?@[?@.a]]", false},
		{"a=$[?@.*]", false},
		{"a=$[?@.a == @.b]", false},
	}
	for _, tc := range cases {
		if got := checkplugins.FormatJSONValue.Valid(tc.in); got != tc.valid {
			t.Errorf("FormatJSONValue.Valid(%q) = %v, want %v", tc.in, got, tc.valid)
		}
	}
}

// TestHTTPAsksForItem: a json_values entry asks for its name; the
// request's own metrics (http.up, …) are on the whole-host item "",
// always asked; without json_values no other item is.
func TestHTTPAsksForItem(t *testing.T) {
	values := []any{"status=$.status", "queue.depth=$.queues[0].depth"}
	cases := []struct {
		name   string
		params map[string]any
		item   string
		want   bool
	}{
		{"an entry's name", map[string]any{"json_values": values}, "status", true},
		{"a name with a dot", map[string]any{"json_values": values}, "queue.depth", true},
		{"a name off the list", map[string]any{"json_values": values}, "version", false},
		{"not the whole entry", map[string]any{"json_values": values}, "status=$.status", false},
		{"not the path", map[string]any{"json_values": values}, "$.status", false},
		{"not a prefix of a name", map[string]any{"json_values": values}, "stat", false},
		{"the whole-host item with values", map[string]any{"json_values": values}, "", true},
		{"the whole-host item without values", map[string]any{"url": "https://{address}/"}, "", true},
		// No list asks for no JSON item: the request's own metrics are
		// all on "", so a removed last entry drops its item at once.
		{"no list asks for no JSON item", map[string]any{"url": "https://{address}/"}, "status", false},
		{"an empty list asks for no JSON item", map[string]any{"json_values": []any{}}, "status", false},
		{"a JSON null list asks for no JSON item", map[string]any{"json_values": nil}, "status", false},
		{"a list that is not strings asks for everything", map[string]any{"json_values": []any{1.0}}, "status", true},
		{"a Go string list", map[string]any{"json_values": []string{"a=$.a"}}, "a", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkplugins.AsksForItem("http", tc.params, tc.item); got != tc.want {
				t.Fatalf("AsksForItem(http, %v, %q) = %v, want %v", tc.params, tc.item, got, tc.want)
			}
		})
	}
}

// TestHTTPAsksForMetric (backlog 3.88): http.body_match is reported only
// while body_contains is set (internal/probe/http reads it the same way:
// a string, and not the empty one), so removing body_contains stops the
// metric being asked at once instead of leaving a stale row behind. Every
// other metric is always asked.
func TestHTTPAsksForMetric(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		metric string
		want   bool
	}{
		{"body match with body_contains", map[string]any{"body_contains": "ok"}, "http.body_match", true},
		{"body match without body_contains", map[string]any{"url": "https://{address}/"}, "http.body_match", false},
		{"body match with no params at all", nil, "http.body_match", false},
		{"body match with an empty body_contains", map[string]any{"body_contains": ""}, "http.body_match", false},
		{"body match with a body_contains that is not a string", map[string]any{"body_contains": 1.0}, "http.body_match", false},
		{"a request metric without body_contains", map[string]any{"url": "https://{address}/"}, "http.up", true},
		{"a timing metric without body_contains", nil, "http.dns_ms", true},
		{"an assertion metric without body_contains", nil, "http.assertion", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkplugins.AsksForMetric("http", tc.params, nil, tc.metric); got != tc.want {
				t.Fatalf("AsksForMetric(http, %v, %q) = %v, want %v", tc.params, tc.metric, got, tc.want)
			}
		})
	}
}

// assertionList is n distinct valid assertions.
func assertionList(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = "a" + strings.Repeat("x", i) + "=status == 200"
	}
	return out
}

// TestValidateHTTPAssertions: the assertions rules of the spec's section 3
// (docs/superpowers/specs/2026-11-16-web-assertions-design.md) and the
// Check hook's refusals.
func TestValidateHTTPAssertions(t *testing.T) {
	runParamCases(t, []paramCase{
		{"one assertion", "http", httpOK(map[string]any{"assertions": []any{"ok=status == 200"}}), true},
		{"32 assertions", "http", httpOK(map[string]any{"assertions": assertionList(32)}), true},
		{"a Go string list", "http", httpOK(map[string]any{"assertions": []string{"ok=status in 200..299 @warn"}}), true},
		{"body subjects with GET", "http", httpOK(map[string]any{"method": "GET", "assertions": []any{`b=body contains "x"`, `j=json("$.a") exists`, `h=html("h1") count == 1`, "s=size < 10"}}), true},
		{"other subjects with HEAD", "http", httpOK(map[string]any{"method": "HEAD", "assertions": []any{"ok=status == 200", `h=header("ETag") exists`, "c=cert_days > 14", `i=cert_issuer contains "R3"`, "t=ttfb_ms < 500", "r=response_ms < 900", "d=redirects == 0"}}), true},
		{"names apart from json_values", "http", httpOK(map[string]any{"json_values": []any{"status=$.status"}, "assertions": []any{`ok=json("$.status") == "ok"`}}), true},
		{"an empty list", "http", httpOK(map[string]any{"assertions": []any{}}), true},
		{"an entry of 512 bytes", "http", httpOK(map[string]any{"assertions": []any{"ok=status == 200" + strings.Repeat(" ", 512-16)}}), true},

		{"33 assertions", "http", httpOK(map[string]any{"assertions": assertionList(33)}), false},
		{"an entry over 512 bytes", "http", httpOK(map[string]any{"assertions": []any{"ok=status == 200" + strings.Repeat(" ", 513-16)}}), false},
		{"not a list", "http", httpOK(map[string]any{"assertions": "ok=status == 200"}), false},
		{"an empty entry", "http", httpOK(map[string]any{"assertions": []any{""}}), false},
		{"a malformed expression", "http", httpOK(map[string]any{"assertions": []any{"ok=status = 200"}}), false},
		{"a bad regular expression", "http", httpOK(map[string]any{"assertions": []any{`ok=body matches "("`}}), false},
		{"a bad selector", "http", httpOK(map[string]any{"assertions": []any{`ok=html("div[") exists`}}), false},
		{"a bad JSONPath", "http", httpOK(map[string]any{"assertions": []any{`ok=json("$[") exists`}}), false},
		{"duplicate names", "http", httpOK(map[string]any{"assertions": []any{"ok=status == 200", "ok=status < 500"}}), false},
		{"a name json_values has", "http", httpOK(map[string]any{"json_values": []any{"ok=$.ok"}, "assertions": []any{"ok=status == 200"}}), false},
		{"body with HEAD", "http", httpOK(map[string]any{"method": "HEAD", "assertions": []any{`b=body contains "x"`}}), false},
		{"json with HEAD", "http", httpOK(map[string]any{"method": "HEAD", "assertions": []any{`j=json("$.a") exists`}}), false},
		{"html with HEAD", "http", httpOK(map[string]any{"method": "HEAD", "assertions": []any{`h=html("p") exists`}}), false},
		{"html count with HEAD", "http", httpOK(map[string]any{"method": "HEAD", "assertions": []any{`h=html("p") count > 0`}}), false},
		{"size with HEAD", "http", httpOK(map[string]any{"method": "HEAD", "assertions": []any{"s=size > 0"}}), false},
	})
}

// TestValidateHTTPAssertionMessages pins the refusals' wording: a
// malformed entry is named with the parser's reason.
func TestValidateHTTPAssertionMessages(t *testing.T) {
	cases := []struct {
		params map[string]any
		want   string
	}{
		{httpOK(map[string]any{"assertions": []any{"ok=status = 200"}}),
			`http: assertions entries must be name=assertion such as ok=status == 200 (see the docs): "ok=status = 200": "=" is not an operator of status`},
		{httpOK(map[string]any{"assertions": []any{"ok=status == 200", "b=status < 500", "ok=size > 1"}}), `http: assertions names "ok" twice`},
		{httpOK(map[string]any{"json_values": []any{"ok=$.ok"}, "assertions": []any{"ok=status == 200"}}), `http: json_values and assertions both name "ok"`},
		{httpOK(map[string]any{"method": "HEAD", "assertions": []any{"ok=status == 200", "s=size > 0"}}), `http: assertion "s" reads the body and needs method GET: a HEAD response has no body`},
	}
	for _, tc := range cases {
		err := checkplugins.Validate("http", tc.params)
		if !errors.Is(err, checkplugins.ErrInvalidParams) || err.Error() != tc.want {
			t.Errorf("Validate(http, %v) = %v, want %q", tc.params, err, tc.want)
		}
	}
}

// TestAssertionFormat: the server's own rows, beyond the vectors the SPA
// shares (testdata/formats.json): what only RE2, the selector parser and
// the JSONPath parser judge.
func TestAssertionFormat(t *testing.T) {
	cases := []struct {
		in    string
		valid bool
	}{
		{`ok=body matches "a+"`, true},
		{`ok=html("div > p.x") exists`, true},
		{`ok=json("$.a[0].b") == "x"`, true},
		{`ok=body matches "("`, false},
		{`ok=body matches "(?P<x"`, false},
		{`ok=html("div[") exists`, false},
		{`ok=html("p:nope") exists`, false},
		{`ok=json("$[") exists`, false},
		{`ok=json("$[0,0]") exists`, false},
		// Selectors whose match on one node could search a whole page.
		{`ok=html("p:has(a)") exists`, false},
		{`ok=html("main div p") exists`, true},
		{`ok=html("nav ul li a") exists`, true},
		{`ok=html("a b c d e f g h i") exists`, false},
		{`ok=html("p:not(.a)") exists`, true},
		{`ok=html("x/**/div/**/p") exists`, false},
		// Sibling combinators and the pseudo-classes of backlog 3.90.
		{`ok=html("a ~ b ~ c") exists`, true},
		{`ok=html("h2 + p") count == 1`, true},
		{`ok=html("tr:nth-child(2n+1) td:nth-last-of-type(1)") exists`, true},
		{`ok=html("li:not(:last-child, .x):only-of-type, p:empty, :root") exists`, true},
		{`ok=html("p:not(a b)") exists`, false},
		{`ok=html("p:not(:not(a))") exists`, false},
		{`ok=html("p:is(a)") exists`, false},
		{`ok=html("p:nth-child(2n+1 of .a)") exists`, false},
		{`ok=html("a + + b") exists`, false},
	}
	for _, tc := range cases {
		if got := checkplugins.FormatAssertion.Valid(tc.in); got != tc.valid {
			t.Errorf("FormatAssertion.Valid(%q) = %v, want %v", tc.in, got, tc.valid)
		}
	}
}

// TestAssertionNamesFollowTheSecretNameRule: internal/probe/assert keeps
// its own copy of the name rule (it may not import this package); both
// must accept the same names.
func TestAssertionNamesFollowTheSecretNameRule(t *testing.T) {
	names := []string{"a", "ok", "A_b.c-9", strings.Repeat("n", 64), strings.Repeat("n", 65), "", "a b", "ä", "a=b", "a/b", "a:b", "-", "."}
	for _, name := range names {
		if strings.Contains(name, "=") {
			continue // the entry splits at its first '='
		}
		got := checkplugins.FormatAssertion.Valid(name + "=status == 200")
		if want := checkplugins.FormatSecretName.Valid(name); got != want {
			t.Errorf("assertion name %q: valid %v, secret name valid %v", name, got, want)
		}
	}
}

// TestHTTPAsksForAssertionItems: an assertion asks for its name, with or
// without json_values.
func TestHTTPAsksForAssertionItems(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		item   string
		want   bool
	}{
		{"an assertion's name", map[string]any{"assertions": []any{"ok=status == 200"}}, "ok", true},
		{"an assertion's name next to json_values", map[string]any{"json_values": []any{"a=$.a"}, "assertions": []any{"ok=status == 200"}}, "ok", true},
		{"a json_values name next to assertions", map[string]any{"json_values": []any{"a=$.a"}, "assertions": []any{"ok=status == 200"}}, "a", true},
		{"a name on neither list", map[string]any{"json_values": []any{"a=$.a"}, "assertions": []any{"ok=status == 200"}}, "b", false},
		{"not the expression", map[string]any{"assertions": []any{"ok=status == 200"}}, "status == 200", false},
		{"no assertions", map[string]any{"url": "https://{address}/"}, "ok", false},
		{"an empty list", map[string]any{"assertions": []any{}}, "ok", false},
		{"a Go string list", map[string]any{"assertions": []string{"ok=status == 200"}}, "ok", true},
		{"the whole-host item", map[string]any{"assertions": []any{"ok=status == 200"}}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkplugins.AsksForItem("http", tc.params, tc.item); got != tc.want {
				t.Fatalf("AsksForItem(http, %v, %q) = %v, want %v", tc.params, tc.item, got, tc.want)
			}
		})
	}
}
