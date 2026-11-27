// Package notifytest holds the events the chat preset renderers are tested
// against (backlog 4.7) and the golden-file helper their tests share, so
// every renderer — and each one 4.8 adds — is pinned on the same inputs.
package notifytest

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/notify"
)

// ID is a fixed id, so rendered bodies are stable.
func ID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-%012d", n))
}

// At is the instant every fixture happens at.
var At = time.Date(2026, 11, 27, 8, 30, 0, 0, time.UTC)

// Base is the organization's link prefix in every fixture.
const Base = "https://pulsix.example/o/acme"

func org() notify.OrgRef { return notify.OrgRef{ID: ID(1), Slug: "acme", Name: "Acme"} }

// Case is one named fixture.
type Case struct {
	Name  string
	Event notify.Event
}

// Cases is every fixture, in a fixed order, for golden-file loops.
func Cases() []Case {
	return []Case{
		{"run_failed", RunFailed()},
		{"run_succeeded", RunSucceeded()},
		{"schedule_skipped", ScheduleSkipped()},
		{"digest", Digest()},
		{"alerts_mixed", AlertsMixed()},
		{"alerts_closed", AlertsClosed()},
		{"hostile", Hostile()},
		{"test", Test()},
	}
}

// RunFailed is a finished run that failed on one host, a follow-up of
// another template.
func RunFailed() notify.Event {
	started := At.Add(-4*time.Minute - 12*time.Second)
	return notify.Event{
		Kind: notify.EventRunFinished, At: At, Org: org(),
		Categories: []notify.Category{notify.CategoryFailure, notify.CategoryChange},
		Schedule:   &notify.NamedRef{ID: ID(2), Name: "Nightly OS patching"},
		Template:   &notify.NamedRef{ID: ID(3), Name: "patch-all"},
		FollowUpOf: &notify.NamedRef{ID: ID(4), Name: "backup-all"},
		Run: &notify.RunSummary{
			ID: ID(5), Result: "failed", Hosts: 12, HostsOK: 10, HostsChanged: 9, HostsFailed: 1, HostsNoResult: 1,
			HostsExcluded: 2, StartedAt: &started, FinishedAt: ptr(At), DurationSeconds: 252,
		},
		URL: Base + "/runs/" + ID(5).String(),
	}
}

// RunSucceeded is a finished run that changed hosts.
func RunSucceeded() notify.Event {
	ev := RunFailed()
	ev.FollowUpOf = nil
	ev.Categories = []notify.Category{notify.CategorySuccess, notify.CategoryChange}
	ev.Run = &notify.RunSummary{ID: ID(5), Result: "succeeded", Hosts: 3, HostsOK: 3, HostsChanged: 1, DurationSeconds: 61}
	return ev
}

// ScheduleSkipped is an occurrence a maintenance window skipped.
func ScheduleSkipped() notify.Event {
	return notify.Event{
		Kind: notify.EventScheduleSkipped, At: At, Org: org(),
		Categories: []notify.Category{notify.CategorySkipped},
		Schedule:   &notify.NamedRef{ID: ID(2), Name: "Nightly OS patching"},
		Template:   &notify.NamedRef{ID: ID(3), Name: "patch-all"},
		Decision:   &notify.DecisionSummary{Outcome: "skipped_maintenance", Reason: "every targeted host is in a maintenance window", DueAt: At},
		URL:        Base + "/schedules/" + ID(2).String(),
	}
}

// Digest is a weekly digest whose schedule list was capped.
func Digest() notify.Event {
	from := At.AddDate(0, 0, -7)
	return notify.Event{
		Kind: notify.EventDigest, At: At, Org: org(),
		Digest: &notify.DigestSummary{
			From: from, To: At, Timezone: "Europe/Berlin",
			Schedules: []notify.DigestSchedule{
				{ID: ID(2), Name: "Nightly OS patching", Runs: 7, Failures: 1, Skipped: 0},
				{ID: ID(6), Name: "Weekly backup", Runs: 1, Failures: 0, Skipped: 1},
			},
			Slowest:   []notify.DigestRun{{Schedule: "Nightly OS patching", RunID: ID(5), StartedAt: from.Add(26 * time.Hour), DurationSeconds: 252, URL: Base + "/runs/" + ID(5).String()}},
			Truncated: true, ScheduleCountTotal: 5,
		},
		URL: Base + "/schedules",
	}
}

// AlertsMixed is a capped batch (5 alerts, 4 carried) of mixed severity:
// a critical opening with a detail, a warning that worsened, a critical
// alert that closed, and a client's absent warning.
func AlertsMixed() notify.Event {
	seen := At.Add(-20 * time.Minute)
	return notify.Event{
		Kind: notify.EventAlertBatch, At: At, Org: org(),
		Alerts: &notify.AlertBatch{Count: 5, Items: []notify.AlertItem{
			{AlertID: ID(10), HostID: ID(20), HostName: "shop-1", CheckKey: "shop", Item: "home", Metric: "http.assertion",
				Severity: "crit", Event: "opened", At: At, Detail: "status == 200 — saw 503", URL: Base + "/alerts?host=" + ID(20).String()},
			{AlertID: ID(11), HostID: ID(21), HostName: "web-2", CheckKey: "cpu", Metric: "used_percent",
				Severity: "warn", Event: "severity_raised", At: At, URL: Base + "/alerts?host=" + ID(21).String()},
			{AlertID: ID(12), HostID: ID(22), HostName: "db-1", CheckKey: "disk", Item: "/var", Metric: "used_percent",
				Severity: "crit", Event: "closed", At: At, URL: Base + "/alerts?host=" + ID(22).String()},
			{AlertID: ID(13), HostID: ID(23), HostName: "edge-3", Severity: "warn", Event: "opened", At: At, NotSeenSince: &seen,
				URL: Base + "/alerts?host=" + ID(23).String()},
		}},
		URL: Base + "/alerts",
	}
}

// AlertsClosed is a batch of alerts that all closed.
func AlertsClosed() notify.Event {
	ev := AlertsMixed()
	items := ev.Alerts.Items[:3]
	for i := range items {
		items[i].Event = "closed"
		items[i].Detail = ""
	}
	ev.Alerts = &notify.AlertBatch{Count: 3, Items: items}
	return ev
}

// Hostile carries markup, mentions, a line break and a bidi override in
// every string an operator or a monitored host chooses.
func Hostile() notify.Event {
	evil := "<!channel> @everyone *bold* _it_ `code` </b><a href=\"x\">&amp; [link](https://evil.example) ~chan #h \u202Egnp.exe\nforged"
	return notify.Event{
		Kind: notify.EventAlertBatch, At: At, Org: notify.OrgRef{ID: ID(1), Slug: "acme", Name: "Acme <!here> & @all"},
		Alerts: &notify.AlertBatch{Count: 1, Items: []notify.AlertItem{
			{AlertID: ID(10), HostID: ID(20), HostName: evil, CheckKey: "shop|x", Item: evil, Metric: "m>1",
				Severity: "crit", Event: "opened", At: At, Detail: evil, URL: "javascript:alert(1)"},
		}},
		URL: Base + "/alerts",
	}
}

// LargeBatch is n alerts with long details, of which the batch already
// left out extra.
func LargeBatch(n, extra int) notify.Event {
	items := make([]notify.AlertItem, n)
	for i := range items {
		items[i] = notify.AlertItem{
			AlertID: ID(100 + i), HostID: ID(200 + i), HostName: fmt.Sprintf("host-%03d.example.com", i), CheckKey: "http",
			Item: "home", Metric: "http.assertion", Severity: "crit", Event: "opened", At: At,
			Detail: strings.Repeat("ä<&>", 200), URL: Base + "/alerts?host=" + ID(200+i).String(),
		}
	}
	return notify.Event{Kind: notify.EventAlertBatch, At: At, Org: org(), Alerts: &notify.AlertBatch{Count: n + extra, Items: items}, URL: Base + "/alerts"}
}

// Test is the "Send test" message.
func Test() notify.Event {
	return notify.Event{Kind: notify.EventTest, At: At, Org: org(), URL: Base + "/settings/notifications"}
}

func ptr[T any](v T) *T { return &v }

// WideDigest is the widest digest a renderer must fit (backlog 4.7 review
// I1): the full MaxDigestSchedules entries, each named with 100 Japanese
// characters (three bytes each), plus five slowest runs.
func WideDigest() notify.Event {
	ev := Digest()
	name := strings.Repeat("夜間更新", 25)
	d := ev.Digest
	d.Schedules = make([]notify.DigestSchedule, notify.MaxDigestSchedules)
	for i := range d.Schedules {
		d.Schedules[i] = notify.DigestSchedule{ID: ID(1000 + i), Name: name, Runs: 7, Failures: 1, Skipped: 1}
	}
	d.Truncated, d.ScheduleCountTotal = true, notify.MaxDigestSchedules+40
	for i := 0; i < 5; i++ {
		d.Slowest = append(d.Slowest, notify.DigestRun{Schedule: name, RunID: ID(2000 + i), StartedAt: At, DurationSeconds: 999, URL: LongBase + "/runs/" + ID(2000+i).String()})
	}
	return ev
}

// LongBase is a public URL prefix near SafeLink's bound, so every linked
// line carries a long link.
var LongBase = "https://pulsix.example/" + strings.Repeat("a", 1800) + "/o/acme"

// WideBatch is the widest alert batch a renderer must fit (review I1): the
// full 50 items with 100-character host names and 300-character details
// of four-byte characters, each linking through LongBase.
func WideBatch() notify.Event {
	items := make([]notify.AlertItem, 50)
	for i := range items {
		items[i] = notify.AlertItem{
			AlertID: ID(300 + i), HostID: ID(400 + i), HostName: strings.Repeat("𝔥", 100), CheckKey: "http",
			Item: "home", Metric: "http.assertion", Severity: "crit", Event: "opened", At: At,
			Detail: strings.Repeat("𝔡", 400), URL: LongBase + "/alerts?host=" + ID(400+i).String(),
		}
	}
	return notify.Event{Kind: notify.EventAlertBatch, At: At, Org: org(), Alerts: &notify.AlertBatch{Count: 50, Items: items}, URL: LongBase + "/alerts"}
}
