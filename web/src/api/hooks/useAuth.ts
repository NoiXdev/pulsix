import { api, unwrap } from '../client.ts';

/** login is the LoginFn features/auth/loginMachine.ts drives — kept as a
 * plain async function (not a useMutation hook) because the login machine
 * itself is framework-free and unit-tested without React. */
export async function login(email: string, password: string) {
  return unwrap(api.POST('/api/v1/auth/login', { body: { email, password } }));
}

/** Mirrors loginMachine.ts's VerifyInput: either a 6-digit authenticator
 * code or a one-time recovery code (finding "Important 5" — the backend's
 * codeInput has always accepted either). */
export async function verifyTotp(input: { code: string } | { recoveryCode: string }) {
  const body = 'recoveryCode' in input ? { recovery_code: input.recoveryCode } : { code: input.code };
  return unwrap(api.POST('/api/v1/auth/totp/verify', { body }));
}

/** reauthWithPassword is the step-up of an account whose password is its
 * step-up (`password_step_up` on /auth/me: the second factor is optional on
 * the instance and the account has none). It refreshes the session's proof
 * like verifyTotp does for a code; 401 is a wrong password. */
export async function reauthWithPassword(password: string) {
  return unwrap(api.POST('/api/v1/auth/password/reauth', { body: { password } }));
}

export async function logout() {
  return unwrap(api.POST('/api/v1/auth/logout', {}));
}

/** renewSession replaces the session with a fresh one for the same account
 * (backlog 3.94). The server needs the password and a second factor — an
 * authenticator code or a recovery code — and answers 401 for a wrong one;
 * an account whose password is its step-up (`password_step_up`) sends the
 * password alone. The new cookie is the result; the answer carries the new
 * expiry. */
export async function renewSession(
  input: { password: string; code: string } | { password: string; recoveryCode: string } | { password: string },
) {
  const body =
    'recoveryCode' in input
      ? { password: input.password, recovery_code: input.recoveryCode }
      : 'code' in input
        ? { password: input.password, code: input.code }
        : { password: input.password };
  return unwrap(api.POST('/api/v1/auth/session/renew', { body }));
}
