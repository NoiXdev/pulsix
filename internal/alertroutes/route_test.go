package alertroutes

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

func TestValidate(t *testing.T) {
	gid := uuid.New()
	base := func() Input {
		return Input{
			Name:       "ops",
			Enabled:    true,
			Scope:      ScopeOrg,
			Severities: []rules.State{rules.StateCrit},
			Events:     []alerts.EventKind{alerts.EventOpened},
			Channels:   []uuid.UUID{uuid.New()},
		}
	}
	tests := []struct {
		name string
		mut  func(*Input)
		want bool // true = valid
	}{
		{"the base case", func(*Input) {}, true},
		{"a group scope carries its id", func(in *Input) {
			in.Scope, in.ScopeID = ScopeMonitoringGroup, &gid
		}, true},
		{"unknown severity", func(in *Input) {
			in.Severities = []rules.State{rules.StateOK}
		}, false},
		{"no severity at all", func(in *Input) { in.Severities = nil }, false},
		{"no event kind at all", func(in *Input) { in.Events = nil }, false},
		// A route with no channel tells nobody. Decision 8: it is rejected
		// here rather than created and left silently ineffective.
		{"no channel", func(in *Input) { in.Channels = nil }, false},
		{"blank name", func(in *Input) { in.Name = "  " }, false},
		{"org scope with an id", func(in *Input) { in.ScopeID = &gid }, false},
		{"group scope without an id", func(in *Input) {
			in.Scope = ScopeMonitoringGroup
		}, false},
		{"unknown scope", func(in *Input) { in.Scope = "fleet" }, false},
		{"a severity twice", func(in *Input) {
			in.Severities = []rules.State{rules.StateCrit, rules.StateCrit}
		}, false},
		{"a channel twice", func(in *Input) {
			c := uuid.New()
			in.Channels = []uuid.UUID{c, c}
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.mut(&in)
			err := Validate(in)
			if tc.want && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !tc.want {
				if err == nil {
					t.Error("Validate() = nil, want an error")
				} else if !errors.Is(err, ErrInvalidInput) {
					t.Errorf("Validate() = %v, want it to wrap ErrInvalidInput", err)
				}
			}
		})
	}
}

// TestValidateWindowAndBreakthrough is this package's own version of the
// brief's test: it is written in-package (unqualified names) rather than as
// alertroutes_test, matching TestValidate right above it in this same file —
// the brief's literal "alertroutes.Input" spelling only compiles from an
// external test package, and route_test.go has always been internal.
func TestValidateWindowAndBreakthrough(t *testing.T) {
	base := func() Input {
		return Input{
			Name: "ops", Enabled: true, Scope: ScopeOrg,
			Severities: []rules.State{rules.StateCrit},
			Events:     []alerts.EventKind{alerts.EventOpened},
			Channels:   []uuid.UUID{uuid.New()},
		}
	}
	win := func(cron string, d time.Duration, tz string) *Window {
		return &Window{Cron: cron, Duration: d, Timezone: tz}
	}
	tests := []struct {
		name string
		mut  func(*Input)
		want bool
	}{
		{"no window at all is the default", func(*Input) {}, true},
		{"business hours", func(in *Input) {
			in.Window = win("0 9 * * 1-5", 9*time.Hour, "Europe/Berlin")
		}, true},
		{"quiet hours crossing midnight", func(in *Input) {
			in.Window = win("0 7 * * *", 15*time.Hour, "Europe/Berlin")
		}, true},
		{"unparseable cron", func(in *Input) {
			in.Window = win("not a cron", time.Hour, "UTC")
		}, false},
		{"unknown timezone", func(in *Input) {
			in.Window = win("0 9 * * *", time.Hour, "Mars/Olympus")
		}, false},
		{"zero duration", func(in *Input) {
			in.Window = win("0 9 * * *", 0, "UTC")
		}, false},
		// Review Focus 4. A window longer than its own recurrence makes
		// occurrences overlap; the GUI must not be able to produce it.
		{"a duration longer than the recurrence", func(in *Input) {
			in.Window = win("0 9 * * *", 48*time.Hour, "UTC")
		}, false},
		// A reviewer's finding: "0 9 * * 1,5" (Mondays and Fridays — an
		// ordinary weekday-checkbox selection, SimpleWeekly's own shape) is
		// unevenly spaced. Anchored at 2000-01-01 (a Saturday), the FIRST
		// pair of occurrences is Monday->Friday, 96h apart; the real
		// minimum, Friday->Monday, is only 72h. A check that only looked at
		// one pair from a fixed anchor would accept a duration up to 96h —
		// including 80h, which overlaps the very Friday->Monday gap it
		// never examined. Both cases use UTC to keep the arithmetic exact
		// (no DST shift to blur the 72h/96h boundary).
		{"weekday cron: a duration between the real min and max gap is rejected", func(in *Input) {
			in.Window = win("0 9 * * 1,5", 80*time.Hour, "UTC")
		}, false},
		{"weekday cron: a duration under the real min gap is accepted", func(in *Input) {
			in.Window = win("0 9 * * 1,5", 71*time.Hour, "UTC")
		}, true},
		// Review Focus 3. A severity that breaks through a window the
		// route never fires on at all describes nothing.
		{"breakthrough outside the route's own severities", func(in *Input) {
			in.Window = win("0 9 * * *", time.Hour, "UTC")
			in.Breakthrough = []rules.State{rules.StateWarn}
		}, false},
		{"breakthrough inside them", func(in *Input) {
			in.Window = win("0 9 * * *", time.Hour, "UTC")
			in.Breakthrough = []rules.State{rules.StateCrit}
		}, true},
		{"breakthrough without a window is pointless", func(in *Input) {
			in.Breakthrough = []rules.State{rules.StateCrit}
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.mut(&in)
			err := Validate(in)
			if tc.want && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !tc.want && err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}
