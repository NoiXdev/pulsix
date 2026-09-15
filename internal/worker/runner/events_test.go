// ParseEvent is the pure ansible-runner line parser. TDD against the
// checked-in fixture (testdata/events.jsonl), whose line shapes follow the
// ansible-runner display callback's documented event_data fields, plus one
// truncated line — malformed input is skipped with a warning, never fatal.
package runner

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func loadFixture(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/events.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// goldenEvents produces the canonical emission sequence of the fixture: the
// events the runner would forward, with the raw payload stripped (it is the
// fixture line itself).
func goldenEvents(t *testing.T) string {
	t.Helper()
	var out []map[string]string
	for _, l := range loadFixture(t) {
		ev, ok, err := ParseEvent([]byte(l))
		if err != nil {
			out = append(out, map[string]string{"state": "parse-error"})
			continue
		}
		if !ok || ev.State == "" { // unknown event or stats-only line
			if ev.Stdout != "" {
				out = append(out, map[string]string{"state": "stdout"})
			}
			continue
		}
		out = append(out, map[string]string{"state": ev.State, "host": ev.Host, "task": ev.Task})
		if ev.Stdout != "" {
			out = append(out, map[string]string{"state": "stdout"})
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func TestParseEventGoldenSequence(t *testing.T) {
	want, err := os.ReadFile("testdata/events.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got := goldenEvents(t); got != string(want) {
		t.Errorf("emission sequence differs from golden:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestParseEventMappings(t *testing.T) {
	lines := loadFixture(t)
	cases := []struct {
		line   int // 1-based fixture line
		state  string
		host   string
		task   string
		stdout bool // an additional stdout event must follow
	}{
		{3, "task", "web1", "Install nginx", false},
		{4, "ok", "web1", "Install nginx", true},
		{5, "changed", "web1", "Enable nginx", true},
		{6, "failed", "app1", "Run migration", true},
		{7, "unreachable", "db1", "Install nginx", true},
		{8, "skipped", "web1", "Only on ubuntu", true},
	}
	for _, c := range cases {
		ev, ok, err := ParseEvent([]byte(lines[c.line-1]))
		if err != nil || !ok {
			t.Fatalf("line %d: ok=%v err=%v", c.line, ok, err)
		}
		if ev.State != c.state || ev.Host != c.host || ev.Task != c.task {
			t.Errorf("line %d: state=%q host=%q task=%q, want %q/%q/%q", c.line, ev.State, ev.Host, ev.Task, c.state, c.host, c.task)
		}
		if (ev.Stdout != "") != c.stdout {
			t.Errorf("line %d: stdout follow-up = %t, want %t (stdout %q)", c.line, ev.Stdout != "", c.stdout, ev.Stdout)
		}
		if ev.Payload != lines[c.line-1] {
			t.Errorf("line %d: payload must be the raw line", c.line)
		}
	}

	// The banner events (playbook_on_start, playbook_on_task_start) map to
	// no run event themselves — only their stdout flows on.
	for _, n := range []int{1, 2} {
		ev, ok, err := ParseEvent([]byte(lines[n-1]))
		if err != nil {
			t.Fatalf("line %d: %v", n, err)
		}
		if ok || ev.State != "" {
			t.Errorf("line %d: banner event must produce no primary event, got ok=%t state=%q", n, ok, ev.State)
		}
		if ev.Stdout == "" {
			t.Errorf("line %d: banner event must yield a stdout follow-up", n)
		}
	}
}

func TestParseEventMalformedLineIsNeverFatal(t *testing.T) {
	lines := loadFixture(t)
	// Line 11 is truncated JSON: no event, an error naming the cause, and
	// the parser must not panic or otherwise stop the stream.
	ev, ok, err := ParseEvent([]byte(lines[len(lines)-1]))
	if ok {
		t.Errorf("malformed line must produce no event, got %+v", ev)
	}
	if err == nil {
		t.Error("malformed line must carry a parse error (advisory: the caller warns)")
	}

	// Line 10 is a known but unmapped ansible-runner event: skipped silently.
	ev, ok, err = ParseEvent([]byte(lines[9]))
	if err != nil {
		t.Fatalf("async poll line: %v", err)
	}
	if ok || ev.State != "" {
		t.Errorf("unmapped event must produce nothing, got ok=%t ev=%+v", ok, ev)
	}
}

func TestParseEventStats(t *testing.T) {
	lines := loadFixture(t)
	ev, ok, err := ParseEvent([]byte(lines[8])) // the playbook_on_stats line
	if err != nil || !ok {
		t.Fatalf("stats line: ok=%v err=%v", ok, err)
	}
	if ev.State != "" {
		t.Errorf("the stats event itself maps to no run event state, got %q", ev.State)
	}
	s := ev.Stats
	if s == nil {
		t.Fatal("stats must be captured from the stats event")
	}
	web1, db1, app1 := s["web1"], s["db1"], s["app1"]
	if web1.Ok != 3 || web1.Changed != 1 || web1.Failed != 0 || web1.Unreachable != 0 || web1.Skipped != 1 {
		t.Errorf("web1 stats = %+v", web1)
	}
	if db1.Unreachable != 1 {
		t.Errorf("db1 stats = %+v (dark is the unreachable count)", db1)
	}
	if app1.Failed != 1 {
		t.Errorf("app1 stats = %+v", app1)
	}
}

func TestParseEventTableDriven(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		want  bool   // produces an event
		err   bool   // advisory parse error
		state string // set → the event's state; "" for ignored lines
	}{
		{"empty line", "", false, false, ""},
		{"whitespace line", "   \t", false, false, ""},
		{"not json", "boom", false, true, ""},
		{"json but no event key", `{"uuid": "x"}`, false, false, ""},
		{"event is not a string", `{"event": 3}`, false, true, ""},
		{"event_data missing", `{"event": "runner_on_ok"}`, true, false, "ok"},
		{"event_data wrong type", `{"event": "runner_on_ok", "event_data": 7}`, false, true, ""},
		{"no host no task", `{"event": "runner_on_ok", "event_data": {}}`, true, false, "ok"},
		{"task falls back to action", `{"event": "runner_on_start", "event_data": {"host": "h", "task_action": "ping"}}`, true, false, "task"},
		{"item failure maps to failed", `{"event": "runner_item_on_failed", "event_data": {"host": "h"}}`, true, false, "failed"},
		{"async failure maps to failed", `{"event": "runner_on_async_failed", "event_data": {"host": "h"}}`, true, false, "failed"},
		{"runner_retry ignored", `{"event": "runner_retry", "event_data": {"host": "h"}}`, false, false, ""},
		{"stats line without maps", `{"event": "playbook_on_stats", "event_data": {"ok": 3}}`, true, false, ""},
		{"ok with censored res", `{"event": "runner_on_ok", "event_data": {"host": "h", "res": {"censored": "..."}}}`, true, false, "ok"},
		{"ok without res is ok not changed", `{"event": "runner_on_ok", "event_data": {"host": "h"}}`, true, false, "ok"},
		{"no_log result has no stdout follow-up", `{"event": "runner_on_ok", "stdout": "", "event_data": {"host": "h"}}`, true, false, "ok"},
		{"stdout only event", `{"event": "playbook_on_no_hosts_matched", "stdout": "skipping: no hosts matched"}`, false, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, ok, err := ParseEvent([]byte(c.line))
			if ok != c.want {
				t.Errorf("ok = %t (event %+v), want %t", ok, ev, c.want)
			}
			if (err != nil) != c.err {
				t.Errorf("err = %v, want error %t", err, c.err)
			}
			if ok && ev.State != c.state {
				t.Errorf("state = %q, want %q", ev.State, c.state)
			}
		})
	}
}

func TestParseEventStdoutFollowUpCarriesPayloadOnly(t *testing.T) {
	line := `{"event": "runner_on_ok", "stdout": "ok: [web1]", "event_data": {"host": "web1", "task": "t"}}`
	ev, ok, err := ParseEvent([]byte(line))
	if err != nil || !ok || ev.Stdout != "ok: [web1]" {
		t.Fatalf("ev=%+v ok=%t err=%v", ev, ok, err)
	}
	if ev.Payload != line {
		t.Errorf("payload must be the raw line")
	}
}
