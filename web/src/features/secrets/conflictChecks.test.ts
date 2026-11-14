import { describe, expect, it } from 'vitest';
import { ApiError } from '../../api/client.ts';
import { conflictChecks } from './conflictChecks.ts';

const inUse = (errors: unknown) => new ApiError(409, { detail: 'secret in use: the checks snmp, snmp-core use it', errors });

describe('conflictChecks', () => {
  it('reads the check keys from the 409’s used_by details', () => {
    expect(
      conflictChecks(
        inUse([
          { location: 'used_by', message: 'snmp' },
          { location: 'used_by', message: 'snmp-core' },
        ]),
      ),
    ).toEqual(['snmp', 'snmp-core']);
  });

  it('skips details located elsewhere', () => {
    expect(conflictChecks(inUse([{ location: 'body', message: 'x' }, { location: 'used_by', message: 'snmp' }]))).toEqual(['snmp']);
  });

  it.each([
    ['a 409 without details, whatever its text', inUse(undefined)],
    ['a 409 without used_by details', inUse([{ location: 'body', message: 'x' }])],
    ['another status', new ApiError(422, { errors: [{ location: 'used_by', message: 'snmp' }] })],
    ['a plain error', new Error('secret in use: the checks snmp use it')],
  ])('is null for %s', (_name, err) => {
    expect(conflictChecks(err)).toBeNull();
  });
});
