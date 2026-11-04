import { describe, expect, it } from 'vitest';
import { MIN_PASSWORD_LENGTH, newPasswordProblem } from './password.ts';

describe('newPasswordProblem', () => {
  it('holds the product-wide floor of 12 characters', () => {
    expect(MIN_PASSWORD_LENGTH).toBe(12);
  });

  it.each([
    ['short', 'short', 'tooShort'],
    ['eleven-char', 'eleven-char', 'tooShort'],
    ['twelve-chars', 'twelve-charz', 'mismatch'],
    ['twelve-chars', 'twelve-chars', null],
    // Characters, not UTF-16 units, the way the server counts them: eleven
    // emoji are 22 units but only 11 characters.
    ['😀'.repeat(11), '😀'.repeat(11), 'tooShort'],
    ['😀'.repeat(12), '😀'.repeat(12), null],
  ] as const)('%s / %s is %s', (next, repeat, want) => {
    expect(newPasswordProblem(next, repeat)).toBe(want);
  });
});
