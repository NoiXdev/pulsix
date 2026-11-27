// Package telegram renders a webhook channel's message for the Telegram
// Bot API's sendMessage (backlog 4.7). The bot token lives in the sealed
// URL; the chat id is a plain setting. The text uses Telegram's HTML parse
// mode, which needs four escapes where MarkdownV2 needs eighteen.
package telegram

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/noixdev/pulsix/internal/notify"
)

// textLimit is sendMessage's limit. Telegram counts the text after
// parsing its entities; measuring the HTML source is never less.
const textLimit = 4096

var (
	// sendMessagePath is the Bot API's own layout, on any host, so a
	// self-hosted Bot API server works too.
	sendMessagePath = regexp.MustCompile(`^/bot[0-9]+:[A-Za-z0-9_-]+/sendMessage$`)
	// chatID is a numeric chat (groups and channels are negative) or a
	// public @username (5-32 characters, a letter first).
	chatID = regexp.MustCompile(`^(-?[1-9][0-9]{0,19}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
)

type preset struct{}

// New returns the Telegram preset.
func New() notify.Preset { return preset{} }

// CheckURL requires the sendMessage method of a bot. The error never
// repeats the URL: its path holds the bot's token.
func (preset) CheckURL(u *url.URL) error {
	if !sendMessagePath.MatchString(u.Path) || u.RawQuery != "" {
		return errors.New("a Telegram URL has the form https://api.telegram.org/bot<token>/sendMessage, with the bot's token and nothing after sendMessage")
	}
	return nil
}

func (preset) CheckSettings(s notify.PresetSettings) (notify.PresetSettings, error) {
	s.ChatID = strings.TrimSpace(s.ChatID)
	if !chatID.MatchString(s.ChatID) {
		return s, errors.New("a Telegram channel needs a chat id: a number such as -1001234567890, or a public @username")
	}
	return s, nil
}

type payload struct {
	ChatID             string      `json:"chat_id"`
	Text               string      `json:"text"`
	ParseMode          string      `json:"parse_mode"`
	LinkPreviewOptions linkPreview `json:"link_preview_options"`
}

type linkPreview struct {
	IsDisabled bool `json:"is_disabled"`
}

// marks stand in for the colour Telegram has no way to show.
var marks = map[notify.Level]string{
	notify.LevelCrit:    "\U0001F534 ",
	notify.LevelWarn:    "\U0001F7E1 ",
	notify.LevelUnknown: "⚪ ",
	notify.LevelOK:      "\U0001F7E2 ",
}

func (preset) Render(ev notify.Event, s notify.PresetSettings) ([]byte, error) {
	m := notify.Compose(ev)
	head := marks[m.Level] + "<b>" + escape(m.Title) + "</b>"
	tail := ""
	if m.URL != "" {
		tail = "\n\n<a href=\"" + escape(m.URL) + "\">" + notify.OpenLabel + "</a>"
	}
	lines := make([]string, len(m.Lines))
	for i, l := range m.Lines {
		lines[i] = line(l)
	}
	// The lines sit between "\n\n" after the head and the tail; Fit counts
	// one separator per line, so the first of the two is its own.
	// Bytes too, for the sender's 64 KiB cap (review I1); Telegram's
	// own limit binds first today, the byte budget keeps it so.
	kept, more := notify.FitWithin(lines, m.Omitted,
		notify.Budget{Limit: textLimit - size(head) - 1 - size(tail), Size: size},
		notify.Budget{Limit: notify.MaxRenderedLines, Size: func(l string) int { return notify.JSONSize(l) + 1 }})
	if more > 0 {
		kept = append(kept[:len(kept):len(kept)], notify.MoreNote(more))
	}
	text := head
	if len(kept) > 0 {
		text += "\n\n" + strings.Join(kept, "\n")
	}
	text += tail
	return marshal(payload{ChatID: s.ChatID, Text: text, ParseMode: "HTML", LinkPreviewOptions: linkPreview{IsDisabled: true}})
}

// line is one message line: the label bold, or a link when the line has
// one, then the text.
func line(l notify.Line) string {
	text := escape(l.Text)
	label := escape(l.Label)
	switch {
	case l.URL != "" && label == "":
		return "<a href=\"" + escape(l.URL) + "\">" + text + "</a>"
	case l.URL != "":
		return "<a href=\"" + escape(l.URL) + "\">" + label + "</a>: " + text
	case label != "":
		return "<b>" + label + ":</b> " + text
	}
	return text
}

// escape is the HTML parse mode's escaping: the four characters it
// supports as named entities.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// size is a text's length as Telegram counts it, in UTF-16 code units.
func size(s string) int { return len(utf16.Encode([]rune(s))) }

// marshal encodes without Go's HTML escaping, so the body reads as sent.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
