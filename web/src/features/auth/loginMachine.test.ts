import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../api/client.ts';
import { beginLogin, initialLoginState, submitPassword, submitTotp } from './loginMachine.ts';

describe('loginMachine', () => {
  it('starts idle', () => {
    expect(initialLoginState).toEqual({ step: 'idle' });
  });

  it('goes idle -> password -> totp when the API answers mfa_required: true', async () => {
    const afterBegin = beginLogin();
    expect(afterBegin.step).toBe('password');

    const afterSubmit = await submitPassword(
      async () => ({ mfa_required: true }),
      'tim@acme.io',
      'correct horse battery staple',
    );

    expect(afterSubmit).toEqual({ step: 'totp' });
  });

  it('goes idle -> password -> done when the API answers mfa_required: false', async () => {
    beginLogin();
    const afterSubmit = await submitPassword(async () => ({ mfa_required: false }), 'tim@acme.io', 'correct-password');
    expect(afterSubmit).toEqual({ step: 'done' });
  });

  it('a 401 shows "invalid credentials" and stays on password', async () => {
    const login = async (): Promise<{ mfa_required: boolean }> => {
      throw new ApiError(401, { detail: 'invalid credentials' });
    };

    const afterSubmit = await submitPassword(login, 'tim@acme.io', 'wrong-password');

    expect(afterSubmit).toEqual({ step: 'password', error: 'invalid credentials' });
  });

  it('rethrows a failure that is not a 401 (e.g. a 500) instead of pretending it was a bad password', async () => {
    const login = async (): Promise<{ mfa_required: boolean }> => {
      throw new ApiError(500, { detail: 'internal error' });
    };

    await expect(submitPassword(login, 'tim@acme.io', 'x')).rejects.toThrow(ApiError);
  });

  it('a non-ApiError failure (e.g. a network error) is rethrown too', async () => {
    const login = async (): Promise<{ mfa_required: boolean }> => {
      throw new TypeError('network error');
    };

    await expect(submitPassword(login, 'tim@acme.io', 'x')).rejects.toThrow(TypeError);
  });
});

describe('loginMachine totp step', () => {
  it('goes totp -> done on a valid code', async () => {
    const result = await submitTotp(async () => ({}), { code: '482913' });
    expect(result).toEqual({ step: 'done' });
  });

  it('an invalid code stays on totp with an inline error', async () => {
    const verify = async (): Promise<unknown> => {
      throw new ApiError(401, { detail: 'invalid code' });
    };

    const result = await submitTotp(verify, { code: '000000' });

    expect(result).toEqual({ step: 'totp', error: 'invalid code' });
  });

  // Backlog 7.26: after repeated wrong codes the account's second factor is
  // locked for a while, and the page has to say so rather than "invalid
  // code" — retyping the right code would only be refused again.
  it('a locked second factor stays on totp and says to wait', async () => {
    const verify = async (): Promise<unknown> => {
      throw new ApiError(429, { detail: 'too many attempts; try again later' });
    };

    const result = await submitTotp(verify, { code: '482913' });

    expect(result).toEqual({ step: 'totp', error: 'too many attempts; wait a few minutes and try again' });
  });

  // Important 5 (task-15a-report.md fix round 1): the recovery-code path
  // — the backend's POST /auth/totp/verify has always accepted a
  // recovery_code as an alternative to a 6-digit code, but the login page
  // never offered a way to use one, so the recovery codes the app hands
  // out on enrolment could never actually be used to sign in.
  it('goes totp -> done on a valid recovery code', async () => {
    const verify = vi.fn(async () => ({}));
    const result = await submitTotp(verify, { recoveryCode: 'r3cov-3ry-c0d3' });

    expect(result).toEqual({ step: 'done' });
    expect(verify).toHaveBeenCalledExactlyOnceWith({ recoveryCode: 'r3cov-3ry-c0d3' });
  });

  it('an invalid recovery code stays on totp with its own inline error, distinct from a bad 6-digit code', async () => {
    const verify = async (): Promise<unknown> => {
      throw new ApiError(400, { detail: 'invalid recovery code' });
    };

    const result = await submitTotp(verify, { recoveryCode: 'already-used' });

    expect(result).toEqual({ step: 'totp', error: 'invalid recovery code' });
  });
});
