import { describe, expect, it } from 'vitest';
import { en } from '../../i18n/en/index.ts';
import { de } from '../../i18n/de/index.ts';
import { intervalLabel } from './interval.ts';

describe('intervalLabel', () => {
  it.each([
    ['whole hours read as hours', 3600, '1 h'],
    ['several hours too', 21_600, '6 h'],
    ['whole minutes read as minutes', 60, '1 min'],
    ['five minutes', 300, '5 min'],
    // 90 s is a whole number of neither, so it stays in the unit the API
    // carries rather than becoming "1.5 min".
    ['anything else stays in seconds', 90, '90 s'],
    ['sub-minute', 30, '30 s'],
    // Zero is divisible by both 3600 and 60; without the `seconds > 0`
    // guard it would read "0 h".
    ['zero is not zero hours', 0, '0 s'],
  ])('%s', (_name, seconds, expected) => {
    expect(intervalLabel(seconds, en)).toBe(expected);
  });

  it('writes the German units', () => {
    expect(intervalLabel(3600, de)).toBe('1 Std.');
    expect(intervalLabel(300, de)).toBe('5 Min.');
    expect(intervalLabel(90, de)).toBe('90 s');
  });
});
