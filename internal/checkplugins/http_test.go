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
	want := []string{"url", "method", "secret", "body_contains", "json_values", "timeout_s", "follow_redirects", "verify_tls"}
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
	metrics := []checkplugins.MetricSpec{
		{Name: "http.up", Kind: checkplugins.Numeric},
		{Name: "http.status_code", Kind: checkplugins.Numeric},
		{Name: "http.response_ms", Kind: checkplugins.Numeric},
		{Name: "http.body_match", Kind: checkplugins.Numeric},
		{Name: "http.json.value", Kind: checkplugins.Numeric},
		{Name: "http.json.text", Kind: checkplugins.Discrete},
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
