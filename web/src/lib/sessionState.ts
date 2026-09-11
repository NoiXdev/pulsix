/**
 * SessionState names the three states a signed-in session can be in. A
 * second factor is mandatory, so a session that is not MFA-complete either
 * still has to verify the factor its account has ('verify', the TOTP step
 * of /login) or has no factor to verify yet and must enrol one ('enrol',
 * /account/enrol) — the API admits such a session to nothing but the
 * enrolment routes, /auth/me and logout.
 */
export type SessionState = 'complete' | 'verify' | 'enrol';

export function sessionState(me: { mfa_complete: boolean; mfa_enrolment_required: boolean }): SessionState {
  if (me.mfa_complete) return 'complete';
  return me.mfa_enrolment_required ? 'enrol' : 'verify';
}
