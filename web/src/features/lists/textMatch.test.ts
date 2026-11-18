import { describe, expect, it } from 'vitest';
import { matchesText } from './textMatch.ts';

describe('matchesText', () => {
  it.each([
    [undefined, true],
    ['', true],
    ['  ', true],
    ['web', true],
    ['WEB prod', true],
    ['web staging', false],
    ['db', false],
  ])('q=%j matches web-1 / production: %s', (q, want) => {
    expect(matchesText(q, 'web-1', undefined, 'production')).toBe(want);
  });
});
