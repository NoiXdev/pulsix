package schedules

import (
	"fmt"
	"maps"
	"slices"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/templates"
)

// checkUntrusted holds overrides an assistant asked for to the rules the
// claim applies to untrusted run overrides (jobs.RunOverrides
// checkUntrusted and checkLimit), so a schedule never stores what its
// runs would then fail on: no name Ansible or Pulsix acts on (playbook
// keywords included), no "__ansible" key or nesting past
// ansiblevars.MaxUntrustedDepth in a value, and a plain host pattern as
// the limit. No refusal echoes a value.
func checkUntrusted(ov templates.Overrides) error {
	for _, name := range slices.Sorted(maps.Keys(ov.ExtraVars)) {
		if err := ansiblevars.CheckRequestedName(name); err != nil {
			return fmt.Errorf("%w: extra var %s: %w", ErrInvalidInput, name, err)
		}
		if err := ansiblevars.CheckUntrusted(ov.ExtraVars[name]); err != nil {
			return fmt.Errorf("%w: extra var %s: %w", ErrInvalidInput, name, err)
		}
	}
	if ov.Limit != nil {
		if err := ansiblevars.CheckUntrustedLimit(*ov.Limit); err != nil {
			return fmt.Errorf("%w: limit: %w", ErrInvalidInput, err)
		}
	}
	return nil
}
