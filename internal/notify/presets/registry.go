// Package presets wires the chat services a webhook channel can render
// its body for (backlog 4.7): one package per service below this one, the
// layout backlog 3.52 gave the agent's check plugins. Adding a service is
// its package plus one line here and one value in the preset CHECK of
// notification_channels.
package presets

import (
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/notify/presets/slack"
)

// Registry is every preset by its stored name.
func Registry() notify.Presets {
	return notify.Presets{
		"slack": slack.New(),
	}
}
