import { describe, expect, it } from 'vitest';
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
    const result = await submitTotp(async () => ({}), '482913');
    expect(result).toEqual({ step: 'done' });
  });

  it('an invalid code stays on totp with an inline error', async () => {
    const verify = async (): Promise<unknown> => {
      throw new ApiError(401, { detail: 'invalid code' });
    };

    const result = await submitTotp(verify, '000000');

    expect(result).toEqual({ step: 'totp', error: 'invalid code' });
  });
});
