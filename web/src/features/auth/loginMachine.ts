import { ApiError } from '../../api/client.ts';

/**
 * loginMachine models the /login page's state as a tiny, framework-free
 * state machine so its transitions — including the one branch that is easy
 * to get backwards, a wrong password leaving the user stuck on a screen
 * that gives no feedback — can be unit-tested without rendering React or
 * mocking fetch.
 *
 *   idle --beginLogin()--> password --submitPassword()--> totp   (mfa_required: true)
 *                              ^                       \-> done  (mfa_required: false)
 *                              \--- 401: stays on password, with an error
 */
export type LoginStep = 'idle' | 'password' | 'totp' | 'done';

export interface LoginState {
  step: LoginStep;
  error?: string;
}

export const initialLoginState: LoginState = { step: 'idle' };

/** beginLogin is the idle -> password transition: the login page calls
 * this once, on mount, before the user has typed anything. */
export function beginLogin(): LoginState {
  return { step: 'password' };
}

/** LoginFn is the one thing submitPassword needs from the outside world —
 * normally `(email, password) => unwrap(api.POST('/api/v1/auth/login', ...))`
 * — injected so tests can drive every branch without a real API client. */
export type LoginFn = (email: string, password: string) => Promise<{ mfa_required: boolean }>;

/** submitPassword drives the password -> {totp | done} transition, or —
 * on invalid credentials — resolves back to `password` carrying an error
 * the form can render inline instead of throwing (a wrong password is an
 * expected outcome of this screen, not an exceptional one). Any other
 * failure (a network error, a 500, ...) is rethrown for the caller to
 * handle as a general failure, since the login form has nothing more
 * specific to say about it. */
export async function submitPassword(login: LoginFn, email: string, password: string): Promise<LoginState> {
  try {
    const res = await login(email, password);
    return { step: res.mfa_required ? 'totp' : 'done' };
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      return { step: 'password', error: 'invalid credentials' };
    }
    throw err;
  }
}

/** VerifyFn is the totp step's counterpart to LoginFn — usually
 * `(code) => unwrap(api.POST('/api/v1/auth/totp/verify', ...))`, resolving
 * (with no particular body) once the session is MFA-complete. */
export type VerifyFn = (code: string) => Promise<unknown>;

/** submitTotp drives the totp -> done transition. Like submitPassword, an
 * invalid code resolves back to the same step with an inline error rather
 * than throwing. */
export async function submitTotp(verify: VerifyFn, code: string): Promise<LoginState> {
  try {
    await verify(code);
    return { step: 'done' };
  } catch (err) {
    if (err instanceof ApiError && (err.status === 401 || err.status === 400)) {
      return { step: 'totp', error: 'invalid code' };
    }
    throw err;
  }
}
