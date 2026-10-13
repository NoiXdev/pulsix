// Package alertroutes is the bridge between monitoring and delivery
// (spec docs/superpowers/specs/2026-10-12-v0.4b-alert-routes-design.md).
// It owns which alert reaches which notification channel, and nothing
// else: alerts stay with internal/alerts and delivery stays with
// internal/notify, and neither of those two depends on this package.
package alertroutes

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// Scope is what a route points at. Routes ADD UP rather than override —
// backlog 4.1b — so a host can be covered by routes at all three levels
// at once and hears from every one of them.
type Scope string

const (
	ScopeOrg             Scope = "org"
	ScopeMonitoringGroup Scope = "monitoring_group"
	ScopeHost            Scope = "host"
)

// Sentinels the API maps to HTTP.
var (
	ErrInvalidInput     = errors.New("invalid input")
	ErrNotFound         = errors.New("not found")
	ErrNameTaken        = errors.New("name already in use")
	ErrRevisionMismatch = errors.New("revision mismatch")
)

// Input is a route as an operator supplies it.
type Input struct {
	Name       string
	Enabled    bool
	Scope      Scope
	ScopeID    *uuid.UUID // nil exactly when Scope is ScopeOrg
	Severities []rules.State
	Events     []alerts.EventKind
	Channels   []uuid.UUID
	// Window is the route's delivery window: nil means the route may
	// deliver at any time, the v0.4b default this feature extends.
	Window *Window
	// Breakthrough is the subset of Severities that crosses a closed
	// Window anyway. Non-empty only when Window is set.
	Breakthrough []rules.State
	// RotationID, when set, makes the on-call rotation it names the
	// route's channel source instead of the fixed Channels list.
	RotationID *uuid.UUID
}

// Route is a stored route with its four selections attached.
type Route struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	Name         string
	Enabled      bool
	Scope        Scope
	ScopeID      *uuid.UUID
	Severities   []rules.State
	Events       []alerts.EventKind
	Channels     []uuid.UUID
	Window       *Window
	Breakthrough []rules.State
	RotationID   *uuid.UUID
	Revision     int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// routableSeverities is the set an alert can actually carry: ok is not
// among them, because an alert exists only while something is wrong.
var routableSeverities = []rules.State{rules.StateWarn, rules.StateCrit, rules.StateUnknown}

// Validate rejects a route that cannot do anything. Each of the three
// selections must be non-empty and free of duplicates: a route with no
// channel source tells nobody, and one with no severity or no event kind
// matches nothing — states that would otherwise sit in the list looking
// configured while being silent, which is the trap backlog 4.29 records.
func Validate(in Input) error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: a route needs a name", ErrInvalidInput)
	}
	switch in.Scope {
	case ScopeOrg, ScopeMonitoringGroup, ScopeHost:
	default:
		return fmt.Errorf("%w: unknown scope %q", ErrInvalidInput, in.Scope)
	}
	if (in.Scope == ScopeOrg) != (in.ScopeID == nil) {
		return fmt.Errorf("%w: scope %q and its target disagree", ErrInvalidInput, in.Scope)
	}
	if err := checkSet("severity", in.Severities, func(s rules.State) bool {
		return slices.Contains(routableSeverities, s)
	}); err != nil {
		return err
	}
	if err := checkSet("event kind", in.Events, func(k alerts.EventKind) bool {
		_, ok := alerts.ParseEventKind(string(k))
		return ok
	}); err != nil {
		return err
	}
	if err := validateChannelSource(in.Channels, in.RotationID); err != nil {
		return err
	}
	if err := validateWindow(in.Window); err != nil {
		return err
	}
	return validateBreakthrough(in.Breakthrough, in.Window, in.Severities)
}

// validateChannelSource enforces decision 1 of the v0.4c design
// (docs/superpowers/specs/2026-10-13-v0.4c-notification-schedules-design.md):
// a route's channel source is the fixed Channels list OR RotationID, never
// neither and never both. Neither reaches nobody — the same defect
// checkSet already exists to catch for severities and events, reused here
// through the empty-RotationID branch so "a route needs at least one
// channel" keeps the exact wording and duplicate/unknown-member checks it
// already had. Both is ambiguous rather than merely redundant:
// channelsOf (follow_resolve.go) reads the rotation's own current channel
// whenever RotationID is set and never falls back to Channels, so a fixed
// list saved alongside a rotation is a list that silently does nothing —
// worth refusing up front rather than leaving an operator to discover it
// only once the rotation breaks and the "fallback" they thought they had
// turns out never to have been read at all.
func validateChannelSource(channels []uuid.UUID, rotationID *uuid.UUID) error {
	if rotationID != nil {
		if len(channels) > 0 {
			return fmt.Errorf("%w: a route needs exactly one channel source: channels and a rotation cannot both be set", ErrInvalidInput)
		}
		return nil
	}
	return checkSet("channel", channels, func(uuid.UUID) bool { return true })
}

// checkSet enforces non-empty, no duplicates and every member known.
func checkSet[T comparable](what string, got []T, known func(T) bool) error {
	if len(got) == 0 {
		return fmt.Errorf("%w: a route needs at least one %s", ErrInvalidInput, what)
	}
	seen := make(map[T]struct{}, len(got))
	for _, v := range got {
		if !known(v) {
			return fmt.Errorf("%w: unknown %s %v", ErrInvalidInput, what, v)
		}
		if _, dup := seen[v]; dup {
			return fmt.Errorf("%w: %s %v listed twice", ErrInvalidInput, what, v)
		}
		seen[v] = struct{}{}
	}
	return nil
}
