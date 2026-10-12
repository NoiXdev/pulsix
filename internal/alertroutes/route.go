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
}

// Route is a stored route with its three selections attached.
type Route struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	Name       string
	Enabled    bool
	Scope      Scope
	ScopeID    *uuid.UUID
	Severities []rules.State
	Events     []alerts.EventKind
	Channels   []uuid.UUID
	Revision   int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// routableSeverities is the set an alert can actually carry: ok is not
// among them, because an alert exists only while something is wrong.
var routableSeverities = []rules.State{rules.StateWarn, rules.StateCrit, rules.StateUnknown}

// Validate rejects a route that cannot do anything. Each of the three
// selections must be non-empty and free of duplicates: a route with no
// channel tells nobody, and one with no severity or no event kind
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
	return checkSet("channel", in.Channels, func(uuid.UUID) bool { return true })
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
