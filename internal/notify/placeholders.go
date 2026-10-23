package notify

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A webhook body template is not Go's text/template: it is literal text
// with "{{ path }}" placeholders drawn from a fixed allowlist of event
// fields (placeholderFields below). This is deliberate — a general
// templating language lets an operator run an unbounded computation
// inside a save-time or delivery-time request on a shared manager, and
// lets a template call methods that mutate the event it is rendering
// (see docs/superpowers/sdd/2026-09-19-v0.2d-notifications/task-2-review.md,
// Critical 1 and Important 2). The placeholder grammar has no loops, no
// user-reachable functions besides "json", and no method calls, so
// neither problem is reachable by construction. Rendering is a single
// left-to-right pass over the parsed template.
//
// Syntax: "{{" <path> "}}" or "{{" <path> "|" "json" "}}", whitespace
// allowed around every token. path is a dot-separated chain of
// identifiers, matched case-sensitively against placeholderFields.
// Anything else between "{{" and "}}" — a missing path, an unknown one,
// parentheses, quotes, extra pipes, "range"/"if"/"with" — is a syntax
// error named in full, and an unterminated "{{" is a syntax error too:
// none of it is treated as literal text, so a mistyped placeholder is
// never sent out verbatim.

// placeholderInner matches the content between "{{" and "}}" once it has
// been located: a path, optionally followed by "| json".
var placeholderInner = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)*)\s*(\|\s*json\s*)?$`)

// templatePart is one piece of a parsed template: either literal text, or
// a resolved placeholder (accessor is looked up once at parse time, so
// render never has to fail on an unknown path).
type templatePart struct {
	literal  string
	path     string
	accessor func(Event) (any, bool)
	asJSON   bool
}

// parseBodyTemplate splits tmpl into literal and placeholder parts,
// resolving every placeholder's path against placeholderFields. It never
// executes anything — the result is data, not code — so it is cheap to
// run on every save and every delivery.
func parseBodyTemplate(tmpl string) ([]templatePart, error) {
	var parts []templatePart
	rest := tmpl
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			if rest != "" {
				parts = append(parts, templatePart{literal: rest})
			}
			return parts, nil
		}
		if i > 0 {
			parts = append(parts, templatePart{literal: rest[:i]})
		}
		rest = rest[i+2:]
		j := strings.Index(rest, "}}")
		if j < 0 {
			return nil, fmt.Errorf("%w: unterminated placeholder %q", ErrInvalidInput, "{{"+truncateForError(rest))
		}
		inner := rest[:j]
		rest = rest[j+2:]
		m := placeholderInner.FindStringSubmatch(inner)
		if m == nil {
			return nil, fmt.Errorf("%w: %q is not a valid placeholder", ErrInvalidInput, "{{"+truncateForError(inner)+"}}")
		}
		path := m[1]
		accessor, ok := placeholderFields[path]
		if !ok {
			return nil, fmt.Errorf("%w: %q is not a known placeholder", ErrInvalidInput, path)
		}
		parts = append(parts, templatePart{path: path, accessor: accessor, asJSON: m[2] != ""})
	}
}

// truncateForError bounds how much of a placeholder's text — the tail
// after an unterminated "{{", or the inner text of one that failed to
// parse — an error message echoes back, so a single malformed placeholder
// cannot blow up the size of a save-time 422.
func truncateForError(s string) string {
	const max = 64
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// renderParts renders parts against ev, refusing to grow the output past
// maxBodyBytes.
func renderParts(parts []templatePart, ev Event) ([]byte, error) {
	var buf strings.Builder
	for _, p := range parts {
		var chunk string
		if p.accessor == nil {
			chunk = p.literal
		} else {
			val, ok := p.accessor(ev)
			if p.asJSON {
				if !ok {
					val = nil
				}
				b, err := json.Marshal(val)
				if err != nil {
					return nil, fmt.Errorf("render placeholder %s: %w", p.path, err)
				}
				chunk = string(b)
			} else {
				chunk = escapedText(val, ok)
			}
		}
		if buf.Len()+len(chunk) > maxBodyBytes {
			return nil, errBodyTooLarge
		}
		buf.WriteString(chunk)
	}
	return []byte(buf.String()), nil
}

// escapedText renders a field's default (no "| json") form: the content
// of a JSON string for that value, without the surrounding quotes, so it
// drops straight into a template-authored string literal. A missing
// field is the empty string.
func escapedText(val any, ok bool) string {
	if !ok || val == nil {
		return ""
	}
	b, err := json.Marshal(fieldText(val))
	if err != nil {
		return ""
	}
	return string(b[1 : len(b)-1])
}

// fieldText is a field's plain text form, before JSON-escaping. The
// []AlertItem case is what keeps "{{ alerts.items }}" from rendering
// Go's own %v of a slice of structs into somebody's chat message: a list
// field's text form is the same one line per alert the e-mail body
// prints, and its structured form is what "| json" is for.
func fieldText(val any) string {
	switch v := val.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case []AlertItem:
		return alertItemsText(v)
	default:
		return fmt.Sprint(v)
	}
}

// checkBodyTemplate validates a body template at save time: it must parse
// (every placeholder a known field, correct syntax) and be at most
// maxTemplateLength bytes. When jsonContentType is true (the channel's
// *effective* Content-Type — see resolveBodyJSON in service.go — names a
// JSON media type), it additionally renders the template against a
// realistic sample of every event kind — each with only that kind's
// sections populated, since a real event leaves the others nil — plus a
// hostile variant whose text fields carry quotes, backslashes and
// newlines, and requires every result to be valid JSON. A field absent
// from a kind's sample renders empty or null, so this never fails just
// because a template mentions a field only some kinds carry.
func checkBodyTemplate(tmpl string, jsonContentType bool) error {
	if tmpl == "" {
		return nil
	}
	if len(tmpl) > maxTemplateLength {
		return fmt.Errorf("%w: the body template must be at most %d bytes", ErrInvalidInput, maxTemplateLength)
	}
	parts, err := parseBodyTemplate(tmpl)
	if err != nil {
		return err
	}
	if !jsonContentType {
		if _, err := renderParts(parts, sampleEvent()); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		return nil
	}
	for _, kind := range sampleEventKinds() {
		for _, ev := range []Event{sampleEventForKind(kind), hostileSampleEventForKind(kind)} {
			body, err := renderParts(parts, ev)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidInput, err)
			}
			if !json.Valid(body) {
				return fmt.Errorf("%w: the rendered body is not valid JSON for a %s event", ErrInvalidInput, kind)
			}
		}
	}
	return nil
}

// placeholderFields is the fixed, documented allowlist a body template's
// placeholders may name (spec 2026-09-18 §5, §7): what a message may
// carry — schedule, template, result, counts, duration and a link — and
// nothing else. There is deliberately no field for log output, a run's
// free-text error, variables or any secret. A nil section (the event
// kind does not carry it) reports ok=false; parseBodyTemplate resolves
// every path exactly once, so this map is never consulted at render time.
var placeholderFields = map[string]func(Event) (any, bool){
	"event.kind": func(ev Event) (any, bool) { return string(ev.Kind), true },
	"event.at":   func(ev Event) (any, bool) { return formatTime(ev.At), true },
	"event.url":  func(ev Event) (any, bool) { return ev.URL, ev.URL != "" },

	"org.id":   func(ev Event) (any, bool) { return ev.Org.ID.String(), true },
	"org.slug": func(ev Event) (any, bool) { return ev.Org.Slug, true },
	"org.name": func(ev Event) (any, bool) { return ev.Org.Name, true },

	"schedule.id": func(ev Event) (any, bool) {
		return refField(ev.Schedule, func(r NamedRef) any { return r.ID.String() })
	},
	"schedule.name": func(ev Event) (any, bool) { return refField(ev.Schedule, func(r NamedRef) any { return r.Name }) },

	"template.id": func(ev Event) (any, bool) {
		return refField(ev.Template, func(r NamedRef) any { return r.ID.String() })
	},
	"template.name": func(ev Event) (any, bool) { return refField(ev.Template, func(r NamedRef) any { return r.Name }) },

	"run.id":     func(ev Event) (any, bool) { return runField(ev.Run, func(r RunSummary) any { return r.ID.String() }) },
	"run.result": func(ev Event) (any, bool) { return runField(ev.Run, func(r RunSummary) any { return r.Result }) },
	"run.url": func(ev Event) (any, bool) {
		if ev.Run == nil {
			return nil, false
		}
		return ev.URL, ev.URL != ""
	},
	"run.hosts":         func(ev Event) (any, bool) { return runField(ev.Run, func(r RunSummary) any { return r.Hosts }) },
	"run.hosts_ok":      func(ev Event) (any, bool) { return runField(ev.Run, func(r RunSummary) any { return r.HostsOK }) },
	"run.hosts_changed": func(ev Event) (any, bool) { return runField(ev.Run, func(r RunSummary) any { return r.HostsChanged }) },
	"run.hosts_failed":  func(ev Event) (any, bool) { return runField(ev.Run, func(r RunSummary) any { return r.HostsFailed }) },
	"run.hosts_no_result": func(ev Event) (any, bool) {
		return runField(ev.Run, func(r RunSummary) any { return r.HostsNoResult })
	},
	"run.duration_seconds": func(ev Event) (any, bool) {
		return runField(ev.Run, func(r RunSummary) any { return r.DurationSeconds })
	},
	"run.started_at": func(ev Event) (any, bool) {
		if ev.Run == nil || ev.Run.StartedAt == nil {
			return nil, false
		}
		return formatTime(*ev.Run.StartedAt), true
	},
	"run.finished_at": func(ev Event) (any, bool) {
		if ev.Run == nil || ev.Run.FinishedAt == nil {
			return nil, false
		}
		return formatTime(*ev.Run.FinishedAt), true
	},

	"decision.outcome": func(ev Event) (any, bool) {
		return decisionField(ev.Decision, func(d DecisionSummary) any { return d.Outcome })
	},
	"decision.reason": func(ev Event) (any, bool) {
		return decisionField(ev.Decision, func(d DecisionSummary) any { return d.Reason })
	},
	"decision.due_at": func(ev Event) (any, bool) {
		if ev.Decision == nil {
			return nil, false
		}
		return formatTime(ev.Decision.DueAt), true
	},

	// Both ends print with the organization's own offset, not in UTC
	// (backlog 2.88). A body may carry these next to digest.period, and
	// period names the organization's local dates: in UTC these two
	// dated the week a day early for an organization east of UTC and a
	// day late for one west of it, leaving a consumer joining the three
	// no way to tell which week was meant. The instant is unchanged —
	// RFC 3339 with an offset is as absolute as one with a Z — so a
	// consumer that only parses the timestamp sees no difference, while
	// one that reads the date off it now agrees with the label.
	"digest.from": func(ev Event) (any, bool) {
		return digestField(ev.Digest, func(d DigestSummary) any { return formatTimeIn(d.From, digestLocation(&d)) })
	},
	"digest.to": func(ev Event) (any, bool) {
		return digestField(ev.Digest, func(d DigestSummary) any { return formatTimeIn(d.To, digestLocation(&d)) })
	},
	"digest.timezone": func(ev Event) (any, bool) {
		return digestField(ev.Digest, func(d DigestSummary) any { return d.Timezone })
	},
	"digest.period": func(ev Event) (any, bool) {
		return digestField(ev.Digest, func(d DigestSummary) any {
			return d.From.Format("2 Jan") + " – " + d.To.Format("2 Jan 2006")
		})
	},
	"digest.schedules_note": func(ev Event) (any, bool) {
		return digestField(ev.Digest, func(d DigestSummary) any { return digestTruncationNote(d) })
	},

	// The alert batch (spec v0.4b, decision 5). Without these a channel
	// with a custom body template that a route then names received an
	// alert.batch event whose every section resolved empty: valid JSON,
	// delivered, logged as sent, and silent about the alert. A template
	// is the Discord/n8n shape (backlog 4.6), so both forms are offered —
	// alerts.items as human text for a chat message, and the same field
	// through "| json" as the structured list for a workflow runner.
	"alerts.count": func(ev Event) (any, bool) { return alertField(ev.Alerts, func(a AlertBatch) any { return a.Count }) },
	"alerts.items": func(ev Event) (any, bool) { return alertField(ev.Alerts, func(a AlertBatch) any { return a.Items }) },
	"alerts.items_note": func(ev Event) (any, bool) {
		return alertField(ev.Alerts, func(a AlertBatch) any { return alertTruncationNote(a) })
	},
}

// formatTime writes an instant as RFC 3339 in UTC: the right default for
// a field with no organization attached to it, where a reader has no zone
// to expect and UTC is the one everyone can convert from.
func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// formatTimeIn writes the same instant with loc's offset, for the fields
// that sit beside an organization-local label and must date the same way
// it does (backlog 2.88).
func formatTimeIn(t time.Time, loc *time.Location) string { return t.In(loc).Format(time.RFC3339) }

func refField(r *NamedRef, get func(NamedRef) any) (any, bool) {
	if r == nil {
		return nil, false
	}
	return get(*r), true
}

func runField(r *RunSummary, get func(RunSummary) any) (any, bool) {
	if r == nil {
		return nil, false
	}
	return get(*r), true
}

func decisionField(d *DecisionSummary, get func(DecisionSummary) any) (any, bool) {
	if d == nil {
		return nil, false
	}
	return get(*d), true
}

// digestField resolves a digest summary field: nil (the event kind does
// not carry a digest) reports ok=false, and the field itself decides —
// digestTruncationNote is empty for an untruncated summary — whether it
// is present.
func digestField(d *DigestSummary, get func(DigestSummary) any) (any, bool) {
	if d == nil {
		return nil, false
	}
	return get(*d), true
}

// alertField resolves an alert-batch field: nil (the event kind is not an
// alert batch) reports ok=false, so a template mentioning alerts.* still
// renders — empty, or null under "| json" — for a run or a digest.
func alertField(a *AlertBatch, get func(AlertBatch) any) (any, bool) {
	if a == nil {
		return nil, false
	}
	return get(*a), true
}

// sampleEventKinds lists every EventKind, for save-time JSON validation.
// Every kind, not most of them: a kind missing here is a kind no body
// template is ever rendered against at save time, so a template that
// produces invalid JSON for it is accepted and only fails at delivery —
// or, worse, succeeds and says nothing. EventAlertBatch was missing for
// exactly as long as it took a route to name a templated channel.
func sampleEventKinds() []EventKind {
	return []EventKind{EventRunFinished, EventScheduleSkipped, EventScheduleFailed,
		EventDigest, EventAlertBatch, EventTest}
}

// sampleEventForKind is sampleEvent() cut down to the sections a real
// event of this kind actually carries — the other sections are nil, the
// same shape a template will see for that kind in production.
func sampleEventForKind(kind EventKind) Event {
	ev := sampleEvent()
	ev.Kind = kind
	switch kind {
	case EventRunFinished:
		ev.Decision, ev.Digest, ev.Alerts = nil, nil, nil
	case EventScheduleSkipped, EventScheduleFailed:
		ev.Run, ev.Digest, ev.Alerts = nil, nil, nil
	case EventDigest:
		ev.Schedule, ev.Template, ev.Run, ev.Decision, ev.Alerts = nil, nil, nil, nil, nil
	case EventAlertBatch:
		// An alert batch carries no schedule, template, run or decision —
		// the follower builds it from alert events and the organization,
		// and nothing else (alertroutes.Follower.enqueue). Categories are
		// run outcomes, so they are absent too.
		ev.Schedule, ev.Template, ev.Run, ev.Decision, ev.Digest = nil, nil, nil, nil, nil
		ev.Categories = nil
	case EventTest:
		ev.Schedule, ev.Template, ev.Run, ev.Decision, ev.Digest = nil, nil, nil, nil, nil
		ev.Alerts, ev.Categories, ev.URL = nil, nil, ""
	}
	return ev
}

// hostileSampleEventForKind is sampleEventForKind with every text field
// replaced by a string carrying a quote, a backslash, a newline and a
// non-ASCII character — the save-time JSON check's defence against a
// future escaping bug (review Minor 1).
func hostileSampleEventForKind(kind EventKind) Event {
	const hostile = "h\"o\\stile\ntext ☃"
	ev := sampleEventForKind(kind)
	ev.Org.Name = hostile
	if ev.Schedule != nil {
		ev.Schedule.Name = hostile
	}
	if ev.Template != nil {
		ev.Template.Name = hostile
	}
	if ev.Run != nil {
		ev.Run.Result = hostile
	}
	if ev.Decision != nil {
		ev.Decision.Outcome, ev.Decision.Reason = hostile, hostile
	}
	if ev.Digest != nil {
		ev.Digest.Timezone = hostile
	}
	if ev.Alerts != nil {
		items := slices.Clone(ev.Alerts.Items)
		for i := range items {
			items[i].HostName, items[i].CheckKey, items[i].Item = hostile, hostile, hostile
			items[i].Metric, items[i].Severity, items[i].Event = hostile, hostile, hostile
		}
		ev.Alerts = &AlertBatch{Count: ev.Alerts.Count, Items: items}
	}
	return ev
}
