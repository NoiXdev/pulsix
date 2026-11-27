// Package pushover renders a webhook channel's message for Pushover's
// message API (backlog 4.8). Pulsix builds the URL; the application
// token and the user (or group) key are the channel's sealed secrets.
package pushover

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/notify"
)

// Pushover's limits, in characters.
const (
	production   = "https://api.pushover.net"
	titleLimit   = 250
	messageLimit = 1024
	urlLimit     = 512
)

// key is the shape of an application token and of a user or group key.
var key = regexp.MustCompile(`^[A-Za-z0-9]{30}$`)

type preset struct{ base string }

// New returns the Pushover preset posting under base ("" is Pushover's
// own API; tests point it at a fake).
func New(base string) notify.Preset {
	if base == "" {
		base = production
	}
	return preset{base: strings.TrimRight(base, "/")}
}

// CheckURL is never asked: the preset builds its URL.
func (preset) CheckURL(*url.URL) error { return nil }

func (preset) CheckSettings(s notify.PresetSettings) (notify.PresetSettings, error) {
	return s, s.Only("Pushover")
}

// Endpoint is the message API.
func (p preset) Endpoint(notify.PresetSettings) string { return p.base + "/1/messages.json" }

// SecretNames are the application token and the recipient's key.
func (preset) SecretNames() []string { return []string{"token", "user"} }

// CheckSecret checks the shape; its error never repeats the value.
func (preset) CheckSecret(name, value string) error {
	if key.MatchString(value) {
		return nil
	}
	if name == "token" {
		return errors.New("a Pushover application token is 30 letters and digits")
	}
	return errors.New("a Pushover user or group key is 30 letters and digits")
}

// priorities by level: high for crit, normal, low for good news; never
// emergency (2), which needs retry, expiry and receipts.
var priorities = map[notify.Level]string{
	notify.LevelCrit: "1", notify.LevelWarn: "0", notify.LevelUnknown: "0", notify.LevelOK: "-1", notify.LevelInfo: "-1",
}

func runes(s string) int { return utf8.RuneCountInString(s) }

// Render is one form-encoded message in plain text (HTML off).
func (preset) Render(ev notify.Event, t notify.Target) ([]notify.Request, error) {
	m := notify.Compose(ev)
	lines := make([]string, len(m.Lines))
	for i, l := range m.Lines {
		lines[i] = l.Text
		if l.Label != "" {
			lines[i] = l.Label + ": " + l.Text
		}
	}
	kept, more := notify.Fit(lines, messageLimit+1, runes, m.Omitted)
	if more > 0 {
		kept = append(kept[:len(kept):len(kept)], notify.MoreNote(more))
	}
	message := strings.Join(kept, "\n")
	if message == "" {
		message = notify.Clip(m.Title, messageLimit)
	}
	v := url.Values{}
	v.Set("token", t.Secrets["token"])
	v.Set("user", t.Secrets["user"])
	v.Set("title", notify.Clip(m.Title, titleLimit))
	v.Set("message", message)
	v.Set("priority", priorities[m.Level])
	if !ev.At.IsZero() {
		v.Set("timestamp", strconv.FormatInt(ev.At.Unix(), 10))
	}
	if m.URL != "" && len(m.URL) <= urlLimit {
		v.Set("url", m.URL)
		v.Set("url_title", notify.OpenLabel)
	}
	return []notify.Request{{ContentType: "application/x-www-form-urlencoded", Body: []byte(v.Encode())}}, nil
}
