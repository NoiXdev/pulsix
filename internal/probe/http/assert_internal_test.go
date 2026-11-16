package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/assert"
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
	if got := textActual(string(long)); len(got) != probe.ShortLabelLen || got[len(got)-1] != '"' {
		t.Fatalf("textActual(1 MiB) = %q (%d bytes), want %d bytes ending in a quote", got, len(got), probe.ShortLabelLen)
	}
	// A cut never splits an escape or a rune and keeps the closing quote.
	for _, s := range []string{strings.Repeat(`\x01`, 100), strings.Repeat("\x01", 100), strings.Repeat("ü", 100), strings.Repeat(`a"`, 100), strings.Repeat(" ", 100)} {
		got := textActual(s)
		var back string
		if len(got) > probe.ShortLabelLen || json.Unmarshal([]byte(got), &back) != nil || !strings.HasPrefix(s, back) || len(got) < probe.ShortLabelLen-6 {
			t.Errorf("textActual(%q…) = %q (%d bytes), want a whole JSON string of the text's start, at most %d bytes", s[:8], got, len(got), probe.ShortLabelLen)
		}
	}
}

// TestRegexWorkIsCharged: each match is charged its program size times
// the input's length before it runs; past the check's budget it is an
// error and nothing runs.
func TestRegexWorkIsCharged(t *testing.T) {
	a, err := assert.Parse(`x=body matches "v[0-9]+"`)
	if err != nil {
		t.Fatal(err)
	}
	cost := int64(a.ProgramSize()) * 152 // 151 bytes, charged as len+1
	w := &regexWork{left: cost + cost/2}
	input := strings.Repeat("x", 146) + "v1234"
	if v := w.text(a, input, true); v.err != "" || !v.holds {
		t.Fatalf("first match = %+v, want it to hold", v)
	}
	if w.left != cost/2 {
		t.Fatalf("budget left %d, want %d", w.left, cost/2)
	}
	if v := w.text(a, input, true); v.err != errRegexTooMuch {
		t.Fatalf("second match = %+v, want %q", v, errRegexTooMuch)
	}
	// contains costs nothing of the regex budget.
	c, _ := assert.Parse(`x=body contains "v1"`)
	if v := w.text(c, input, true); v.err != "" || !v.holds || w.left != cost/2 {
		t.Fatalf("contains = %+v, left %d", v, w.left)
	}
}
