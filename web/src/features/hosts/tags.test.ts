import { describe, expect, it } from 'vitest';
import { manualTagPairs } from './tags.ts';

describe('manualTagPairs', () => {
  it('keeps only the manual tags and drops the source', () => {
    expect(
      manualTagPairs([
        { key: 'env', value: 'prod', source: 'manual' },
        { key: 'os', value: 'debian', source: 'fact' },
        { key: 'team', value: 'ops', source: 'manual' },
      ]),
    ).toEqual([
      { key: 'env', value: 'prod' },
      { key: 'team', value: 'ops' },
    ]);
  });

  it.each([null, undefined])('returns an empty list for %s', (tags) => {
    expect(manualTagPairs(tags)).toEqual([]);
  });
});
