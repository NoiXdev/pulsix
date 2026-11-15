package http_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	httpprobe "github.com/noixdev/pulsix/internal/probe/http"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// mi keys an observation by metric and item.
type mi struct{ metric, item string }

// byItem indexes observations by metric and item and fails on a duplicate
// or on an observation that carries not exactly one of value, label and
// error.
func byItem(t *testing.T, obs []probe.Observation) map[mi]probe.Observation {
	t.Helper()
	m := make(map[mi]probe.Observation, len(obs))
	for _, o := range obs {
		k := mi{o.Metric, o.Item}
		if _, dup := m[k]; dup {
			t.Fatalf("%v reported twice: %+v", k, obs)
		}
		n := 0
		if o.Value != nil {
			n++
		}
		if o.Label != nil {
			n++
		}
		if o.Err != "" {
			n++
		}
		if n != 1 {
			t.Fatalf("%v must carry exactly one of value, label and error: %+v", k, o)
		}
		m[k] = o
	}
	return m
}

func wantNumber(t *testing.T, m map[mi]probe.Observation, item string, want float64) {
	t.Helper()
	o, ok := m[mi{"http.json.value", item}]
	if !ok || o.Value == nil || *o.Value != want {
		t.Fatalf("http.json.value %q = %+v, want %v", item, o, want)
	}
	if _, ok := m[mi{"http.json.text", item}]; ok {
		t.Fatalf("item %q reported as text too", item)
	}
}

func wantText(t *testing.T, m map[mi]probe.Observation, item, want string) {
	t.Helper()
	o, ok := m[mi{"http.json.text", item}]
	if !ok || o.Label == nil || *o.Label != want {
		t.Fatalf("http.json.text %q = %+v, want %q", item, o, want)
	}
	if _, ok := m[mi{"http.json.value", item}]; ok {
		t.Fatalf("item %q reported as a number too", item)
	}
}

func wantItemError(t *testing.T, m map[mi]probe.Observation, item, want string) {
	t.Helper()
	o, ok := m[mi{"http.json.value", item}]
	if !ok || o.Err != want {
		t.Fatalf("http.json.value %q = %+v, want error %q", item, o, want)
	}
	if _, ok := m[mi{"http.json.text", item}]; ok {
		t.Fatalf("item %q reported as text too", item)
	}
}

// jsonServer serves body with status on every request.
func jsonServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func values(entries ...string) []any {
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = e
	}
	return out
}

const document = `{
	"status": "ok",
	"healthy": true,
	"degraded": false,
	"queue": {"depth": 42, "ratio": -0.25},
	"big": 12345678901234567890,
	"huge": 1e400,
	"tiny": 1e-400,
	"label": "  line\none\ttab\u0000  ",
	"empty": "",
	"obj": {"a": 1},
	"list": [1, 2, 3],
	"nothing": null,
	"items": [{"id": 1, "v": 10}, {"id": 2, "v": 20}]
}`

// TestHTTPJSONScalars: a number is http.json.value, a string or a boolean
// http.json.text, each on its entry's name; the whole-host metrics stand.
func TestHTTPJSONScalars(t *testing.T) {
	srv := jsonServer(t, http.StatusOK, document)
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
		"json_values": values(
			"status=$.status",
			"healthy=$.healthy",
			"degraded=$.degraded",
			"depth=$.queue.depth",
			"ratio=$['queue']['ratio']",
			"big=$.big",
			"tiny=$.tiny",
			"label=$.label",
			"empty=$.empty",
			"second=$.items[?@.id==2].v",
		),
	}), "")
	m := byItem(t, obs)
	for _, metric := range []string{"http.up", "http.status_code", "http.response_ms"} {
		if o, ok := m[mi{metric, ""}]; !ok || o.Value == nil {
			t.Fatalf("%s = %+v, want a value", metric, o)
		}
	}
	wantText(t, m, "status", "ok")
	wantText(t, m, "healthy", "true")
	wantText(t, m, "degraded", "false")
	wantNumber(t, m, "depth", 42)
	wantNumber(t, m, "ratio", -0.25)
	wantNumber(t, m, "big", 12345678901234567890)
	wantNumber(t, m, "tiny", 0)
	wantText(t, m, "label", "lineone tab")
	wantText(t, m, "empty", probe.EmptyText)
	wantNumber(t, m, "second", 20)
	if len(obs) != 3+10 {
		t.Fatalf("want 3 host metrics and 10 items, got %+v", obs)
	}
}

// TestHTTPJSONItemErrors: a path that does not select exactly one scalar
// is an error on its own item only.
func TestHTTPJSONItemErrors(t *testing.T) {
	srv := jsonServer(t, http.StatusOK, document)
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
		"json_values": values(
			"missing=$.nope",
			"many=$.list[*]",
			"obj=$.obj",
			"list=$.list",
			"null=$.nothing",
			"huge=$.huge",
			"fine=$.queue.depth",
		),
	}), "")
	m := byItem(t, obs)
	wantItemError(t, m, "missing", "the path matched nothing")
	wantItemError(t, m, "many", "the path matched 3 values")
	wantItemError(t, m, "obj", "the path matched an object")
	wantItemError(t, m, "list", "the path matched a list")
	wantItemError(t, m, "null", "the path matched null")
	wantItemError(t, m, "huge", "the number is out of range")
	wantNumber(t, m, "fine", 42)
	if o := m[mi{"http.up", ""}]; o.Value == nil || *o.Value != 1 {
		t.Fatalf("http.up = %+v, want 1", o)
	}
}

// TestHTTPJSONWholeResponseErrors: a body that is not JSON, or longer than
// the cap, is an error on every JSON item while the whole-host metrics
// stand.
func TestHTTPJSONWholeResponseErrors(t *testing.T) {
	const mib = 1 << 20
	exact := `{"a":"` + strings.Repeat("x", mib-8) + `"}`
	if len(exact) != mib {
		t.Fatalf("fixture is %d bytes, want %d", len(exact), mib)
	}
	for _, tc := range []struct {
		name, body, want string
	}{
		{"html", "<html>ok</html>", "the response is not JSON"},
		{"empty", "", "the response is not JSON"},
		{"trailing", `{"a":1} {"a":2}`, "the response is not JSON"},
		{"truncated", `{"a":`, "the response is not JSON"},
		{"over the cap", `{"a":"` + strings.Repeat("x", mib) + `"}`, "the response is larger than 1 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := jsonServer(t, http.StatusOK, tc.body)
			obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
				"json_values": values("a=$.a", "b=$.b"),
			}), "")
			m := byItem(t, obs)
			wantItemError(t, m, "a", tc.want)
			wantItemError(t, m, "b", tc.want)
			if o := m[mi{"http.status_code", ""}]; o.Value == nil || *o.Value != 200 {
				t.Fatalf("http.status_code = %+v, want 200", o)
			}
			if len(obs) != 5 {
				t.Fatalf("want 3 host metrics and 2 items, got %+v", obs)
			}
		})
	}
	t.Run("exactly the cap", func(t *testing.T) {
		srv := jsonServer(t, http.StatusOK, exact)
		obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
			"json_values": values("b=$.b"),
		}), "")
		wantItemError(t, byItem(t, obs), "b", "the path matched nothing")
	})
}

// TestHTTPJSONAndBodyContainsTogether: both read the same bytes.
func TestHTTPJSONAndBodyContainsTogether(t *testing.T) {
	srv := jsonServer(t, http.StatusOK, document)
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
		"body_contains": `"status": "ok"`,
		"json_values":   values("depth=$.queue.depth"),
	}), "")
	m := byItem(t, obs)
	if o := m[mi{"http.body_match", ""}]; o.Value == nil || *o.Value != 1 {
		t.Fatalf("http.body_match = %+v, want 1", o)
	}
	wantNumber(t, m, "depth", 42)
}

// TestHTTPJSONFromAnErrorStatus: the values are read whatever the status.
func TestHTTPJSONFromAnErrorStatus(t *testing.T) {
	srv := jsonServer(t, http.StatusServiceUnavailable, `{"error":{"code":503,"reason":"maintenance"}}`)
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
		"json_values": values("code=$.error.code", "reason=$.error.reason"),
	}), "")
	m := byItem(t, obs)
	if o := m[mi{"http.status_code", ""}]; o.Value == nil || *o.Value != 503 {
		t.Fatalf("http.status_code = %+v, want 503", o)
	}
	wantNumber(t, m, "code", 503)
	wantText(t, m, "reason", "maintenance")
}

// TestHTTPJSONTextIsBounded: a long string is cut to the label bound on a
// rune boundary.
func TestHTTPJSONTextIsBounded(t *testing.T) {
	srv := jsonServer(t, http.StatusOK, `{"s":"`+strings.Repeat("ä", 400)+`"}`)
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
		"json_values": values("s=$.s"),
	}), "")
	o := byItem(t, obs)[mi{"http.json.text", "s"}]
	if o.Label == nil || len(*o.Label) > probe.MaxLabelLen || len(*o.Label) < probe.MaxLabelLen-1 {
		t.Fatalf("label = %+v, want it cut to %d bytes", o, probe.MaxLabelLen)
	}
}

// TestHTTPJSONEntriesAreBounded: params arrive validated, but the probe
// reads at most 32 entries and refuses a path over 512 bytes on its item.
func TestHTTPJSONEntriesAreBounded(t *testing.T) {
	srv := jsonServer(t, http.StatusOK, `{"a":1}`)
	entries := make([]string, 0, 40)
	for i := range 40 {
		entries = append(entries, fmt.Sprintf("v%d=$.a", i))
	}
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
		"json_values": values(entries...),
	}), "")
	if len(obs) != 3+32 {
		t.Fatalf("want 3 host metrics and 32 items, got %d observations", len(obs))
	}

	long := "$." + strings.Repeat("a", 511)
	obs = run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
		"json_values": values("long="+long, "bad=$[", "nameless", "=$.a", "ok=$.a"),
	}), "")
	m := byItem(t, obs)
	wantItemError(t, m, "long", "the path is not allowed")
	wantItemError(t, m, "bad", "the path is not allowed")
	wantNumber(t, m, "ok", 1)
	if len(obs) != 3+3 {
		t.Fatalf("an entry without a name has no item to report on; got %+v", obs)
	}
}

// TestHTTPJSONItemsShareTheRequestErrors: an attempt that could not be
// made is an error on every JSON item too; a failed one is http.up = 0
// alone.
func TestHTTPJSONItemsShareTheRequestErrors(t *testing.T) {
	params := httpParams("http://{address}/", map[string]any{"json_values": values("a=$.a")})
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), params, ""))
	wantItemError(t, m, "a", "host has no address")
	if len(m) != 4 {
		t.Fatalf("want the 3 host metrics and the item, got %+v", m)
	}

	params = httpParams(fmt.Sprintf("http://127.0.0.1:%d/", probetest.ClosedPort(t)), map[string]any{"json_values": values("a=$.a")})
	probetest.WantOnly(t, run(context.Background(), probetest.LoopbackGuard(), params, ""), "http.up", 0)
}

// TestHTTPFailAll is what the runner reports for a check it cannot
// attempt: every metric the params would have produced, with the JSON
// items by name, and nothing else.
func TestHTTPFailAll(t *testing.T) {
	m := byItem(t, httpprobe.FailAll(map[string]any{
		"url": "http://x/", "body_contains": "ok", "json_values": values("a=$.a", "b=$.b"),
	}, "boom"))
	want := []mi{
		{"http.up", ""}, {"http.status_code", ""}, {"http.response_ms", ""}, {"http.body_match", ""},
		{"http.json.value", "a"}, {"http.json.value", "b"},
	}
	if len(m) != len(want) {
		t.Fatalf("FailAll = %+v, want %v", m, want)
	}
	for _, k := range want {
		if m[k].Err != "boom" {
			t.Fatalf("%v = %+v, want error boom", k, m[k])
		}
	}

	m = byItem(t, httpprobe.FailAll(map[string]any{"url": "http://x/"}, "boom"))
	if len(m) != 3 {
		t.Fatalf("FailAll without body_contains or json_values = %+v, want the 3 host metrics", m)
	}
}

// nested is a JSON document of depth levels of arrays around a number.
func nested(depth int) string {
	return strings.Repeat("[", depth) + "1" + strings.Repeat("]", depth)
}

// TestHTTPJSONDepthIsBounded: a document nested deeper than 64 is an error
// on every JSON item; 64 levels are read.
func TestHTTPJSONDepthIsBounded(t *testing.T) {
	for _, tc := range []struct {
		depth int
		want  string
	}{{64, "the path matched 1 values"}, {65, "the response is nested too deeply"}} {
		t.Run(fmt.Sprint(tc.depth), func(t *testing.T) {
			srv := jsonServer(t, http.StatusOK, nested(tc.depth))
			obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
				"json_values": values("leaf=$..[?@==1]", "root=$"),
			}), "")
			m := byItem(t, obs)
			if tc.depth == 64 {
				wantNumber(t, m, "leaf", 1)
				wantItemError(t, m, "root", "the path matched a list")
				return
			}
			wantItemError(t, m, "leaf", tc.want)
			wantItemError(t, m, "root", tc.want)
		})
	}
	t.Run("brackets inside strings do not count", func(t *testing.T) {
		srv := jsonServer(t, http.StatusOK, `{"s":"`+strings.Repeat("[{", 100)+`\"[","n":2}`)
		obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
			"json_values": values("n=$.n"),
		}), "")
		wantNumber(t, byItem(t, obs), "n", 2)
	})
}

// TestHTTPJSONPathWorkIsBounded: the paths that multiplied the work in
// the full RFC 9535 language are refused on their items before they run,
// and the costliest paths of the subset stay within the budget over the
// largest, deepest bodies.
func TestHTTPJSONPathWorkIsBounded(t *testing.T) {
	refused := []string{
		"$..*..*..*",
		"$..[?@..x]",
		"$..[?@[?@[?@[?@==7]]]]",
		"$" + strings.Repeat("[0,0]", 26),
		"$..[" + strings.Repeat("*,", 63) + "*]",
		"$[0][?count($[0][*])>0]",
		"$[0][?count($[0][?count($[0][*])>0])>0]",
		"$[?search(@.a, @.b)]",
		"$[?match(@, '[a-z]{1000}')]",
	}
	// Close to 1 MiB, 64 deep: every level wide, or many deep chains.
	var wide strings.Builder
	for range 63 {
		wide.WriteString("[" + strings.Repeat("0,", 200))
	}
	wide.WriteString("[" + strings.Repeat("1,", (1<<20-wide.Len())/2-200) + "1")
	wide.WriteString(strings.Repeat("]", 64))
	chain := strings.Repeat("[", 63) + "1" + strings.Repeat("]", 63)
	chains := "[" + strings.Repeat(chain+",", (1<<20)/(len(chain)+1)-2) + chain + "]"
	deep := "@" + strings.Repeat("[0]", 14)
	terms := make([]string, 8)
	for i := range terms {
		terms[i] = deep + " == 2"
	}
	accepted := map[string]string{
		"all":     "$..*",
		"filter":  "$..[?@ == 7]",
		"deep":    "$..*" + strings.Repeat(".*", 63),
		"terms":   "$..[?" + strings.Join(terms, " || ") + "]",
		"indexes": "$..[0]" + strings.Repeat("[0]", 62),
	}
	for name, body := range map[string]string{"wide": wide.String(), "chains": chains} {
		t.Run(name, func(t *testing.T) {
			if len(body) > 1<<20 {
				t.Fatalf("body is %d bytes, over the cap", len(body))
			}
			srv := jsonServer(t, http.StatusOK, body)
			entries := make([]string, 0, len(refused)+len(accepted))
			for i, p := range refused {
				entries = append(entries, fmt.Sprintf("r%d=%s", i, p))
			}
			for name, p := range accepted {
				entries = append(entries, name+"="+p)
			}
			start := time.Now()
			obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
				"json_values": values(entries...),
			}), "")
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("probe took %v: the path work is not bounded", d)
			}
			m := byItem(t, obs)
			for i := range refused {
				wantItemError(t, m, fmt.Sprintf("r%d", i), "the path is not allowed")
			}
			for name := range accepted {
				o := m[mi{"http.json.value", name}]
				if (o.Err == "" && o.Value == nil) || o.Err == "the path is not allowed" {
					t.Fatalf("%s = %+v, want it evaluated", name, o)
				}
				t.Logf("%s: %s", name, o.Err)
			}
		})
	}
}

// TestHTTPJSONCutShort: a body that breaks off is not called invalid JSON.
func TestHTTPJSONCutShort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte(`{"a":1,`))
		// Returning short of Content-Length closes the connection.
	}))
	defer srv.Close()
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{
		"json_values": values("a=$.a"),
	}), "")
	m := byItem(t, obs)
	wantItemError(t, m, "a", "the response was cut short")
	if o := m[mi{"http.up", ""}]; o.Value == nil || *o.Value != 1 {
		t.Fatalf("http.up = %+v, want 1", o)
	}
}
