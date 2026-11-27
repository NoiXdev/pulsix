// The host's own probe policy (backlog 3.82): agent.yaml's probes section
// decides whether this agent probes other hosts for the manager at all,
// and which address ranges its probes must never reach. It is applied on
// every look at the file — at start, with every heartbeat and by the
// periodic look — so an edit takes effect within about a minute, and the
// heartbeat reports the outcome as probes_supported.
package agent

import (
	"net/netip"

	"github.com/noixdev/pulsix/internal/agent/secrets"
)

// probePolicyOf is the probe policy the file in state gives: its own once
// it was read, the defaults (probing on, no deny list) when it does not
// exist, and probing off while it is refused — a refused file may hold an
// opt-out or a deny list its operator relies on, and a mistake elsewhere
// in it must not turn probing for other hosts back on.
func probePolicyOf(set secrets.Set, state secrets.State) (enabled bool, deny []netip.Prefix) {
	switch state {
	case secrets.StateOK:
		p := set.Probes()
		return !p.Disabled, p.DenyCIDRs
	case secrets.StateAbsent:
		return true, nil
	default:
		return false, nil
	}
}

// applyProbePolicy hands the file's probe policy to the runner, which adds
// the deny list to its default guard, and logs a change once. localMu is
// held.
func (l *Loop) applyProbePolicy(set secrets.Set, state secrets.State) {
	enabled, deny := probePolicyOf(set, state)
	if !l.probes.SetPolicy(enabled, deny) {
		return
	}
	switch {
	case enabled:
		l.log.Info("probing for other hosts is on", "path", l.secrets.Path(), "denied_ranges", len(deny))
	case state == secrets.StateOK:
		l.log.Info("probing for other hosts is off (probes.enabled is false); the manager assigns this agent no probes",
			"path", l.secrets.Path())
	default:
		l.log.Warn("probing for other hosts is off while the configuration file is refused", "path", l.secrets.Path())
	}
}
