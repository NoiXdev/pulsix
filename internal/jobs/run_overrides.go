// Claim-time half of run overrides: the sealed payload of a run job is
// opened and merged over the template's limit and extra vars.
package jobs

import (
	"encoding/json"
	"fmt"
	"maps"
)

// runOverrides opens a run job's sealed overrides; nil when it has none.
func (q *SQLQueue) runOverrides(job Job) (*RunOverrides, error) {
	if len(job.Payload) == 0 {
		return nil, nil
	}
	plain, err := q.openKindPayload(job)
	if err != nil {
		return nil, err
	}
	if plain == "" {
		return nil, nil
	}
	var ov RunOverrides
	if err := json.Unmarshal([]byte(plain), &ov); err != nil {
		return nil, fmt.Errorf("decode run overrides: %w", err)
	}
	return &ov, nil
}

// mergeExtraVars overlays the override's top-level vars on the template's.
func mergeExtraVars(base json.RawMessage, ov *RunOverrides) (json.RawMessage, error) {
	if ov == nil || len(ov.ExtraVars) == 0 {
		return base, nil
	}
	merged := map[string]any{}
	if len(base) > 0 {
		if err := json.Unmarshal(base, &merged); err != nil {
			return nil, fmt.Errorf("%w: template extra vars are not a JSON object", ErrInvalidInput)
		}
	}
	maps.Copy(merged, ov.ExtraVars)
	return json.Marshal(merged)
}
