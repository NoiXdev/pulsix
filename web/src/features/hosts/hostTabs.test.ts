import { describe, expect, it } from 'vitest';
import { parseHostTab, validateHostSearch } from './hostTabs.ts';

describe('parseHostTab', () => {
  it.each([
    { raw: 'checks', want: 'checks' },
    { raw: 'alerts', want: 'alerts' },
    { raw: 'runs', want: 'runs' },
    { raw: 'inventory', want: 'inventory' },
    { raw: 'connection', want: 'connection' },
    { raw: 'overview', want: 'overview' },
    { raw: undefined, want: 'overview' },
    { raw: 'bogus', want: 'overview' },
    { raw: 'constructor', want: 'overview' },
    { raw: 3, want: 'overview' },
  ])('$raw reads as $want', ({ raw, want }) => {
    expect(parseHostTab(raw)).toBe(want);
  });
});

describe('validateHostSearch', () => {
  it('keeps a known tab', () => {
    expect(validateHostSearch({ tab: 'connection' })).toEqual({ tab: 'connection' });
  });

  it('leaves the default and anything unknown out of the URL', () => {
    expect(validateHostSearch({ tab: 'overview' })).toEqual({ tab: undefined });
    expect(validateHostSearch({ tab: 'nope' })).toEqual({ tab: undefined });
    expect(validateHostSearch({})).toEqual({ tab: undefined });
  });
});
