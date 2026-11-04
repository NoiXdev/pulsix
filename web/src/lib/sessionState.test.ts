import { describe, expect, it } from 'vitest';
import { sessionState } from './sessionState.ts';

describe('sessionState', () => {
  it.each([
    [{ mfa_complete: true, mfa_enrolment_required: false }, 'complete'],
    [{ mfa_complete: true, mfa_enrolment_required: false, must_change_password: false }, 'complete'],
    [{ mfa_complete: false, mfa_enrolment_required: false }, 'verify'],
    [{ mfa_complete: false, mfa_enrolment_required: true }, 'enrol'],
    // The second factor comes first: the password is changed by a session
    // that has completed it.
    [{ mfa_complete: false, mfa_enrolment_required: true, must_change_password: true }, 'enrol'],
    [{ mfa_complete: false, mfa_enrolment_required: false, must_change_password: true }, 'verify'],
    [{ mfa_complete: true, mfa_enrolment_required: false, must_change_password: true }, 'password'],
  ] as const)('%o is %s', (me, want) => {
    expect(sessionState(me)).toBe(want);
  });
});
