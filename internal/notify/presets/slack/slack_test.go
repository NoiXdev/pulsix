package slack_test

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
	"github.com/noixdev/pulsix/internal/notify/presets/slack"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json")

type text struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type block struct {
	Type     string `json:"type"`
	Text     *text  `json:"text"`
	Elements []struct {
		Type string `json:"type"`
		Text any    `json:"text"`
		URL  string `json:"url"`
	} `json:"elements"`
}

type payload struct {
	Text        string `json:"text"`
	Attachments []struct {
		Color  string  `json:"color"`
		Blocks []block `json:"blocks"`
	} `json:"attachments"`
}

func render(t *testing.T, ev notify.Event) ([]byte, payload) {
	t.Helper()
	body, err := slack.New().Render(ev, notify.PresetSettings{})
	if err != nil {
		t.Fatal(err)
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, body)
	}
	if len(p.Attachments) != 1 {
		t.Fatalf("attachments = %d", len(p.Attachments))
	}
	return body, p
}

func TestSlackGolden(t *testing.T) {
	for _, c := range notifytest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			body, _ := render(t, c.Event)
			notifytest.Golden(t, filepath.Join("testdata", c.Name+".golden.json"), body, *update)
		})
	}
}

func TestSlackColourAndButton(t *testing.T) {
	_, p := render(t, notifytest.AlertsMixed())
	a := p.Attachments[0]
	if a.Color != "#F87171" {
		t.Fatalf("colour = %s", a.Color)
	}
	last := a.Blocks[len(a.Blocks)-1]
	if last.Type != "actions" || last.Elements[0].Type != "button" || last.Elements[0].URL != notifytest.Base+"/alerts" {
		t.Fatalf("last block = %+v", last)
	}
	// The title is the message text, shown once (review M1): no header
	// block repeats it inside the attachment.
	if p.Text != "5 alerts for Acme" || a.Blocks[0].Type != "section" {
		t.Fatalf("first block = %+v, text %q", a.Blocks[0], p.Text)
	}
	for _, b := range a.Blocks {
		if b.Type == "header" {
			t.Fatal("a header block repeats the title")
		}
	}
	if _, p := render(t, notifytest.AlertsClosed()); p.Attachments[0].Color != "#34D399" {
		t.Fatalf("all closed colour = %s", p.Attachments[0].Color)
	}
}

func TestSlackNoButtonWithoutLink(t *testing.T) {
	ev := notifytest.Test()
	ev.URL = ""
	_, p := render(t, ev)
	for _, b := range p.Attachments[0].Blocks {
		if b.Type == "actions" {
			t.Fatal("a button without a link")
		}
	}
}

// TestSlackEscapes: nothing an operator or host chose can mention a
// channel, open a link or close Slack's own markup.
func TestSlackEscapes(t *testing.T) {
	body, p := render(t, notifytest.Hostile())
	for _, b := range p.Attachments[0].Blocks {
		if b.Text != nil && b.Text.Type == "mrkdwn" {
			if strings.ContainsAny(strings.ReplaceAll(b.Text.Text, "<"+notifytest.Base, ""), "<>") {
				t.Errorf("unescaped angle bracket in %q", b.Text.Text)
			}
			if !strings.Contains(b.Text.Text, "&amp;amp;") {
				t.Errorf("ampersand not escaped in %q", b.Text.Text)
			}
		}
	}
	for _, bad := range []string{"<!channel>", "<!here>", "@everyone", "@all", "javascript:", "\u202E"} {
		if strings.Contains(string(body), bad) {
			t.Errorf("body holds %q", bad)
		}
	}
	if strings.ContainsAny(p.Text, "<>") {
		t.Errorf("fallback text = %q", p.Text)
	}
}

func TestSlackBoundsLargeBatch(t *testing.T) {
	body, p := render(t, notifytest.LargeBatch(50, 7))
	if len(body) > 60<<10 {
		t.Fatalf("body is %d bytes", len(body))
	}
	blocks := p.Attachments[0].Blocks
	if len(blocks) > 50 {
		t.Fatalf("%d blocks", len(blocks))
	}
	sections, shown, note := 0, 0, ""
	for _, b := range blocks {
		switch b.Type {
		case "header":
			if n := utf8.RuneCountInString(b.Text.Text); n > 150 {
				t.Errorf("header %d characters", n)
			}
		case "section":
			sections++
			if n := utf8.RuneCountInString(b.Text.Text); n > 3000 {
				t.Errorf("section %d characters", n)
			}
			shown += strings.Count(b.Text.Text, "\n") + 1
		case "context":
			for _, e := range b.Elements {
				if s, _ := e.Text.(string); strings.HasPrefix(s, "…") {
					note = s
				}
			}
		}
	}
	if sections > 10 || shown >= 50 {
		t.Fatalf("%d sections showing %d alerts", sections, shown)
	}
	if want := fmt.Sprintf("… and %d more", 50-shown+7); note != want {
		t.Fatalf("note %q, want %q", note, want)
	}
}

func TestSlackSettings(t *testing.T) {
	if _, err := slack.New().CheckSettings(notify.PresetSettings{ChatID: "1"}); err == nil {
		t.Fatal("a chat id accepted")
	}
	if err := slack.New().CheckURL(nil); err != nil {
		t.Fatal(err)
	}
}

// TestSlackBoundsWideMessages (review I1): the widest digest and alert
// batch stay well under the sender's 64 KiB body cap, and say what they
// left out.
func TestSlackBoundsWideMessages(t *testing.T) {
	for name, ev := range map[string]notify.Event{"digest": notifytest.WideDigest(), "alerts": notifytest.WideBatch()} {
		body, err := slack.New().Render(ev, notify.PresetSettings{ChatID: "-1"})
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

// TestSlackDefusesFormatting (review M3): bold, italic, strike and code
// markers an operator or host chose reach Slack as look-alikes, while one
// inside a word (a metric's underscore) stays as written.
func TestSlackDefusesFormatting(t *testing.T) {
	ev := notifytest.AlertsMixed()
	ev.Alerts.Items[0].HostName = "*web* _a_ ~b~ `c` used_percent 2*3"
	_, p := render(t, ev)
	first, _, _ := strings.Cut(p.Attachments[0].Blocks[0].Text.Text, "\n")
	label := first[strings.Index(first, "|")+1 : strings.Index(first, ">:*")]
	if want := "\u2217web\u2217 \u02CDa\u02CD \u223Cb\u223C \u02CBc\u02CB used_percent 2*3"; label != want {
		t.Fatalf("label %q, want %q", label, want)
	}
	if !strings.HasPrefix(first, "*<") || !strings.Contains(first, ">:* ") {
		t.Fatalf("own bold markup lost: %q", first)
	}
}

// TestSlackLinkTargetsKeepFormattingCharacters (re-review N1): a link
// target gets Slack's entity escaping only, never the look-alikes meant
// for text, or the link would point elsewhere.
func TestSlackLinkTargetsKeepFormattingCharacters(t *testing.T) {
	link := "https://pulsix.example/~ops/_pulsix/o/acme/alerts?host=1&a=b"
	ev := notifytest.AlertsMixed()
	ev.Alerts.Items[0].URL = link
	_, p := render(t, ev)
	first := p.Attachments[0].Blocks[0].Text.Text
	if want := "*<https://pulsix.example/~ops/_pulsix/o/acme/alerts?host=1&amp;a=b|shop-1>:*"; !strings.HasPrefix(first, want) {
		t.Fatalf("line %q, want prefix %q", first, want)
	}
	ev = notifytest.Test()
	ev.URL = link
	_, p = render(t, ev)
	blocks := p.Attachments[0].Blocks
	if got := blocks[len(blocks)-1].Elements[0].URL; got != link {
		t.Fatalf("button url %q", got)
	}
}
