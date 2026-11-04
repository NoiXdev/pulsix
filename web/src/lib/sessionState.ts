/**
 * SessionState names the states a signed-in session can be in. A second
 * factor is mandatory, so a session that is not MFA-complete either still
 * has to verify the factor its account has ('verify', the TOTP step of
 * /login) or has no factor to verify yet and must enrol one ('enrol',
 * /account/enrol) — the API admits such a session to nothing but the
 * enrolment routes, /auth/me and logout.
 *
 * An MFA-complete session whose account must replace a password an
 * administrator set is 'password' (/account/password): the API admits it to
 * nothing but changing the password, /auth/me and logout (backlog 7.74).
 * The second factor comes first; the password change is for a session that
 * has passed it.
 */
export type SessionState = 'complete' | 'verify' | 'enrol' | 'password';

export function sessionState(me: {
  mfa_complete: boolean;
  mfa_enrolment_required: boolean;
  must_change_password?: boolean;
}): SessionState {
  if (me.mfa_complete) return me.must_change_password ? 'password' : 'complete';
  return me.mfa_enrolment_required ? 'enrol' : 'verify';
}
