// Package presets wires the services a webhook channel can render its
// requests for (backlog 4.7, 4.8): one package per service below this
// one, the layout backlog 3.52 gave the agent's check plugins. Adding a
// service is its package plus one line here and one value in the preset
// CHECK of notification_channels.
package presets

import (
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/notify/presets/mattermost"
	"github.com/noixdev/pulsix/internal/notify/presets/ntfy"
	"github.com/noixdev/pulsix/internal/notify/presets/pagerduty"
	"github.com/noixdev/pulsix/internal/notify/presets/pushover"
	"github.com/noixdev/pulsix/internal/notify/presets/slack"
	"github.com/noixdev/pulsix/internal/notify/presets/telegram"
	"github.com/noixdev/pulsix/internal/notify/presets/twilio"
)

// Registry is every preset by its stored name, posting to the real
// services.
func Registry() notify.Presets { return RegistryAt("") }

// RegistryAt is Registry with every preset on a fixed host (Pushover,
// Twilio, PagerDuty) posting under base instead ("" is the real
// services): tests point them at one fake server.
func RegistryAt(base string) notify.Presets {
	return notify.Presets{
		"slack":      slack.New(),
		"mattermost": mattermost.New(),
		"telegram":   telegram.New(),
		"ntfy":       ntfy.New(),
		"pushover":   pushover.New(base),
		"twilio":     twilio.New(base),
		"pagerduty":  pagerduty.New(base),
	}
}
