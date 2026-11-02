import { describe, expect, it } from 'vitest';
import { de } from '../../i18n/de/index.ts';

// German counts agree with their noun: one is singular, any other number
// plural. Each is pinned at 0, 1 and 2, the way the shared-definition
// counts are.
describe('the German counts of the Checks section', () => {
  it.each([
    ['definitions', de.checks.list.summary, 'Definition', 'Definitionen'],
    ['monitoring groups', de.checks.groups.summary, 'Gruppe', 'Gruppen'],
    ['rules', de.checks.rules.summary, 'Regel', 'Regeln'],
    ['item decisions', de.checks.items.summary, 'Entscheidung', 'Entscheidungen'],
  ] as const)('%s: one is singular, any other number plural', (_name, summary, singular, plural) => {
    expect(summary(1)).toMatch(new RegExp(`^1 ${singular} · `));
    expect(summary(2)).toMatch(new RegExp(`^2 ${plural} · `));
    expect(summary(0)).toMatch(new RegExp(`^0 ${plural} · `));
  });

  it('matches: one host or item is singular', () => {
    expect(de.checks.groups.matches(1)).toBe('Trifft auf 1 Host zu');
    expect(de.checks.groups.matches(3)).toBe('Trifft auf 3 Hosts zu');
    expect(de.checks.itemDialog.matches(1)).toBe('Trifft auf 1 Element zu');
    expect(de.checks.itemDialog.matches(3)).toBe('Trifft auf 3 Elemente zu');
  });
});
