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
 *
 *   totp --submitTotp({code})--------> done
 *   totp --submitTotp({recoveryCode})-> done
 *        \--- invalid: stays on totp, with an error
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

/** VerifyInput is either a 6-digit authenticator code or one of the
 * one-time recovery codes the app hands out on TOTP enrolment — finding
 * "Important 5" (task-15a-report.md fix round 1): the backend's
 * POST /auth/totp/verify has always accepted either (internal/api's
 * codeInput has both a Code and a RecoveryCode field), but the login page
 * only ever offered the 6-digit form, so the recovery codes it hands out
 * elsewhere in the app could never actually be used to log in. */
export type VerifyInput = { code: string } | { recoveryCode: string };

/** VerifyFn is the totp step's counterpart to LoginFn — usually
 * `(input) => unwrap(api.POST('/api/v1/auth/totp/verify', ...))`, resolving
 * (with no particular body) once the session is MFA-complete. */
export type VerifyFn = (input: VerifyInput) => Promise<unknown>;

/** submitTotp drives the totp -> done transition, for either a code or a
 * recovery code. Like submitPassword, an invalid one resolves back to the
 * same step with an inline error rather than throwing. */
export async function submitTotp(verify: VerifyFn, input: VerifyInput): Promise<LoginState> {
  try {
    await verify(input);
    return { step: 'done' };
  } catch (err) {
    if (err instanceof ApiError && (err.status === 401 || err.status === 400)) {
      const message = 'recoveryCode' in input ? 'invalid recovery code' : 'invalid code';
      return { step: 'totp', error: message };
    }
    throw err;
  }
}
