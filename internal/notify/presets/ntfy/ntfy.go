// Package ntfy renders a webhook channel's message for an ntfy server
// (backlog 4.8): the channel URL is the topic's address on any server,
// and the message is published as JSON to that server's root. A protected
// topic's access token is an operator Authorization header, sealed like
// every header value.
package ntfy

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/noixdev/pulsix/internal/notify"
)

// messageLimit is ntfy's default message size limit in bytes; a longer
// message would become an attachment or be refused.
const messageLimit = 4096

// topicName is the topic syntax ntfy accepts.
var topicName = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

type preset struct{}

// New returns the ntfy preset.
func New() notify.Preset { return preset{} }

// split is a topic URL's server root path and topic.
func split(u *url.URL) (root, topic string, ok bool) {
	i := strings.LastIndex(u.Path, "/")
	if i < 0 {
		return "", "", false
	}
	root, topic = u.Path[:i+1], u.Path[i+1:]
	return root, topic, topicName.MatchString(topic)
}

// CheckURL requires a topic's address with nothing after the topic. The
// error never repeats the URL: on a public server the topic name is what
// keeps the topic private.
func (preset) CheckURL(u *url.URL) error {
	if _, _, ok := split(u); !ok || u.RawQuery != "" {
		return errors.New("an ntfy URL is the topic's address, such as https://ntfy.sh/<topic>: a topic of up to 64 letters, digits, - and _, with nothing after it")
	}
	return nil
}

func (preset) CheckSettings(s notify.PresetSettings) (notify.PresetSettings, error) {
	return s, s.Only("ntfy")
}

type payload struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
	Click    string   `json:"click,omitempty"`
}

// priorities are ntfy's 1 (min) to 5 (max) by level; tags are emoji
// short codes ntfy shows in front of the title.
var (
	priorities = map[notify.Level]int{notify.LevelCrit: 5, notify.LevelWarn: 4, notify.LevelUnknown: 3, notify.LevelOK: 2, notify.LevelInfo: 3}
	tags       = map[notify.Level]string{
		notify.LevelCrit: "rotating_light", notify.LevelWarn: "warning", notify.LevelUnknown: "grey_question",
		notify.LevelOK: "white_check_mark", notify.LevelInfo: "information_source",
	}
)

// Render publishes one plain-text message to the server root.
func (preset) Render(ev notify.Event, t notify.Target) ([]notify.Request, error) {
	if t.URL == nil {
		return nil, errors.New("no channel URL")
	}
	root, topic, ok := split(t.URL)
	if !ok {
		return nil, errors.New("the channel URL names no topic")
	}
	m := notify.Compose(ev)
	lines := make([]string, len(m.Lines))
	for i, l := range m.Lines {
		lines[i] = l.Text
		if l.Label != "" {
			lines[i] = l.Label + ": " + l.Text
		}
	}
	// Each line costs one more for its separator, the first one too,
	// which leaves a byte to spare.
	kept, more := notify.Fit(lines, messageLimit, func(s string) int { return len(s) }, m.Omitted)
	if more > 0 {
		kept = append(kept[:len(kept):len(kept)], notify.MoreNote(more))
	}
	message := strings.Join(kept, "\n")
	if message == "" {
		message = m.Title
	}
	body, err := marshal(payload{
		Topic: topic, Title: m.Title, Message: message,
		Priority: priorities[m.Level], Tags: []string{tags[m.Level]}, Click: m.URL,
	})
	if err != nil {
		return nil, err
	}
	return []notify.Request{{Path: root, Body: body}}, nil
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
