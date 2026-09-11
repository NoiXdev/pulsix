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

export async function logout() {
  return unwrap(api.POST('/api/v1/auth/logout', {}));
}
