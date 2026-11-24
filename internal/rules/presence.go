// This file is the client-host half of an evaluation (backlog 3.57, spec
// docs/superpowers/specs/2026-11-13-client-hosts-design.md, "Evaluation"):
// what a host's kind and its agent's presence change about how it is
// judged. A client — a notebook, a workstation — is expected to be off,
// so while its agent is away the host is frozen rather than judged, and
// once the agent is back each tuple's staleness is measured from the
// later of its reading and the start of the agent's online period. A server, and a
// client without an agent, are judged exactly as before.
package rules

import "time"

// HostPresence is what an evaluation knows about a host's kind and its
// agent. Load fills it; the zero value is a server without an agent,
// which is judged exactly as every host was before client hosts existed.
type HostPresence struct {
	// Client is hosts.kind = 'client'.
	Client bool
	// HasAgent reports whether the host has an agent (a host_agents row).
	HasAgent bool
	// AgentOnline is the agents list's own verdict (agents.isOnline):
	// last_seen within the agent's online window (OnlineWindow). An agent
	// that never touched (last_seen NULL) is not online. The store
	// computes it with the window rule it was built with
	// (NewEvalSQLStore).
	AgentOnline bool
	// AgentLastSeen is the agent's last contact: last_seen, or — when it
	// has not touched since its registration (last_seen NULL after an
	// enrollment or a certificate renewal) — the registration itself.
	// Zero when the host has no agent.
	AgentLastSeen time.Time
	// AgentOnlineSince is when the agent's current online period began
	// (host_agents.online_since, 00092: reset by the first touch after a
	// gap longer than the agent's online window or its disconnect buffer
	// window, whichever is shorter), or zero when unknown. It is not
	// connected_since (00088), which spans gaps of up to 24 hours — a
	// notebook shut down overnight keeps that one, and its 1-hour checks
	// would read unknown every morning.
	AgentOnlineSince time.Time
	// AbsentAfter is the organization's client absent threshold
	// (org_monitoring_settings.client_absent_days, default applied); zero
	// means the warning is off. Load fills it for a client with an agent
	// only; nothing else reads it.
	AbsentAfter time.Duration
	// OnlineWindow is the agent's own online window (agents.OnlineWindow,
	// backlog 3.58: three of its reported heartbeat intervals, at least
	// the floor the store was built with). Load fills it for a client with
	// an agent only: it is how long that client's unknown roll-up must
	// stand before its host alert opens (unknownGrace).
	OnlineWindow time.Duration
}

// AgentOnlineWindow is the rule an agent's online window follows
// (agents.OnlineWindow, which this package never imports; internal/app
// passes its constants in and pins the two together): HeartbeatsPerWindow
// of the agent's reported heartbeat interval, never less than Floor, and
// Floor for an agent that never reported its interval. The presence read
// applies it per agent in SQL (GetHostPresence).
type AgentOnlineWindow struct {
	Floor               time.Duration
	HeartbeatsPerWindow int
}

// frozen reports whether the evaluation must leave the host exactly as it
// is: a client whose agent is not online (spec item 1). A client without
// an agent has no presence to wait for and is judged like a server.
func (p HostPresence) frozen() bool {
	return p.clientWithAgent() && !p.AgentOnline
}

// clientWithAgent reports whether the host is a client with an agent —
// the only host whose presence changes anything.
func (p HostPresence) clientWithAgent() bool {
	return p.Client && p.HasAgent
}

// stalenessAnchor is the moment no tuple's staleness may be measured from
// before (spec item 2): the start of the online client's online period.
// Zero — no anchor, today's rule — for a server, a client without an
// agent and an online start that is unknown.
func (p HostPresence) stalenessAnchor() time.Time {
	if !p.Client || !p.HasAgent || !p.AgentOnline {
		return time.Time{}
	}
	return p.AgentOnlineSince
}

// unknownGrace is how long the host's unknown roll-up must stand before
// its host alert opens (AlertInput.UnknownGrace): the online window for a
// client with an agent, zero for every other host.
func (p HostPresence) unknownGrace() time.Duration {
	if !p.clientWithAgent() {
		return 0
	}
	return p.OnlineWindow
}

// graceDue tightens due to the moment an unknown roll-up's grace ends —
// counted from the later of the roll-up's start and the online period's
// (AlertInput.UnknownGraceStart, the same rule) — so
// the ticker is back exactly when the host alert may open rather than on
// the silent heartbeat. It only ever brings due forward: a grace that has
// already ended, or ends after due, changes nothing — and a host with no
// grace (a server) is never touched.
func graceDue(host HostState, p HostPresence, now, due time.Time) time.Time {
	grace := p.unknownGrace()
	if grace <= 0 || host.State != StateUnknown {
		return due
	}
	if end := graceStart(host.Since, p.stalenessAnchor()).Add(grace); end.After(now) && end.Before(due) {
		return end
	}
	return due
}

// absentDeadline is when a frozen client becomes absent, and whether the
// warning is on at all: AbsentAfter after the agent's last contact.
func (p HostPresence) absentDeadline() (time.Time, bool) {
	if p.AbsentAfter <= 0 || p.AgentLastSeen.IsZero() {
		return time.Time{}, false
	}
	return p.AgentLastSeen.Add(p.AbsentAfter), true
}

// evaluateFrozen is the whole evaluation of a frozen host: no tuple is
// judged, so no state, no change, nothing reported unmeasured or dropped,
// and the roll-up is the one the last evaluation wrote (Apply writes none
// of it; see Result.Frozen). What it does decide is the absent warning's
// input and when to look again: the absent deadline when that comes
// first, never later than the silent heartbeat. The cap is what bounds a
// missed wake-up — a kind change whose post-commit dirty mark failed, or
// an agent that reconnects without sending anything that would mark the
// host dirty.
func evaluateFrozen(s Snapshot) Result {
	res := Result{
		Frozen:        true,
		AgentLastSeen: s.Presence.AgentLastSeen,
		NextDue:       s.Now.Add(silentHeartbeat),
	}
	if s.PreviousHost != nil {
		res.Host = *s.PreviousHost
	}
	if deadline, on := s.Presence.absentDeadline(); on {
		res.Absent = !s.Now.Before(deadline)
		if deadline.After(s.Now) && deadline.Before(res.NextDue) {
			res.NextDue = deadline
		}
	}
	return res
}
