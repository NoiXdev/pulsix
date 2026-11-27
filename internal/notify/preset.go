package notify

import "net/url"

// PresetSettings are a preset channel's own settings, none of them secret
// (they are stored in clear and returned to readers): Telegram's chat id.
// A secret a service needs travels in the sealed URL or a sealed header.
type PresetSettings struct {
	ChatID string `json:"chat_id,omitempty"`
}

// Preset is one chat service a webhook channel can render its body for
// (backlog 4.7): the channel keeps its kind, URL, headers and signing, and
// the preset owns the body. One package per service under
// internal/notify/presets implements it; presets.Registry wires them.
type Preset interface {
	// CheckURL checks a newly submitted URL beyond the raw webhook's own
	// syntax check. Its error is shown to the operator, so it must never
	// repeat the URL or any part of its path: a path can hold a token.
	CheckURL(u *url.URL) error
	// CheckSettings validates and normalises the preset's settings.
	CheckSettings(s PresetSettings) (PresetSettings, error)
	// Render builds the JSON request body for ev, within the service's
	// own message limits.
	Render(ev Event, s PresetSettings) ([]byte, error)
}

// Presets maps a preset's stored name to its implementation.
type Presets map[string]Preset
