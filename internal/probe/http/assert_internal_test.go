package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/cascadia"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/assert"
	"golang.org/x/net/html"
)

// TestHTTPAssertionsWaitForTheGate: html and json assertions parse in a
// slot of the JSON gate; when none frees before the deadline they are
// errTooBusy, while the other assertions are judged.
func TestHTTPAssertionsWaitForTheGate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"a":1}`))
	}))
	t.Cleanup(srv.Close)
	gate := NewJSONGate(1)
	leave, ok := gate.enter(context.Background())
	if !ok {
		t.Fatal("an empty gate refused a slot")
	}
	defer leave()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]any{"url": srv.URL, "assertions": []any{`j=json("$.a") exists`, `h=html("p") count == 0`, "st=status == 200"}}
	obs := probeHTTP(ctx, loopbackOnly(), gate, params, "", nil, nil)
	if ctx.Err() == nil {
		t.Fatal("the probe returned before its deadline: the assertions did not wait for the gate")
	}
	got := map[string]probe.Observation{}
	for _, o := range obs {
		got[o.Metric+"/"+o.Item] = o
	}
	for _, k := range []string{"http.assertion/j", "http.assertion_actual/j", "http.assertion/h", "http.assertion_actual/h"} {
		if got[k].Err != errAssertTooBusy {
			t.Fatalf("%s = %+v, want %q", k, got[k], errAssertTooBusy)
		}
	}
	if o := got["http.assertion/st"]; o.Label == nil || *o.Label != "pass" {
		t.Fatalf("status assertion = %+v, want pass", o)
	}
}

// TestKeepBodyForAssertions: only an assertion that reads the kept body
// keeps it; size counts without keeping.
func TestKeepBodyForAssertions(t *testing.T) {
	cases := []struct {
		list []any
		want bool
	}{
		{[]any{"st=status == 200", `h=header("X") exists`, "s=size > 1"}, false},
		{[]any{`b=body contains "x"`}, true},
		{[]any{`j=json("$.a") exists`}, true},
		{[]any{`h=html("p") exists`}, true},
		{[]any{`h=html("p") count > 1`}, true},
		{[]any{"bad=body"}, false},
	}
	for _, tc := range cases {
		if got := wantsOf(map[string]any{"assertions": tc.list}).keepBody(); got != tc.want {
			t.Errorf("keepBody(%v) = %v, want %v", tc.list, got, tc.want)
		}
	}
}

// TestNumberActual: a number as printed — milliseconds whole, days to a
// tenth, the rest as they are.
func TestNumberActual(t *testing.T) {
	cases := []struct {
		subject assert.Subject
		v       float64
		want    string
	}{
		{assert.SubjectStatus, 503, "503"},
		{assert.SubjectResponseMS, 1834.4999, "1834"},
		{assert.SubjectTTFBMS, 0.6, "1"},
		{assert.SubjectSize, 1048576, "1048576"},
		{assert.SubjectRedirects, 0, "0"},
		{assert.SubjectCertDays, 12.345, "12.3"},
		{assert.SubjectCertDays, -0.04, "0"},
		{assert.SubjectCertDays, -1.26, "-1.3"},
		{assert.SubjectHTMLCount, 3, "3"},
	}
	for _, tc := range cases {
		if got := numberActual(tc.subject, tc.v); got != tc.want {
			t.Errorf("numberActual(%s, %v) = %q, want %q", tc.subject, tc.v, got, tc.want)
		}
	}
}

// TestTextActual: a text quoted as a JSON string, cleaned and cut to
// probe.ShortLabelLen; a long text is cut before it is quoted.
func TestTextActual(t *testing.T) {
	if got := textActual("ok"); got != `"ok"` {
		t.Fatalf("textActual(ok) = %q", got)
	}
	if got := textActual("a\x00<b>"); got != `"a\u0000<b>"` {
		t.Fatalf("textActual = %q, want control characters escaped and no HTML escaping", got)
	}
	long := make([]byte, 1<<20)
	for i := range long {
		long[i] = 'x'
	}
	if got := textActual(string(long)); len(got) != probe.ShortLabelLen {
		t.Fatalf("textActual(1 MiB) = %d bytes, want %d", len(got), probe.ShortLabelLen)
	}
}

// htmlOf parses s.
func htmlOf(t *testing.T, s string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestSelectAllIsDocumentOrder: the walk finds what cascadia finds, in
// the same order, charging each node visited.
func TestSelectAllIsDocumentOrder(t *testing.T) {
	doc := htmlOf(t, `<div><p id=a>1<p id=b>2</div><section><p id=c><span><p id=d></span></section>`)
	a, err := assert.Parse(`x=html("div p, section p") count > 0`)
	if err != nil {
		t.Fatal(err)
	}
	left := 1000
	got, problem := selectAll(context.Background(), doc, a, 1000, &left)
	want := cascadia.QueryAll(doc, a.Selector())
	if problem != "" || len(got) != len(want) || len(got) != 4 {
		t.Fatalf("selectAll = %d nodes, %q; cascadia found %d", len(got), problem, len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("node %d differs: %v, want %v", i, got[i].Attr, want[i].Attr)
		}
	}
	if left >= 1000 {
		t.Fatalf("budget left %d: the walk charged nothing", left)
	}
}

// TestSelectAllIsBounded: a selector that searches ancestors pays for the
// depth of each node; past the selector's or the check's budget it stops
// with errHTMLTooMuch, and once ctx ends with errOutOfTime.
func TestSelectAllIsBounded(t *testing.T) {
	deep := htmlOf(t, strings.Repeat("<div>", 400)+"x")
	searching, err := assert.Parse(`x=html("section div") exists`)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := assert.Parse(`x=html("div") exists`)
	if err != nil {
		t.Fatal(err)
	}
	// 400 divs at depths up to about 400: about 80 000 for a search, about
	// 400 without.
	left := 1 << 30
	if _, problem := selectAll(context.Background(), deep, searching, 50_000, &left); problem != errHTMLTooMuch {
		t.Fatalf("a searching selector over a deep page = %q, want %q", problem, errHTMLTooMuch)
	}
	left = 1 << 30
	if nodes, problem := selectAll(context.Background(), deep, plain, 50_000, &left); problem != "" || len(nodes) != 400 {
		t.Fatalf("a plain selector over a deep page = %d nodes, %q", len(nodes), problem)
	}
	left = 100
	if _, problem := selectAll(context.Background(), deep, plain, 50_000, &left); problem != errHTMLTooMuch {
		t.Fatalf("past the check's budget = %q, want %q", problem, errHTMLTooMuch)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	left = 1 << 30
	if _, problem := selectAll(ctx, deep, plain, 50_000, &left); problem != errOutOfTime {
		t.Fatalf("an ended probe = %q, want %q", problem, errOutOfTime)
	}
}
