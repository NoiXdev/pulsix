import { describe, expect, it } from 'vitest';
import { limitMatcher } from './limit.ts';

describe('limitMatcher', () => {
  it.each([
    ['', 'web-01', true],
    ['web-01', 'web-01', true],
    ['web-01', 'web-02', false],
    ['web-*', 'web-02', true],
    ['web-*', 'db-01', false],
    ['web-0?', 'web-02', true],
    ['web-0[12]', 'web-03', false],
    ['web-0[12]', 'web-02', true],
    ['web-*:db-01', 'db-01', true],
    ['web-*,db-01', 'db-01', true],
    ['!web-02', 'web-01', true],
    ['!web-02', 'web-02', false],
    ['web-*:!web-02', 'web-02', false],
    ['web-*:!web-02', 'web-01', true],
    ['Web-01', 'web-01', false],
  ])('limit %j on %j is %s', (limit, name, want) => {
    expect(limitMatcher(limit)(name)).toBe(want);
  });

  it.each(['~web.*', 'web-01:&db', '{a,b}', '@file', 'web 01', '[!a]b', 'web[9-0]', '[a-.]', 'web-*:!db[z-a]'])('keeps every host when %j cannot be decided here', (limit) => {
    expect(limitMatcher(limit)('anything')).toBe(true);
  });
});
