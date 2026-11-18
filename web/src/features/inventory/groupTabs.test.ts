import { describe, expect, it } from 'vitest';
import { isRedirect } from '@tanstack/react-router';
import { formerGroupLink, formerSection, parseGroupTab, validateGroupSearch, type GroupRouteLoad } from './groupTabs.ts';

const params = { org: 'acme', id: 'g1' };

/** The redirect formerGroupLink sends a load on to, or undefined. */
function redirected(load: Omit<GroupRouteLoad, 'params'>) {
  try {
    formerGroupLink({ ...load, params });
  } catch (err) {
    if (isRedirect(err)) return { search: err.options.search, hash: err.options.hash, replace: err.options.replace };
    throw err;
  }
  return undefined;
}

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
    // An old ?tab=schema is kept apart for beforeLoad, which sends it on.
    expect(validateGroupSearch({ tab: 'schema' })).toEqual({ tab: undefined, schema: true });
    expect(validateGroupSearch({ schema: true })).toEqual({ tab: undefined, schema: true });
    expect(validateGroupSearch({ schema: 'yes' })).toEqual({ tab: undefined });
  });

  it('maps the former section anchors to their tabs', () => {
    expect(formerSection('#children')).toBe('subgroups');
    expect(formerSection('preview')).toBe('inventory');
    expect(formerSection('#toString')).toBeUndefined();
    expect(formerSection('')).toBeUndefined();
  });

  it('sends links of the earlier pages on, and leaves current ones alone', () => {
    expect(redirected({ search: {}, location: { hash: 'members' } })).toEqual({ search: { tab: 'members' }, hash: '', replace: true });
    // ?tab=schema (as validateSearch keeps it apart) opens the fields.
    expect(redirected({ search: { schema: true }, location: { hash: '' } })).toEqual({ search: { tab: undefined }, hash: 'schema', replace: true });
    // #values is what Variables is: the anchor goes, the tab stays.
    expect(redirected({ search: {}, location: { hash: 'values' } })).toEqual({ search: {}, hash: '', replace: true });
    expect(redirected({ search: { tab: 'members' }, location: { hash: 'values' } })).toEqual({ search: { tab: 'members' }, hash: '', replace: true });
    // A tab named in the URL wins over an old anchor; a new group's #schema
    // and no anchor at all stay.
    expect(redirected({ search: { tab: 'inventory' }, location: { hash: 'members' } })).toBeUndefined();
    expect(redirected({ search: {}, location: { hash: 'schema' } })).toBeUndefined();
    expect(redirected({ search: {}, location: { hash: '' } })).toBeUndefined();
  });
});
