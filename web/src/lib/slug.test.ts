import { describe, expect, it } from 'vitest';
import { slugify } from './slug.ts';

// The two patterns the server enforces, copied verbatim from
// internal/setup/service.go and internal/hosts/service.go. Every
// generated slug must satisfy BOTH: one generator feeds both forms, and
// a slug valid for only one would fill the field and fail the save.
const ORG = /^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$/;
const ZONE = /^[a-z0-9]+(-[a-z0-9]+)*$/;

describe('slugify', () => {
  const cases: Array<[string, string]> = [
    ['Acme GmbH', 'acme-gmbh'],
    ['  leading and trailing  ', 'leading-and-trailing'],
    ['Köln Süd', 'koln-sud'],
    ['many---hyphens', 'many-hyphens'],
    ['Punctuation! & symbols?', 'punctuation-symbols'],
    ['MiXeD CaSe', 'mixed-case'],
    ['123 numeric start', '123-numeric-start'],
  ];
  for (const [input, want] of cases) {
    it(`turns ${JSON.stringify(input)} into ${JSON.stringify(want)}`, () => {
      expect(slugify(input)).toBe(want);
    });
  }

  it('never produces a slug either server pattern would refuse', () => {
    const inputs = [
      'Acme GmbH',
      'Köln Süd',
      'many---hyphens',
      '!!! ???',
      'a'.repeat(60),
      'a very long organisation name that certainly exceeds the forty character limit',
      'trailing hyphen after the cut aaaaaaaaaa-bbbbbbbbbb',
    ];
    for (const input of inputs) {
      const got = slugify(input);
      if (got === '') continue; // an all-punctuation name has no slug; the form asks for one
      expect(got, `${JSON.stringify(input)} -> ${JSON.stringify(got)} fails the organization pattern`).toMatch(ORG);
      expect(got, `${JSON.stringify(input)} -> ${JSON.stringify(got)} fails the zone pattern`).toMatch(ZONE);
      expect(got.length).toBeLessThanOrEqual(40);
    }
  });

  it('returns empty for a name with nothing slug-worthy in it', () => {
    expect(slugify('!!! ???')).toBe('');
  });
});
