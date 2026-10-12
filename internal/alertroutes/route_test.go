package alertroutes

import (
	"errors"
	"testing"

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
