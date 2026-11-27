// Package slack renders a webhook channel's message for a Slack incoming
// webhook (backlog 4.7): one attachment in the message's level colour
// holding Block Kit blocks — a header, mrkdwn sections, the organization,
// and a button to Pulsix.
package slack

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/notify"
)

// Slack's own limits (Block Kit reference): a header's text, a section's
// text; and how many sections a message may use of its 50 blocks.
const (
	headerLimit  = 150
	sectionLimit = 3000
	maxSections  = 10
)

type preset struct{}

// New returns the Slack preset.
func New() notify.Preset { return preset{} }

// CheckURL accepts any URL the raw webhook does: Slack-compatible
// receivers and relays exist, and the destination is policed at send time.
func (preset) CheckURL(*url.URL) error { return nil }

func (preset) CheckSettings(s notify.PresetSettings) (notify.PresetSettings, error) {
	if s.ChatID != "" {
		return s, errors.New("a Slack channel takes no chat id; the URL names the channel")
	}
	return s, nil
}

type textObj struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type element struct {
	Type string   `json:"type"`
	Text *textObj `json:"text,omitempty"`
	URL  string   `json:"url,omitempty"`
}

type block struct {
	Type     string    `json:"type"`
	Text     *textObj  `json:"text,omitempty"`
	Elements []element `json:"elements,omitempty"`
}

// contextElement is a mrkdwn element of a context block, which carries
// its text directly rather than in a text object.
type contextElement struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type contextBlock struct {
	Type     string           `json:"type"`
	Elements []contextElement `json:"elements"`
}

type attachment struct {
	Color  string `json:"color"`
	Blocks []any  `json:"blocks"`
}

type payload struct {
	Text        string       `json:"text"`
	Attachments []attachment `json:"attachments"`
}

func (preset) Render(ev notify.Event, _ notify.PresetSettings) ([]byte, error) {
	m := notify.Compose(ev)
	title := notify.Clip(m.Title, headerLimit)
	blocks := []any{block{Type: "header", Text: &textObj{Type: "plain_text", Text: title}}}
	lines := make([]string, len(m.Lines))
	for i, l := range m.Lines {
		lines[i] = line(l)
	}
	sections, shown := pack(lines)
	for _, s := range sections {
		blocks = append(blocks, block{Type: "section", Text: &textObj{Type: "mrkdwn", Text: s}})
	}
	context := []contextElement{{Type: "mrkdwn", Text: "Pulsix · " + escape(m.Org)}}
	if more := len(lines) - shown + m.Omitted; more > 0 {
		context = append(context, contextElement{Type: "mrkdwn", Text: notify.MoreNote(more)})
	}
	blocks = append(blocks, contextBlock{Type: "context", Elements: context})
	if m.URL != "" {
		blocks = append(blocks, block{Type: "actions", Elements: []element{{
			Type: "button", Text: &textObj{Type: "plain_text", Text: notify.OpenLabel}, URL: m.URL,
		}}})
	}
	return marshal(payload{Text: escape(title), Attachments: []attachment{{Color: m.Level.Color(), Blocks: blocks}}})
}

// line is one message line in mrkdwn: the label bold (and linked when the
// line has a link), then the text.
func line(l notify.Line) string {
	text := escape(l.Text)
	if l.Label == "" {
		if l.URL != "" {
			return "<" + escape(l.URL) + "|" + text + ">"
		}
		return text
	}
	label := escape(l.Label)
	if l.URL != "" {
		label = "<" + escape(l.URL) + "|" + label + ">"
	}
	return "*" + label + ":* " + text
}

// pack puts lines into at most maxSections sections of at most
// sectionLimit characters, in order, and reports how many lines it placed.
func pack(lines []string) (sections []string, shown int) {
	var cur strings.Builder
	for _, l := range lines {
		n := utf8.RuneCountInString(l)
		if cur.Len() > 0 && utf8.RuneCountInString(cur.String())+1+n > sectionLimit {
			if len(sections)+1 == maxSections {
				break
			}
			sections = append(sections, cur.String())
			cur.Reset()
		}
		if n > sectionLimit { // cannot happen with Compose's clipping
			break
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(l)
		shown++
	}
	if cur.Len() > 0 {
		sections = append(sections, cur.String())
	}
	return sections, shown
}

// escape is Slack's own escaping for text it parses: the three characters
// that start an entity, a mention ("<!channel>") or a link ("<url|text>").
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
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
