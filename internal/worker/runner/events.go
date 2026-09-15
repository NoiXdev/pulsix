// The pure ansible-runner event line parser: one JSONL line of the runner's
// stdout event stream in, the run's RunEvent fields out. The mapping follows
// the event names the ansible-runner display callback emits (pinned against
// ansible-runner's awx_display callback, the callback `ansible-runner run`
// uses):
//
//	runner_on_start       → task
//	runner_on_ok          → ok (changed, when the result says changed)
//	runner_on_failed      → failed
//	runner_on_skipped     → skipped
//	runner_on_unreachable → unreachable
//	playbook_on_stats     → not an event; the stats source (Event.Stats)
//	everything else       → no primary event
//
// Any line carrying a non-empty top-level `stdout` (the display text the
// callback captured for the event) additionally yields a `stdout` event —
// for mapped events as a follow-up, for banner events (playbook_on_start,
// playbook_on_task_start, the PLAY RECAP) as the only emission. The runner's
// own run_start/run_end bookends are synthetic, emitted by runner.Run.
package runner

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Event is one parsed ansible-runner event: what the worker forwards and
// what the terminal stats build consumes. Payload is the raw JSONL line —
// job_events stores it verbatim (the manager normalizes it into jsonb).
type Event struct {
	// State is the run event state: ok | failed | skipped | changed |
	// unreachable | task. Empty means the line maps to no run event (an
	// ansible-runner-internal event such as playbook_on_start, or the
	// stats event itself).
	State string
	Host  string
	Task  string
	// Stdout is the event's captured stdout; when non-empty, the runner
	// also emits a `stdout` event with the raw line as payload.
	Stdout string
	// Payload is the raw JSONL line of the event, stored verbatim.
	Payload string
	// Stats is the per-host count map the stats event carries; nil on
	// every other line.
	Stats map[string]hostStat
}

// hostStat is one host's row of ansible's recap: ok / changed / failed /
// unreachable / skipped counts (ansible-runner names unreachable `dark`).
type hostStat struct {
	Ok          int
	Changed     int
	Failed      int
	Unreachable int
	Skipped     int
}

// ParseEvent parses one ansible-runner JSONL event line. A true ok reports a
// line the runner knows; the caller emits the primary event when State is
// non-empty and a `stdout` follow-up when Stdout is non-empty. A false ok
// with a nil error means the line produces nothing at all (unknown or
// ansible-runner-internal events) — silently, they are expected stream
// noise. A non-nil error means the line could not be read at all (malformed
// JSON, wrong event_data shape); it is advisory: the caller logs a warning
// and continues, a single bad line never fails the run.
func ParseEvent(line []byte) (Event, bool, error) {
	text := strings.TrimSpace(string(line))
	if text == "" {
		return Event{}, false, nil
	}
	var raw struct {
		Event     string          `json:"event"`
		Stdout    string          `json:"stdout"`
		EventData json.RawMessage `json:"event_data"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return Event{}, false, fmt.Errorf("parse event line: %w", err)
	}
	if raw.Event == "" {
		return Event{}, false, nil
	}
	if len(raw.EventData) > 0 && raw.EventData[0] != '{' {
		return Event{}, false, fmt.Errorf("event_data must be an object, got %s", raw.EventData)
	}
	ev := Event{Payload: text, Stdout: raw.Stdout}

	switch raw.Event {
	case "playbook_on_stats":
		stats, err := parseStats(raw.EventData)
		if err != nil {
			return Event{}, false, fmt.Errorf("parse stats event: %w", err)
		}
		ev.Stats = stats
		return ev, true, nil
	case "runner_on_start":
		ev.State = "task"
		ev.Host, ev.Task = hostTask(raw.EventData)
		return ev, true, nil
	case "runner_on_ok", "runner_item_on_ok":
		ev.State = "ok"
		ev.Host, ev.Task = hostTask(raw.EventData)
		if resChanged(raw.EventData) {
			ev.State = "changed"
		}
		return ev, true, nil
	case "runner_on_failed", "runner_item_on_failed",
		"runner_on_async_failed", "runner_on_async_item_failed":
		ev.State = "failed"
		ev.Host, ev.Task = hostTask(raw.EventData)
		return ev, true, nil
	case "runner_on_skipped", "runner_item_on_skipped":
		ev.State = "skipped"
		ev.Host, ev.Task = hostTask(raw.EventData)
		return ev, true, nil
	case "runner_on_unreachable":
		ev.State = "unreachable"
		ev.Host, ev.Task = hostTask(raw.EventData)
		return ev, true, nil
	default:
		// Every other event the callback emits (playbook_on_start,
		// playbook_on_task_start, runner_on_no_hosts, runner_retry, ...)
		// produces no run event itself; only its stdout, if any, flows on.
		return ev, false, nil
	}
}

// parseStats reads the stats event's event_data: ansible's recap maps
// (ok/changed/failures/dark/skipped, each host → count). Values that are not
// per-host maps, and counts that are not numbers, are ignored — the recap is
// best-effort bookkeeping, never the run's authority for anything else.
func parseStats(data json.RawMessage) (map[string]hostStat, error) {
	stats := map[string]hostStat{}
	if len(data) == 0 {
		return stats, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("event_data: %w", err)
	}
	for _, key := range []string{"ok", "changed", "failures", "dark", "skipped"} {
		var hosts map[string]json.RawMessage
		if err := json.Unmarshal(raw[key], &hosts); err != nil {
			continue // absent, or not a per-host map
		}
		for host, v := range hosts {
			n, err := count(v)
			if err != nil {
				continue
			}
			row := stats[host]
			switch key {
			case "ok":
				row.Ok = n
			case "changed":
				row.Changed = n
			case "failures":
				row.Failed = n
			case "dark":
				row.Unreachable = n
			case "skipped":
				row.Skipped = n
			}
			stats[host] = row
		}
	}
	return stats, nil
}

// count decodes a stats value that may arrive as a bare number, a string
// number or a float-typed JSON value.
func count(v json.RawMessage) (int, error) {
	if len(v) == 0 {
		return 0, fmt.Errorf("missing")
	}
	s := strings.Trim(string(v), ` "`)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return int(n), nil
	}
	return 0, fmt.Errorf("not a number: %q", v)
}

// hostTask extracts the host and the task name of a runner_* event's
// event_data. The callback writes the task's display name under "task" and
// the module name under "task_action"; fall back to the action when a
// runner version omits the name.
func hostTask(data json.RawMessage) (host, task string) {
	var raw struct {
		Host       string `json:"host"`
		Task       string `json:"task"`
		TaskAction string `json:"task_action"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", ""
	}
	if raw.Task != "" {
		task = raw.Task
	} else {
		task = raw.TaskAction
	}
	return raw.Host, task
}

// resChanged reports whether the event's result is a change: the callback
// emits runner_on_ok for both ok and changed results, with res.changed
// telling them apart. A result ansible censored (no_log) carries no changed
// flag and stays plain ok.
func resChanged(data json.RawMessage) bool {
	var raw struct {
		Res struct {
			Changed bool `json:"changed"`
		} `json:"res"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	return raw.Res.Changed
}
