package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/noixdev/pulsix/internal/rules"
)

// Level is how urgent a chat message reads, its colour where the service
// has one (backlog 4.7).
type Level string

const (
	LevelCrit    Level = "crit"
	LevelWarn    Level = "warn"
	LevelUnknown Level = "unknown"
	LevelOK      Level = "ok"
	LevelInfo    Level = "info"
)

// Color is the level's brand colour (web/src/styles/tokens.css).
func (l Level) Color() string {
	switch l {
	case LevelCrit:
		return "#F87171"
	case LevelWarn:
		return "#FBBF24"
	case LevelUnknown:
		return "#6B7280"
	case LevelOK:
		return "#34D399"
	}
	return "#00E5FF"
}

// OpenLabel is the text of a message's link to Pulsix.
const OpenLabel = "Open in Pulsix"

// Bounds Compose applies to what an operator or a host chose.
const (
	maxTitleRunes = 200
	maxLabelRunes = 100
	maxTextRunes  = 300
)

// Line is one line of a chat message: an optional label (rendered bold),
// the text, and an optional link (on the label, else the text).
type Line struct {
	Label, Text, URL string
}

// Message is an event as every chat preset renders it: Compose decides
// what a message says, a renderer only how its service shows it. Every
// string is already defused (ChatText) and clipped; URL and Line.URL are
// set only when SafeLink holds. Omitted is how many entries the event
// itself left out (a capped alert batch), for the renderer's MoreNote.
type Message struct {
	Title   string
	Level   Level
	Lines   []Line
	Omitted int
	URL     string
	Org     string
}

// Compose builds the service-neutral message for ev.
func Compose(ev Event) Message {
	m := Message{Org: ChatText(ev.Org.Name), Level: LevelInfo}
	if SafeLink(ev.URL) {
		m.URL = ev.URL
	}
	name := func(r *NamedRef) string {
		if r == nil {
			return ""
		}
		return r.Name
	}
	add := func(label, text, link string) {
		if text == "" {
			return
		}
		l := Line{Label: Clip(ChatText(label), maxLabelRunes), Text: Clip(ChatText(text), maxTextRunes)}
		if SafeLink(link) {
			l.URL = link
		}
		m.Lines = append(m.Lines, l)
	}
	switch ev.Kind {
	case EventRunFinished:
		composeRun(&m, ev, name, add)
	case EventScheduleSkipped, EventScheduleFailed:
		verb, level := "skipped", LevelWarn
		if ev.Kind == EventScheduleFailed {
			verb, level = "could not start", LevelCrit
		}
		m.Title, m.Level = name(ev.Schedule)+": "+verb, level
		add("Template", name(ev.Template), "")
		add("Follow-up of", name(ev.FollowUpOf), "")
		if d := ev.Decision; d != nil {
			add("Reason", d.Reason, "")
			add("Outcome", d.Outcome, "")
			add("Due", formatTime(d.DueAt), "")
		}
	case EventDigest:
		m.Title = "Weekly schedule digest for " + ev.Org.Name
		if d := ev.Digest; d != nil {
			composeDigest(*d, add)
		}
	case EventAlertBatch:
		composeAlerts(&m, ev, add)
	case EventTest:
		m.Title = "Test notification from " + ev.Org.Name
		add("", "This is a test message from a Pulsix notification channel. It worked.", "")
	default:
		m.Title = "Notification from " + ev.Org.Name
	}
	m.Title = Clip(ChatText(m.Title), maxTitleRunes)
	return m
}

func composeRun(m *Message, ev Event, name func(*NamedRef) string, add func(label, text, link string)) {
	result := "succeeded"
	if ev.Run != nil {
		result = ev.Run.Result
	}
	m.Title, m.Level = name(ev.Schedule)+": "+result, LevelCrit
	if result == "succeeded" {
		m.Level = LevelOK
		if ev.Run != nil && ev.Run.HostsChanged > 0 {
			m.Title += " with changes"
		}
	}
	add("Template", name(ev.Template), "")
	add("Follow-up of", name(ev.FollowUpOf), "")
	r := ev.Run
	if r == nil {
		return
	}
	hosts := fmt.Sprintf("%d (ok %d, changed %d, failed %d)", r.Hosts, r.HostsOK, r.HostsChanged, r.HostsFailed)
	if r.HostsNoResult > 0 {
		hosts += fmt.Sprintf(", no result %d", r.HostsNoResult)
	}
	if r.HostsExcluded > 0 {
		hosts += fmt.Sprintf(" — %d excluded for maintenance", r.HostsExcluded)
	}
	add("Hosts", hosts, "")
	if r.StartedAt != nil {
		add("Started", formatTime(*r.StartedAt), "")
	}
	add("Duration", (time.Duration(r.DurationSeconds) * time.Second).String(), "")
}

func composeDigest(d DigestSummary, add func(label, text, link string)) {
	loc := digestLocation(&d)
	add("Period", fmt.Sprintf("%s to %s (%s)", d.From.In(loc).Format("2 Jan"), d.To.In(loc).Format("2 Jan 2006"), d.Timezone), "")
	if len(d.Schedules) == 0 {
		add("", "No schedules.", "")
	}
	for _, s := range d.Schedules {
		add(s.Name, fmt.Sprintf("%s, %s, %d skipped or missed",
			count(s.Runs, "run", "runs"), count(s.Failures, "failure", "failures"), s.Skipped), "")
	}
	if omitted := d.ScheduleCountTotal - len(d.Schedules); d.Truncated && omitted > 0 {
		add("", "+"+count(omitted, "schedule", "schedules")+" without activity", "")
	}
	for _, r := range d.Slowest {
		add(r.Schedule, fmt.Sprintf("slowest run, %s: %s",
			r.StartedAt.In(loc).Format("Mon 2 Jan 15:04 MST"), time.Duration(r.DurationSeconds)*time.Second), r.URL)
	}
}

// composeAlerts lists a batch's alerts, one line each. Its level is the
// highest severity among the items in the alert list's own order
// (rules.StateRank: crit > warn > unknown); an item whose alert closed
// counts as ok, so a batch of closings is not painted red (decision 4).
func composeAlerts(m *Message, ev Event, add func(label, text, link string)) {
	a := ev.Alerts
	if a == nil {
		m.Title = "Alerts for " + ev.Org.Name
		return
	}
	m.Title = count(a.Count, "alert", "alerts") + " for " + ev.Org.Name
	if omitted := a.Count - len(a.Items); omitted > 0 {
		m.Omitted = omitted
	}
	worst := rules.State("")
	for _, it := range a.Items {
		state := rules.State(it.Severity)
		if it.Event == "closed" {
			state = rules.StateOK
		}
		if worst == "" || rules.StateRank(state) > rules.StateRank(worst) {
			worst = state
		}
		parts := []string{it.Severity, alertEventText(it.Event)}
		what := alertSubjectText(it)
		if it.Detail != "" {
			what += ": " + agentText(it.Detail)
		}
		parts = append(parts, what)
		add(it.HostName, joinNonEmpty(parts, " · "), it.URL)
	}
	switch worst {
	case rules.StateCrit:
		m.Level = LevelCrit
	case rules.StateWarn:
		m.Level = LevelWarn
	case rules.StateUnknown:
		m.Level = LevelUnknown
	case "":
		m.Level = LevelInfo
	default:
		m.Level = LevelOK
	}
}

// alertEventText is an alert event as a chat line names it: the GUI's
// "worsened" for a raised severity, the rest as written.
func alertEventText(e string) string {
	if e == "severity_raised" {
		return "worsened"
	}
	return strings.ReplaceAll(e, "_", " ")
}

func joinNonEmpty(parts []string, sep string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// count is n with the singular or plural noun.
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
