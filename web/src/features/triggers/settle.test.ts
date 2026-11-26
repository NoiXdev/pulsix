import { describe, expect, it } from 'vitest';
import { duration, settleBody, settleProblem } from './settle.ts';

describe('settleProblem', () => {
  it.each([
    ['0', '', null],
    ['', '', null],
    ['5', '', null],
    ['3600', '', null],
    ['4', '', 'settle'],
    ['3601', '', 'settle'],
    ['-1', '', 'settle'],
    ['1.5', '', 'settle'],
    ['abc', '', 'settle'],
    ['30', '30', null],
    ['30', '86400', null],
    // 0 is the default, as the API takes it.
    ['30', '0', null],
    ['30', '29', 'maxWait'],
    ['30', '86401', 'maxWait'],
    ['30', 'x', 'maxWait'],
    // The maximum wait is ignored while the settle delay is off.
    ['0', '5', null],
  ])('settle %j, max wait %j → %j', (settle, maxWait, want) => {
    expect(settleProblem(settle, maxWait)).toBe(want);
  });
});

describe('settleBody', () => {
  it('sends the delay and, with one, the maximum wait (0 is the default)', () => {
    expect(settleBody('30', '600')).toEqual({ settle_seconds: 30, max_wait_seconds: 600 });
    expect(settleBody('30', '')).toEqual({ settle_seconds: 30, max_wait_seconds: 0 });
    expect(settleBody('30', '0')).toEqual({ settle_seconds: 30, max_wait_seconds: 0 });
    expect(settleBody('0', '600')).toEqual({ settle_seconds: 0 });
    expect(settleBody('', '')).toEqual({ settle_seconds: 0 });
  });
});

describe('duration', () => {
  it.each([
    [30, '30 s'],
    [90, '90 s'],
    [300, '5 min'],
    [3600, '1 h'],
    [5400, '90 min'],
    [86400, '24 h'],
  ])('%d seconds read %j', (n, want) => {
    expect(duration(n)).toBe(want);
  });
});
