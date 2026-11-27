// Package mattermost renders a webhook channel's message for a Mattermost
// incoming webhook (backlog 4.7). Mattermost reads Slack's attachment
// format but not Slack's blocks or mrkdwn: one attachment in the level
// colour, a title linking to Pulsix, Markdown text and a footer.
package mattermost

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/notify"
)

// textLimit bounds the attachment's text, well inside Mattermost's post
// size (16383 characters by default).
const textLimit = 12000

type preset struct{}

// New returns the Mattermost preset.
func New() notify.Preset { return preset{} }

// CheckURL accepts any URL the raw webhook does: Mattermost is
// self-hosted, so no host can be required.
func (preset) CheckURL(*url.URL) error { return nil }

func (preset) CheckSettings(s notify.PresetSettings) (notify.PresetSettings, error) {
	if s.ChatID != "" {
		return s, errors.New("a Mattermost channel takes no chat id; the URL names the channel")
	}
	return s, nil
}

type attachment struct {
	Fallback  string `json:"fallback"`
	Color     string `json:"color"`
	Title     string `json:"title"`
	TitleLink string `json:"title_link,omitempty"`
	Text      string `json:"text,omitempty"`
	Footer    string `json:"footer"`
}

type payload struct {
	Attachments []attachment `json:"attachments"`
}

func (preset) Render(ev notify.Event, _ notify.PresetSettings) ([]byte, error) {
	m := notify.Compose(ev)
	lines := make([]string, len(m.Lines))
	for i, l := range m.Lines {
		lines[i] = line(l)
	}
	kept, more := notify.Fit(lines, textLimit, utf8.RuneCountInString, m.Omitted)
	if more > 0 {
		kept = append(kept[:len(kept):len(kept)], notify.MoreNote(more))
	}
	// The title, fallback and footer are shown as plain text; they come
	// from Compose already defused.
	return marshal(payload{Attachments: []attachment{{
		Fallback: m.Title, Color: m.Level.Color(), Title: m.Title, TitleLink: m.URL,
		Text: strings.Join(kept, "\n"), Footer: "Pulsix · " + m.Org,
	}}})
}

// line is one message line in Markdown: the label bold (and linked when
// the line has a link), then the text.
func line(l notify.Line) string {
	text := escape(l.Text)
	if l.Label == "" {
		if l.URL != "" {
			return "[" + text + "](" + l.URL + ")"
		}
		return text
	}
	label := escape(l.Label)
	if l.URL != "" {
		label = "[" + label + "](" + l.URL + ")"
	}
	return "**" + label + ":** " + text
}

// markdownPunct is every character Mattermost's Markdown gives a meaning
// to and lets a backslash escape (exactly its renderer's escape set):
// emphasis, code, links, headings, lists, tables, strikethrough, quotes,
// "~channel" links and "#hashtags". A character outside the set keeps a
// backslash in front of it visible, so nothing else is escaped. '<'
// and '>' never get here (Compose's ChatText swaps them); '&' becomes an
// entity, so "&amp;" stays text.
const markdownPunct = "\\`*_{}[]()#+-.!|~>"

func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r < 0x80 && strings.ContainsRune(markdownPunct, r):
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

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
