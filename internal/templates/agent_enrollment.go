// Agent enrollment inside an approved run (spec 2026-11-25 assistant API
// gaps, B, decision D2): a template with AgentEnrollment on enrols the
// Pulsix agent on the hosts it runs on. The claim renders, for each host
// that gets an enrollment token, the host variables
// pulsix_agent_enroll_token (the token) and pulsix_agent_enroll_tokens (an
// empty object, so a stale per-host dict in group vars cannot win).
//
// Extra vars beat host variables, so nothing may set those names as extra
// vars. Nothing can: the whole pulsix_ namespace is reserved for every
// template whatever its flag — its values (ansiblevars.ForbiddenIn in
// prepareVars), its schema's top-level field names (schema.ReservedName),
// the overrides of schedules, triggers and run requests (ValidateOverrides)
// — and the claim refuses a template stored with one from before the
// reservation (jobs' claim). agent_enrollment_test.go pins that for a
// template with the flag.
package templates

import "errors"

// ErrEnrollmentNotAllowed refuses a write that switches a template's agent
// enrollment on or off without the caller's leave (CreateInput.
// MayEnrolAgents, UpdateInput.MayEnrolAgents): runs of such a template
// mint enrollment tokens, which needs agents.write, and the REST layer
// checks it.
var ErrEnrollmentNotAllowed = errors.New("switching agent enrollment on or off needs the agents.write permission")

// enrollmentAfter is the flag a write leaves: next when given (nil keeps
// cur), refused when it changes cur without may.
func enrollmentAfter(cur bool, next *bool, may bool) (bool, error) {
	if next == nil || *next == cur {
		return cur, nil
	}
	if !may {
		return cur, ErrEnrollmentNotAllowed
	}
	return *next, nil
}
