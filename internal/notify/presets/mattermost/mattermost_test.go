package mattermost_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/notify/notifytest"
	"github.com/noixdev/pulsix/internal/notify/presets/mattermost"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json")

type attachment struct {
	Fallback  string `json:"fallback"`
	Color     string `json:"color"`
	Title     string `json:"title"`
	TitleLink string `json:"title_link"`
	Text      string `json:"text"`
	Footer    string `json:"footer"`
}

func render(t *testing.T, ev notify.Event) ([]byte, attachment) {
	t.Helper()
	body, err := notifytest.Body(mattermost.New().Render(ev, notify.Target{Settings: notify.PresetSettings{}}))
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Attachments []attachment `json:"attachments"`
	}
	if err := json.Unmarshal(body, &p); err != nil || len(p.Attachments) != 1 {
		t.Fatalf("body %s: %v", body, err)
	}
	return body, p.Attachments[0]
}

func TestMattermostGolden(t *testing.T) {
	for _, c := range notifytest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			body, _ := render(t, c.Event)
			notifytest.Golden(t, filepath.Join("testdata", c.Name+".golden.json"), body, *update)
		})
	}
}

// TestMattermostAttachment: Slack's attachment format as Mattermost reads
// it — colour, a title linking to Pulsix, Markdown text, a footer.
func TestMattermostAttachment(t *testing.T) {
	_, a := render(t, notifytest.AlertsMixed())
	if a.Color != "#F87171" || a.Title != "5 alerts for Acme" || a.Fallback != a.Title || a.TitleLink != notifytest.Base+"/alerts" || a.Footer != "Pulsix · Acme" {
		t.Fatalf("attachment = %+v", a)
	}
	first, _, _ := strings.Cut(a.Text, "\n")
	if want := "**[shop\\-1](" + notifytest.Base + "/alerts?host=" + notifytest.ID(20).String() + "):** crit · opened · shop/home http\\.assertion: status == 200 — saw 503"; first != want {
		t.Fatalf("first line\n%s\nwant\n%s", first, want)
	}
	if !strings.HasSuffix(a.Text, "\n… and 1 more") {
		t.Fatalf("no note: %q", a.Text)
	}
	if _, closed := render(t, notifytest.AlertsClosed()); closed.Color != "#34D399" {
		t.Fatalf("all closed colour = %s", closed.Color)
	}
}

// TestMattermostEscapesMarkdown: an operator's or host's text cannot
// format, link, mention or reference a channel.
func TestMattermostEscapesMarkdown(t *testing.T) {
	body, a := render(t, notifytest.Hostile())
	for _, raw := range []string{"*bold*", "_it_", "`code`", "](", " ~chan", " #h", "<", ">", "@everyone", "@all", "javascript:", "\u202E"} {
		if strings.Contains(a.Text, raw) {
			t.Errorf("text holds %q: %s", raw, a.Text)
		}
	}
	for _, want := range []string{`\*bold\*`, `\_it\_`, "\\`code\\`", `\~chan`, `\#h`, "&amp;amp;"} {
		if !strings.Contains(a.Text, want) {
			t.Errorf("text lacks %q: %s", want, a.Text)
		}
	}
	if strings.Contains(string(body), "@here") || strings.Contains(a.Title, "<") {
		t.Errorf("title %q", a.Title)
	}
}

func TestMattermostBoundsLargeBatch(t *testing.T) {
	body, a := render(t, notifytest.LargeBatch(50, 7))
	if len(body) > 60<<10 {
		t.Fatalf("body is %d bytes", len(body))
	}
	if n := utf8.RuneCountInString(a.Text); n > 12000 {
		t.Fatalf("text %d characters", n)
	}
	lines := strings.Split(a.Text, "\n")
	shown := len(lines) - 1
	if shown >= 50 {
		t.Fatalf("all %d shown", shown)
	}
	if want := fmt.Sprintf("… and %d more", 50-shown+7); lines[len(lines)-1] != want {
		t.Fatalf("note %q, want %q", lines[len(lines)-1], want)
	}
}

func TestMattermostSettings(t *testing.T) {
	if _, err := mattermost.New().CheckSettings(notify.PresetSettings{ChatID: "1"}); err == nil {
		t.Fatal("a chat id accepted")
	}
	if err := mattermost.New().CheckURL(nil); err != nil {
		t.Fatal(err)
	}
}

// TestMattermostBoundsWideMessages (review I1): the widest digest and alert
// batch stay well under the sender's 64 KiB body cap, and say what they
// left out.
func TestMattermostBoundsWideMessages(t *testing.T) {
	for name, ev := range map[string]notify.Event{"digest": notifytest.WideDigest(), "alerts": notifytest.WideBatch()} {
		body, err := notifytest.Body(mattermost.New().Render(ev, notify.Target{Settings: notify.PresetSettings{ChatID: "-1"}}))
		if err != nil {
			t.Fatal(err)
		}
		if len(body) > 60<<10 {
			t.Errorf("%s: body is %d bytes", name, len(body))
		}
		if !strings.Contains(string(body), "… and ") {
			t.Errorf("%s: no note for what was left out", name)
		}
		if !json.Valid(body) {
			t.Errorf("%s: invalid JSON", name)
		}
	}
}
