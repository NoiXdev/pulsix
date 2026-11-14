import { describe, expect, it } from 'vitest';
import { ApiError } from '../../api/client.ts';
import { conflictChecks } from './conflictChecks.ts';

describe('conflictChecks', () => {
  it('reads the check keys out of the store’s 409', () => {
    expect(conflictChecks(new ApiError(409, { detail: 'secret in use: the checks snmp, snmp-core use it' }))).toBe('snmp, snmp-core');
  });

  it.each([
    ['another 409 text', new ApiError(409, { detail: 'something else' })],
    ['another status', new ApiError(422, { detail: 'secret in use: the checks snmp use it' })],
    ['a plain error', new Error('secret in use: the checks snmp use it')],
  ])('is null for %s', (_name, err) => {
    expect(conflictChecks(err)).toBeNull();
  });
});
