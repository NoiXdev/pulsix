package notify_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/notify/notifytest"
)

func TestComposeLevel(t *testing.T) {
	warnAndClosedCrit := notifytest.AlertsMixed()
	warnAndClosedCrit.Alerts.Items = warnAndClosedCrit.Alerts.Items[1:3] // warn worsened + crit closed
	unknownOnly := notifytest.AlertsMixed()
	unknownOnly.Alerts.Items = unknownOnly.Alerts.Items[:1]
	unknownOnly.Alerts.Items[0].Severity = "unknown"
	cancelled := notifytest.RunFailed()
	cancelled.Run.Result = "cancelled"
	couldNotStart := notifytest.ScheduleSkipped()
	couldNotStart.Kind = notify.EventScheduleFailed
	empty := notifytest.AlertsMixed()
	empty.Alerts.Items = nil
	cases := []struct {
		name string
		ev   notify.Event
		want notify.Level
	}{
		{"mixed batch takes the highest", notifytest.AlertsMixed(), notify.LevelCrit},
		{"a closed crit does not outrank an open warn", warnAndClosedCrit, notify.LevelWarn},
		{"all closed is ok", notifytest.AlertsClosed(), notify.LevelOK},
		{"unknown only", unknownOnly, notify.LevelUnknown},
		{"empty batch", empty, notify.LevelInfo},
		{"failed run", notifytest.RunFailed(), notify.LevelCrit},
		{"cancelled run", cancelled, notify.LevelCrit},
		{"succeeded run", notifytest.RunSucceeded(), notify.LevelOK},
		{"skipped schedule", notifytest.ScheduleSkipped(), notify.LevelWarn},
		{"schedule could not start", couldNotStart, notify.LevelCrit},
		{"digest", notifytest.Digest(), notify.LevelInfo},
		{"test", notifytest.Test(), notify.LevelInfo},
	}
	for _, c := range cases {
		if got := notify.Compose(c.ev).Level; got != c.want {
			t.Errorf("%s: level %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLevelColor(t *testing.T) {
	want := map[notify.Level]string{
		notify.LevelCrit: "#F87171", notify.LevelWarn: "#FBBF24", notify.LevelUnknown: "#6B7280",
		notify.LevelOK: "#34D399", notify.LevelInfo: "#00E5FF",
	}
	for l, c := range want {
		if got := l.Color(); got != c {
			t.Errorf("%s: %s, want %s", l, got, c)
		}
	}
}

func TestComposeAlertBatch(t *testing.T) {
	m := notify.Compose(notifytest.AlertsMixed())
	if m.Title != "5 alerts for Acme" || m.Omitted != 1 || len(m.Lines) != 4 || m.URL != notifytest.Base+"/alerts" || m.Org != "Acme" {
		t.Fatalf("message = %+v", m)
	}
	first := m.Lines[0]
	if first.Label != "shop-1" || first.Text != "crit · opened · shop/home http.assertion: status == 200 — saw 503" || !strings.HasPrefix(first.URL, notifytest.Base) {
		t.Fatalf("first line = %+v", first)
	}
	if got := m.Lines[1].Text; got != "warn · worsened · cpu used_percent" {
		t.Fatalf("worsened line = %q", got)
	}
	if got := m.Lines[3].Text; got != "warn · opened · not seen since 2026-11-27T08:10:00Z" {
		t.Fatalf("absent line = %q", got)
	}
	one := notifytest.AlertsMixed()
	one.Alerts = &notify.AlertBatch{Count: 1, Items: one.Alerts.Items[:1]}
	if got := notify.Compose(one).Title; got != "1 alert for Acme" {
		t.Fatalf("singular title = %q", got)
	}
}

func TestComposeRunAndSchedule(t *testing.T) {
	m := notify.Compose(notifytest.RunFailed())
	want := []notify.Line{
		{Label: "Template", Text: "patch-all"},
		{Label: "Follow-up of", Text: "backup-all"},
		{Label: "Hosts", Text: "12 (ok 10, changed 9, failed 1), no result 1 — 2 excluded for maintenance"},
		{Label: "Started", Text: "2026-11-27T08:25:48Z"},
		{Label: "Duration", Text: "4m12s"},
	}
	if m.Title != "Nightly OS patching: failed" || len(m.Lines) != len(want) {
		t.Fatalf("message = %+v", m)
	}
	for i := range want {
		if m.Lines[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, m.Lines[i], want[i])
		}
	}
	if got := notify.Compose(notifytest.RunSucceeded()).Title; got != "Nightly OS patching: succeeded with changes" {
		t.Fatalf("succeeded title = %q", got)
	}
	s := notify.Compose(notifytest.ScheduleSkipped())
	if s.Title != "Nightly OS patching: skipped" || s.Lines[1] != (notify.Line{Label: "Reason", Text: "every targeted host is in a maintenance window"}) {
		t.Fatalf("skipped = %+v", s)
	}
}

func TestComposeDigestSingulars(t *testing.T) {
	m := notify.Compose(notifytest.Digest())
	if m.Title != "Weekly schedule digest for Acme" {
		t.Fatalf("title = %q", m.Title)
	}
	texts := make([]string, len(m.Lines))
	for i, l := range m.Lines {
		texts[i] = l.Label + "|" + l.Text
	}
	got := strings.Join(texts, "\n")
	for _, w := range []string{
		"Period|20 Nov to 27 Nov 2026 (Europe/Berlin)",
		"Nightly OS patching|7 runs, 1 failure, 0 skipped or missed",
		"Weekly backup|1 run, 0 failures, 1 skipped or missed",
		"|+3 schedules without activity",
		"Nightly OS patching|slowest run, Sat 21 Nov 11:30 CET: 4m12s",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("digest lines lack %q:\n%s", w, got)
		}
	}
}

func TestComposeTest(t *testing.T) {
	m := notify.Compose(notifytest.Test())
	if m.Title != "Test notification from Acme" || len(m.Lines) != 1 || !strings.Contains(m.Lines[0].Text, "It worked.") {
		t.Fatalf("test = %+v", m)
	}
}

// TestComposeDefusesAndClips: whatever an operator or host chose reaches a
// renderer without markup delimiters, mentions or line breaks, clipped,
// and an unsafe link is dropped.
func TestComposeDefusesAndClips(t *testing.T) {
	m := notify.Compose(notifytest.Hostile())
	all := m.Title + m.Org
	for _, l := range m.Lines {
		all += l.Label + l.Text
		if l.URL != "" {
			t.Errorf("unsafe link kept: %q", l.URL)
		}
		if utf8.RuneCountInString(l.Label) > 100 || utf8.RuneCountInString(l.Text) > 300 {
			t.Errorf("line not clipped: %d/%d", utf8.RuneCountInString(l.Label), utf8.RuneCountInString(l.Text))
		}
	}
	for _, bad := range []string{"<", ">", "@", "[", "]", "\n", "\u202E"} {
		if strings.Contains(all, bad) {
			t.Errorf("composed text holds %q: %q", bad, all)
		}
	}
	long := notifytest.LargeBatch(1, 0)
	if got := []rune(notify.Compose(long).Lines[0].Text); len(got) != 300 || got[299] != '…' {
		t.Fatalf("clipped text = %d runes", len(got))
	}
}
