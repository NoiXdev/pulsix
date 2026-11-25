// Claim-time half of run overrides: the sealed payload of a run job is
// opened and merged over the template's limit and extra vars.
package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

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
	// Numbers stay exact (json.Number): an integer past 2^53 an override
	// carries reaches the worker as it was given, not rounded to a float64.
	var ov RunOverrides
	dec := json.NewDecoder(strings.NewReader(plain))
	dec.UseNumber()
	if err := dec.Decode(&ov); err != nil {
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
		dec := json.NewDecoder(bytes.NewReader(base))
		dec.UseNumber()
		if err := dec.Decode(&merged); err != nil {
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
// overrides (a person's schedule, spec §14) pass unchanged.
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

// checkUntrusted re-checks an untrusted override's extra vars at claim
// time, the same rules the webhook mapping and the run request apply when
// the values arrive: no name Ansible or Pulsix acts on
// (ansiblevars.CheckUntrustedName for a webhook, CheckRequestedName with
// the playbook keywords for a run request — inert values do not help when Ansible
// executes the variable, as with ansible_python_interpreter), no object
// key starting with "__ansible" and no nesting past
// ansiblevars.MaxUntrustedDepth. The last two would make Ansible's JSON
// load of the extra-vars file fail and fall back to YAML, dropping every
// unsafe marker (final re-reviews 1 and 2). trigger is the job's, which
// names the source in the reason (untrustedSource). The reason names only
// the variable, never a key or value from the payload.
func (o *RunOverrides) checkUntrusted(trigger string) error {
	if o == nil || !o.Untrusted {
		return nil
	}
	source := untrustedSource(trigger)
	// A webhook keeps mapping onto keyword-named variables; a run request
	// is held to the keywords too, as when it was made.
	checkName := ansiblevars.CheckRequestedName
	if trigger == TriggerWebhook {
		checkName = ansiblevars.CheckUntrustedName
	}
	for _, name := range slices.Sorted(maps.Keys(o.ExtraVars)) {
		if err := checkName(name); err != nil {
			return fmt.Errorf("%w: run override extra var %s: a %s may not set it: %w", ErrInvalidInput, name, source, err)
		}
		if err := ansiblevars.CheckUntrusted(o.ExtraVars[name]); err != nil {
			return fmt.Errorf("%w: run override extra var %s: a %s may not hold an object key starting with \"__ansible\" or nest more than %d levels deep",
				ErrInvalidInput, name, source, ansiblevars.MaxUntrustedDepth)
		}
	}
	return nil
}

// untrustedSource names where a run's untrusted overrides came from, by
// the job's trigger: a webhook delivery, a schedule an approved schedule
// request set them on, else an approved run request (the only other
// caller that sets Untrusted).
func untrustedSource(trigger string) string {
	switch trigger {
	case TriggerWebhook:
		return "webhook value"
	case TriggerSchedule:
		return "schedule request value"
	}
	return "run request value"
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

// checkWireMarkers checks the merged extra-vars document an untrusted run
// sends: every object with an "__ansible" key inside a variable must be a
// well-formed marker (ansiblevars.CheckMarkers), or Ansible's JSON load
// fails and the markers on the untrusted values are dropped. The overrides
// passed checkUntrusted, so a failure here names a template variable its
// operator must fix. Top-level "__ansible" names are refused for every run
// by the ansiblevars.Forbidden check that follows in buildPayload.
func checkWireMarkers(raw json.RawMessage, trigger string) error {
	var vars map[string]any
	if err := json.Unmarshal(raw, &vars); err != nil {
		return fmt.Errorf("%w: extra vars are not a JSON object", ErrInvalidInput)
	}
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if err := ansiblevars.CheckMarkers(vars[name]); err != nil {
			return fmt.Errorf("%w: template extra var %s cannot run with %ss: %w", ErrInvalidInput, name, untrustedSource(trigger), err)
		}
	}
	return nil
}
