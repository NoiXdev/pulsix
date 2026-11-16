package http_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/probe"
	httpprobe "github.com/noixdev/pulsix/internal/probe/http"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// wantVerdict fails unless item's http.assertion is verdict and its
// http.assertion_actual is actual ("" skips the actual).
func wantVerdict(t *testing.T, m map[mi]probe.Observation, item, verdict, actual string) {
	t.Helper()
	o, ok := m[mi{"http.assertion", item}]
	if !ok || o.Label == nil || *o.Label != verdict {
		t.Fatalf("http.assertion %q = %+v, want %q", item, o, verdict)
	}
	a, ok := m[mi{"http.assertion_actual", item}]
	if !ok || a.Label == nil {
		t.Fatalf("http.assertion_actual %q = %+v, want a label", item, a)
	}
	if actual != "" && *a.Label != actual {
		t.Fatalf("http.assertion_actual %q = %q, want %q", item, *a.Label, actual)
	}
}

// wantAssertionError fails unless both of item's metrics carry msg.
func wantAssertionError(t *testing.T, m map[mi]probe.Observation, item, msg string) {
	t.Helper()
	for _, metric := range []string{"http.assertion", "http.assertion_actual"} {
		if o := m[mi{metric, item}]; o.Err != msg {
			t.Fatalf("%s %q = %+v, want error %q", metric, item, o, msg)
		}
	}
}

// assertionItems counts the items reporting http.assertion.
func assertionItems(m map[mi]probe.Observation) int {
	n := 0
	for k := range m {
		if k.metric == "http.assertion" {
			n++
		}
	}
	return n
}

const page = `<!doctype html><html><head><title>
  Shop   Home </title></head><body><h1>Welcome</h1>
<p class="x">first <b>bold</b></p><p class="x">second</p></body></html>`

func pageServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Add("X-Multi", "a")
		w.Header().Add("X-Multi", "b")
		w.Header().Set("Set-Cookie", "session=s3cr3t; HttpOnly")
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestHTTPAssertionsOnAPage: every subject a plain http page offers, each
// op passing or failing with its severity, and the actual seen.
func TestHTTPAssertionsOnAPage(t *testing.T) {
	srv := pageServer(t)
	params := httpParams(srv.URL, map[string]any{"assertions": values(
		"st=status == 200",
		"st_warn=status in 500..599 @warn",
		"st_crit=status != 200",
		"fast=response_ms < 60000",
		"ttfb=ttfb_ms >= 0",
		"sz=size == "+strconv.Itoa(len(page)),
		"rd=redirects == 0",
		`ct=header("content-type") contains "html"`,
		`multi=header("X-Multi") == "a, b"`,
		`pw=header("X-Powered-By") exists`,
		`npw=header("X-Powered-By") !exists`,
		`ne=header("X-Powered-By") != "PHP"`,
		`ck=header("Set-Cookie") contains "session"`,
		`ck_warn=header("set-cookie") == "x" @warn`,
		`b=body contains "Welcome"`,
		`b_not=body !contains "Exception"`,
		`b_re=body matches "<h1>[A-Z][a-z]+</h1>"`,
		`title=html("title") == "Shop Home"`,
		`first=html("p.x") == "first bold"`,
		`count=html("p.x") count == 2`,
		`none=html("table") !exists`,
		`none_eq=html("table") == "x"`,
		`none_count=html("table") count == 0`,
	)})
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), params, ""))
	if o := m[mi{"http.up", ""}]; o.Value == nil || *o.Value != 1 {
		t.Fatalf("http.up = %+v", o)
	}
	wantVerdict(t, m, "st", "pass", "200")
	wantVerdict(t, m, "st_warn", "warn", "200")
	wantVerdict(t, m, "st_crit", "crit", "200")
	wantVerdict(t, m, "fast", "pass", "")
	wantVerdict(t, m, "ttfb", "pass", "")
	wantVerdict(t, m, "sz", "pass", strconv.Itoa(len(page)))
	wantVerdict(t, m, "rd", "pass", "0")
	wantVerdict(t, m, "ct", "pass", `"text/html; charset=utf-8"`)
	wantVerdict(t, m, "multi", "pass", `"a, b"`)
	wantVerdict(t, m, "pw", "crit", "absent")
	wantVerdict(t, m, "npw", "pass", "absent")
	wantVerdict(t, m, "ne", "pass", "absent")
	wantVerdict(t, m, "ck", "pass", "(hidden)")
	wantVerdict(t, m, "ck_warn", "warn", "(hidden)")
	wantVerdict(t, m, "b", "pass", "")
	wantVerdict(t, m, "b_not", "pass", "")
	wantVerdict(t, m, "b_re", "pass", "")
	wantVerdict(t, m, "title", "pass", `"Shop Home"`)
	wantVerdict(t, m, "first", "pass", `"first bold"`)
	wantVerdict(t, m, "count", "pass", "2")
	wantVerdict(t, m, "none", "pass", "absent")
	wantVerdict(t, m, "none_eq", "crit", "absent")
	wantVerdict(t, m, "none_count", "pass", "0")
	if n := assertionItems(m); n != 23 {
		t.Fatalf("%d assertion items, want 23", n)
	}
	// A millisecond reading is printed whole; the body's start, quoted.
	if a := *m[mi{"http.assertion_actual", "fast"}].Label; strings.ContainsAny(a, ".e") {
		t.Fatalf("response_ms actual = %q, want whole milliseconds", a)
	}
	if a := *m[mi{"http.assertion_actual", "b"}].Label; !strings.HasPrefix(a, `"<!doctype html><html><head><title>\n`) {
		t.Fatalf("body actual = %q, want the body's start quoted", a)
	}
	// The cookie's value is never stored, anywhere.
	for k, o := range m {
		if o.Label != nil && strings.Contains(*o.Label, "s3cr3t") {
			t.Fatalf("%v carries the cookie: %q", k, *o.Label)
		}
	}
}

// TestHTTPAssertionCertificateOverHTTP: the certificate subjects cannot be
// evaluated without TLS — an error on both metrics, not a failure.
func TestHTTPAssertionCertificateOverHTTP(t *testing.T) {
	srv := pageServer(t)
	params := httpParams(srv.URL, map[string]any{"assertions": values("cd=cert_days > 14", `ci=cert_issuer == "R3"`, "st=status == 200")})
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), params, ""))
	wantAssertionError(t, m, "cd", "the response did not arrive over https")
	wantAssertionError(t, m, "ci", "the response did not arrive over https")
	wantVerdict(t, m, "st", "pass", "200")
}

// TestHTTPAssertionCertificate: over https, cert_days and cert_issuer are
// the final hop's certificate.
func TestHTTPAssertionCertificate(t *testing.T) {
	ca := probetest.NewCA(t)
	now := time.Now()
	srv, _ := probetest.TLSServer(t, ca.Leaf(t, now.Add(-time.Hour), now.Add(36*time.Hour)))
	params := httpParams(srv.URL, map[string]any{"verify_tls": false, "assertions": values(
		"cd=cert_days in 1..2", "cd_warn=cert_days > 14 @warn", `ci=cert_issuer == "probe test CA"`)})
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), params, ""))
	wantVerdict(t, m, "cd", "pass", "1.5")
	wantVerdict(t, m, "cd_warn", "warn", "1.5")
	wantVerdict(t, m, "ci", "pass", `"probe test CA"`)
}

const jsonDoc = `{"status": "ok", "code": 200, "ratio": 0.25, "big": 1e400, "up": true, "gone": null,
	"obj": {"a": 1}, "list": [1, 2], "items": [{"id": 1}, {"id": 2}], "label": "a\u0001b"}`

// TestHTTPAssertionsOnJSON: a string compares by its value, a number by
// its text, a boolean as true/false, null as null; an object or array
// fails everything but exists; a path matching nothing is absent, one
// matching several an error.
func TestHTTPAssertionsOnJSON(t *testing.T) {
	srv := jsonServer(t, 200, jsonDoc)
	params := httpParams(srv.URL, map[string]any{"json_values": values("c=$.code"), "assertions": values(
		`st=json("$.status") == "ok"`,
		`code=json("$.code") == "200"`,
		`ratio=json("$.ratio") matches "^0\\.25$"`,
		`up=json("$.up") == "true"`,
		`gone=json("$.gone") == "null"`,
		`obj=json("$.obj") != "x"`,
		`obj_ex=json("$.obj") exists`,
		`list=json("$.list") contains "1"`,
		`miss=json("$.missing") exists @warn`,
		`miss_not=json("$.missing") !exists`,
		`many=json("$.items[*].id") == "1"`,
		`many_ex=json("$.items[*].id") exists`,
		`big=json("$.big") == "x"`,
		`label=json("$.label") contains "b"`,
	)})
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), params, ""))
	wantNumber(t, m, "c", 200)
	wantVerdict(t, m, "st", "pass", `"ok"`)
	wantVerdict(t, m, "code", "pass", "200")
	wantVerdict(t, m, "ratio", "pass", "0.25")
	wantVerdict(t, m, "up", "pass", "true")
	wantVerdict(t, m, "gone", "pass", "null")
	wantVerdict(t, m, "obj", "crit", "object")
	wantVerdict(t, m, "obj_ex", "pass", "object")
	wantVerdict(t, m, "list", "crit", "array")
	wantVerdict(t, m, "miss", "warn", "absent")
	wantVerdict(t, m, "miss_not", "pass", "absent")
	wantAssertionError(t, m, "many", "the path matched 2 values")
	wantVerdict(t, m, "many_ex", "pass", "")
	wantAssertionError(t, m, "big", "the number is out of range")
	wantVerdict(t, m, "label", "pass", `"a\u0001b"`)
}

// TestHTTPAssertionsOnABodyThatIsNotJSON: JSON that does not parse
// is an error on each json assertion; HTML parses whatever arrives.
func TestHTTPAssertionsOnABodyThatIsNotJSON(t *testing.T) {
	srv := jsonServer(t, 200, `{"a": `)
	params := httpParams(srv.URL, map[string]any{"assertions": values(`a=json("$.a") exists`, `h=html("p") count == 0`)})
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), params, ""))
	wantAssertionError(t, m, "a", "the response is not JSON")
	wantVerdict(t, m, "h", "pass", "0")
}

// TestHTTPAssertionsOnHTMLTooDeep: a page nested deeper than the HTML
// parser takes (512 elements) does not parse.
func TestHTTPAssertionsOnHTMLTooDeep(t *testing.T) {
	srv := jsonServer(t, 200, strings.Repeat("<div>", 600))
	params := httpParams(srv.URL, map[string]any{"assertions": values(`h=html("div") count > 0`, "st=status == 200")})
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), params, ""))
	wantAssertionError(t, m, "h", "the response is not HTML")
	wantVerdict(t, m, "st", "pass", "200")
}

// TestHTTPAssertionsOnALargeBody: past 1 MiB, a negative op on the body
// and every json or html subject cannot be evaluated; contains and
// matches still pass when found in the first MiB.
func TestHTTPAssertionsOnALargeBody(t *testing.T) {
	body := "<p>start</p>" + strings.Repeat("x", 1<<20) + "end"
	srv := jsonServer(t, 200, body)
	params := httpParams(srv.URL, map[string]any{"assertions": values(
		`found=body contains "start"`,
		`found_re=body matches "s.art"`,
		`beyond=body contains "end"`,
		`not=body !contains "nope"`,
		`not_re=body !matches "nope"`,
		`eq=body == "x"`,
		`ne=body != "x"`,
		`html=html("p") exists`,
		`html_not=html("table") !exists`,
		`html_count=html("p") count == 1`,
		`json=json("$.a") exists`,
		"size=size == "+strconv.Itoa(len(body)),
	)})
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), params, ""))
	const over = "the response is larger than 1 MiB"
	wantVerdict(t, m, "found", "pass", "")
	wantVerdict(t, m, "found_re", "pass", "")
	wantVerdict(t, m, "beyond", "crit", "")
	wantAssertionError(t, m, "not", over)
	wantAssertionError(t, m, "not_re", over)
	wantVerdict(t, m, "eq", "crit", "")
	wantVerdict(t, m, "ne", "pass", "")
	wantAssertionError(t, m, "html", over)
	wantAssertionError(t, m, "html_not", over)
	wantAssertionError(t, m, "html_count", over)
	wantAssertionError(t, m, "json", over)
	wantVerdict(t, m, "size", "pass", strconv.Itoa(len(body)))
}

// TestHTTPAssertionsOnACutBody: a body whose read broke off is treated
// like a truncated one, and == and != cannot be judged either: the rest
// of the body is unknown.
func TestHTTPAssertionsOnACutBody(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<p>partial</p>"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	params := httpParams(srv.URL, map[string]any{"timeout_s": 0.5, "assertions": values(
		`found=body contains "partial"`,
		`not=body !contains "nope"`,
		`eq=body == "x"`,
		`ne=body != "x"`,
		`html=html("p") exists`,
		"st=status == 200",
	)})
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), params, ""))
	const cut = "the response was cut short"
	wantVerdict(t, m, "found", "pass", "")
	wantAssertionError(t, m, "not", cut)
	wantAssertionError(t, m, "eq", cut)
	wantAssertionError(t, m, "ne", cut)
	wantAssertionError(t, m, "html", cut)
	wantVerdict(t, m, "st", "pass", "200")
}

// TestHTTPAssertionsWhenDown: a failed attempt reports up 0 and its
// reason only; the assertion items go stale rather than erroring the
// host into unknown.
func TestHTTPAssertionsWhenDown(t *testing.T) {
	params := httpParams(fmt.Sprintf("http://127.0.0.1:%d/", probetest.ClosedPort(t)), map[string]any{
		"assertions": values("st=status == 200", `b=body contains "x"`)})
	probetest.WantDown(t, run(context.Background(), probetest.LoopbackGuard(), params, ""), "http", probe.FailureRefused)
}

// TestHTTPAssertionsWhenNotAttempted: an attempt that could not be made,
// in the probe or the runner's FailAll, errors both metrics of every
// assertion item.
func TestHTTPAssertionsWhenNotAttempted(t *testing.T) {
	params := httpParams("http://{address}/", map[string]any{"assertions": values("st=status == 200", "bad=status = 1")})
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), params, ""))
	wantAssertionError(t, m, "st", "host has no address")
	wantAssertionError(t, m, "bad", "host has no address")
	if len(m) != len(failAllHostMetrics)+4 {
		t.Fatalf("want the host metrics and two assertion items, got %+v", m)
	}

	m = byItem(t, httpprobe.FailAll(map[string]any{"url": "http://x/", "json_values": values("a=$.a"), "assertions": values("st=status == 200", `b=body contains "x" @warn`)}, "boom"))
	want := []mi{{"http.json.value", "a"}, {"http.assertion", "st"}, {"http.assertion_actual", "st"}, {"http.assertion", "b"}, {"http.assertion_actual", "b"}}
	for _, metric := range failAllNamedMetrics {
		want = append(want, mi{metric, ""})
	}
	if len(m) != len(want) {
		t.Fatalf("FailAll = %+v, want %v", m, want)
	}
	for _, k := range want {
		if m[k].Err != "boom" {
			t.Fatalf("%v = %+v, want error boom", k, m[k])
		}
	}
}

// TestHTTPAssertionEntriesBypassingValidation: an entry that does not
// parse is an error on its item; one without a valid name, or repeating
// one, has no item and is left out; past 32 entries the rest are.
func TestHTTPAssertionEntriesBypassingValidation(t *testing.T) {
	srv := pageServer(t)
	list := []any{"bad=status = 200", "a b=status == 200", "dup=status == 200", "dup=status == 500", 7.0, "noequals"}
	for i := range 40 {
		list = append(list, fmt.Sprintf("n%d=status == 200", i))
	}
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{"assertions": list}), ""))
	wantAssertionError(t, m, "bad", "the assertion is not valid")
	wantVerdict(t, m, "dup", "pass", "200")
	if _, ok := m[mi{"http.assertion", "a b"}]; ok {
		t.Fatal("an entry without a valid name reported")
	}
	if n := assertionItems(m); n != 28 {
		t.Fatalf("%d assertion items, want the 2 valid of the first 32 entries' first 6 and 26 more", n)
	}
}

// TestHTTPAssertionActualIsCleanAndShort: what a target sent is quoted,
// cleaned and cut to 128 bytes on a rune boundary.
func TestHTTPAssertionActualIsCleanAndShort(t *testing.T) {
	long := strings.Repeat("ü", 200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Long", long)
		_, _ = w.Write([]byte("<title> " + long + "</title>\x00\x1b[31m"))
	}))
	defer srv.Close()
	params := httpParams(srv.URL, map[string]any{"assertions": values(
		`h=header("X-Long") == "x"`, `t=html("title") == "x"`, `b=body == "x"`)})
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), params, ""))
	for _, item := range []string{"h", "t", "b"} {
		o := m[mi{"http.assertion_actual", item}]
		if o.Label == nil {
			t.Fatalf("actual %q = %+v", item, o)
		}
		a := *o.Label
		if len(a) > probe.ShortLabelLen || !utf8.ValidString(a) || strings.ContainsFunc(a, unicode.IsControl) ||
			strings.ContainsRune(a, ' ') || !strings.HasPrefix(a, `"`) {
			t.Fatalf("actual %q = %q (%d bytes), want a quoted, clean label of at most %d bytes", item, a, len(a), probe.ShortLabelLen)
		}
	}
}

// TestHTTPNoAssertionsNoItems: without assertions no assertion item is
// reported, and the body is not kept for nothing.
func TestHTTPNoAssertionsNoItems(t *testing.T) {
	srv := pageServer(t)
	m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{"assertions": values()}), ""))
	if n := assertionItems(m); n != 0 || len(m) != upHostMetrics {
		t.Fatalf("got %+v, want only the host metrics", m)
	}
}
