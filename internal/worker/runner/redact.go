// Redaction of run output (final review I4): ansible-runner's events carry
// module results and display text verbatim, and a task can print anything
// it can reach — a become password, a secret extra var, a key file. Every
// event is reduced to what the run view needs and every secret value the
// claim revealed is replaced by [redacted] BEFORE the event leaves the
// runner (and so before the loop's size bound truncates it: a secret is
// always replaced whole, never cut into a surviving prefix).
package runner

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// redactedMarker replaces every secret occurrence.
const redactedMarker = "[redacted]"

// minRedactLen is the shortest secret value redacted: shorter values would
// shred ordinary output (a 2-character password matches everywhere) and
// cannot be protected in free text anyway.
const minRedactLen = 4

// minRedactLineLen is the shortest single line of a multi-line secret (a
// private key) redacted on its own, for output that shows the key line by
// line.
const minRedactLineLen = 16

// redactor removes a claim's secret values from event text.
type redactor struct {
	// values are the literal strings to replace, longest first so a secret
	// that contains another is replaced whole.
	values []string
}

// newRedactor collects the claim's secrets: the manager's redact list
// (secret extra vars, secret inventory vars, one-time logins, ...) plus
// every part of every credential the claim ships — defense in depth for a
// claim whose list is incomplete. Each value is also redacted in its
// JSON-escaped spelling (display text shows module results as JSON) and,
// for a multi-line value, line by line.
func newRedactor(job *workerpb.Job) *redactor {
	set := map[string]bool{}
	add := func(v string) {
		if len(v) < minRedactLen {
			return
		}
		set[v] = true
		if b, err := json.Marshal(v); err == nil {
			if esc := string(b[1 : len(b)-1]); esc != v {
				set[esc] = true
			}
		}
		if strings.Contains(v, "\n") {
			for _, line := range strings.Split(v, "\n") {
				if line = strings.TrimSpace(line); len(line) >= minRedactLineLen {
					set[line] = true
				}
			}
		}
	}
	for _, v := range job.GetRedact() {
		add(v)
	}
	for _, raw := range job.GetCredentials() {
		var s credentials.Secret
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			continue
		}
		add(s.PrivateKey)
		add(s.Password)
		add(s.Token)
	}
	r := &redactor{values: make([]string, 0, len(set))}
	for v := range set {
		r.values = append(r.values, v)
	}
	slices.SortFunc(r.values, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	return r
}

// text redacts one string.
func (r *redactor) text(s string) string {
	for _, v := range r.values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, redactedMarker)
		}
	}
	return s
}

// value redacts every string inside a decoded JSON value (keys included).
func (r *redactor) value(v any) any {
	switch t := v.(type) {
	case string:
		return r.text(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[r.text(k)] = r.value(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = r.value(e)
		}
		return out
	default:
		return v
	}
}

// eventPayload is the one payload a mapped ansible event is forwarded
// with: the event name, the display text, and the event data the run view
// reads (host, task, module, result) — runner bookkeeping (uuids, counters,
// pids, playbook paths) stays behind. Everything is redacted.
func (r *redactor) eventPayload(ev Event) string {
	var raw struct {
		Event     string         `json:"event"`
		EventData map[string]any `json:"event_data"`
	}
	_ = json.Unmarshal([]byte(ev.Payload), &raw)
	data := map[string]any{}
	for _, k := range []string{"host", "task", "task_action", "res"} {
		if v, ok := raw.EventData[k]; ok {
			data[k] = r.value(v)
		}
	}
	out := map[string]any{"event": raw.Event, "event_data": data}
	if ev.Stdout != "" {
		out["stdout"] = r.text(ev.Stdout)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"event":"` + raw.Event + `"}`
	}
	return string(b)
}

// textPayload is the payload of a display-only event (a banner, the
// recap): {"text": <redacted display text>} — the shape the run view
// renders as a plain output line.
func (r *redactor) textPayload(stdout string) string {
	b, err := json.Marshal(map[string]string{"text": r.text(stdout)})
	if err != nil {
		return `{"text":""}`
	}
	return string(b)
}
