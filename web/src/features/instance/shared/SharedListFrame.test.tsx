import { describe, expect, it } from 'vitest';
import { de } from '../../../i18n/de/index.ts';

describe('the German counts of shared definitions', () => {
  it.each([
    ['repos', 'Repository-Definition', 'Repository-Definitionen'],
    ['templates', 'Job-Vorlagen-Definition', 'Job-Vorlagen-Definitionen'],
    ['checks', 'Check-Definition', 'Check-Definitionen'],
  ] as const)('%s: one is singular, any other number plural', (kind, singular, plural) => {
    const count = de.instance.shared[kind].count;
    expect(count(1)).toMatch(new RegExp(`^1 ${singular} · `));
    expect(count(2)).toMatch(new RegExp(`^2 ${plural} · `));
    expect(count(0)).toMatch(new RegExp(`^0 ${plural} · `));
  });
});
