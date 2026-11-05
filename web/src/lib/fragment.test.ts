import { describe, expect, it } from 'vitest';
import { tokenFromFragment } from './fragment.ts';

describe('tokenFromFragment', () => {
  it.each([
    ['#token=abc', 'abc'],
    ['token=abc', 'abc'],
    ['#other=1&token=abc', 'abc'],
    ['#token=', null],
    ['#other=1', null],
    ['', null],
  ])('reads %j as %j', (hash, want) => {
    expect(tokenFromFragment(hash)).toBe(want);
  });
});
