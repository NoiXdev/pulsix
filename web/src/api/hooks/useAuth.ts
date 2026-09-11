import { api, unwrap } from '../client.ts';

/** login is the LoginFn features/auth/loginMachine.ts drives — kept as a
 * plain async function (not a useMutation hook) because the login machine
 * itself is framework-free and unit-tested without React. */
export async function login(email: string, password: string) {
  return unwrap(api.POST('/api/v1/auth/login', { body: { email, password } }));
}

export async function verifyTotp(code: string) {
  return unwrap(api.POST('/api/v1/auth/totp/verify', { body: { code } }));
}

export async function logout() {
  return unwrap(api.POST('/api/v1/auth/logout', {}));
}
