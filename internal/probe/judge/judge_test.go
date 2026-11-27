package judge_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/assert"
	"github.com/noixdev/pulsix/internal/probe/judge"
)

func list(entries ...string) map[string]any {
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = e
	}
	return map[string]any{"assertions": out}
}

// TestEntries: an entry without a valid name or repeating one is left
// out, one that does not parse in the dialect keeps its item with a
// problem, and no more than assert.MaxEntries are read.
func TestEntries(t *testing.T) {
	got := judge.Entries(assert.TCP, list("a=connect_ms < 5", "a=connect_ms < 6", "bad name=connect_ms < 1", "noequals", "b=status == 200", "c=response contains \"x\""))
	if len(got) != 3 {
		t.Fatalf("Entries = %+v, want a, b, c", got)
	}
	if got[0].Name != "a" || got[0].Problem != "" || got[0].A.Subject() != assert.SubjectConnectMS {
		t.Errorf("a = %+v", got[0])
	}
	if got[1].Name != "b" || got[1].Problem != judge.ErrInvalid {
		t.Errorf("b = %+v, want the http subject refused in tcp", got[1])
	}
	if got[2].Name != "c" || got[2].Problem != "" {
		t.Errorf("c = %+v", got[2])
	}
	many := make([]string, assert.MaxEntries+5)
	for i := range many {
		many[i] = "n" + strings.Repeat("x", i) + "=connect_ms < 1"
	}
	if n := len(judge.Entries(assert.TCP, list(many...))); n != assert.MaxEntries {
		t.Errorf("Entries(%d) = %d entries, want %d", len(many), n, assert.MaxEntries)
	}
	if n := len(judge.Entries(assert.TCP, map[string]any{})); n != 0 {
		t.Errorf("Entries(no param) = %d", n)
	}
	if n := len(judge.Entries(assert.TCP, map[string]any{"assertions": []string{"a=connect_ms < 1"}})); n != 1 {
		t.Errorf("Entries([]string) = %d, want 1", n)
	}
}

// TestObserve: each entry's verdict as its two observations — pass, its
// severity, an error on both, or its own problem without being judged.
func TestObserve(t *testing.T) {
	entries := judge.Entries(assert.TCP, list("fast=connect_ms < 100", "slow=connect_ms < 10 @warn", "crit=connect_ms < 10", "broken=status == 1", "err=response contains \"x\""))
	judged := 0
	obs := judge.Observe("tcp", entries, func(a assert.Assertion, w *judge.Work) judge.Verdict {
		judged++
		if a.Subject() == assert.SubjectResponse {
			return judge.Failed("the read failed")
		}
		return judge.Number(a, 42.4)
	})
	if judged != 4 {
		t.Errorf("judged %d entries, want 4 (not the invalid one)", judged)
	}
	want := map[string]string{
		"fast|tcp.assertion": "pass", "fast|tcp.assertion_actual": "42",
		"slow|tcp.assertion": "warn", "slow|tcp.assertion_actual": "42",
		"crit|tcp.assertion": "crit", "crit|tcp.assertion_actual": "42",
		"broken|tcp.assertion": "!" + judge.ErrInvalid, "broken|tcp.assertion_actual": "!" + judge.ErrInvalid,
		"err|tcp.assertion": "!the read failed", "err|tcp.assertion_actual": "!the read failed",
	}
	if len(obs) != len(want) {
		t.Fatalf("Observe = %+v", obs)
	}
	for _, o := range obs {
		got := "!" + o.Err
		if o.Label != nil {
			got = *o.Label
		}
		if want[o.Item+"|"+o.Metric] != got || o.Value != nil {
			t.Errorf("%s %s = %q, want %q", o.Item, o.Metric, got, want[o.Item+"|"+o.Metric])
		}
	}
	if obs := judge.Observe("tcp", nil, nil); obs != nil {
		t.Errorf("Observe(no entries) = %+v", obs)
	}
}

// TestFailAll: msg on both metrics of every entry's item.
func TestFailAll(t *testing.T) {
	obs := judge.FailAll("dns", judge.Entries(assert.DNS, list("a=answer_count > 0", "b=nonsense")), "address not allowed")
	if len(obs) != 4 {
		t.Fatalf("FailAll = %+v", obs)
	}
	for _, o := range obs {
		if o.Err != "address not allowed" || !strings.HasPrefix(o.Metric, "dns.assertion") {
			t.Errorf("observation %+v", o)
		}
	}
}

// TestJudgeAbsentText keeps 3.13b's absent rule for text; an absent
// number fails every operator (backlog 3.87, R4).
func TestJudgeAbsentText(t *testing.T) {
	w := judge.NewWork()
	for entry, holds := range map[string]bool{
		`a=response == "x"`: false, `a=response != "x"`: true, `a=response contains "x"`: false,
		`a=response matches "x"`: false, `a=response !matches "x"`: false,
	} {
		a, err := assert.TCP.Parse(entry)
		if err != nil {
			t.Fatal(err)
		}
		if v := w.Text(a, "", false); v.Holds != holds || v.Actual != judge.ActualAbsent {
			t.Errorf("%s on absent = %+v, want holds %v", entry, v, holds)
		}
	}
	for _, entry := range []string{"a=response_ms < 5", "a=response_ms != 5", "a=response_ms in 0..9", "a=response_ms > 0"} {
		a, err := assert.TCP.Parse(entry)
		if err != nil {
			t.Fatal(err)
		}
		if v := judge.Absent(a); v.Holds || v.Actual != judge.ActualAbsent || v.Err != "" {
			t.Errorf("%s on absent = %+v, want a failure seeing absent", entry, v)
		}
	}
}

// TestWorkBudget: a pattern's program size times the input's length is
// charged before each match; past the budget it is an error and nothing
// runs.
func TestWorkBudget(t *testing.T) {
	a, err := assert.TCP.Parse(`a=response matches "x+y"`)
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Repeat("x", 1000) + "y"
	cost := int64(a.ProgramSize()) * int64(len(input)+1)
	w := judge.NewWorkOf(cost + cost/2)
	if v := w.Text(a, input, true); v.Err != "" || !v.Holds {
		t.Fatalf("first match = %+v", v)
	}
	if v := w.Text(a, input, true); v.Err != judge.ErrRegexTooMuch {
		t.Fatalf("second match = %+v, want %q", v, judge.ErrRegexTooMuch)
	}
}

// TestNumberActual: units are not printed; milliseconds whole,
// certificate days to a tenth, a TLS version with one decimal.
func TestNumberActual(t *testing.T) {
	cases := []struct {
		s    assert.Subject
		v    float64
		want string
	}{
		{assert.SubjectResponseMS, 1834.4, "1834"},
		{assert.SubjectTTFBMS, 0.4, "0"},
		{assert.SubjectConnectMS, 12.6, "13"},
		{assert.SubjectResolveMS, 3.2, "3"},
		{assert.SubjectHandshakeMS, 99.5, "100"},
		{assert.SubjectCertDays, 13.96, "14"},
		{assert.SubjectCertDays, -0.04, "0"},
		{assert.SubjectStatus, 503, "503"},
		{assert.SubjectAnswerCount, 2, "2"},
		{assert.SubjectCertValid, 1, "1"},
		{assert.SubjectVersion, 1.0, "1.0"},
		{assert.SubjectVersion, 1.3, "1.3"},
	}
	for _, tc := range cases {
		if got := judge.NumberActual(tc.s, tc.v); got != tc.want {
			t.Errorf("NumberActual(%s, %v) = %q, want %q", tc.s, tc.v, got, tc.want)
		}
	}
}

// TestTextActual: quoted, control characters escaped, no HTML escaping,
// cut to probe.ShortLabelLen with the closing quote kept.
func TestTextActual(t *testing.T) {
	if got := judge.TextActual("a\x00<b>"); got != `"a\u0000<b>"` {
		t.Fatalf("TextActual = %q", got)
	}
	if got := judge.TextActual(strings.Repeat("é", 400)); len(got) > probe.ShortLabelLen || !strings.HasSuffix(got, `"`) {
		t.Fatalf("TextActual(long) = %q (%d bytes)", got, len(got))
	}
}

// TestUnevaluated (backlog 3.87, R12): an up round from a prober that did
// not evaluate the check's assertions gets an error on both metrics of
// every entry it left out; a down round, a round without an up reading
// and a complete round get nothing.
func TestUnevaluated(t *testing.T) {
	params := list("a=connect_ms < 5", "b=connect_ms < 6", "b=connect_ms < 7", "bad name=connect_ms < 1")
	up := []probe.Observation{probe.Value("tcp.up", 1), probe.Value("tcp.connect_ms", 3)}
	got := judge.Unevaluated("tcp", params, up)
	if len(got) != 4 {
		t.Fatalf("Unevaluated(up, nothing judged) = %+v, want a and b on both metrics", got)
	}
	for _, o := range got {
		if o.Err != judge.ErrUnevaluated || (o.Item != "a" && o.Item != "b") {
			t.Errorf("observation %+v", o)
		}
	}
	label := "pass"
	partial := append(append([]probe.Observation{}, up...),
		probe.Observation{Metric: "tcp.assertion", Item: "a", Label: &label},
		probe.Observation{Metric: "tcp.assertion_actual", Item: "a", Label: &label})
	if got := judge.Unevaluated("tcp", params, partial); len(got) != 2 || got[0].Item != "b" {
		t.Errorf("Unevaluated(a judged) = %+v, want b only", got)
	}
	errored := append(append([]probe.Observation{}, up...),
		probe.Observation{Metric: "tcp.assertion", Item: "a", Err: "x"},
		probe.Observation{Metric: "tcp.assertion", Item: "b", Err: "x"})
	if got := judge.Unevaluated("tcp", params, errored); len(got) != 0 {
		t.Errorf("Unevaluated(every entry reported) = %+v", got)
	}
	down := probe.Down("tcp", probe.FailureTimeout)
	if got := judge.Unevaluated("tcp", params, down); len(got) != 0 {
		t.Errorf("Unevaluated(down) = %+v", got)
	}
	if got := judge.Unevaluated("tcp", params, []probe.Observation{{Metric: "tcp.up", Err: "x"}}); len(got) != 0 {
		t.Errorf("Unevaluated(error) = %+v", got)
	}
	if got := judge.Unevaluated("icmp", params, []probe.Observation{probe.Value("icmp.up", 1)}); len(got) != 0 {
		t.Errorf("Unevaluated(icmp) = %+v", got)
	}
	httpUp := []probe.Observation{probe.Value("http.up", 1)}
	if got := judge.Unevaluated("http", list("ok=status == 200"), httpUp); len(got) != 2 || got[0].Metric != "http.assertion" {
		t.Errorf("Unevaluated(http) = %+v, want http's entry flagged too", got)
	}
	if got := judge.Unevaluated("tcp", map[string]any{}, up); len(got) != 0 {
		t.Errorf("Unevaluated(no assertions) = %+v", got)
	}
}
