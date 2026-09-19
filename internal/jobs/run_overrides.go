// Claim-time half of run overrides: the sealed payload of a run job is
// opened and merged over the template's limit and extra vars.
package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/noixdev/pulsix/internal/ansiblevars"
)

// runOverrides opens a run job's sealed overrides; nil when it has none.
// The stored form is always the sealed-string form Service.sealPayload
// writes (a JSON string carrying the base64 ciphertext, see
// openKindPayload): a run payload that is not that shape never came from
// the enqueue path, so it is refused rather than trusted as plaintext
// overrides.
func (q *SQLQueue) runOverrides(job Job) (*RunOverrides, error) {
	if len(job.Payload) == 0 {
		return nil, nil
	}
	var sealed string
	if err := json.Unmarshal(job.Payload, &sealed); err != nil {
		return nil, fmt.Errorf("%w: run job %s payload is not the sealed-string form", ErrInvalidInput, job.ID)
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
	// A base that is JSON "null" (a template with no stored extra vars,
	// whatever normalises it to {} before storage does not run here) must
	// not reach json.Unmarshal: encoding/json sets a map destination to
	// nil for a null source even when it started non-nil, and maps.Copy
	// panics ("assignment to entry in nil map") on that nil destination.
	if len(base) > 0 && !bytes.Equal(bytes.TrimSpace(base), []byte("null")) {
		if err := json.Unmarshal(base, &merged); err != nil {
			return nil, fmt.Errorf("%w: template extra vars are not a JSON object", ErrInvalidInput)
		}
	}
	maps.Copy(merged, ov.ExtraVars)
	return json.Marshal(merged)
}

// inert returns the overrides as the worker receives them: an untrusted
// override (a webhook payload) has every string inside its extra vars
// marked unsafe for Ansible, so a "{{ … }}" or "{% … %}" value stays
// literal text instead of running on the worker (final review C1). Trusted
// overrides (schedules, set by operators — spec §14) pass unchanged.
func (o *RunOverrides) inert() *RunOverrides {
	if o == nil || !o.Untrusted || len(o.ExtraVars) == 0 {
		return o
	}
	out := *o
	out.ExtraVars = make(map[string]any, len(o.ExtraVars))
	for k, v := range o.ExtraVars {
		out.ExtraVars[k] = ansiblevars.Inert(v)
	}
	return &out
}

// checkLimit re-checks an untrusted override's limit at claim time, the
// same rule the webhook mapping applies at receive time: no "@file",
// regex or subscript patterns. The value itself is never echoed.
func (o *RunOverrides) checkLimit() error {
	if o == nil || !o.Untrusted || o.Limit == nil {
		return nil
	}
	if err := ansiblevars.CheckUntrustedLimit(*o.Limit); err != nil {
		return fmt.Errorf("%w: run override limit: %w", ErrInvalidInput, err)
	}
	return nil
}
