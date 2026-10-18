package alertroutes

import (
	"time"

	"github.com/google/uuid"
)

// Step is one rung of a route's ladder. Position 0 is never a Step: it is
// the route's own channels, and only the ledger records it.
type Step struct {
	Position   int
	Delay      time.Duration
	RotationID *uuid.UUID
	ChannelIDs []uuid.UUID
}

// ChainState is everything the decision needs about one (alert, route)
// pair, read before any of it is written.
type ChainState struct {
	Steps        []Step    // ordered by Position, 1..n
	FiredUpTo    int       // highest position with a ledger row; -1 when even the base is missing
	LastFiredAt  time.Time // fired_at of FiredUpTo; zero when FiredUpTo is -1
	LastUrgentAt time.Time // most recent severity_raised / ack_voided / end-of-mute; zero if none
}

// DueStep is pure: no clock, no database, no logging — the same shape
// rules.Resolve and alerts.Mute already have, so the timing rules can be
// read and tested without a Postgres container anywhere near them.
func DueStep(cs ChainState, now time.Time) (Step, bool) {
	// The base is the route's first real delivery. Until it exists there
	// is no clock to start: a route that has not yet said anything has
	// nothing to escalate from (spec decision 3).
	if cs.FiredUpTo < 0 {
		return Step{}, false
	}
	next := cs.FiredUpTo + 1
	for _, s := range cs.Steps {
		if s.Position != next {
			continue
		}
		// The anchor is the later of the previous rung's real firing and
		// the last thing that made this alert newly urgent, so a chain
		// resuming after a pause gives the next rung its full delay
		// instead of firing at once (spec decision 5).
		anchor := cs.LastFiredAt
		if cs.LastUrgentAt.After(anchor) {
			anchor = cs.LastUrgentAt
		}
		if !now.Before(anchor.Add(s.Delay)) {
			return s, true
		}
		return Step{}, false
	}
	return Step{}, false
}
