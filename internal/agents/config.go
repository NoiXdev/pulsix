package agents

import (
	"context"
	"time"
)

// MaxConfigVersionBytes bounds the configuration version an agent's
// heartbeat may store (backlog 3.13c). The manager's own versions are hex
// SHA-256 (64 bytes, checks.ResolvedSet.Version); anything the agent sends
// beyond this is not a version the manager ever handed out.
const MaxConfigVersionBytes = 128

// ConfigCheck is the manager's latest comparison of the configuration
// version an agent reported in a heartbeat against the one it resolved for
// the host at that moment (internal/agentapi processHeartbeat).
//
// Known is false until a comparison ran — the registration is new, or the
// resolve was throttled or failed every time so far; the other fields are
// then zero. Reported is the empty string for an agent that has applied no
// configuration yet, which is a known answer, not an unknown one.
type ConfigCheck struct {
	Known     bool
	Reported  string
	Expected  string
	CheckedAt time.Time
}

// Current reports whether the agent ran the configuration the manager
// expected at the last comparison. An unknown check is never current.
func (c ConfigCheck) Current() bool {
	return c.Known && c.Reported == c.Expected
}

// RecordConfigCheck records one comparison of the heartbeat path: reported
// is what the agent said it runs, expected what the manager resolved.
// Both are bounded first (MaxConfigVersionBytes, one line of valid UTF-8),
// since the reported one is the agent's to fill. The store writes the row
// only when either version changed or the stored check is older than
// configCheckRewrite, so a steady agent adds no write per heartbeat.
func (s *Service) RecordConfigCheck(ctx context.Context, serial, reported, expected string) error {
	return s.store.RecordConfigCheck(ctx, serial,
		boundLine(reported, MaxConfigVersionBytes), boundLine(expected, MaxConfigVersionBytes))
}
