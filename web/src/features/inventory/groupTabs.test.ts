import { describe, expect, it } from 'vitest';
import { formerSection, parseGroupTab, validateGroupSearch } from './groupTabs.ts';

describe('group tabs', () => {
  it.each([
    [undefined, 'variables'],
    ['members', 'members'],
    ['subgroups', 'subgroups'],
    ['inheritance', 'inheritance'],
    ['inventory', 'inventory'],
    ['bogus', 'variables'],
    [3, 'variables'],
    // The earlier pages' names.
    ['values', 'variables'],
    ['schema', 'variables'],
    ['children', 'subgroups'],
    ['preview', 'inventory'],
    ['constructor', 'variables'],
  ])('parses ?tab=%s as %s', (raw, tab) => {
    expect(parseGroupTab(raw)).toBe(tab);
  });

  it('keeps the default and anything unknown out of the URL', () => {
    expect(validateGroupSearch({})).toEqual({ tab: undefined });
    expect(validateGroupSearch({ tab: 'variables' })).toEqual({ tab: undefined });
    expect(validateGroupSearch({ tab: 'nope' })).toEqual({ tab: undefined });
    expect(validateGroupSearch({ tab: 'preview' })).toEqual({ tab: 'inventory' });
    expect(validateGroupSearch({ tab: 'members', other: 1 })).toEqual({ tab: 'members' });
  });

  it('maps the former section anchors to their tabs', () => {
    expect(formerSection('#children')).toBe('subgroups');
    expect(formerSection('preview')).toBe('inventory');
    expect(formerSection('#toString')).toBeUndefined();
    expect(formerSection('')).toBeUndefined();
  });
});
