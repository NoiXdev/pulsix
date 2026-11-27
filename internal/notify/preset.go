package notify

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
)

// PresetSettings are a preset channel's own settings, none of them secret
// (they are stored in clear and returned to readers): Telegram's chat id
// (backlog 4.7), Twilio's account and numbers, PagerDuty's region (4.8).
// A secret a service needs travels in the sealed URL, a sealed header or
// the channel's sealed preset secrets (Credentialed).
type PresetSettings struct {
	ChatID     string `json:"chat_id,omitempty"`
	AccountSID string `json:"account_sid,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Region     string `json:"region,omitempty"`
}

// settingNames are the settings' names as the API and Only spell them, in
// the order of values.
var settingNames = []string{"chat_id", "account_sid", "from", "to", "region"}

func (s PresetSettings) values() []string {
	return []string{s.ChatID, s.AccountSID, s.From, s.To, s.Region}
}

// firstSet names the first setting that is set ("" for none).
func (s PresetSettings) firstSet() string {
	for i, v := range s.values() {
		if v != "" {
			return settingNames[i]
		}
	}
	return ""
}

// Only refuses every setting outside allowed (names as the API spells
// them), naming the first one: a preset calls it from CheckSettings, so a
// setting it ignores is never stored.
func (s PresetSettings) Only(service string, allowed ...string) error {
	for i, v := range s.values() {
		if v != "" && !slices.Contains(allowed, settingNames[i]) {
			return fmt.Errorf("a %s channel takes no %s", service, settingNames[i])
		}
	}
	return nil
}

// Target is what one send of a preset channel renders for: the stored
// settings, the opened channel URL and the opened preset secrets. It is
// held only for the duration of that send and never logged.
type Target struct {
	Settings PresetSettings
	URL      *url.URL
	Secrets  map[string]string
}

// Request is one HTTP request a preset sends for an event (backlog 4.8).
// The sender adds everything else of a webhook send: the method (POST),
// the operator's headers, the X-Pulsix-* headers and the signature.
type Request struct {
	// Path, when set, replaces the channel URL's path (and drops its
	// query); scheme and host stay the stored destination.
	Path string
	// ContentType defaults to application/json.
	ContentType string
	// Header is set after the operator's headers, so the preset's own
	// value wins (Twilio's basic auth).
	Header [][2]string
	Body   []byte
}

// JSONRequest is the one JSON request a chat preset renders.
func JSONRequest(body []byte, err error) ([]Request, error) {
	if err != nil {
		return nil, err
	}
	return []Request{{Body: body}}, nil
}

// Preset is one service a webhook channel can render its requests for
// (backlog 4.7, 4.8): the channel keeps its kind, URL, headers and
// signing, and the preset owns the body. One package per service under
// internal/notify/presets implements it; presets.Registry wires them.
type Preset interface {
	// CheckURL checks a newly submitted URL beyond the raw webhook's own
	// syntax check. Its error is shown to the operator, so it must never
	// repeat the URL or any part of its path: a path can hold a token.
	CheckURL(u *url.URL) error
	// CheckSettings validates and normalises the preset's settings.
	CheckSettings(s PresetSettings) (PresetSettings, error)
	// Render builds the requests for ev, each within the service's own
	// message limits; they are sent in order. At least one.
	Render(ev Event, t Target) ([]Request, error)
}

// Endpointed is a preset whose URL Pulsix builds from its settings (a
// service on a fixed host): the operator submits none, and the URL is
// rebuilt and sealed on every save.
type Endpointed interface {
	Endpoint(s PresetSettings) string
}

// Credentialed is a preset that needs secrets beyond the URL and headers,
// sealed in the channel's config and never returned.
type Credentialed interface {
	// SecretNames lists the secrets, every one of them required.
	SecretNames() []string
	// CheckSecret validates a newly submitted value. Its error is shown
	// to the operator and must never repeat the value.
	CheckSecret(name, value string) error
}

// Presets maps a preset's stored name to its implementation.
type Presets map[string]Preset

// errNoRequests is a preset that rendered nothing to send.
var errNoRequests = errors.New("the preset rendered no request")
