package telegram_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/notify/notifytest"
	"github.com/noixdev/pulsix/internal/notify/presets/telegram"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json")

type payload struct {
	ChatID             string `json:"chat_id"`
	Text               string `json:"text"`
	ParseMode          string `json:"parse_mode"`
	LinkPreviewOptions struct {
		IsDisabled bool `json:"is_disabled"`
	} `json:"link_preview_options"`
}

func render(t *testing.T, ev notify.Event) ([]byte, payload) {
	t.Helper()
	body, err := telegram.New().Render(ev, notify.PresetSettings{ChatID: "-1001234567890"})
	if err != nil {
		t.Fatal(err)
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	return body, p
}

func TestTelegramGolden(t *testing.T) {
	for _, c := range notifytest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			body, _ := render(t, c.Event)
			notifytest.Golden(t, filepath.Join("testdata", c.Name+".golden.json"), body, *update)
		})
	}
}

func TestTelegramMessage(t *testing.T) {
	_, p := render(t, notifytest.AlertsMixed())
	if p.ChatID != "-1001234567890" || p.ParseMode != "HTML" || !p.LinkPreviewOptions.IsDisabled {
		t.Fatalf("payload = %+v", p)
	}
	if !strings.HasPrefix(p.Text, "\U0001F534 <b>5 alerts for Acme</b>\n\n") {
		t.Fatalf("head = %q", p.Text)
	}
	if !strings.HasSuffix(p.Text, "\n… and 1 more\n\n<a href=\""+notifytest.Base+"/alerts\">Open in Pulsix</a>") {
		t.Fatalf("tail = %q", p.Text)
	}
	if _, closed := render(t, notifytest.AlertsClosed()); !strings.HasPrefix(closed.Text, "\U0001F7E2 ") {
		t.Fatalf("all closed = %q", closed.Text)
	}
	if _, test := render(t, notifytest.Test()); !strings.HasPrefix(test.Text, "<b>Test notification") {
		t.Fatalf("info has a mark: %q", test.Text)
	}
}

// TestTelegramEscapesHTML: only Pulsix's own tags reach Telegram.
func TestTelegramEscapesHTML(t *testing.T) {
	body, p := render(t, notifytest.Hostile())
	stripped := p.Text
	for _, own := range []string{"<b>", "</b>", "<a href=\"" + notifytest.Base + "/alerts\">", "</a>"} {
		stripped = strings.ReplaceAll(stripped, own, "")
	}
	if strings.ContainsAny(stripped, "<>\"") {
		t.Fatalf("foreign markup survives: %q", stripped)
	}
	for _, bad := range []string{"@everyone", "@all", "javascript:", "\u202E"} {
		if strings.Contains(string(body), bad) {
			t.Errorf("body holds %q", bad)
		}
	}
	if !strings.Contains(p.Text, "&amp;amp;") || !strings.Contains(p.Text, "&quot;x&quot;") {
		t.Errorf("entities not escaped: %q", p.Text)
	}
}

func TestTelegramBoundsLargeBatch(t *testing.T) {
	_, p := render(t, notifytest.LargeBatch(50, 7))
	if n := len(utf16.Encode([]rune(p.Text))); n > 4096 {
		t.Fatalf("text is %d UTF-16 units", n)
	}
	body := strings.Split(strings.Split(p.Text, "\n\n")[1], "\n")
	shown := len(body) - 1
	if want := fmt.Sprintf("… and %d more", 50-shown+7); body[len(body)-1] != want || shown >= 50 || shown == 0 {
		t.Fatalf("note %q with %d shown, want %q", body[len(body)-1], shown, want)
	}
}

func TestTelegramCheckURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://api.telegram.org/bot123456:AAH-abc_DEF/sendMessage": true,
		"http://botapi.internal:8081/bot1:x/sendMessage":             true,
		"https://api.telegram.org/bot123456:AAH-abc_DEF":             false,
		"https://api.telegram.org/bot123456:AAH-abc_DEF/sendPhoto":   false,
		"https://api.telegram.org/123456:AAH/sendMessage":            false,
		"https://api.telegram.org/botabc:AAH/sendMessage":            false,
		"https://api.telegram.org/bot1:x/sendMessage?chat_id=1":      false,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := telegram.New().CheckURL(u); (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestTelegramURLErrorNeverEchoesToken(t *testing.T) {
	u, _ := url.Parse("https://api.telegram.org/bot123456:S3CRETTOKEN/getMe?x=S3CRETTOKEN")
	err := telegram.New().CheckURL(u)
	if err == nil || strings.Contains(err.Error(), "S3CRET") || strings.Contains(err.Error(), "123456") {
		t.Fatalf("error = %v", err)
	}
}

func TestTelegramChatID(t *testing.T) {
	cases := []struct {
		id string
		ok bool
	}{
		{"123456789", true},
		{"-1001234567890", true},
		{"\t-42 ", true},
		{"@pulsix_alerts", true},
		{"", false},
		{"0", false},
		{"-0", false},
		{"12a", false},
		{"@abc", false},
		{"@1abcde", false},
		{"@" + strings.Repeat("a", 33), false},
		{"123456789012345678901", false},
	}
	for _, c := range cases {
		s, err := telegram.New().CheckSettings(notify.PresetSettings{ChatID: c.id})
		if (err == nil) != c.ok {
			t.Errorf("%q: %v", c.id, err)
		}
		if err == nil && s.ChatID != strings.TrimSpace(c.id) {
			t.Errorf("%q normalised to %q", c.id, s.ChatID)
		}
	}
}
