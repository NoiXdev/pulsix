import { describe, expect, it } from 'vitest';
import { sessionState } from './sessionState.ts';

describe('sessionState', () => {
  it.each([
    [{ mfa_complete: true, mfa_enrolment_required: false }, 'complete'],
    [{ mfa_complete: false, mfa_enrolment_required: false }, 'verify'],
    [{ mfa_complete: false, mfa_enrolment_required: true }, 'enrol'],
  ] as const)('%o is %s', (me, want) => {
    expect(sessionState(me)).toBe(want);
  });
});
