package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// maxBodyBytes bounds a rendered webhook body: the default JSON body and
// a placeholder-rendered one alike.
const maxBodyBytes = 64 << 10

// MaxDigestSchedules is how many schedule entries a digest summary may
// list. The cap lives here, next to maxBodyBytes, because both bound the
// same thing — the rendered message: past roughly 600 schedule entries a
// default webhook body crosses maxBodyBytes and the delivery would be
// marked permanent, a large organization's digest never arriving (backlog
// 2.84). schedules.Service caps its list at this and sets
// DigestSummary.Truncated/ScheduleCountTotal so the renderers can say
// what was left out.
const MaxDigestSchedules = 200

var errBodyTooLarge = errors.New("the rendered body exceeds 64 KiB")

// renderWebhookBody renders a webhook body: the event's JSON when tmpl is
// empty, the parsed placeholder template over the event otherwise. See
// placeholders.go for the template syntax and its bounds.
func renderWebhookBody(tmpl string, ev Event) ([]byte, error) {
	if tmpl == "" {
		body, err := json.Marshal(ev)
		if err != nil {
			return nil, fmt.Errorf("marshal event: %w", err)
		}
		if len(body) > maxBodyBytes {
			return nil, errBodyTooLarge
		}
		return body, nil
	}
	parts, err := parseBodyTemplate(tmpl)
	if err != nil {
		return nil, err
	}
	return renderParts(parts, ev)
}

// sampleEvent fills every section with example values: templates are
// validated against it (per-kind cut-downs in placeholders.go) and "Send
// test" sends it (as kind test).
func sampleEvent() Event {
	started := time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC)
	finished := started.Add(4*time.Minute + 12*time.Second)
	id := uuid.MustParse("00000000-0000-7000-8000-000000000001")
	return Event{
		Kind: EventRunFinished, At: finished,
		Org:        OrgRef{ID: id, Slug: "example", Name: "Example"},
		Categories: []Category{CategoryFailure, CategoryChange},
		Schedule:   &NamedRef{ID: id, Name: "Nightly OS patching"},
		Template:   &NamedRef{ID: id, Name: "patch-all"},
		Run: &RunSummary{
			ID: id, Result: "failed", Hosts: 12, HostsOK: 11, HostsChanged: 9, HostsFailed: 1,
			StartedAt: &started, FinishedAt: &finished, DurationSeconds: 252,
		},
		Decision: &DecisionSummary{Outcome: "skipped_maintenance", Reason: "every targeted host is in a maintenance window", DueAt: started},
		Digest: &DigestSummary{
			From: started.AddDate(0, 0, -7), To: started, Timezone: "Europe/Berlin",
			Schedules: []DigestSchedule{{ID: id, Name: "Nightly OS patching", Runs: 2, Failures: 1, Skipped: 1}},
			Slowest:   []DigestRun{{Schedule: "Nightly OS patching", RunID: id, StartedAt: started, DurationSeconds: 252, URL: "https://pulsix.example/o/example/runs/" + id.String()}},
		},
		// Two items against a count of three, so the sample is a
		// TRUNCATED batch: that is the only shape in which
		// alerts.items_note renders anything at all, and the save-time
		// JSON check is worth nothing on a field that is always empty in
		// the sample (the note carries a non-ASCII ellipsis, which is
		// exactly the kind of character that check exists to catch).
		Alerts: &AlertBatch{Count: 3, Items: []AlertItem{
			{AlertID: id, HostID: id, HostName: "db-1", CheckKey: "disk", Item: "/var", Metric: "used_percent",
				Severity: "crit", Event: "opened", At: finished,
				URL: "https://pulsix.example/o/example/alerts?host=" + id.String()},
			{AlertID: id, HostID: id, HostName: "web-2", CheckKey: "cpu", Metric: "used_percent",
				Severity: "warn", Event: "severity_raised", At: finished,
				URL: "https://pulsix.example/o/example/alerts?host=" + id.String()},
		}},
		URL: "https://pulsix.example/o/example/runs/" + id.String(),
	}
}

// renderEmail renders the plain-text e-mail for an event.
func renderEmail(ev Event) (subject, body string) {
	var b strings.Builder
	line := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%-10s %s\n", label+":", value)
		}
	}
	name := func(r *NamedRef) string {
		if r == nil {
			return ""
		}
		return r.Name
	}
	switch ev.Kind {
	case EventRunFinished:
		result := "succeeded"
		if ev.Run != nil {
			result = ev.Run.Result
		}
		subject = fmt.Sprintf("[Pulsix] %s: %s", name(ev.Schedule), result)
		if ev.Run != nil && ev.Run.HostsChanged > 0 && result == "succeeded" {
			subject += " with changes"
		}
		line("Schedule", name(ev.Schedule))
		line("Template", name(ev.Template))
		if r := ev.Run; r != nil {
			line("Result", r.Result)
			line("Hosts", fmt.Sprintf("%d (ok %d, changed %d, failed %d)", r.Hosts, r.HostsOK, r.HostsChanged, r.HostsFailed))
			if r.StartedAt != nil {
				line("Started", r.StartedAt.UTC().Format(time.RFC1123))
			}
			line("Duration", (time.Duration(r.DurationSeconds) * time.Second).String())
		}
	case EventScheduleSkipped, EventScheduleFailed:
		verb := "skipped"
		if ev.Kind == EventScheduleFailed {
			verb = "could not start"
		}
		subject = fmt.Sprintf("[Pulsix] %s: %s", name(ev.Schedule), verb)
		line("Schedule", name(ev.Schedule))
		line("Template", name(ev.Template))
		if d := ev.Decision; d != nil {
			line("Outcome", d.Outcome)
			line("Reason", d.Reason)
			line("Due", d.DueAt.UTC().Format(time.RFC1123))
		}
	case EventDigest:
		subject = fmt.Sprintf("[Pulsix] Weekly schedule digest for %s", ev.Org.Name)
		if d := ev.Digest; d != nil {
			loc := digestLocation(d)
			fmt.Fprintf(&b, "Scheduled runs from %s to %s (%s)\n\n", d.From.In(loc).Format("2 Jan"), d.To.In(loc).Format("2 Jan 2006"), d.Timezone)
			if len(d.Schedules) == 0 {
				b.WriteString("No schedules.\n")
			}
			for _, s := range d.Schedules {
				fmt.Fprintf(&b, "%s: %d run(s), %d failure(s), %d skipped or missed\n", s.Name, s.Runs, s.Failures, s.Skipped)
			}
			if note := digestTruncationNote(*d); note != "" {
				b.WriteString(note + "\n")
			}
			if len(d.Slowest) > 0 {
				b.WriteString("\nSlowest runs:\n")
				for _, r := range d.Slowest {
					fmt.Fprintf(&b, "%s, %s: %s  %s\n", r.Schedule, r.StartedAt.In(loc).Format("Mon 2 Jan 15:04 MST"), time.Duration(r.DurationSeconds)*time.Second, r.URL)
				}
			}
		}
	case EventAlertBatch:
		if a := ev.Alerts; a != nil {
			subject = fmt.Sprintf("[Pulsix] %d alert(s) for %s", a.Count, ev.Org.Name)
			b.WriteString(alertItemsText(a.Items))
			if len(a.Items) > 0 {
				b.WriteString("\n")
			}
			if note := alertTruncationNote(*a); note != "" {
				b.WriteString(note + "\n")
			}
		} else {
			subject = fmt.Sprintf("[Pulsix] alerts for %s", ev.Org.Name)
		}
	default:
		subject = fmt.Sprintf("[Pulsix] Test notification from %s", ev.Org.Name)
		b.WriteString("This is a test message from a Pulsix notification channel. It worked.\n")
	}
	if ev.URL != "" {
		fmt.Fprintf(&b, "\n%s\n", ev.URL)
	}
	return subject, b.String()
}

// digestTruncationNote is what the e-mail body (and, through the
// digest.schedules_note placeholder, a webhook body) says when the
// summary's schedule list was capped: the count of schedules left out.
// Empty when the list is complete.
func digestTruncationNote(d DigestSummary) string {
	if !d.Truncated {
		return ""
	}
	omitted := d.ScheduleCountTotal - len(d.Schedules)
	if omitted <= 0 {
		return ""
	}
	return fmt.Sprintf("+%d schedules without activity", omitted)
}

// alertItemsText is a batch's item list as plain text, one alert per
// line: severity, host, what happened, and the subject it happened to.
// The e-mail body and the alerts.items placeholder share it, so a webhook
// template and a mail about the same batch cannot describe it
// differently. No trailing newline — the callers decide what follows.
func alertItemsText(items []AlertItem) string {
	lines := make([]string, len(items))
	for i, it := range items {
		what := it.CheckKey
		if it.Item != "" {
			what += "/" + it.Item
		}
		if it.Metric != "" {
			what += " " + it.Metric
		}
		lines[i] = fmt.Sprintf("%-8s %-20s %-8s %s", it.Severity, it.HostName, it.Event, what)
	}
	return strings.Join(lines, "\n")
}

// alertTruncationNote is what an alert batch's message says when its item
// list was capped: how many alerts the list does not name. Empty when the
// list is complete.
//
// The cap itself belongs to the follower (alertroutes.MaxBatchItems) —
// this package knows nothing of routing and must not — which is exactly
// why AlertBatch.Count is the number BEFORE the cut: the difference
// between Count and the items actually carried is the only evidence here
// that anything was left out. Without this note a capped batch would be
// indistinguishable from a complete one, and decision 5's own motivating
// example ("fifty hosts filling their disks") sits right at the cap, so
// the first message that needs the note is also the most urgent one. The
// same promise digestTruncationNote already keeps for the weekly digest.
func alertTruncationNote(a AlertBatch) string {
	omitted := a.Count - len(a.Items)
	if omitted <= 0 {
		return ""
	}
	return fmt.Sprintf("… and %d more", omitted)
}

// digestLocation is the zone every time in a digest message prints in: the
// organization's own, the one the period header is labelled with. The
// slowest-run list used to print UTC there, so it could date a run before
// the period it belongs to — Pacific/Auckland's Monday 08:30 NZST reads as
// the previous Sunday 20:30 in UTC (final review, Important #4).
//
// The zone comes from the summary's Timezone name, not from From's own
// Location: the event reaches the renderer through the delivery payload's
// JSON, and a decoded time.Time carries a fixed numeric offset rather than
// the named zone, so MST would print "+1200" instead of "NZST". From's
// Location is the fallback for a name that no longer resolves, which keeps
// the wall-clock reading right even when the abbreviation is not.
func digestLocation(d *DigestSummary) *time.Location {
	if loc, err := time.LoadLocation(d.Timezone); err == nil {
		return loc
	}
	return d.From.Location()
}
