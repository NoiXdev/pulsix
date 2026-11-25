package schedules

import (
	"fmt"
	"maps"
	"slices"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/templates"
)

// untrustedParts are the parts of a schedule's overrides an assistant set.
type untrustedParts struct{ limit, vars bool }

// partsOf is s's marks.
func partsOf(s Schedule) untrustedParts {
	return untrustedParts{limit: s.UntrustedLimit, vars: s.UntrustedExtraVars}
}

// checkUntrusted holds the parts of overrides an assistant asked for to the rules the
// claim applies to untrusted run overrides (jobs.RunOverrides
// checkUntrusted and checkLimit), so a schedule never stores what its
// runs would then fail on: no name Ansible or Pulsix acts on (playbook
// keywords included), no "__ansible" key or nesting past
// ansiblevars.MaxUntrustedDepth in a value, and a plain host pattern as
// the limit. No refusal echoes a value.
func checkUntrusted(ov templates.Overrides, parts untrustedParts) error {
	if !parts.vars {
		return checkUntrustedLimit(ov, parts)
	}
	for _, name := range slices.Sorted(maps.Keys(ov.ExtraVars)) {
		if err := ansiblevars.CheckRequestedName(name); err != nil {
			return fmt.Errorf("%w: extra var %s: %w", ErrInvalidInput, name, err)
		}
		if err := ansiblevars.CheckUntrusted(ov.ExtraVars[name]); err != nil {
			return fmt.Errorf("%w: extra var %s: %w", ErrInvalidInput, name, err)
		}
	}
	return checkUntrustedLimit(ov, parts)
}

func checkUntrustedLimit(ov templates.Overrides, parts untrustedParts) error {
	if parts.limit && ov.Limit != nil {
		if err := ansiblevars.CheckUntrustedLimit(*ov.Limit); err != nil {
			return fmt.Errorf("%w: limit: %w", ErrInvalidInput, err)
		}
	}
	return nil
}
